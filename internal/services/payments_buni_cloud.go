package services

// payments_buni_cloud.go — the KCB Buni M-Pesa Express route (v1.4.0).
//
// BUNI IS THE MAIN M-PESA RAIL; Paystack is demoted to fallback. Route
// priority lives in MpesaRoute(): buni → paystack → daraja/mock → manual.
//
// Key custody: the Buni consumer key/secret NEVER reach the till. They sit
// in Edge Function secrets; this service calls buni-stkpush with the
// till's device identity and the function performs the KCB OAuth + push.
// Completion is CALLBACK-DRIVEN (Buni has no STK query API): KCB posts the
// result to the public buni-callback Edge Function, which stores the event
// server-side; the till's sweeper claims it through the device-gated
// buni_claim_events RPC (exactly-once per device, team-scoped by the push
// mapping) and funnels it through the SAME fail-closed completion guard as
// the Daraja public webhook (HandleCallback — stored-amount match or the
// sale does not complete).
//
// Discriminator: Buni legs carry a "BUNI-" prefix on payments
// .checkout_request_id (e.g. "BUNI-ws_CO_2610..."). The Daraja sweeper
// skips prefixed ids; HandleCallback resolves both forms. No schema change
// needed — payments.mode keeps its checkout-mode meaning (auto/stk/manual).

import (
        "context"
        "encoding/json"
        "fmt"
        "log"
        "strings"
        "time"

        "posapp/internal/auth"
        "posapp/internal/models"
        "posapp/internal/mpesa"
)

// buniViaCloud reports whether the owner's cloud config enables the Buni
// route (a public app_config row distributed with the vault config). The
// buni_enabled setting can always turn it off locally; it defaults OFF
// until the vault distributes the row or the owner flips it in Settings.
func (s *Service) buniViaCloud() bool {
        return s.settings.GetBool("buni_via_cloud", false)
}

// buniUsable: the cloud link exists AND the route is enabled. The public
// app_config row (buni_via_cloud) is the primary switch; buni_enabled is
// the local kill-switch (default true so flipping the vault row on is the
// whole rollout).
func (s *Service) buniUsable() bool {
        if !s.cloudEdgeAvailable() {
                return false
        }
        return s.buniViaCloud() && s.settings.GetBool("buni_enabled", true)
}

// buniCloud builds the provider for the current link, or nil. Cached on
// the link identity (the Service carries buniKey/buni, mirroring paystack).
func (s *Service) buniCloud() *mpesa.BuniCloud {
        c, ok := s.syncConfig()
        if !ok || c.mode != "rpc" {
                return nil
        }
        cacheKey := c.base + "|" + c.key + "|" + c.device + "|" + c.secretHash
        if s.buniKey != cacheKey {
                s.buniKey = cacheKey
                s.buni = mpesa.NewBuniCloud(c.base, c.key, c.device, c.secretHash)
        }
        return s.buni
}

