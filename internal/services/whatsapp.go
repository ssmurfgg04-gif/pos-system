// whatsapp.go — receipt delivery over WhatsApp (v1.4.6).
//
// Two paths, one message source:
//
//  1. MANUAL (always available, zero config): the receipt screen's
//     "WhatsApp" button asks the server for the receipt text + wa.me deep
//     link and hands them to the shop's own WhatsApp (phone app or
//     WhatsApp Desktop linked to the business number). The cashier
//     presses send. No vendor, no token, nothing can break the money path.
//
//  2. AUTOMATED (opt-in): when a linked-number gateway is configured
//     (whapi.cloud / ultramsg presets or a custom endpoint), payment
//     completion queues the receipt in whatsapp_jobs and a worker delivers
//     it to the customer's number with bounded retries. Jobs persist, so
//     restarts never lose a receipt; failures land in the audit log and
//     the order's activity timeline.
//
// Design guardrails: sending NEVER blocks or fails a sale (queue is
// best-effort, like printing); the gateway key is a masked secret that
// deliberately does NOT team-sync; and only COMPLETED orders queue.

package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"posapp/internal/models"
	"posapp/internal/printer"
)

const (
	whatsappMaxAttempts   = 5
	whatsappHTTPTimeout   = 15 * time.Second
	whatsappTickInterval  = 10 * time.Second
	whatsappRetryInterval = 60 * time.Second // a failed job waits a minute before attempt 2+
)

// WhatsappReceipt is what the manual-send endpoint hands the frontend.
type WhatsappReceipt struct {
	Phone  string `json:"phone"`  // normalized 2547… ("" when unknown)
	Link   string `json:"link"`   // https://wa.me/…?text=… ("" when no phone)
	Text   string `json:"text"`   // the receipt body (single source of truth)
	Queued bool   `json:"queued"` // automated send enqueued (gateway configured)
	Reason string `json:"reason"` // why not queued ("" when queued)
}

