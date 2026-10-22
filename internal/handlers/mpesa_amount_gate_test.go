package handlers_test

import (
        "fmt"
        "net/http/httptest"
        "testing"
)

// Batch A hardening (REVIEW-3 critical #4): the public M-Pesa callback used to
// trust the payload's amount once the CheckoutRequestID matched — completePayment
// only *flagged* a mismatch and still flipped the order to PAID. A forged
// callback spoofing a CheckoutRequestID with a 1-shilling success could settle a
// KES 50,000 order over the internet. completePayment is now a hard gate: a
// completing result that does not carry the exact stored amount fails closed
// (order PENDING, payment FAILED, audited) — and an authoritative result (STK
// sweeper re-query / manual recheck) still completes the leg afterwards.
func TestForgedCallbackWrongAmountCannotComplete(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        a5SetSetting(t, engine, admin, "mpesa_mock_delay_ms", "60000")

        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items":         []map[string]any{{"productId": 1, "qty": 1}},
                "paymentMethod": "mpesa", "paymentMode": "stk",
                "customerPhone": "0722123456", "clientUuid": "amount-gate-1",
        })
        if w.Code != 201 {
                t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
        }
        order := dataMap(t, w)
        oid := int64(order["id"].(float64))
        pays, _ := order["payments"].([]any)
        p0, _ := pays[0].(map[string]any)
        coID, _ := p0["checkoutRequestId"].(string)
        if coID == "" {
                t.Fatalf("no checkout id: %v", p0)
        }
        stored := int64(p0["amountCents"].(float64))
        if stored <= 0 {
                t.Fatalf("stored amount must be positive, got %d", stored)
        }

        forged := func(amount float64, receipt string) map[string]any {
                return map[string]any{
                        "Body": map[string]any{"stkCallback": map[string]any{
                                "MerchantRequestID": "m-forge", "CheckoutRequestID": coID,
                                "ResultCode": 0, "ResultDesc": "success",
                                "CallbackMetadata": map[string]any{"Item": []map[string]any{
                                        {"Name": "Amount", "Value": amount},
                                        {"Name": "MpesaReceiptNumber", "Value": receipt},
                                }},
                        }},
                }
        }
        readOrder := func() (map[string]any, map[string]any) {
                w := do(t, engine, "GET", fmt.Sprintf("/api/v1/orders/%d", oid), cashier, nil)
                if w.Code != 200 {
                        t.Fatalf("order read: %d %s", w.Code, w.Body.String())
                }
                o := dataMap(t, w)
                pays, _ := o["payments"].([]any)
                p, _ := pays[0].(map[string]any)
                return o, p
        }

        // 1. Forged success with a 1-shilling amount: the endpoint acks 200
        // (no information leak to the forger) but the sale must NOT complete.
        if c := do(t, engine, "POST", "/api/v1/payments/mpesa/callback", "", forged(1.0, "FAKE111111")).Code; c != 200 {
                t.Fatalf("forged callback should ack 200, got %d", c)
        }
        o, p := readOrder()
        if o["status"] != "PENDING" {
                t.Fatalf("FORGERY WINDOW: forged amount completed order (status=%v) — the hard gate failed", o["status"])
        }
        if o["discrepancy"] != true {
                t.Fatalf("forged amount must raise the staff discrepancy tell-tale")
        }
        if p["status"] != "FAILED" {
                t.Fatalf("payment must be FAILED after forged amount, got %v (desc=%v)", p["status"], p["resultDesc"])
        }

        // 2. Zero/missing amount is equally unverified — never completes.
        cb0 := map[string]any{
                "Body": map[string]any{"stkCallback": map[string]any{
                        "MerchantRequestID": "m-forge2", "CheckoutRequestID": coID,
                        "ResultCode": 0, "ResultDesc": "success",
                        "CallbackMetadata": map[string]any{"Item": []map[string]any{
                                {"Name": "MpesaReceiptNumber", "Value": "FAKE222222"},
                        }},
                }},
        }
        if c := do(t, engine, "POST", "/api/v1/payments/mpesa/callback", "", cb0).Code; c != 200 {
                t.Fatalf("zero-amount callback ack: %d", c)
        }
        if o, _ = readOrder(); o["status"] == "PAID" {
                t.Fatalf("zero-amount callback completed the order — gate bypassed")
        }

        // 3. No over-blocking: the authoritative result (correct amount, as the
        // STK-Query sweeper re-query or /recheck would deliver) completes the
        // sale and heals the FAILED marking.
        if c := do(t, engine, "POST", "/api/v1/payments/mpesa/callback", "", forged(float64(stored)/100, "REAL333333")).Code; c != 200 {
                t.Fatalf("real callback ack: %d", c)
        }
        o, p = readOrder()
        if o["status"] != "PAID" {
                t.Fatalf("correct-amount callback must complete the order (status=%v)", o["status"])
        }
        if p["status"] != "COMPLETED" || p["mpesaReceipt"] != "REAL333333" {
                t.Fatalf("payment must be COMPLETED with the real receipt, got %v / %v", p["status"], p["mpesaReceipt"])
        }
        // Note: the order-level discrepancy flag set by the earlier forgery
        // attempt intentionally stays visible for staff — completion never
        // silently clears a raised tell-tale.
}

// CORS hardening (REVIEW-3 critical #2/#3): non-allowlisted origins get no
// ACAO header (browsers block the read) and preflights get 403, while
// same-origin clients (the embedded SPA) are unaffected.
func TestCORSWildcardRemoved(t *testing.T) {
        engine, _, _, _ := newTestServer(t)

        req := httptest.NewRequest("GET", "/api/v1/auth/pin-users", nil)
        req.Header.Set("Origin", "https://evil.example")
        w := httptest.NewRecorder()
        engine.ServeHTTP(w, req)
        if acao := w.Header().Get("Access-Control-Allow-Origin"); acao != "" {
                t.Fatalf("cross-origin response must not carry ACAO (enumeration surface), got %q", acao)
        }

        pre := httptest.NewRequest("OPTIONS", "/api/v1/auth/pin-users", nil)
        pre.Header.Set("Origin", "https://evil.example")
        pre.Header.Set("Access-Control-Request-Method", "GET")
        pw := httptest.NewRecorder()
        engine.ServeHTTP(pw, pre)
        if pw.Code != 403 {
                t.Fatalf("cross-origin preflight must be 403, got %d", pw.Code)
        }
        if acao := pw.Header().Get("Access-Control-Allow-Origin"); acao != "" {
                t.Fatalf("preflight for disallowed origin must not carry ACAO, got %q", acao)
        }

        // Same-origin (no Origin header) keeps working — the SPA must not break.
        if c := do(t, engine, "GET", "/api/v1/auth/pin-users", "", nil).Code; c != 200 {
                t.Fatalf("same-origin pin-users broke: %d", c)
        }
}
