package handlers_test

// sales_visibility_test.go — v19 sales-visibility guarantees, end to end
// through the real router:
//
//      1. staff without orders.view.all see ONLY their own sales in list,
//         detail, receipt, events, notes, void and customer-history — no
//         matter what ids or filters they send (URL/ID probing finds nothing);
//      2. Admin/Owner cross-till oversight keeps working;
//      3. every sale carries its originating till (device_id) and the daily
//         report exposes the per-till breakdown;
//      4. other staff members' shift X/Z reports need reports.view.
//
// The seeded harness boots the real engine (handlers_test.go): admin and
// cashier both hold orders.view + pos.void — the v19 change is DATA
// scoping, not permission stripping, so these tests prove the scoping.

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/gin-gonic/gin"
)

func orderIDFromBody(t *testing.T, body []byte) int64 {
	t.Helper()
	var env struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("order decode: %v", err)
	}
	return env.Data.ID
}

// checkoutCash rings up one cash sale for the token holder and returns
// the order id. Seed catalog product 1 ("Test Product") is 100.00.
func checkoutCash(t *testing.T, engine *gin.Engine, token string) int64 {
	t.Helper()
	w := do(t, engine, "POST", "/api/v1/orders/checkout", token, checkoutBody(map[string]any{
		"paymentMethod": "cash",
	}))
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
	}
	id := orderIDFromBody(t, w.Body.Bytes())
	if id == 0 {
		t.Fatalf("checkout returned no id: %s", w.Body.String())
	}
	return id
}

