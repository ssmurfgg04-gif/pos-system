// whatsapp.go — WhatsApp receipt delivery (v1.5.0 automatic, v1.6.0
// manual Share button too).
//
// One gateway (whapi.cloud). Two triggers, one delivery pipeline:
//
//  1. AUTOMATIC (v1.5.0): when a sale completes as PAID, the completion
//     hook enqueues one job; a worker delivers a branded receipt IMAGE
//     (logo, brand color, sale details — see receipt_image.go) with a
//     short English caption to the customer's number, with bounded
//     retries. Zero cashier steps.
//  2. MANUAL (v1.6.0): the receipt screen's Share-via-WhatsApp button.
//     When the gateway is connected it queues the same branded IMAGE
//     server-side (a deliberate re-send — the common trigger is "I
//     didn't get it"); when it is not (the API sat under review for
//     weeks), the cashier shares the very same image from the device:
//     the Web Share sheet carries image + caption into WhatsApp, or the
//     button saves the PNG and opens the customer's chat.
//
// Policy guardrails:
//   - Sending NEVER blocks or fails a sale (the queue is best-effort,
//     like printing). No key configured = no automatic job, sale
//     completes normally; the manual button falls back to the device.
//   - The gateway token is a masked secret that deliberately does NOT
//     team-sync (each till that completes sales needs its own key).
//   - A job whose order is no longer PAID (voided/refunded before the
//     worker got to it) is cancelled, not sent.

package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"posapp/internal/models"
	"posapp/internal/printer"
)

const (
	whatsappMaxAttempts   = 5
	whatsappHTTPTimeout   = 20 * time.Second
	whatsappTickInterval  = 10 * time.Second
	whatsappRetryInterval = 60 * time.Second // a failed job waits a minute before attempt 2+
	whatsappDefaultBase   = "https://gate.whapi.cloud"
)

// paymentWord names a payment leg on the customer-facing receipt.
// Status-honest (v1.6.0 criticism fix): the automatic path only fires for
// PAID orders, but the SAME words now reach pending sales through the
// manual Share button and the receipt-image endpoint — an unconfirmed
// M-Pesa leg must never print "Paid" (that promise is the P1-4 rule the
// paper + HTML renderers already follow).
func paymentWord(pm models.Payment) string {
	done := pm.Status == "COMPLETED"
	switch pm.Method {
	case "cash":
		return "Paid cash" // cash on the counter needs no confirmation
	case "mpesa":
		if done {
			return "Paid M-Pesa"
		}
		return "M-Pesa (pending)"
	case "credit":
		if !done {
			return "Store credit (pending)"
		}
		return "Store credit"
	case "account":
		if !done {
			return "Tab charge (pending)"
		}
		return "Tab charge"
	case "paystack":
		if done {
			return "Paid card / mobile money"
		}
		return "Card / mobile money (pending)"
	default:
		if done {
			return "Paid " + pm.Method
		}
		return pm.Method + " (pending)"
	}
}

func trimPercent(f float64) string {
	s := fmt.Sprintf("%.2f", f)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" {
		return "0"
	}
	return s
}