// buniMpesaSTK pushes the M-Pesa prompt through the Buni cloud route.
// Mirrors paystackMpesaSTK: supersede stale pending legs, bind this
// payment to Buni, store the prefixed checkout id, audit everything.
func (s *Service) buniMpesaSTK(ctx context.Context, order *models.Order, pay *models.Payment, p *auth.Principal) (*models.Order, error) {
        prov := s.buniCloud()
        if prov == nil {
                return nil, fmt.Errorf("%w: buni route needs the LedgerPOS cloud link", ErrNotConfigured)
        }
        if !s.buniUsable() {
                return nil, fmt.Errorf("%w: buni route is disabled", ErrNotConfigured)
        }
        phone := pay.Phone
        if norm, err := mpesa.NormalizePhone(phone); err == nil {
                phone = norm
        }
        if !mpesaPhoneOK(phone) {
                return nil, fmt.Errorf("invalid customer phone %q, expected 07XX/2547XX/2541XX", pay.Phone)
        }
        // Supersede previous pending mpesa legs (fresh checkout every push) —
        // EXCLUDING the leg being pushed: it is itself a pending mpesa leg, and
        // a blanket supersede would mark it FAILED before the new checkout id
        // could bind (the push would "succeed" while the till stored nothing).
        s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = 'superseded by new STK push' WHERE order_id = ? AND method = 'mpesa' AND status = 'PENDING' AND id != ?`), order.ID, pay.ID)
        // This payment now belongs to the Buni route.
        s.db.Exec(s.db.Rebind(`UPDATE payments SET method = 'mpesa', mode = 'stk' WHERE id = ? AND status = 'PENDING'`), pay.ID)

        invoiceRef := buniInvoiceRef(order.Number)
        resp, err := prov.InitiateSTK(ctx, mpesa.STKRequest{
                OrderID:          order.ID,
                PaymentID:        pay.ID,
                Phone:            phone,
                AmountCents:      pay.AmountCents,
                AccountReference: invoiceRef,
                Description:      "Payment " + order.Number,
                ClientUUID:       order.ClientUUID,
        })
        if err != nil {
                s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = ? WHERE id = ? AND status = 'PENDING'`),
                        truncStr(err.Error(), 200), pay.ID)
                s.Audit(p.ID, p.Username, "BUNI_MPESA_FAILED", "order", order.Number, err.Error())
                return nil, err
        }
        s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'PENDING', checkout_request_id = ?, merchant_request_id = ?, result_desc = ? WHERE id = ?`),
                mpesa.BuniCheckoutPrefix+resp.CheckoutRequestID, resp.MerchantRequestID, truncStr(resp.CustomerMessage, 200), pay.ID)
        s.Audit(p.ID, p.Username, "BUNI_MPESA_INIT", "order", order.Number, "checkout "+resp.CheckoutRequestID)
        return s.GetOrder(order.ID)
}

// buniInvoiceRef folds the order identity into the customer-visible
// invoiceNumber "7853959-XXXX" (the KCB till prefix leaves 4 characters).
// Correctness never depends on this ref — the CheckoutRequestID is the
// matching key — so a truncated, collision-safe display ref is fine.
func buniInvoiceRef(orderNumber string) string {
        n := strings.ToUpper(strings.Map(func(r rune) rune {
                switch {
                case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
                        return r
                default:
                        return -1
                }
        }, orderNumber))
        if len(n) > 4 {
                // keep the head (ORD20261022 0001 → "ORD2" style) — distinctive
                // enough for the customer's statement, unique per mint because the
                // checkout id is the real key
                n = n[:4]
        }
        if n == "" {
                n = "POS"
        }
        return n
}

// SweepBuni claims verified Buni callback events for this till's pending
// AND recently-failed Buni legs (late-PIN recovery mirrors
// SweepFailedPaystack), then times out pushes whose callback never
// arrived. Called from the Sweeper tick.
func (s *Service) SweepBuni() {
        if !s.buniUsable() {
                return
        }
        type leg struct {
                id         int64
                checkoutID string
                created    string
        }
        rows, err := s.db.Query(`SELECT id, checkout_request_id, created_at FROM payments
                WHERE method = 'mpesa' AND checkout_request_id LIKE ?
                  AND status IN ('PENDING','FAILED')
                ORDER BY id DESC LIMIT 100`, mpesa.BuniCheckoutPrefix+"%")
        if err != nil {
                return
        }
        var legs []leg
        for rows.Next() {
                var l leg
                if err := rows.Scan(&l.id, &l.checkoutID, &l.created); err == nil {
                        legs = append(legs, l)
                }
        }
        rows.Close()
        if len(legs) == 0 {
                return
        }

        // Late-money timeout backstop: a push older than 3 minutes with no
        // callback fails (the order stays PENDING for manual receipt / retry).
        for _, l := range legs {
                if t, ok := parseStamp(l.created); ok && s.stkLegExpired(t) {
                        s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = 'STK timeout (no Buni callback)' WHERE id = ? AND status = 'PENDING'`), l.id)
                }
        }

        // Claim exactly-once events for the raw checkout ids.
        refs := make([]string, 0, len(legs))
        for _, l := range legs {
                refs = append(refs, strings.TrimPrefix(l.checkoutID, mpesa.BuniCheckoutPrefix))
        }
        for _, ev := range s.claimBuniEvents(refs) {
                cb, err := mpesa.ParseCallback(ev.Payload)
                if err != nil {
                        log.Printf("[buni] relay event %s parse: %v", ev.Reference, err)
                        continue
                }
                if _, err := s.HandleCallback(cb); err != nil {
                        // Unknown checkout / duplicate receipt / mismatch — all audited
                        // inside HandleCallback; the event was already claimed.
                        log.Printf("[buni] relay event %s apply: %v", ev.Reference, err)
                } else {
                        log.Printf("[buni] relay event %s applied (checkout %s)", ev.Reference, cb.CheckoutRequestID)
                }
        }
}

// claimBuniEvents pulls cloud-verified callback events for THIS till's
// checkout ids (same-team scoped server-side; each event is delivered to
// each device exactly once).
func (s *Service) claimBuniEvents(checkoutIDs []string) []buniClaimedEvent {
        if s.claimBuniEventsForTest != nil {
                return s.claimBuniEventsForTest(checkoutIDs)
        }
        if len(checkoutIDs) == 0 {
                return nil
        }
        c, ok := s.syncConfig()
        if !ok || c.mode != "rpc" {
                return nil
        }
        var out struct {
                OK     bool               `json:"ok"`
                Events []buniClaimedEvent `json:"events"`
        }
        if err := rpcCall(c.base, "buni_claim_events", map[string]any{
                "p_device_id":    c.device,
                "p_secret_hash":  c.secretHash,
                "p_checkout_ids": checkoutIDs,
        }, &out); err != nil || !out.OK {
                return nil // relay is best-effort; the timeout backstop owns cleanup
        }
        return out.Events
}

type buniClaimedEvent struct {
        ID        int64           `json:"id"`
        Reference string          `json:"reference"`
        Checkout  string          `json:"checkoutRequestId"`
        TeamCode  string          `json:"teamCode"`
        Payload   json.RawMessage `json:"payload"`
        CreatedAt string          `json:"createdAt"`
}

// stkLegExpired mirrors the Daraja sweeper's 3-minute STK window.
func (s *Service) stkLegExpired(t time.Time) bool {
        return time.Since(t) > 3*time.Minute
}