func TestCashierSeesOnlyOwnSales(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	adminOrder := checkoutCash(t, engine, admin)
	cashierOrder := checkoutCash(t, engine, cashier)

	// Cashier list: only their own sale, whatever filters they send.
	for _, q := range []string{"", "?cashierId=1", "?status=PAID", fmt.Sprintf("?cashierId=%d", 1)} {
		w := do(t, engine, "GET", "/api/v1/orders"+q, cashier, nil)
		if w.Code != 200 {
			t.Fatalf("cashier list %q: %d %s", q, w.Code, w.Body.String())
		}
		var env struct {
			Data []struct {
				ID     int64  `json:"id"`
				Number string `json:"number"`
			} `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &env)
		for _, o := range env.Data {
			if o.ID == adminOrder {
				t.Fatalf("cashier list %q leaked admin order %d", q, adminOrder)
			}
		}
		found := false
		for _, o := range env.Data {
			if o.ID == cashierOrder {
				found = true
			}
		}
		if !found {
			t.Fatalf("cashier list %q missing their own order %d", q, cashierOrder)
		}
	}

	// Admin list: cross-till oversight intact — both orders visible.
	w := do(t, engine, "GET", "/api/v1/orders", admin, nil)
	if w.Code != 200 {
		t.Fatalf("admin list: %d", w.Code)
	}
	var env struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &env)
	seenAdmin, seenCashier := false, false
	for _, o := range env.Data {
		if o.ID == adminOrder {
			seenAdmin = true
		}
		if o.ID == cashierOrder {
			seenCashier = true
		}
	}
	if !seenAdmin || !seenCashier {
		t.Fatalf("admin must see both orders (admin=%v cashier=%v)", seenAdmin, seenCashier)
	}
}

func TestCashierCannotOpenOthersOrder(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	adminOrder := checkoutCash(t, engine, admin)
	cashierOrder := checkoutCash(t, engine, cashier)

	// The whole detail surface hides out-of-scope orders as 404 —
	// existence itself is not disclosed.
	probes := []struct {
		method, path string
		body         any
	}{
		{"GET", fmt.Sprintf("/api/v1/orders/%d", adminOrder), nil},
		{"GET", fmt.Sprintf("/api/v1/orders/%d/receipt", adminOrder), nil},
		{"GET", fmt.Sprintf("/api/v1/orders/%d/events", adminOrder), nil},
		{"POST", fmt.Sprintf("/api/v1/orders/%d/notes", adminOrder), map[string]any{"message": "probe"}},
		{"POST", fmt.Sprintf("/api/v1/orders/%d/void", adminOrder), map[string]any{"reason": "Wrong item"}},
	}
	for _, p := range probes {
		w := do(t, engine, p.method, p.path, cashier, p.body)
		if w.Code == 200 || w.Code == 201 {
			t.Fatalf("%s %s must NOT succeed for a cashier on someone else's order", p.method, p.path)
		}
		if w.Code != 404 {
			t.Fatalf("%s %s: want 404 (indistinguishable), got %d %s", p.method, p.path, w.Code, w.Body.String())
		}
	}

	// Own order stays fully operational for the cashier.
	w := do(t, engine, "GET", fmt.Sprintf("/api/v1/orders/%d", cashierOrder), cashier, nil)
	if w.Code != 200 {
		t.Fatalf("own order detail: %d %s", w.Code, w.Body.String())
	}
	// Admin opens the cashier's order (oversight surface).
	w = do(t, engine, "GET", fmt.Sprintf("/api/v1/orders/%d", cashierOrder), admin, nil)
	if w.Code != 200 {
		t.Fatalf("admin detail of cashier order: %d", w.Code)
	}
	// Detail carries the originating till.
	d := dataMap(t, w)
	if dev, _ := d["deviceId"].(string); dev == "" {
		t.Fatalf("order %d missing deviceId attribution", cashierOrder)
	}
}

func TestCustomerHistoryScoped(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	w := do(t, engine, "POST", "/api/v1/customers", admin,
		map[string]any{"name": "History Probe", "phone": "0720003004"})
	if w.Code != 201 {
		t.Fatalf("customer: %d %s", w.Code, w.Body.String())
	}
	cid := int64(dataMap(t, w)["id"].(float64))

	// One sale each, both linked to the same customer.
	aw := do(t, engine, "POST", "/api/v1/orders/checkout", admin, checkoutBody(map[string]any{
		"paymentMethod": "cash", "customerId": cid, "customerName": "History Probe",
	}))
	cw := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, checkoutBody(map[string]any{
		"paymentMethod": "cash", "customerId": cid, "customerName": "History Probe",
	}))
	if aw.Code != 200 && aw.Code != 201 {
		t.Fatalf("admin checkout: %d %s", aw.Code, aw.Body.String())
	}
	if cw.Code != 200 && cw.Code != 201 {
		t.Fatalf("cashier checkout: %d %s", cw.Code, cw.Body.String())
	}
	adminOrder := orderIDFromBody(t, aw.Body.Bytes())
	cashierOrder := orderIDFromBody(t, cw.Body.Bytes())

	w = do(t, engine, "GET", fmt.Sprintf("/api/v1/customers/%d/orders", cid), cashier, nil)
	if w.Code != 200 {
		t.Fatalf("cashier customer history: %d", w.Code)
	}
	var env struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &env)
	for _, o := range env.Data {
		if o.ID == adminOrder {
			t.Fatalf("customer history leaked admin sale %d to cashier", adminOrder)
		}
	}
	if len(env.Data) != 1 || env.Data[0].ID != cashierOrder {
		t.Fatalf("cashier should see exactly their own sale, got %v", env.Data)
	}

	w = do(t, engine, "GET", fmt.Sprintf("/api/v1/customers/%d/orders", cid), admin, nil)
	json.Unmarshal(w.Body.Bytes(), &env)
	if len(env.Data) != 2 {
		t.Fatalf("admin customer history: want 2 sales, got %d", len(env.Data))
	}
}

func TestShiftReportOfOthersNeedsReportsView(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	// Both open shifts (shifts.manage is on every till role).
	w := do(t, engine, "POST", "/api/v1/shifts/open", admin, map[string]any{"openingFloatCents": 10000})
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("admin open shift: %d %s", w.Code, w.Body.String())
	}
	adminShift := int64(dataMap(t, w)["id"].(float64))
	w = do(t, engine, "POST", "/api/v1/shifts/open", cashier, map[string]any{"openingFloatCents": 5000})
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("cashier open shift: %d %s", w.Code, w.Body.String())
	}
	cashierShift := int64(dataMap(t, w)["id"].(float64))

	// Cashier reads their own X report: fine.
	w = do(t, engine, "GET", fmt.Sprintf("/api/v1/shifts/report?shift=%d", cashierShift), cashier, nil)
	if w.Code != 200 {
		t.Fatalf("own shift report: %d %s", w.Code, w.Body.String())
	}
	// Cashier reads the ADMIN's shift: hidden.
	w = do(t, engine, "GET", fmt.Sprintf("/api/v1/shifts/report?shift=%d", adminShift), cashier, nil)
	if w.Code != 404 {
		t.Fatalf("other user's shift report: want 404, got %d %s", w.Code, w.Body.String())
	}
	// Admin reads the cashier's Z: management visibility, fine.
	w = do(t, engine, "GET", fmt.Sprintf("/api/v1/shifts/report?shift=%d", cashierShift), admin, nil)
	if w.Code != 200 {
		t.Fatalf("admin other-shift report: %d %s", w.Code, w.Body.String())
	}
}

func TestDailyReportBreaksDownByTill(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	checkoutCash(t, engine, admin)
	checkoutCash(t, engine, cashier)

	w := do(t, engine, "GET", "/api/v1/reports/daily", admin, nil)
	if w.Code != 200 {
		t.Fatalf("daily report: %d %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			SalesCents int64 `json:"salesCents"`
			OrdersPaid int   `json:"ordersPaid"`
			ByTill     []struct {
				DeviceID   string `json:"deviceId"`
				Orders     int    `json:"orders"`
				SalesCents int64  `json:"salesCents"`
			} `json:"byTill"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &env)
	if env.Data.OrdersPaid != 2 {
		t.Fatalf("ordersPaid=%d, want 2", env.Data.OrdersPaid)
	}
	total := int64(0)
	for _, trow := range env.Data.ByTill {
		total += trow.SalesCents
		if trow.DeviceID == "" {
			t.Fatalf("byTill row with empty deviceId — attribution missing")
		}
	}
	if total != env.Data.SalesCents {
		t.Fatalf("byTill sum %d != daily sales %d — report does not reconcile", total, env.Data.SalesCents)
	}
	if len(env.Data.ByTill) != 1 {
		// Both tills in this test share one device (single test server);
		// both sales must land in that one bucket.
		t.Fatalf("byTill rows=%d, want 1 (one device in test)", len(env.Data.ByTill))
	}
}
