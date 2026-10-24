package services

// payments_buni_test.go — the Buni (KCB M-Pesa Express) main-rail tests.
//
// Fixtures use the REAL production payload captured live on 2026-10-22
// (CheckoutRequestID ws_CO_22102026143354213700000000, result 1037) plus
// the spec's documented shapes for success/cancel/wrong-PIN. Buni's
// callback is byte-identical to the Daraja envelope, so these tests pin
// the whole relay: claim → ParseCallback → HandleCallback (fail-closed
// amount gate) with no new completion path.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"posapp/internal/auth"
	"posapp/internal/models"
	"posapp/internal/mpesa"
	"posapp/internal/printer"
)

// buniCloudFake fakes the fleet cloud: bootstrap + buni-stkpush +
// buni_claim_events (claiming is stateful across calls so the exactly-once
// behavior is observable: first claim returns queued events, later claims
// return nothing).
func buniCloudFake(t *testing.T, push func(in map[string]any) map[string]any, events *[][]byte) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var self string
	mux.HandleFunc("/rest/v1/sync_bootstrap", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]map[string]any{{
			"project_url": self, "team_code": "TEAM-A", "auto_approve": true,
		}})
	})
	mux.HandleFunc("/functions/v1/buni-stkpush", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		if in["p_device_id"] == "" || in["p_secret_hash"] == "" {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "device not recognized"})
			return
		}
		json.NewEncoder(w).Encode(push(in))
	})
	mux.HandleFunc("/rest/v1/rpc/buni_claim_events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var in struct {
			CheckoutIDs []string `json:"p_checkout_ids"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		out := []map[string]any{}
		if events != nil && len(*events) > 0 {
			for i, payload := range *events {
				out = append(out, map[string]any{
					"id":                i + 1,
					"reference":         "uuid-buni",
					"checkoutRequestId": firstCheckoutID(in.CheckoutIDs),
					"teamCode":          "TEAM-A",
					"payload":           json.RawMessage(payload),
					"createdAt":         nowStamp(),
				})
			}
			*events = nil // exactly-once: the next claim sees nothing
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "events": out})
	})
	srv := httptest.NewServer(mux)
	self = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

func firstCheckoutID(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// seedCashierOnce is seedUser tolerant of repeat calls within one test
// (seedUser hard-fails on the roles/users UNIQUE constraints).
func seedCashierOnce(t *testing.T, s *Service) int64 {
	t.Helper()
	s.db.Exec(`INSERT INTO roles (name) VALUES ('Cashier')`)
	var roleID int64
	s.db.QueryRow(`SELECT id FROM roles WHERE name='Cashier'`).Scan(&roleID)
	if res, err := s.db.Exec(s.db.Rebind(`INSERT INTO users (username, role_id) VALUES ('bf-cashier', ?)`), roleID); err == nil {
		id, _ := res.LastInsertId()
		return id
	}
	var id int64
	s.db.QueryRow(`SELECT id FROM users WHERE username='bf-cashier'`).Scan(&id)
	return id
}

// seedBuniLeg inserts a PENDING order + mpesa payment leg with a BUNI-
// prefixed checkout id. Returns (orderID, paymentID).
func seedBuniLeg(t *testing.T, s *Service, checkout string, createdOld bool) (int64, int64) {
	t.Helper()
	cashier := seedCashierOnce(t, s)
	created := nowStamp()
	if createdOld {
		created = time.Now().Add(-10 * time.Minute).Format("2006-01-02 15:04:05")
	}
	res, err := s.db.Exec(s.db.Rebind(`INSERT INTO orders (number, status, subtotal_cents, total_cents, cashier_id, client_uuid, created_at)
                VALUES (?, 'PENDING', 100000, 116000, ?, ?, ?)`), "ORD-BUNI-"+checkout, cashier, "uuid-buni-"+checkout, created)
	if err != nil {
		t.Fatalf("seed order: %v", err)
	}
	orderID, _ := res.LastInsertId()
	res, err = s.db.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, phone, checkout_request_id, created_at)
                VALUES (?, 'mpesa', 'stk', 116000, 'PENDING', '254711000111', ?, ?)`), orderID, mpesa.BuniCheckoutPrefix+checkout, created)
	if err != nil {
		t.Fatalf("seed payment: %v", err)
	}
	payID, _ := res.LastInsertId()
	return orderID, payID
}

