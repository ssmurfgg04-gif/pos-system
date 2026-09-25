package services

// payments_paystack.go — Paystack card / mobile-money checkout.
//
// Flow (mirrors the M-Pesa pipeline, so reporting/voids/sync need zero
// special cases):
//  1. Checkout with paymentMethod=paystack creates a PENDING order +
//     PENDING payment (no stock movement yet).
//  2. InitPaystack calls POST /transaction/initialize (secret key, server
//     side) and returns access_code + authorization_url + PUBLIC key.
//  3. The till opens the Paystack popup (access_code) or redirects.
//  4. Completion is trusted ONLY from VerifyPaystack (server-side verify)
//     or HandlePaystackWebhook (HMAC-SHA512-checked). Both funnel into the
//     same completePayment guard the M-Pesa paths use — exactly-once
//     stock deduction, loyalty earn, printing, and team sync.
//
// SECURITY: the secret key is read from PAYSTACK_SECRET_KEY (env override)
// or the per-shop settings store. It never appears in API responses, logs,
// audits, or sync events. The webhook endpoint validates the raw-body
// signature in constant time before parsing anything.

import (
        "context"
        "errors"
        "fmt"
        "log"
        "os"
        "strings"
        "time"

        "posapp/internal/auth"
        "posapp/internal/models"
        "posapp/internal/paystack"
)

// GetPaystack builds (and caches) the client for the current settings.
// Env var PAYSTACK_SECRET_KEY overrides the stored key — useful for
// headless deployments that never touch the Settings UI.
func (s *Service) GetPaystack() *paystack.Client {
        key := strings.TrimSpace(os.Getenv("PAYSTACK_SECRET_KEY"))
        if key == "" {
                key = s.settings.Get("paystack_secret_key")
        }
        if s.paystackKey != key {
                s.paystackKey = key
                if paystack.ValidKey(key) {
                        s.paystack = paystack.NewClient(key)
                } else {
                        s.paystack = nil
                }
        }
        return s.paystack
}

// paystackConfigured reports whether a charge can actually be initialized.
func (s *Service) paystackConfigured() bool {
        return s.GetPaystack() != nil && s.settings.GetBool("paystack_enabled", false)
}

// PaymentConfig powers the till's tender buttons (public data only).
func (s *Service) PaymentConfig() *models.PaymentConfig {
        cfg := &models.PaymentConfig{
                CreditEnabled:  s.settings.GetBool("credit_enabled", true),
                LoyaltyEnabled: s.settings.GetBool("loyalty_enabled", true),
        }
        cfg.Paystack.Enabled = s.settings.GetBool("paystack_enabled", false)
        cfg.Paystack.PublicKey = s.settings.Get("paystack_public_key")
        cfg.Paystack.Currency = s.paystackCurrency()
        cfg.Paystack.Callback = s.settings.GetString("paystack_callback_url", "https://awesomeposs.netlify.app/")
        cfg.Paystack.Configured = s.GetPaystack() != nil
        cfg.Mpesa.Env = s.settings.GetString("mpesa_env", "mock")
        cfg.Mpesa.Till = s.settings.Get("till_number")
        cfg.Mpesa.Paybill = s.settings.Get("paybill_number")
        return cfg
}

func (s *Service) paystackCurrency() string {
        cur := strings.ToUpper(strings.TrimSpace(s.settings.Get("paystack_currency")))
        if cur == "" {
                cur = strings.ToUpper(strings.TrimSpace(s.settings.GetString("currency_code", "KES")))
        }
        if cur == "" {
                cur = "KES"
        }
        return cur
}

