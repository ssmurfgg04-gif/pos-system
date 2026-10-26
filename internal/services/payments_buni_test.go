package services

// payments_buni_test.go — the Buni (KCB M-Pesa Express) main-rail tests.
//
// v1.4.4: Buni is the ONLY real-money STK rail (Paystack + Daraja retired),
// so this file also hosts the shared STK-leg test service moved out of the
// retired paystack grace test file.
//
// Fixtures use the REAL production payload captured live on 2026-10-22
// (CheckoutRequestID ws_CO_22102026143354213700000000, result 1037) plus
// the spec's documented shapes for success/cancel/wrong-PIN. Buni's
// callback is byte-identical to the Daraja envelope, so these tests pin
// the whole relay: claim → ParseCallback → HandleCallback (fail-closed
// amount gate) with no new completion path.

import (
	"encoding/json"
	"errors"
	"fmt"
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

// seedSTKLegService prepares a cloud-linked test service with a printer
// worker (completePayment enqueues receipts).
// (Moved here from the retired paystack grace test file, v1.4.4.)
func seedSTKLegService(t *testing.T) *Service {
	t.Helper()
	s := newSyncTestService(t)
	s.printer = printer.NewWorker(s.db, s.settings)
	return s
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

// TestBuniRouteIsTheOnlySTKRail — v1.4.4: the paystack fallback route is
// GONE, so buni is the only way MpesaRoute() ever reports a real-money STK
// rail. With the vault row armed the route is buni; killing the local
// switch (or the cloud link) drops to manual — never to a retired rail.
func TestBuniRouteIsTheOnlySTKRail(t *testing.T) {
	s := seedSTKLegService(t)
	oldURL := cloudBaseURL
	srv := buniCloudFake(t, func(in map[string]any) map[string]any {
		return map[string]any{"ok": false, "error": "unused"}
	}, nil)
	cloudBaseURL = srv.URL
	t.Cleanup(func() { cloudBaseURL = oldURL })

	// No buni: the retired rails must NOT come back — route is manual.
	if got := s.MpesaRoute(); got != "manual" {
		t.Fatalf("route without buni = %q, want manual (no fallback rails exist)", got)
	}
	// Flip the vault row on: Buni takes over.
	armBuni(s)
	if got := s.MpesaRoute(); got != "buni" {
		t.Fatalf("route with buni armed = %q, want buni", got)
	}
	// Local kill-switch wins over the vault row.
	s.settings.Set("buni_enabled", "false")
	if got := s.MpesaRoute(); got != "manual" {
		t.Fatalf("route with buni disabled = %q, want manual", got)
	}
	s.settings.Set("buni_enabled", "true")
	// No cloud link → never buni.
	s.settings.Set("sync_disabled", "1")
	defer s.settings.Set("sync_disabled", "0")
	if got := s.MpesaRoute(); got == "buni" {
		t.Fatal("buni must require the cloud link")
	}
	// A till still set to the retired Daraja env never simulates money:
	// GetProvider refuses loudly.
	s.settings.Set("mpesa_env", "production")
	if p, err := s.GetProvider(); err == nil || p != nil {
		t.Fatal("retired daraja env must not yield a provider")
	}
	if got := s.MpesaRoute(); got != "manual" {
		t.Fatalf("route with retired daraja env = %q, want manual", got)
	}
}

// TestMpesaPhoneOK — the STK-leg phone validation (moved from the retired
// paystack wire-phone test; Buni uses the canonical 2547/2541 form).
func TestMpesaPhoneOK(t *testing.T) {
	good := []string{"254745000111", "254122345678", "0745000111", "0110123456", " 254745000111 "}
	for _, in := range good {
		if !mpesaPhoneOK(in) {
			t.Errorf("mpesaPhoneOK(%q) = false, want true", in)
		}
	}
	bad := []string{"", "254", "254845000111", "0845000111", "25474500011"}
	for _, in := range bad {
		if mpesaPhoneOK(in) {
			t.Errorf("mpesaPhoneOK(%q) = true, want false", in)
		}
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

// TestBuniSTKCooldownBlocksSecondPush — v1.4.4 double-push guard: a Buni
// push younger than the KCB USSD session window makes the next push for
// the same order refuse with ErrSTKPromptBusy (the 4999 "Duplicated
// MSISDN" storm), while an older push lifts the guard.
func TestBuniSTKCooldownBlocksSecondPush(t *testing.T) {
	s := seedSTKLegService(t)
	pushes := 0
	srv := buniCloudFake(t, func(in map[string]any) map[string]any {
		pushes++
		return map[string]any{"ok": true, "merchant_request_id": "m", "checkout_request_id": fmt.Sprintf("ws_CO_%d", pushes), "customer_message": "ok"}
	}, nil)
	oldURL := cloudBaseURL
	srvURL := srv.URL
	cloudBaseURL = srvURL
	t.Cleanup(func() { cloudBaseURL = oldURL })
	armBuni(s)
	p := &auth.Principal{ID: 1, Username: "admin", Active: true}

	orderID, _ := seedBuniLeg(t, s, "ws_CO_FRESH", false)
	// The Resend path (RetrySTKWithPhone) supersedes the live leg and
	// mints a FRESH pending leg before pushing — mirror that: the new leg
	// carries no checkout id yet, and the guard must see the young BUNI
	// leg beside it and refuse.
	res, err := s.db.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, phone, created_at)
                VALUES (?, 'mpesa', 'stk', 116000, 'PENDING', '254711000111', ?)`), orderID, nowStamp())
	if err != nil {
		t.Fatalf("seed resend leg: %v", err)
	}
	newPayID, _ := res.LastInsertId()
	order, err := s.GetOrder(orderID)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	var pay *models.Payment
	for i := range order.Payments {
		if order.Payments[i].ID == newPayID {
			pay = &order.Payments[i]
		}
	}
	if pay == nil {
		t.Fatal("resend leg not loaded")
	}
	if _, err := s.buniMpesaSTK(t.Context(), order, pay, p); !errors.Is(err, ErrSTKPromptBusy) {
		t.Fatalf("fresh push after a live push: want ErrSTKPromptBusy, got %v", err)
	}
	if pushes != 0 {
		t.Fatalf("KCB must not be called during the cooldown, got %d calls", pushes)
	}

	// Age every leg past the session window: the same leg pushes fine.
	future := time.Now().UTC().Add(-10 * time.Minute).Format(nowStampLayout)
	if _, err := s.db.Exec(`UPDATE payments SET created_at = ? WHERE order_id = ?`, future, orderID); err != nil {
		t.Fatalf("age leg: %v", err)
	}
	if _, err := s.buniMpesaSTK(t.Context(), order, pay, p); err != nil {
		t.Fatalf("push after session window: %v", err)
	}
	if pushes != 1 {
		t.Fatalf("pushes = %d, want 1", pushes)
	}

	// A leg created right now on a DIFFERENT order never blocks (the
	// guard is per-order, not per-till).
	otherID, _ := seedBuniLeg(t, s, "ws_CO_OTHER", true)
	other, err := s.GetOrder(otherID)
	if err != nil {
		t.Fatalf("other order: %v", err)
	}
	if s.stkPromptBusy(other.ID, other.Payments[len(other.Payments)-1].ID) {
		t.Fatal("per-order guard leaked across orders")
	}
}

// TestBuniDuplicatedMSISDNFriendlyError — the cross-order 4999 (session
// opened by another till/app on the same phone) is translated into an
// actionable message instead of the raw KCB string, and the leg fails
// closed (order stays PENDING; nothing is marked paid).
func TestBuniDuplicatedMSISDNFriendlyError(t *testing.T) {
	s := seedSTKLegService(t)
	srv := buniCloudFake(t, func(in map[string]any) map[string]any {
		return map[string]any{"ok": false, "error": "Duplicated MSISDN. MSISDN has an existing USSD Session"}
	}, nil)
	oldURL := cloudBaseURL
	srvURL := srv.URL
	cloudBaseURL = srvURL
	t.Cleanup(func() { cloudBaseURL = oldURL })
	armBuni(s)
	p := &auth.Principal{ID: 1, Username: "admin", Active: true}

	orderID, payID := seedBuniLeg(t, s, "ws_CO_LIVE1", true) // old leg → cooldown open
	order, err := s.GetOrder(orderID)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	pay := &order.Payments[len(order.Payments)-1]
	_, err = s.buniMpesaSTK(t.Context(), order, pay, p)
	if err == nil || strings.Contains(err.Error(), "buni proxy") {
		t.Fatalf("want translated friendly error, got %v", err)
	}
	if !strings.Contains(err.Error(), "active M-Pesa session") {
		t.Fatalf("error %q missing the actionable text", err)
	}
	var status, desc string
	if err := s.db.QueryRow(`SELECT status, result_desc FROM payments WHERE id = ?`, payID).Scan(&status, &desc); err != nil {
		t.Fatalf("read leg: %v", err)
	}
	if status != "FAILED" || !strings.Contains(desc, "Duplicated MSISDN") {
		t.Fatalf("leg status=%s desc=%q, want FAILED with the KCB detail for audit", status, desc)
	}
	if order.Status != models.OrderPending {
		t.Fatalf("order must stay PENDING, got %s", order.Status)
	}
}