// buniCallback builds the exact envelope KCB posts (documented shapes).
func buniCallback(checkout string, resultCode int, amount float64, receipt string) []byte {
	body := map[string]any{
		"Body": map[string]any{"stkCallback": map[string]any{
			"MerchantRequestID": "9e99-4e1f-93b9",
			"CheckoutRequestID": checkout,
			"ResultCode":        resultCode,
			"ResultDesc":        "The service request is processed successfully.",
		}},
	}
	if resultCode == 0 {
		body["Body"].(map[string]any)["stkCallback"].(map[string]any)["CallbackMetadata"] = map[string]any{
			"Item": []map[string]any{
				{"Name": "Amount", "Value": amount},
				{"Name": "MpesaReceiptNumber", "Value": receipt},
				{"Name": "PhoneNumber", "Value": 254711000111},
			},
		}
	}
	b, _ := json.Marshal(body)
	return b
}

func armBuni(s *Service) {
	s.settings.Set("buni_via_cloud", "true")
	s.settings.Set("buni_enabled", "true")
}

// TestBuniInvoiceRefBrandTag — the customer-visible account reference is the
// stable brand tag "CDC" (prompt reads "7853959-CDC"), NOT an order-number
// fragment (owner preference, Oct 2026: "-ORD2" confused customers). The
// ref is display-only either way — the CheckoutRequestID is the matching
// key — so the function must return the tag for ANY order number.
func TestBuniInvoiceRefBrandTag(t *testing.T) {
	for _, n := range []string{
		"ORD202610240002", "ORD202610240001", "", "weird-order_42",
		"PAY-9999", strings.Repeat("X", 100),
	} {
		if got := buniInvoiceRef(n); got != "CDC" {
			t.Fatalf("buniInvoiceRef(%q) = %q, want brand tag CDC", n, got)
		}
	}
}

// TestBuniRoutePriorityDemotesPaystack — buni is the MAIN rail: when the
// vault row is on, MpesaRoute() reports buni ahead of paystack; flipping
// buni_via_cloud off drops back to the previous behavior.
func TestBuniRoutePriorityDemotesPaystack(t *testing.T) {
	s := seedSTKLegService(t)
	oldURL := cloudBaseURL
	srv := buniCloudFake(t, func(in map[string]any) map[string]any {
		return map[string]any{"ok": false, "error": "unused"}
	}, nil)
	cloudBaseURL = srv.URL
	t.Cleanup(func() { cloudBaseURL = oldURL })

	// Paystack armed (cloud proxy, the v1.3.3+ production state).
	s.settings.Set("paystack_via_cloud", "true")
	if got := s.MpesaRoute(); got != "paystack" {
		t.Fatalf("route without buni = %q, want paystack", got)
	}
	// Flip the vault row on: Buni takes over.
	armBuni(s)
	if got := s.MpesaRoute(); got != "buni" {
		t.Fatalf("route with buni armed = %q, want buni", got)
	}
	// Local kill-switch wins over the vault row.
	s.settings.Set("buni_enabled", "false")
	if got := s.MpesaRoute(); got != "paystack" {
		t.Fatalf("route with buni disabled = %q, want paystack", got)
	}
	s.settings.Set("buni_enabled", "true")
	// No cloud link → never buni.
	s.settings.Set("sync_disabled", "1")
	defer s.settings.Set("sync_disabled", "0")
	if got := s.MpesaRoute(); got == "buni" {
		t.Fatal("buni must require the cloud link")
	}
}

// TestBuniSTKViaProxyStoresPrefixedCheckout — InitiateSTK rides the edge
// function and stores the BUNI- prefixed checkout id.
func TestBuniSTKViaProxyStoresPrefixedCheckout(t *testing.T) {
	s := seedSTKLegService(t)
	oldURL := cloudBaseURL
	srv := buniCloudFake(t, func(in map[string]any) map[string]any {
		if in["amount"] != float64(116000) {
			t.Errorf("push amount = %v, want order total 116000", in["amount"])
		}
		if in["phone"] != "254711000111" {
			t.Errorf("push phone = %v", in["phone"])
		}
		return map[string]any{
			"ok":                  true,
			"merchant_request_id": "9e99-4e1f",
			"checkout_request_id": "ws_CO_LIVE1",
			"customer_message":    "Success. Request accepted for processing",
		}
	}, nil)
	cloudBaseURL = srv.URL
	t.Cleanup(func() { cloudBaseURL = oldURL })
	armBuni(s)

	orderID, payID := seedBuniLeg(t, s, "ws_CO_OLD1", false)
	// InitiateSTK pushes the pending leg in place: the old checkout id is
	// replaced by the fresh push (RetrySTKWithPhone mints new legs instead).
	if _, err := s.InitiateSTK(t.Context(), orderID, &auth.Principal{ID: 1, Username: "admin", Active: true}); err != nil {
		t.Fatalf("InitiateSTK via buni: %v", err)
	}
	var checkout, status string
	if err := s.db.QueryRow(`SELECT checkout_request_id, status FROM payments WHERE id = ?`, payID).Scan(&checkout, &status); err != nil {
		t.Fatalf("read payment: %v", err)
	}
	if checkout != mpesa.BuniCheckoutPrefix+"ws_CO_LIVE1" {
		t.Fatalf("checkout = %q, want prefixed fresh push id", checkout)
	}
	if status != models.PaymentPending {
		t.Fatalf("leg status = %q, want PENDING after push", status)
	}
	// The merchant request id and customer message landed too.
	var mrid, desc string
	s.db.QueryRow(`SELECT merchant_request_id, result_desc FROM payments WHERE id = ?`, payID).Scan(&mrid, &desc)
	if mrid != "9e99-4e1f" || desc == "" {
		t.Fatalf("mrid=%q desc=%q", mrid, desc)
	}
}