// PaystackInit opens (or re-opens) checkout for a PENDING paystack order.
// Each call mints a FRESH reference and supersedes stale pending payments
// — abandoned popups never block a retry.
func (s *Service) PaystackInit(orderID int64, email string, p *auth.Principal) (*models.Order, *models.PaystackInitResult, error) {
        client := s.GetPaystack()
        if client == nil {
                return nil, nil, fmt.Errorf("%w: paystack secret key missing", ErrNotConfigured)
        }
        if !s.settings.GetBool("paystack_enabled", false) {
                return nil, nil, fmt.Errorf("%w: paystack is disabled in settings", ErrNotConfigured)
        }
        order, err := s.GetOrder(orderID)
        if err != nil {
                return nil, nil, err
        }
        if order.Status != models.OrderPending {
                if order.Status == models.OrderPaid {
                        return nil, nil, ErrOrderAlreadyPaid
                }
                return nil, nil, fmt.Errorf("%w: cannot start paystack checkout on %s order", ErrInvalidState, order.Status)
        }

        // Supersede previous pending paystack attempts (fresh reference each time).
        s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = 'superseded by new checkout' WHERE order_id = ? AND method = 'paystack' AND status = 'PENDING'`), orderID)

        // Find or create the PENDING payment row for the payable amount.
        var pay *models.Payment
        for i := range order.Payments {
                if order.Payments[i].Status == models.PaymentPending && order.Payments[i].Method == models.MethodPaystack {
                        pay = &order.Payments[i]
                }
        }
        if pay == nil {
                res, err := s.db.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, created_at)
                        VALUES (?, 'paystack', 'popup', ?, 'PENDING', ?)`), orderID, order.TotalCents, nowStamp())
                if err != nil {
                        return nil, nil, err
                }
                id, _ := res.LastInsertId()
                pay = &models.Payment{ID: id, AmountCents: order.TotalCents}
        }

        // Email: collected value, else a deterministic placeholder (Paystack
        // requires one; the receipt still lands in the Paystack dashboard).
        email = strings.TrimSpace(email)
        if email == "" {
                email = "customer+" + strings.ToLower(order.Number) + "@ledgerpos.app"
        }
        if len(email) > 200 || !strings.Contains(email, "@") {
                return nil, nil, errors.New("invalid customer email")
        }

        reference := fmt.Sprintf("LP-%s-%s", order.Number, randToken(4))
        amount := pay.AmountCents
        if amount <= 0 {
                return nil, nil, fmt.Errorf("%w: order has no payable amount", ErrInvalidState)
        }
        init, err := client.Initialize(paystack.InitializeRequest{
                Email:       email,
                Amount:      amount,
                Currency:    s.paystackCurrency(),
                Reference:   reference,
                CallbackURL: s.settings.GetString("paystack_callback_url", "https://awesomeposs.netlify.app/"),
                Metadata: map[string]any{
                        "order_number": order.Number,
                        "cashier":      p.Username,
                        "custom_fields": []map[string]string{
                                {"display_name": "Order", "variable_name": "order", "value": order.Number},
                        },
                },
        })
        if err != nil {
                s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = ? WHERE id = ? AND status = 'PENDING'`),
                        truncStr(err.Error(), 200), pay.ID)
                s.Audit(p.ID, p.Username, "PAYSTACK_INIT_FAILED", "order", order.Number, err.Error())
                return nil, nil, err
        }
        s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'PENDING', checkout_request_id = ?, email = ?, result_desc = ? WHERE id = ?`),
                reference, email, "checkout opened "+nowStamp(), pay.ID)
        s.Audit(p.ID, p.Username, "PAYSTACK_INIT", "order", order.Number, "ref "+reference)

        order, err = s.GetOrder(orderID)
        if err != nil {
                return nil, nil, err
        }
        return order, &models.PaystackInitResult{
                Reference:        reference,
                AccessCode:       init.AccessCode,
                AuthorizationURL: init.AuthorizationURL,
                PublicKey:        s.settings.Get("paystack_public_key"),
                Currency:         s.paystackCurrency(),
                AmountCents:      amount,
        }, nil
}

// display_name metadata field is rendered in the Paystack checkout UI.

// PaystackVerify is the authoritative completion path when the popup
// callback fires. The reference is re-verified against the API — client
// callbacks are never trusted on their own.
func (s *Service) PaystackVerify(orderID int64, reference string, p *auth.Principal) (*models.Order, error) {
        client := s.GetPaystack()
        if client == nil {
                return nil, fmt.Errorf("%w: paystack secret key missing", ErrNotConfigured)
        }
        reference = strings.TrimSpace(reference)
        if reference == "" || len(reference) > 120 {
                return nil, errors.New("invalid payment reference")
        }
        var paymentID int64
        err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM payments WHERE checkout_request_id = ? AND order_id = ? AND method = 'paystack'`),
                reference, orderID).Scan(&paymentID)
        if err != nil {
                return nil, fmt.Errorf("%w: no paystack payment for reference %s", ErrNotFound, reference)
        }
        if err := s.paystackGuardVoid(paymentID); err != nil {
                return nil, err
        }
        v, err := client.Verify(reference)
        if err != nil {
                s.Audit(p.ID, p.Username, "PAYSTACK_VERIFY_FAILED", "order", fmt.Sprint(orderID), err.Error())
                return nil, err
        }
        if v.Status != "success" {
                // abandoned / failed / pending — payment stays PENDING unless
                // Paystack says definitively abandoned/failed, so the cashier can
                // re-open checkout.
                if v.Status == "failed" || v.Status == "abandoned" {
                        s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = ? WHERE id = ? AND status = 'PENDING'`),
                                truncStr("paystack "+v.Status, 200), paymentID)
                }
                return nil, fmt.Errorf("payment not successful (status %s)", v.Status)
        }
        order, err := s.completePayment(paymentID, reference, v.Amount, "Paystack "+v.Channel)
        if err != nil {
                return nil, err
        }
        s.Audit(p.ID, p.Username, "PAYSTACK_VERIFIED", "order", order.Number,
                fmt.Sprintf("ref %s, channel %s, amount %d", reference, v.Channel, v.Amount))
        return order, nil
}