// customerPhone resolves the number a receipt should go to: the linked
// CRM record's phone (checkout auto-captures it), normalized to 2547….
// Walk-in STK sales have no CRM record — the money's own payer phone
// (on the payment leg) is the customer, so the receipt follows the
// payment instead of silently skipping the shop's most common sale.
func (s *Service) customerPhone(o *models.Order) string {
	if o.CustomerID != 0 {
		var phone string
		if err := s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(phone,'') FROM customers WHERE id = ?`), o.CustomerID).Scan(&phone); err == nil {
			phone = strings.TrimSpace(phone)
			if phone != "" {
				if norm, err := normalizeWhatsappPhone(phone); err == nil {
					return norm
				}
			}
		}
	}
	// CRM record missing/phoneless: fall back to the payer leg.
	for _, pm := range o.Payments {
		if pm.Status == "FAILED" || strings.TrimSpace(pm.Phone) == "" {
			continue
		}
		if norm, err := normalizeWhatsappPhone(pm.Phone); err == nil {
			return norm
		}
	}
	return ""
}

// normalizeWhatsappPhone mirrors mpesa.NormalizePhone (07…/01…/+254/254 →
// 2547XXXXXXXX) without importing the payment package here.
func normalizeWhatsappPhone(in string) (string, error) {
	p := strings.TrimSpace(in)
	p = strings.ReplaceAll(p, " ", "")
	p = strings.ReplaceAll(p, "-", "")
	if p == "" {
		return "", fmt.Errorf("empty phone")
	}
	switch {
	case strings.HasPrefix(p, "+254"):
		p = "254" + p[4:]
	case strings.HasPrefix(p, "254"):
		// already international
	case strings.HasPrefix(p, "0"):
		p = "254" + p[1:]
	case len(p) == 9 && (strings.HasPrefix(p, "7") || strings.HasPrefix(p, "1")):
		p = "254" + p
	}
	if len(p) < 10 || !strings.HasPrefix(p, "254") {
		// Message mentions "phone" on purpose: the handler's error map turns
		// that into a 422 (bad input), not a 500 (server fault).
		return "", fmt.Errorf("invalid phone number (need a Kenyan mobile): %s", in)
	}
	return p, nil
}

// receiptCaption is the short English message that travels with the
// branded receipt image: the thank-you the owner asked for plus just
// enough sale detail to identify the receipt at a glance.
func (s *Service) receiptCaption(o *models.Order) string {
	store := s.settings.Get("store_name")
	if store == "" {
		store = s.settings.Get("app_name")
	}
	if store == "" {
		store = "us"
	}
	symbol := s.settings.Get("currency_symbol")
	if symbol == "" {
		symbol = "KES"
	}

	var b strings.Builder
	first := o.CustomerName
	if i := strings.IndexByte(first, ' '); i > 0 {
		first = first[:i]
	}
	if first != "" {
		fmt.Fprintf(&b, "Hi %s! Thank you for shopping with %s — we're glad you chose us.\n", first, store)
	} else {
		fmt.Fprintf(&b, "Thank you for shopping with %s — we're glad you chose us.\n", store)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "Receipt %s", o.Number)
	if t, err := time.Parse(time.RFC3339, o.PaidAtOrCreated()); err == nil {
		fmt.Fprintf(&b, " · %s", t.Local().Format("02/01/2006 15:04"))
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "Total: %s\n", printer.FormatMoney(o.TotalCents, symbol))
	for _, pm := range o.Payments {
		if pm.Status == "FAILED" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", paymentWord(pm), printer.FormatMoney(pm.AmountCents, symbol))
		if pm.MpesaReceipt != "" {
			fmt.Fprintf(&b, "M-Pesa code: %s\n", pm.MpesaReceipt)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// QueueWhatsappReceipt is the completion hook: best-effort, never errors
// into the money path. Queues only when the gateway is configured, the
// order is PAID, and it carries a customer phone. Auto-send is ON by
// default — connecting the token in Settings is the only setup step.
func (s *Service) QueueWhatsappReceipt(o *models.Order) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[whatsapp] queue panic: %v", r)
		}
	}()
	if !s.settings.GetBool("whatsapp_auto_send", true) || !s.whatsappGatewayReady() {
		return
	}
	if o.Status != models.OrderPaid {
		return
	}
	phone := s.customerPhone(o)
	if phone == "" {
		return
	}
	if err := s.queueWhatsappJob(o, phone, s.receiptCaption(o)); err != nil {
		log.Printf("[whatsapp] queue %s: %v", o.Number, err)
	}
}

func (s *Service) queueWhatsappJob(o *models.Order, phone, caption string) error {
	_, err := s.db.Exec(s.db.Rebind(`
                INSERT INTO whatsapp_jobs (order_id, order_number, phone, body, status, created_at)
                VALUES (?, ?, ?, ?, 'PENDING', ?)`), o.ID, o.Number, phone, caption, nowStamp())
	return err
}

// ResendWhatsappReceipt is the Share-via-WhatsApp button's SERVER leg:
// one branded-image send queued to the customer through the gateway.
// Unlike QueueWhatsappReceipt (fire-and-forget completion hook) this one
// answers the UI: it returns the queued phone, or a refusal that says
// which path to take instead.
//
//	fallback=true  → the gateway can't do it (not connected / the sale
//	                 carries no phone): the UI shares the image from the
//	                 DEVICE (Web Share sheet → download + wa.me chat).
//	fallback=false → a real failure worth showing (unpaid order, DB).
//
// A re-send is deliberately allowed even when the automatic job already
// ran: "I didn't get it" is the button's reason to exist.
func (s *Service) ResendWhatsappReceipt(o *models.Order) (phone string, fallback bool, err error) {
	if !s.whatsappGatewayReady() {
		return "", true, fmt.Errorf("WhatsApp gateway not connected on this till — the image is shared from the device instead")
	}
	if o.Status != models.OrderPaid {
		return "", false, fmt.Errorf("only a PAID sale can be sent to the customer's WhatsApp")
	}
	phone = s.customerPhone(o)
	if phone == "" {
		return "", true, fmt.Errorf("no customer phone on this sale — the image is shared from the device instead")
	}
	if err := s.queueWhatsappJob(o, phone, s.receiptCaption(o)); err != nil {
		return "", false, err
	}
	return phone, false, nil
}

// whatsappGatewayReady reports whether an automated send can happen on
// this device: a saved whapi.cloud token. One gateway, one field.
func (s *Service) whatsappGatewayReady() bool {
	return s.settings.Get("whatsapp_api_key") != ""
}

func whatsappBase(svc *Service) string {
	if base := strings.TrimRight(svc.settings.Get("whatsapp_api_url"), "/"); base != "" {
		return base // hidden override; also the test seam
	}
	return whatsappDefaultBase
}

// sendWhatsappImage performs one delivery attempt: multipart upload of
// the branded receipt PNG to whapi.cloud.
//
//	POST {base}/messages/image   Authorization: Bearer <key>
//	fields: to, caption, media (file)
func (s *Service) sendWhatsappImage(orderNumber, phone, caption string, png []byte) error {
	key := s.settings.Get("whatsapp_api_key")
	if key == "" {
		return fmt.Errorf("whatsapp token not set")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("to", phone); err != nil {
		return err
	}
	if err := mw.WriteField("caption", caption); err != nil {
		return err
	}
	fh, err := mw.CreateFormFile("media", "receipt-"+orderNumber+".png")
	if err != nil {
		return err
	}
	if _, err := fh.Write(png); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, whatsappBase(s)+"/messages/image", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	return doWhatsapp(req)
}

// sendWhatsappText posts a plain text message (the Settings test send).
//
//	POST {base}/messages/text   {"to","body"}
func (s *Service) sendWhatsappText(phone, body string) error {
	key := s.settings.Get("whatsapp_api_key")
	if key == "" {
		return fmt.Errorf("whatsapp token not set")
	}
	payload, err := json.Marshal(map[string]string{"to": phone, "body": body})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, whatsappBase(s)+"/messages/text", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	return doWhatsapp(req)
}

func doWhatsapp(req *http.Request) error {
	client := &http.Client{Timeout: whatsappHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("whatsapp gateway %d: %s", resp.StatusCode, truncStr(string(data), 200))
	}
	return nil
}

// WhatsappTestSend validates a freshly pasted token end-to-end by
// messaging the given number (usually the shop's own). Returns the
// normalized phone on success.
func (s *Service) WhatsappTestSend(phone string) (string, error) {
	norm, err := normalizeWhatsappPhone(phone)
	if err != nil {
		return "", err
	}
	store := s.settings.Get("store_name")
	if store == "" {
		store = "Your shop"
	}
	body := fmt.Sprintf("%s: WhatsApp receipts are connected. This was a test message from your POS.", store)
	if err := s.sendWhatsappText(norm, body); err != nil {
		return "", err
	}
	return norm, nil
}

// drainWhatsappJobs runs one worker tick: every due PENDING job gets a
// delivery attempt; success marks SENT (+ timeline + audit), failure
// counts an attempt and dead-letters at the cap. Backoff is coarse on
// purpose — receipts are not latency-sensitive, and a single pooled
// SQLite connection must never be held hostage by a chatty retry loop.
func (s *Service) drainWhatsappJobs(now time.Time) int {
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT id, order_id, order_number, phone, body, attempts, COALESCE(last_error,'')
                FROM whatsapp_jobs WHERE status = 'PENDING' ORDER BY id LIMIT 10`))
	if err != nil {
		return 0
	}
	type job struct {
		id, orderID, attempts               int64
		orderNumber, phone, body, lastError string
	}
	var batch []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.orderID, &j.orderNumber, &j.phone, &j.body, &j.attempts, &j.lastError); err == nil {
			batch = append(batch, j)
		}
	}
	rows.Close()
	sent := 0
	for _, j := range batch {
		if j.attempts > 0 {
			// retry backoff: nth attempt waits n minutes (1, 2, 3…) —
			// stored implicitly via created_at + attempts math
			if err := s.db.QueryRow(s.db.Rebind(`SELECT 1 FROM whatsapp_jobs WHERE id = ? AND created_at <= ?`),
				j.id, now.Add(-time.Duration(j.attempts)*whatsappRetryInterval).Format(nowStampLayout)).Scan(new(int)); err == nil {
				// due
			} else {
				continue
			}
		}
		// The order may have changed between queue and send (voided,
		// refunded): never deliver a receipt for a sale that isn't paid.
		o, err := s.GetOrder(j.orderID)
		if err != nil {
			// Criticism fix (P1): a job whose order row is
			// unreadable must FAIL, not panic — the tick-level
			// recover would otherwise leave it PENDING at the
			// head of the batch, starving every later send.
			s.db.Exec(s.db.Rebind(`UPDATE whatsapp_jobs SET status = 'FAILED', attempts = ?, last_error = ? WHERE id = ?`),
				j.attempts+1, "order not found — receipt send cancelled", j.id)
			s.Audit(0, "system", "WHATSAPP_FAILED", "order", j.orderNumber, "cancelled: order not found")
			continue
		}
		if o.Status != models.OrderPaid {
			s.db.Exec(s.db.Rebind(`UPDATE whatsapp_jobs SET status = 'FAILED', attempts = ?, last_error = ? WHERE id = ?`),
				j.attempts+1, "order no longer paid — receipt send cancelled", j.id)
			s.Audit(0, "system", "WHATSAPP_FAILED", "order", j.orderNumber, "cancelled: order no longer paid")
			continue
		}
		png, err := s.ReceiptPNG(o)
		if err == nil {
			err = s.sendWhatsappImage(j.orderNumber, j.phone, j.body, png)
		}
		if err == nil {
			s.db.Exec(s.db.Rebind(`UPDATE whatsapp_jobs SET status = 'SENT', sent_at = ?, last_error = '' WHERE id = ?`),
				nowStamp(), j.id)
			s.AddOrderEvent(j.orderID, models.OrderEventWhatsApp,
				fmt.Sprintf("Receipt sent to %s on WhatsApp", j.phone), nil)
			s.Audit(0, "system", "WHATSAPP_SENT", "order", j.orderNumber, j.phone)
			sent++
			continue
		}
		attempts := j.attempts + 1
		if attempts >= whatsappMaxAttempts {
			s.db.Exec(s.db.Rebind(`UPDATE whatsapp_jobs SET status = 'FAILED', attempts = ?, last_error = ? WHERE id = ?`),
				attempts, truncStr(err.Error(), 200), j.id)
			s.Audit(0, "system", "WHATSAPP_FAILED", "order", j.orderNumber, truncStr(err.Error(), 160))
			s.AddOrderEvent(j.orderID, models.OrderEventWhatsApp,
				fmt.Sprintf("WhatsApp receipt to %s failed: %s", j.phone, truncStr(err.Error(), 80)), nil)
			continue
		}
		s.db.Exec(s.db.Rebind(`UPDATE whatsapp_jobs SET attempts = ?, last_error = ? WHERE id = ?`),
			attempts, truncStr(err.Error(), 200), j.id)
	}
	return sent
}

