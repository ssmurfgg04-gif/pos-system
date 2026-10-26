package handlers_test

// recovery_probe_test.go — late-money recovery semantics after the v1.4.4
// rail retirement (Paystack/Daraja removed; Buni is the only STK rail).
//
// The original field report: a KES 10 STK sale confirmed by M-Pesa 21
// minutes after the push, when nothing was watching anymore. Two guards
// remain in the Buni world:
//
//  1. SweepBuni — claims exactly-once from the cloud event log every few
//     seconds, INCLUDING recently-FAILED legs that were bound to a push
//     (a customer who pays a superseded/timed-out push still completes
//     the sale). Covered with real fixtures in services/payments_buni_test.go.
//  2. RecheckPayment (POST /orders/:id/recheck) — the cashier's "Check
//     payment status" button: re-queries plain STK legs when a local
//     provider exists (mock training mode here), answers with guidance
//     for callback-driven Buni legs, and NEVER re-completes a paid order.
//
// These tests pin the provider-agnostic invariants: idempotency (one
// completed leg, ever), no resurrection of paid orders, and honest
// messages when there is nothing to verify.

import (
	"testing"

	"posapp/internal/models"
)

func TestRecheckPaymentIdempotentAndHonest(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)

	// A paid order (manual receipt — deterministic, no provider involved).
	w := do(t, engine, "POST", "/api/v1/orders/checkout", admin, checkoutBody(map[string]any{
		"paymentMethod": "mpesa", "paymentMode": "manual",
	}))
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
	}
	oid := orderIDFromBody(t, w.Body.Bytes())
	if c := a5Code(t, engine, "POST", "/api/v1/orders/"+itoa64(oid)+"/manual", admin,
		map[string]any{"receiptCode": "QA9Z8Y7X6W"}); c != 200 {
		t.Fatalf("manual confirm: %d", c)
	}

	// Recheck on a PAID order: honest message, and the second completion
	// must never happen (the manual code and any late charge are the
	// SAME money — the old double-count bug).
	w = do(t, engine, "POST", "/api/v1/orders/"+itoa64(oid)+"/recheck", admin, nil)
	if w.Code != 200 {
		t.Fatalf("recheck paid: %d %s", w.Code, w.Body.String())
	}
	w = do(t, engine, "GET", "/api/v1/orders/"+itoa64(oid), admin, nil)
	if w.Code != 200 {
		t.Fatalf("order after recheck: %d", w.Code)
	}
	d := dataMap(t, w)
	if d["status"] != models.OrderPaid {
		t.Fatalf("recheck paid order status = %v, want PAID", d["status"])
	}
	completed := 0
	for _, leg := range d["payments"].([]any) {
		if leg.(map[string]any)["status"] == "COMPLETED" {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("idempotency broken: %d completed legs (want 1)", completed)
	}

	// A PENDING order with no verifiable charge: honest guidance, order
	// untouched.
	w = do(t, engine, "POST", "/api/v1/orders/checkout", admin, checkoutBody(map[string]any{
		"paymentMethod": "mpesa", "paymentMode": "manual",
	}))
	oid2 := orderIDFromBody(t, w.Body.Bytes())
	w = do(t, engine, "POST", "/api/v1/orders/"+itoa64(oid2)+"/recheck", admin, nil)
	if w.Code != 200 {
		t.Fatalf("recheck pending: %d %s", w.Code, w.Body.String())
	}
	w = do(t, engine, "GET", "/api/v1/orders/"+itoa64(oid2), admin, nil)
	if got := dataMap(t, w)["status"]; got != models.OrderPending {
		t.Fatalf("recheck with no money must not complete the sale, got %v", got)
	}
}