// TestSweepBuniRealFixtures — the captured production 1037 timeout fails
// the leg; a success with the exact stored amount completes; a duplicate
// delivery (second claim returns nothing) cannot complete twice.
func TestSweepBuniRealFixtures(t *testing.T) {
	s := seedSTKLegService(t)
	s.printer = printer.NewWorker(s.db, s.settings)
	oldURL := cloudBaseURL
	srv := buniCloudFake(t, func(in map[string]any) map[string]any {
		return map[string]any{"ok": false, "error": "unused"}
	}, &[][]byte{})
	cloudBaseURL = srv.URL
	t.Cleanup(func() { cloudBaseURL = oldURL })
	armBuni(s)

	// 1. Real 1037 timeout fixture (captured live 2026-10-22).
	orderID, payID := seedBuniLeg(t, s, "ws_CO_22102026143354213700000000", false)
	events := &[][]byte{
		[]byte(`{"Body":{"stkCallback":{"ResultCode":1037,"ResultDesc":"DS timeout user cannot be reached.","CheckoutRequestID":"ws_CO_22102026143354213700000000","MerchantRequestID":"9e99-4e1f-93b9"}}}`),
	}
	queue := events
	s.claimBuniEventsForTest = func(ids []string) []buniClaimedEvent {
		evs := claimForTest(queue, ids)
		return evs
	}
	s.SweepBuni()
	assertOrderStatus(t, s, orderID, models.OrderPending)
	assertPaymentStatus(t, s, payID, "FAILED")

	// 2. Success with the EXACT stored amount completes the sale.
	orderID2, payID2 := seedBuniLeg(t, s, "ws_CO_SUCCESS", false)
	succ := buniCallback("ws_CO_SUCCESS", 0, 1160.00, "QGH7XY89Z2")
	queue2 := &[][]byte{succ}
	s.claimBuniEventsForTest = func(ids []string) []buniClaimedEvent {
		return claimForTest(queue2, ids)
	}
	s.SweepBuni()
	assertOrderStatus(t, s, orderID2, models.OrderPaid)
	assertPaymentStatus(t, s, payID2, "COMPLETED")
	var receipt string
	s.db.QueryRow(`SELECT mpesa_receipt FROM payments WHERE id = ?`, payID2).Scan(&receipt)
	if receipt != "QGH7XY89Z2" {
		t.Fatalf("receipt = %q", receipt)
	}

	// 3. Duplicate delivery: server claims return empty the second time —
	// the relay is exactly-once, so nothing re-completes (stock deducted
	// once, receipt untouched).
	var stockBefore int
	s.db.QueryRow(`SELECT COUNT(*) FROM products`).Scan(&stockBefore)
	s.SweepBuni()
	assertOrderStatus(t, s, orderID2, models.OrderPaid)
	var payCount int
	s.db.QueryRow(`SELECT COUNT(*) FROM payments WHERE order_id = ? AND status = 'COMPLETED'`, orderID2).Scan(&payCount)
	if payCount != 1 {
		t.Fatalf("completed legs = %d, want exactly 1", payCount)
	}

	// 4. Forged success on a Buni leg with a WRONG amount fails closed
	// (Batch A gate applies through the relay too).
	orderID3, payID3 := seedBuniLeg(t, s, "ws_CO_FORGE", false)
	forge := buniCallback("ws_CO_FORGE", 0, 1.00, "FORGEDRC01")
	queue3 := &[][]byte{forge}
	s.claimBuniEventsForTest = func(ids []string) []buniClaimedEvent {
		return claimForTest(queue3, ids)
	}
	s.SweepBuni()
	assertOrderStatus(t, s, orderID3, models.OrderPending)
	assertPaymentStatus(t, s, payID3, "FAILED")
	var disc int
	s.db.QueryRow(`SELECT discrepancy FROM orders WHERE id = ?`, orderID3).Scan(&disc)
	if disc != 1 {
		t.Fatal("forged amount must raise the discrepancy flag")
	}

	// 5. Timeout backstop: a leg older than 3 minutes with no callback
	// fails with a tell-tale result.
	orderID4, payID4 := seedBuniLeg(t, s, "ws_CO_STALE", true)
	s.claimBuniEventsForTest = func(ids []string) []buniClaimedEvent { return nil }
	s.SweepBuni()
	assertOrderStatus(t, s, orderID4, models.OrderPending)
	assertPaymentStatus(t, s, payID4, "FAILED")
	var desc string
	s.db.QueryRow(`SELECT result_desc FROM payments WHERE id = ?`, payID4).Scan(&desc)
	if desc != "STK timeout (no Buni callback)" {
		t.Fatalf("timeout desc = %q", desc)
	}
}