// WhatsappJobRow is one entry of the admin's failed-sends view.
type WhatsappJobRow struct {
	ID          int64  `json:"id"`
	OrderID     int64  `json:"orderId"`
	OrderNumber string `json:"orderNumber"`
	Phone       string `json:"phone"`
	Status      string `json:"status"`
	Attempts    int64  `json:"attempts"`
	LastError   string `json:"lastError"`
	CreatedAt   string `json:"createdAt"`
	SentAt      string `json:"sentAt"`
}

// ListWhatsappJobs returns the most recent delivery jobs (newest first).
func (s *Service) ListWhatsappJobs(limit int) ([]WhatsappJobRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT id, order_id, order_number, phone, status, attempts, COALESCE(last_error,''),
                       created_at, COALESCE(sent_at,'')
                FROM whatsapp_jobs ORDER BY id DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WhatsappJobRow{} // never nil — JSON null breaks the admin panel
	for rows.Next() {
		var j WhatsappJobRow
		if err := rows.Scan(&j.ID, &j.OrderID, &j.OrderNumber, &j.Phone, &j.Status, &j.Attempts, &j.LastError, &j.CreatedAt, &j.SentAt); err == nil {
			out = append(out, j)
		}
	}
	return out, nil
}

// RetryWhatsappJob re-queues a FAILED job (admin action from the failed-
// sends list). Refuses when the order is no longer paid — a cancelled
// receipt must not resurrect.
func (s *Service) RetryWhatsappJob(id int64) error {
	var orderID int64
	var status string
	if err := s.db.QueryRow(s.db.Rebind(`SELECT order_id, status FROM whatsapp_jobs WHERE id = ?`), id).Scan(&orderID, &status); err != nil {
		return ErrNotFound
	}
	if status != "FAILED" {
		return fmt.Errorf("only failed sends can be retried")
	}
	o, err := s.GetOrder(orderID)
	if err != nil {
		return err
	}
	if o.Status != models.OrderPaid {
		return fmt.Errorf("order %s is no longer paid — receipt send stays cancelled", o.Number)
	}
	_, err = s.db.Exec(s.db.Rebind(`UPDATE whatsapp_jobs SET status = 'PENDING', attempts = 0, last_error = '', created_at = ? WHERE id = ?`),
		nowStamp(), id)
	return err
}

// WhatsappWorker drains the receipt queue for one shop (started per shop
// in EnsureStarted, next to the STK sweeper).
type WhatsappWorker struct {
	svc *Service
}

func NewWhatsappWorker(svc *Service) *WhatsappWorker { return &WhatsappWorker{svc: svc} }

func (w *WhatsappWorker) Run(ctx context.Context) {
	tick := time.NewTicker(whatsappTickInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[whatsapp] tick panic: %v", r)
					}
				}()
				w.svc.drainWhatsappJobs(time.Now())
			}()
		}
	}
}
