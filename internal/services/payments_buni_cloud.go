package services

// payments_buni_cloud.go — the KCB Buni M-Pesa Express route (v1.4.0).
//
// BUNI IS THE MAIN M-PESA RAIL — and since v1.4.4 (Paystack + Daraja
// retired) the ONLY real-money STK rail. Route priority lives in
// MpesaRoute(): buni → mock (ALLOW_MOCK_PAYMENTS) → manual.
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
// .checkout_request_id (e.g. "BUNI-ws_CO_2610..."). HandleCallback
// resolves both forms. No schema change
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

// cloudEdgeAvailable resolves the cloud link (rpc mode = Supabase project).
// (Moved here from the retired payments_paystack_cloud.go, v1.4.4.)
func (s *Service) cloudEdgeAvailable() bool {
	c, ok := s.syncConfig()
	return ok && c.mode == "rpc"
}

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
// the link identity (the Service carries buniKey/buni).
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

// stkPromptBusy reports whether another Buni STK push for this order went
// out within the KCB USSD session window (~75s, well under the observed
// session lifetime but long enough to stop double-tap storms).
func (s *Service) stkPromptBusy(orderID, payID int64) bool {
	cutoff := time.Now().UTC().Add(-75 * time.Second).Format(nowStampLayout)
	var n int
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM payments
                WHERE order_id = ? AND method = 'mpesa' AND id != ?
                AND COALESCE(checkout_request_id,'') LIKE ?
                AND created_at >= ?`),
		orderID, payID, mpesa.BuniCheckoutPrefix+"%", cutoff).Scan(&n)
	return n > 0
}

// buniMpesaSTK pushes the M-Pesa prompt through the Buni cloud route:
// supersede stale pending legs, bind this payment to Buni, store the
// prefixed checkout id, audit everything.
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
	// Double-push guard (KCB 4999 "Duplicated MSISDN. MSISDN has an
	// existing USSD Session"): a USSD session is per MSISDN and outlives
	// the request that opened it. A Buni push for this order younger than
	// the session window means a prompt is almost certainly still on the
	// phone — refuse here instead of stacking a second one KCB will reject
	// anyway (Resend stays clickable during the payment watch). The
	// customer tapping Cancel flips the leg via callback, which lifts the
	// guard early; manual legs never open a session and are ignored.
	if s.stkPromptBusy(order.ID, pay.ID) {
		return nil, fmt.Errorf("%w", ErrSTKPromptBusy)
	}
	// Supersede previous pending mpesa legs (fresh checkout every push) —
	// EXCLUDING the leg being pushed: it is itself a pending mpesa leg, and
	// a blanket supersede would mark it FAILED before the new checkout id
	// could bind (the push would "succeed" while the till stored nothing).
	// Supersede + re-tag run in ONE transaction: as two autocommits, a
	// concurrent push (double-tap) could interleave and leave TWO live
	// full-balance legs → two prompts, over-recorded payment.
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = 'superseded by new STK push' WHERE order_id = ? AND method = 'mpesa' AND status = 'PENDING' AND id != ?`), order.ID, pay.ID); err != nil {
		tx.Rollback()
		return nil, err
	}
	// This payment now belongs to the Buni route (only while still pending).
	if _, err := tx.Exec(s.db.Rebind(`UPDATE payments SET method = 'mpesa', mode = 'stk' WHERE id = ? AND status = 'PENDING'`), pay.ID); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

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
		// Cross-order 4999 (a session opened by another order/till on the
		// same phone) cannot be cooldown-guarded — translate the raw KCB
		// string into something a cashier can act on. Same semantics: leg
		// FAILED, order stays PENDING, money only ever moves on callback.
		msg := err.Error()
		if strings.Contains(msg, "Duplicated MSISDN") || strings.Contains(msg, "existing USSD Session") {
			err = fmt.Errorf("the customer's phone already has an active M-Pesa session (KCB: Duplicated MSISDN) — let them finish or cancel it, then retry")
		}
		s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = ? WHERE id = ? AND status = 'PENDING'`),
			truncStr(err.Error(), 200), pay.ID)
		s.Audit(p.ID, p.Username, "BUNI_MPESA_FAILED", "order", order.Number, err.Error())
		return nil, err
	}
	// Bind the checkout id ONLY while the leg is still pending — if it
	// was voided or re-pushed mid-flight, the live leg owns completion
	// and resurrecting this one would double the exposure. The claim
	// sweep still recovers recently-FAILED legs that DID get bound.
	res, err := s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'PENDING', checkout_request_id = ?, merchant_request_id = ?, result_desc = ? WHERE id = ? AND status = 'PENDING'`),
		mpesa.BuniCheckoutPrefix+resp.CheckoutRequestID, resp.MerchantRequestID, truncStr(resp.CustomerMessage, 200), pay.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		s.Audit(p.ID, p.Username, "BUNI_MPESA_ORPHAN", "order", order.Number,
			"checkout "+resp.CheckoutRequestID+" arrived on a leg that is no longer pending")
	}
	s.Audit(p.ID, p.Username, "BUNI_MPESA_INIT", "order", order.Number, "checkout "+resp.CheckoutRequestID)
	return s.GetOrder(order.ID)
}