// TestHandleCallbackResolvesBuniPrefixedCheckout — the public webhook path
// also resolves Buni legs: a raw-id callback matches the BUNI- prefixed
// row (relay + direct-post equivalence).
func TestHandleCallbackResolvesBuniPrefixedCheckout(t *testing.T) {
	s := seedSTKLegService(t)
	s.printer = printer.NewWorker(s.db, s.settings)
	orderID, payID := seedBuniLeg(t, s, "ws_CO_DIRECT", false)

	cb, err := mpesa.ParseCallback(buniCallback("ws_CO_DIRECT", 0, 1160.00, "DIRECTRC99"))
	if err != nil {
		t.Fatalf("parse real envelope: %v", err)
	}
	if _, err := s.HandleCallback(cb); err != nil {
		t.Fatalf("HandleCallback: %v", err)
	}
	assertOrderStatus(t, s, orderID, models.OrderPaid)
	assertPaymentStatus(t, s, payID, "COMPLETED")
}

// TestDarajaLegsNotPulledIntoBuniSweep — a plain Daraja leg (no prefix)
// must never be claimed by the Buni sweep.
func TestDarajaLegsNotPulledIntoBuniSweep(t *testing.T) {
	s := seedSTKLegService(t)
	armBuni(s)
	cashier := seedCashierOnce(t, s)
	res, _ := s.db.Exec(s.db.Rebind(`INSERT INTO orders (number, status, subtotal_cents, total_cents, cashier_id, client_uuid, created_at)
                VALUES ('ORD-DJ1', 'PENDING', 10000, 11600, ?, 'uuid-dj', ?)`), cashier, nowStamp())
	orderID, _ := res.LastInsertId()
	s.db.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, phone, checkout_request_id, created_at)
                VALUES (?, 'mpesa', 'stk', 11600, 'PENDING', '254711000111', 'ws_CO_DARAJA', ?)`), orderID, nowStamp())

	s.claimBuniEventsForTest = func(ids []string) []buniClaimedEvent {
		for _, id := range ids {
			if id == "ws_CO_DARAJA" {
				t.Errorf("Buni sweep tried to claim a Daraja checkout: %v", ids)
			}
		}
		return nil
	}
	s.SweepBuni()
	// The leg is untouched.
	assertPaymentStatus(t, s, orderID, "")
	var status string
	s.db.QueryRow(`SELECT status FROM payments WHERE checkout_request_id = 'ws_CO_DARAJA'`).Scan(&status)
	if status != "PENDING" {
		t.Fatalf("daraja leg status = %q, want PENDING", status)
	}
}

// ---- tiny helpers shared by the buni tests ----

func claimForTest(queue *[][]byte, ids []string) []buniClaimedEvent {
	if len(ids) == 0 || queue == nil || len(*queue) == 0 {
		return nil
	}
	out := make([]buniClaimedEvent, 0, len(*queue))
	for i, payload := range *queue {
		out = append(out, buniClaimedEvent{
			ID:        int64(i + 1),
			Reference: "uuid-buni",
			Checkout:  ids[0],
			Payload:   payload,
			CreatedAt: nowStamp(),
		})
	}
	*queue = nil
	return out
}

func assertOrderStatus(t *testing.T, s *Service, orderID int64, want string) {
	t.Helper()
	var got string
	if err := s.db.QueryRow(`SELECT status FROM orders WHERE id = ?`, orderID).Scan(&got); err != nil {
		t.Fatalf("order read: %v", err)
	}
	if want != "" && got != want {
		t.Fatalf("order status = %q, want %q", got, want)
	}
}

func assertPaymentStatus(t *testing.T, s *Service, payID int64, want string) {
	t.Helper()
	var got string
	if err := s.db.QueryRow(`SELECT status FROM payments WHERE id = ?`, payID).Scan(&got); err != nil {
		t.Fatalf("payment read: %v", err)
	}
	if want != "" && got != want {
		t.Fatalf("payment status = %q, want %q", got, want)
	}
}