// receiptText renders the plain-text WhatsApp receipt. WhatsApp renders
// *bold* between asterisks and keeps monospace for ``` blocks — the
// items/totals block is fenced so columns line up on any phone.
func (s *Service) receiptText(o *models.Order) string {
	store := s.settings.Get("store_name")
	if store == "" {
		store = s.settings.Get("app_name")
	}
	if store == "" {
		store = "Store"
	}
	symbol := s.settings.Get("currency_symbol")
	if symbol == "" {
		symbol = "KES"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "*%s*\n", store)
	fmt.Fprintf(&b, "Receipt %s\n", o.Number)
	if t, err := time.Parse(time.RFC3339, o.PaidAtOrCreated()); err == nil {
		b.WriteString(t.Local().Format("02/01/2006 15:04"))
		b.WriteString("\n")
	}
	b.WriteString("\n```\n")
	for _, it := range o.Items {
		fmt.Fprintf(&b, "%s\n  %d x %s", it.Name, it.Qty, printer.FormatMoney(it.UnitPriceCents, symbol))
		if it.LineTotalCents != it.UnitPriceCents*int64(it.Qty) {
			// discounted line: show the charged total
			fmt.Fprintf(&b, " -> %s", printer.FormatMoney(it.LineTotalCents, symbol))
		}
		b.WriteString("\n")
	}
	b.WriteString("```\n\n")
	if o.DiscountCents != 0 {
		fmt.Fprintf(&b, "Discount: -%s\n", printer.FormatMoney(o.DiscountCents, symbol))
	}
	if o.TaxCents != 0 {
		fmt.Fprintf(&b, "VAT (%s%%): %s\n", trimPercent(o.TaxPercent), printer.FormatMoney(o.TaxCents, symbol))
	}
	fmt.Fprintf(&b, "*TOTAL: %s*\n", printer.FormatMoney(o.TotalCents, symbol))
	for _, pm := range o.Payments {
		if pm.Status == "FAILED" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", paymentWord(pm), printer.FormatMoney(pm.AmountCents, symbol))
		if pm.MpesaReceipt != "" {
			fmt.Fprintf(&b, "M-Pesa code: %s\n", pm.MpesaReceipt)
		}
	}
	if o.CustomerName != "" {
		fmt.Fprintf(&b, "Customer: %s\n", o.CustomerName)
	}
	if o.CashierName != "" {
		fmt.Fprintf(&b, "Served by: %s\n", o.CashierName)
	}
	if note := s.settings.Get("receipt_footer"); note != "" {
		b.WriteString("\n")
		b.WriteString(note)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func paymentWord(pm models.Payment) string {
	// A not-yet-confirmed leg must never read as money received (the
	// client explicitly removed "(pending)" from the PAPER receipt — the
	// WhatsApp text keeps the honest marker instead of hiding the state).
	if pm.Method == "mpesa" && pm.Status != models.PaymentCompleted {
		return "M-Pesa (pending)"
	}
	switch pm.Method {
	case "cash":
		return "Paid cash"
	case "mpesa":
		return "Paid M-Pesa"
	case "credit":
		return "Store credit"
	case "account":
		return "Tab charge"
	default:
		return "Paid " + pm.Method
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
func (s *Service) customerPhone(o *models.Order) string {
	if o.CustomerID == 0 {
		return ""
	}
	var phone string
	if err := s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(phone,'') FROM customers WHERE id = ?`), o.CustomerID).Scan(&phone); err != nil {
		return ""
	}
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return ""
	}
	norm, err := normalizeWhatsappPhone(phone)
	if err != nil {
		return ""
	}
	return norm
}

// whatsappLink builds the wa.me deep link for the manual path.
func whatsappLink(phone, text string) string {
	if phone == "" {
		return ""
	}
	return fmt.Sprintf("https://wa.me/%s?text=%s", phone, url.QueryEscape(text))
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
		return "", fmt.Errorf("not a Kenyan mobile number: %s", in)
	}
	return p, nil
}

// WhatsappReceiptResult builds (and optionally queues) the WhatsApp
// receipt for one order. Manual path: the frontend opens Link. Auto path:
// QueueWhatsappReceipt was already called by the completion hooks; this
// re-queues on demand (re-send from the order screen).
func (s *Service) BuildWhatsappReceipt(orderID int64) (*WhatsappReceipt, error) {
	o, err := s.GetOrder(orderID)
	if err != nil {
		return nil, err
	}
	text := s.receiptText(o)
	phone := s.customerPhone(o)
	out := &WhatsappReceipt{Phone: phone, Text: text, Link: whatsappLink(phone, text)}
	if phone == "" {
		out.Reason = "no customer phone on this order"
		return out, nil
	}
	if !s.whatsappGatewayReady() {
		out.Reason = "gateway not configured — opening WhatsApp to send manually"
		return out, nil
	}
	if err := s.queueWhatsappJob(o, phone, text); err != nil {
		out.Reason = "queue failed: " + err.Error()
		return out, nil
	}
	out.Queued = true
	return out, nil
}

// QueueWhatsappReceipt is the completion hook: best-effort, never errors
// into the money path. Queues only when auto-send is on, the gateway is
// configured, and the order carries a customer phone.
func (s *Service) QueueWhatsappReceipt(o *models.Order) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[whatsapp] queue panic: %v", r)
		}
	}()
	if !s.settings.GetBool("whatsapp_auto_send", false) || !s.whatsappGatewayReady() {
		return
	}
	if o.Status != models.OrderPaid {
		return
	}
	phone := s.customerPhone(o)
	if phone == "" {
		return
	}
	if err := s.queueWhatsappJob(o, phone, s.receiptText(o)); err != nil {
		log.Printf("[whatsapp] queue %s: %v", o.Number, err)
	}
}

func (s *Service) queueWhatsappJob(o *models.Order, phone, text string) error {
	_, err := s.db.Exec(s.db.Rebind(`
                INSERT INTO whatsapp_jobs (order_id, order_number, phone, body, status, created_at)
                VALUES (?, ?, ?, ?, 'PENDING', ?)`), o.ID, o.Number, phone, text, nowStamp())
	return err
}

// whatsappGatewayReady reports whether an automated send can happen on
// this device: a provider preset (or custom URL) plus an API key.
func (s *Service) whatsappGatewayReady() bool {
	if s.settings.Get("whatsapp_api_key") == "" {
		return false
	}
	switch s.settings.Get("whatsapp_provider") {
	case "whapi", "ultramsg", "custom":
		return s.settings.Get("whatsapp_api_url") != "" || s.settings.Get("whatsapp_provider") == "whapi"
	}
	return false
}

// sendWhatsappJob performs one delivery attempt against the configured
// gateway. Presets:
//   - whapi:   POST {base}/messages/text        Bearer <key>   {"to","body"}
//   - ultramsg: POST {url}                      (URL holds instance path) {"token","to","body"}
//   - custom:  POST {url}                       Bearer <key>   {"to","text","order"}
func (s *Service) sendWhatsappJob(orderNumber, phone, body string) error {
	key := s.settings.Get("whatsapp_api_key")
	provider := s.settings.Get("whatsapp_provider")
	apiURL := s.settings.Get("whatsapp_api_url")

	var payload map[string]any
	var headers map[string]string
	switch provider {
	case "whapi":
		base := apiURL
		if base == "" {
			base = "https://gate.whapi.cloud"
		}
		payload = map[string]any{"to": phone, "body": body}
		headers = map[string]string{"Authorization": "Bearer " + key}
		apiURL = strings.TrimRight(base, "/") + "/messages/text"
	case "ultramsg":
		if apiURL == "" {
			return fmt.Errorf("ultramsg instance URL not set")
		}
		payload = map[string]any{"token": key, "to": phone, "body": body}
	default:
		if apiURL == "" {
			return fmt.Errorf("gateway URL not set")
		}
		payload = map[string]any{"to": phone, "text": body, "order": orderNumber}
		headers = map[string]string{"Authorization": "Bearer " + key}
	}

	buf, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: whatsappHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("gateway %d: %s", resp.StatusCode, truncStr(string(data), 200))
	}
	return nil
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
			// stored implicitly via updated created_at + attempts math
			if err := s.db.QueryRow(s.db.Rebind(`SELECT 1 FROM whatsapp_jobs WHERE id = ? AND created_at <= ?`),
				j.id, now.Add(-time.Duration(j.attempts)*whatsappRetryInterval).Format(nowStampLayout)).Scan(new(int)); err == nil {
				// due
			} else {
				continue
			}
		}
		err := s.sendWhatsappJob(j.orderNumber, j.phone, j.body)
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