// paystackGuardVoid refuses to complete payments on voided orders (money
// moved, the sale is gone — staff must refund from the Paystack dashboard).
func (s *Service) paystackGuardVoid(paymentID int64) error {
        var orderID int64
        var status string
        if err := s.db.QueryRow(`SELECT order_id, (SELECT status FROM orders WHERE id = order_id) FROM payments WHERE id = ?`, paymentID).
                Scan(&orderID, &status); err != nil {
                return ErrNotFound
        }
        if status == models.OrderVoided {
                s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = 'paid after void — refund via paystack dashboard' WHERE id = ? AND status = 'PENDING'`), paymentID)
                s.Audit(0, "system", "PAYSTACK_AFTER_VOID", "order", fmt.Sprint(orderID), "refund required in paystack dashboard")
                return fmt.Errorf("%w: order was voided — refund this charge from the Paystack dashboard", ErrInvalidState)
        }
        return nil
}

// HandlePaystackWebhook processes server-to-server charge.success events.
// Signature is checked against the RAW body BEFORE parsing. Safe to call
// with any payload: unknown refs and non-success events are acknowledged
// (200) so Paystack stops retrying, but nothing changes locally.
func (s *Service) HandlePaystackWebhook(raw []byte, signature string) error {
        client := s.GetPaystack()
        if client == nil {
                return errors.New("paystack secret key not configured")
        }
        if !client.CheckWebhookSignature(raw, signature) {
                return errors.New("invalid webhook signature")
        }
        ev, err := paystack.ParseWebhook(raw)
        if err != nil {
                return err
        }
        if ev.Event != "charge.success" || ev.Data.Status != "success" || ev.Data.Reference == "" {
                return nil // not a completion event — ack and ignore
        }
        var paymentID int64
        err = s.db.QueryRow(s.db.Rebind(`SELECT id FROM payments WHERE checkout_request_id = ? AND method = 'paystack'`),
                ev.Data.Reference).Scan(&paymentID)
        if err != nil {
                log.Printf("[paystack] webhook for unknown reference %s", ev.Data.Reference)
                return nil
        }
        if err := s.paystackGuardVoid(paymentID); err != nil {
                return nil // ack: Paystack must not retry; the audit trail notes the refund
        }
        if _, err := s.completePayment(paymentID, ev.Data.Reference, ev.Data.Amount, "Paystack "+ev.Data.Channel+" (webhook)"); err != nil {
                // Returning an error (non-200) makes Paystack retry — right answer
                // for transient failures like DB contention.
                return err
        }
        log.Printf("[paystack] webhook completed reference %s", ev.Data.Reference)
        return nil
}

// PaystackTimeout is how long a pending paystack checkout may sit before
// the sweeper gives up (the popup was likely abandoned).
const PaystackTimeout = 15 * time.Minute

// SweepPaystack polls pending paystack payments via the verify API — the
// completion path for redirect flows whose callback never reached us.
func (s *Service) SweepPaystack(ctx context.Context) {
        client := s.GetPaystack()
        if client == nil || !s.settings.GetBool("paystack_enabled", false) {
                return
        }
        type pending struct {
                id        int64
                reference string
                createdAt time.Time
        }
        rows, err := s.db.Query(`SELECT id, checkout_request_id, created_at FROM payments
                WHERE status = 'PENDING' AND method = 'paystack' AND checkout_request_id != ''`)
        if err != nil {
                return
        }
        var list []pending
        now := time.Now()
        for rows.Next() {
                var p pending
                var created string
                if err := rows.Scan(&p.id, &p.reference, &created); err != nil {
                        break
                }
                if t, err := time.Parse("2006-01-02 15:04:05", created); err == nil {
                        p.createdAt = t
                } else if t, err := time.Parse(time.RFC3339, created); err == nil {
                        p.createdAt = t
                } else {
                        p.createdAt = now
                }
                list = append(list, p)
        }
        rows.Close()
        if rows.Err() != nil {
                return
        }
        for _, p := range list {
                select {
                case <-ctx.Done():
                        return
                default:
                }
                if now.Sub(p.createdAt) > PaystackTimeout {
                        s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = 'paystack checkout timeout (15 min)' WHERE id = ? AND status = 'PENDING'`), p.id)
                        continue
                }
                v, err := client.Verify(p.reference)
                if err != nil {
                        continue // transient — keep waiting
                }
                switch {
                case v.Status == "success":
                        if err := s.paystackGuardVoid(p.id); err != nil {
                                continue
                        }
                        if _, err := s.completePayment(p.id, p.reference, v.Amount, "Paystack "+v.Channel); err != nil {
                                log.Printf("[paystack] sweep complete %d: %v", p.id, err)
                        }
                case v.Status == "abandoned" || v.Status == "failed":
                        s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = ? WHERE id = ? AND status = 'PENDING'`),
                                truncStr("paystack "+v.Status, 200), p.id)
                }
        }
}