// buniInvoiceRef is the customer-visible account reference the edge function
// appends to the till prefix, so the STK prompt reads "7853959-CDC" (KCB caps
// invoiceNumber at 12 chars; the till prefix leaves 4). Owner preference,
// Oct 2026: a stable brand tag instead of an order-number fragment.
// Correctness never depends on this ref — the CheckoutRequestID is the
// matching key — so a fixed display ref is fine.
func buniInvoiceRef(orderNumber string) string {
	// orderNumber is intentionally unused today; the parameter stays so the
	// call site documents what WOULD be folded in if KCB ever needs per-order
	// references (e.g. a dedicated short code).
	_ = orderNumber
	return "CDC"
}

// SweepBuni claims verified Buni callback events for this till's pending
// AND recently-failed Buni legs (late-PIN recovery), then times out pushes
// whose callback never arrived. Called from the Sweeper tick.
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

// stkLegExpired is the 3-minute STK window (inherited from the retired
// Daraja sweeper's timeout semantics).
func (s *Service) stkLegExpired(t time.Time) bool {
	return time.Since(t) > 3*time.Minute
}

// RecheckPayment re-verifies what can still be verified for this order's
// M-Pesa charges and completes the order when a provider confirms the
// money. This is the engine behind the "Check payment status" button: the
// recovery path for money that arrived after the till stopped watching. It
// never voids and never double-completes — completePayment stays the only
// PENDING → PAID transition, with all its guards intact.
//
// Since the v1.4.4 rail retirement there is no Paystack verify anymore:
// Buni legs are callback-driven (no query API) — the claim sweep owns
// their recovery — and plain STK legs can only be re-queried when a local
// provider exists (mock training mode today). Historical paystack rows are
// deliberately NOT re-verified: their rail is gone, the money report and
// refund-owed flags already describe them.
func (s *Service) RecheckPayment(orderID int64, p *auth.Principal) (*models.Order, string, error) {
	order, err := s.GetOrder(orderID)
	if err != nil {
		return nil, "", err
	}
	if order.Status == models.OrderPaid {
		return order, "This order is already paid.", nil
	}
	if order.Status != models.OrderPending {
		return nil, "", fmt.Errorf("%w: cannot recheck a %s order", ErrInvalidState, order.Status)
	}

	buniPending := false
	for _, pm := range order.Payments {
		if pm.Method == models.MethodMpesa && pm.Status == models.PaymentPending && strings.HasPrefix(pm.CheckoutRequestID, mpesa.BuniCheckoutPrefix) {
			buniPending = true
			break
		}
	}

	// Plain STK legs, when this till runs a local provider (mock training
	// mode today; the interface stays for future direct providers).
	if provider, perr := s.GetProvider(); provider != nil && perr == nil {
		for _, pm := range order.Payments {
			if pm.Method != models.MethodMpesa || pm.Status != models.PaymentPending || pm.CheckoutRequestID == "" || strings.HasPrefix(pm.CheckoutRequestID, mpesa.BuniCheckoutPrefix) {
				continue
			}
			res, err := provider.QuerySTK(context.Background(), pm.CheckoutRequestID)
			if err != nil || res.ResultCode != 0 {
				continue
			}
			order, err := s.completePayment(pm.ID, res.MpesaReceiptNumber, res.AmountCents, res.ResultDesc)
			if err != nil {
				return nil, "", err
			}
			s.Audit(p.ID, p.Username, "PAYMENT_RECHECK", "order", order.Number,
				fmt.Sprintf("receipt %s, money arrived after the till moved on", res.MpesaReceiptNumber))
			return order, "Payment confirmed, the M-Pesa money arrived. Sale complete.", nil
		}
	}

	if buniPending {
		return order, "Buni callbacks are claimed automatically every few seconds — if the customer paid, this sale completes by itself. Otherwise enter the M-Pesa receipt code manually.", nil
	}
	return order, "No M-Pesa charge on this order to re-check. Enter the M-Pesa receipt code manually if the customer paid.", nil
}
