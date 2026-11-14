package handlers_test

// v170_ledger_test.go — the v1.7.0 "shopkeeper's ledger": line cost
// snapshots + gross-margin reporting, opening balances as real ledger
// rows, soft reversals, structured disposals, and the system-health
// self-audit. Everything drives the real HTTP surface of a booted server
// (same harness as v163_retail_test.go).

import (
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// createCostedProduct makes a tracked product with a known cost and
// opening stock, returning its id.
func createCostedProduct(t *testing.T, engine *gin.Engine, token, name string, priceCents, costCents, stockQty int64) int64 {
	t.Helper()
	qty := int(stockQty) * 1000
	w := do(t, engine, "POST", "/api/v1/products", token, map[string]any{
		"name": name, "sku": "V170-" + name, "priceCents": priceCents,
		"costCents": costCents, "stockQtyMilli": &qty,
	})
	if w.Code != 201 && w.Code != 200 {
		t.Fatalf("create product: %d %s", w.Code, w.Body.String())
	}
	return int64(dataMap(t, w)["id"].(float64))
}

// eatDay converts an API timestamp (UTC RFC3339) into the East-Africa
// calendar day the daily report buckets it under (UTC+3) — tests must ask
// for the same day the report does, whatever the wall clock says.
func eatDay(ts string) string {
	t0, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts[:10]
	}
	return t0.UTC().Add(3 * time.Hour).Format("2006-01-02")
}

// dailyReport fetches the daily summary for the EAT day of a timestamp.
func dailyReport(t *testing.T, engine *gin.Engine, token, date string) map[string]any {
	if len(date) > 10 {
		date = eatDay(date)
	}
	t.Helper()
	w := do(t, engine, "GET", "/api/v1/reports/daily?date="+date, token, nil)
	if w.Code != 200 {
		t.Fatalf("daily report: %d %s", w.Code, w.Body.String())
	}
	return dataMap(t, w)
}

// ledgerRows fetches a customer's ledger as raw maps.
func ledgerRows(t *testing.T, engine *gin.Engine, token string, customerID int64) []map[string]any {
	t.Helper()
	w := do(t, engine, "GET", "/api/v1/customers/"+itoa64(customerID)+"/ledger", token, nil)
	if w.Code != 200 {
		t.Fatalf("ledger: %d %s", w.Code, w.Body.String())
	}
	rows, ok := decode(t, w)["data"].([]any)
	if !ok {
		t.Fatalf("ledger: no rows")
	}
	out := []map[string]any{}
	for _, r := range rows {
		out = append(out, r.(map[string]any))
	}
	return out
}

// TestV170CostSnapshotAndMargin: a sale freezes the unit cost into
// order_items, the daily report turns it into COGS / gross profit /
// margin, and top products carry per-product profit.
func TestV170CostSnapshotAndMargin(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	// Cost 10000, price 25000: three units -> 30000 cost, 75000 sales.
	pid := createCostedProduct(t, engine, admin, "MarginWidget", 25000, 10000, 50)
	order := checkoutLines(t, engine, cashier, []map[string]any{
		{"productId": pid, "qtyMilli": 3000},
	}, nil)

	items := order["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	it := items[0].(map[string]any)
	if got := int64(it["costCents"].(float64)); got != 10000 {
		t.Fatalf("line cost snapshot: want 10000, got %v", it["costCents"])
	}

	day := order["createdAt"].(string)
	if len(day) < 10 {
		t.Fatalf("bad createdAt %q", day)
	}
	rep := dailyReport(t, engine, admin, day)
	if got := int64(rep["cogsCents"].(float64)); got != 30000 {
		t.Fatalf("cogs: want 30000, got %v", rep["cogsCents"])
	}
	if got := int64(rep["grossProfitCents"].(float64)); got < 45000 {
		t.Fatalf("gross profit too small: %v (sales %v)", rep["grossProfitCents"], rep["salesCents"])
	}
	if rep["marginPct"].(float64) <= 0 {
		t.Fatalf("margin %% should be positive, got %v", rep["marginPct"])
	}

	// Top products carry profit.
	tops, _ := rep["topProducts"].([]any)
	found := false
	for _, raw := range tops {
		tp := raw.(map[string]any)
		if int64(tp["productId"].(float64)) == pid {
			found = true
			if int64(tp["profitCents"].(float64)) != 3*15000 {
				t.Fatalf("top product profit: want 45000, got %v", tp["profitCents"])
			}
		}
	}
	if !found {
		t.Fatalf("costed product missing from top products: %v", tops)
	}
}

// TestV170OpeningBalanceAndReversal: debt carried in is a real ledger row
// with a balance snapshot; it reverses with a mirrored row; double
// reversal is rejected; order-linked entries refuse direct reversal.
func TestV170OpeningBalanceAndReversal(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)

	w := do(t, engine, "POST", "/api/v1/customers", admin, map[string]any{"name": "Debt Carrier"})
	if w.Code != 201 && w.Code != 200 {
		t.Fatalf("create customer: %d %s", w.Code, w.Body.String())
	}
	cid := int64(dataMap(t, w)["id"].(float64))

	// Cashier lacks customers.manage: opening balance must 403.
	cashierTok := login(t, engine, "cashier", rotatedPassword)
	w = do(t, engine, "POST", "/api/v1/customers/"+itoa64(cid)+"/opening-balance", cashierTok,
		map[string]any{"amountCents": 5000, "note": "old book"})
	if w.Code != 403 {
		t.Fatalf("cashier opening balance should 403, got %d", w.Code)
	}

	w = do(t, engine, "POST", "/api/v1/customers/"+itoa64(cid)+"/opening-balance", admin,
		map[string]any{"amountCents": 5000, "note": "old book debt"})
	if w.Code != 200 {
		t.Fatalf("opening balance: %d %s", w.Code, w.Body.String())
	}
	if got := int64(dataMap(t, w)["balanceCents"].(float64)); got != 5000 {
		t.Fatalf("balance after opening: want 5000, got %v", dataMap(t, w)["balanceCents"])
	}

	rows := ledgerRows(t, engine, admin, cid)
	var opening map[string]any
	for _, r := range rows {
		if r["kind"] == "opening" {
			opening = r
		}
	}
	if opening == nil {
		t.Fatalf("no opening row in ledger: %v", rows)
	}
	if ba, ok := opening["balanceAfter"].(float64); !ok || int64(ba) != 5000 {
		t.Fatalf("opening balanceAfter: want 5000, got %v", opening["balanceAfter"])
	}
	eid := int64(opening["id"].(float64))

	// Zero amount rejected.
	w = do(t, engine, "POST", "/api/v1/customers/"+itoa64(cid)+"/opening-balance", admin,
		map[string]any{"amountCents": 0})
	if w.Code != 400 {
		t.Fatalf("zero opening balance should 400, got %d", w.Code)
	}

	// Reverse it.
	w = do(t, engine, "POST", "/api/v1/customers/"+itoa64(cid)+"/ledger/"+itoa64(eid)+"/reverse", admin,
		map[string]any{"reason": "entered twice"})
	if w.Code != 200 {
		t.Fatalf("reverse: %d %s", w.Code, w.Body.String())
	}
	if got := int64(dataMap(t, w)["balanceCents"].(float64)); got != 0 {
		t.Fatalf("balance after reversal: want 0, got %v", dataMap(t, w)["balanceCents"])
	}
	rows = ledgerRows(t, engine, admin, cid)
	rev := -1
	for i, r := range rows {
		rof, _ := r["reversalOf"].(float64)
		if int64(rof) == eid {
			rev = i
			if int64(r["amountCents"].(float64)) != -5000 {
				t.Fatalf("reversal amount: want -5000, got %v", r["amountCents"])
			}
			ba, _ := r["balanceAfter"].(float64)
			if int64(ba) != 0 {
				t.Fatalf("reversal balanceAfter: want 0, got %v", r["balanceAfter"])
			}
		}
	}
	if rev < 0 {
		t.Fatalf("no reversal row pointing at %d", eid)
	}

	// Double reversal rejected (ErrInvalidState -> 409); missing reason 400.
	w = do(t, engine, "POST", "/api/v1/customers/"+itoa64(cid)+"/ledger/"+itoa64(eid)+"/reverse", admin,
		map[string]any{"reason": "again"})
	if w.Code != 409 {
		t.Fatalf("double reversal should 409, got %d", w.Code)
	}
	w = do(t, engine, "POST", "/api/v1/customers/"+itoa64(cid)+"/ledger/"+itoa64(eid)+"/reverse", admin, map[string]any{})
	if w.Code != 400 {
		t.Fatalf("reasonless reversal should 400, got %d", w.Code)
	}
}

// TestV170TabChargeHasBalanceSnapshot: an account (tab) charge writes the
// ledger row through the guarded path and still freezes balance_after.
func TestV170TabChargeHasBalanceSnapshot(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	w := do(t, engine, "POST", "/api/v1/customers", admin, map[string]any{"name": "Tab Walker", "creditLimitCents": 100000})
	if w.Code != 201 && w.Code != 200 {
		t.Fatalf("create customer: %d %s", w.Code, w.Body.String())
	}
	cid := int64(dataMap(t, w)["id"].(float64))

	checkoutLines(t, engine, cashier, []map[string]any{
		{"productId": 1, "qtyMilli": 2000},
	}, map[string]any{"paymentMethod": "account", "customerId": cid})

	rows := ledgerRows(t, engine, admin, cid)
	found := false
	for _, r := range rows {
		if r["kind"] == "charge" {
			found = true
			ba, ok := r["balanceAfter"].(float64)
			if !ok || int64(ba) <= 0 {
				t.Fatalf("charge row missing/invalid balance_after: %v", r["balanceAfter"])
			}
		}
	}
	if !found {
		t.Fatalf("no charge row: %v", rows)
	}
}

// TestV170DisposalFlow: guarded deduction, structured reason, DISPOSAL
// ledger row, and the disposal report with loss value.
func TestV170DisposalFlow(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)

	pid := createCostedProduct(t, engine, admin, "Perishable", 20000, 8000, 10)

	// Invalid reason rejected.
	w := do(t, engine, "POST", "/api/v1/products/"+itoa64(pid)+"/dispose", admin,
		map[string]any{"qtyMilli": 1000, "reason": "because"})
	if w.Code != 400 {
		t.Fatalf("invalid reason should 400, got %d", w.Code)
	}
	// Over-stock disposal rejected.
	w = do(t, engine, "POST", "/api/v1/products/"+itoa64(pid)+"/dispose", admin,
		map[string]any{"qtyMilli": 99000, "reason": "spoilt"})
	if w.Code != 400 {
		t.Fatalf("overstock disposal should 400, got %d", w.Code)
	}
	// The real thing.
	w = do(t, engine, "POST", "/api/v1/products/"+itoa64(pid)+"/dispose", admin,
		map[string]any{"qtyMilli": 4000, "reason": "spoilt", "note": "fridge died"})
	if w.Code != 200 {
		t.Fatalf("dispose: %d %s", w.Code, w.Body.String())
	}
	if got := int64(dataMap(t, w)["stockQtyMilli"].(float64)); got != 6000 {
		t.Fatalf("stock after disposal: want 6000 milli, got %v", dataMap(t, w)["stockQtyMilli"])
	}

	// Ledger row audited.
	w = do(t, engine, "GET", "/api/v1/stock-movements?kind=DISPOSAL", admin, nil)
	if w.Code != 200 {
		t.Fatalf("stock movements: %d", w.Code)
	}
	moves := decode(t, w)["data"].([]any)
	okMove := false
	for _, raw := range moves {
		m := raw.(map[string]any)
		if int64(m["productId"].(float64)) == pid && int64(m["deltaMilli"].(float64)) == -4000 {
			okMove = true
		}
	}
	if !okMove {
		t.Fatalf("no DISPOSAL ledger row: %v", moves)
	}

	// Report carries loss value 4 x 8000.
	w = do(t, engine, "GET", "/api/v1/reports/disposals", admin, nil)
	if w.Code != 200 {
		t.Fatalf("disposal report: %d %s", w.Code, w.Body.String())
	}
	rows := decode(t, w)["data"].([]any)
	found := false
	for _, raw := range rows {
		r := raw.(map[string]any)
		if r["productName"] == "Perishable" {
			found = true
			if int64(r["lossCents"].(float64)) != 32000 {
				t.Fatalf("loss value: want 32000, got %v", r["lossCents"])
			}
			if r["reason"] != "spoilt" {
				t.Fatalf("reason: want spoilt, got %v", r["reason"])
			}
		}
	}
	if !found {
		t.Fatalf("disposal missing from report: %v", rows)
	}
}

// TestV170SystemHealthPage: the self-audit endpoint reports the schema
// version, a clean index audit, and refuses non-managers.
func TestV170SystemHealthPage(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	w := do(t, engine, "GET", "/api/v1/system/health", cashier, nil)
	if w.Code != 403 {
		t.Fatalf("cashier health should 403, got %d", w.Code)
	}
	w = do(t, engine, "GET", "/api/v1/system/health", admin, nil)
	if w.Code != 200 {
		t.Fatalf("health: %d %s", w.Code, w.Body.String())
	}
	h := dataMap(t, w)
	if int64(h["schemaVersion"].(float64)) != 29 {
		t.Fatalf("schemaVersion: want 29, got %v", h["schemaVersion"])
	}
	if h["indexesOk"] != true {
		t.Fatalf("indexes should be complete, missing: %v", h["indexesMissing"])
	}
	if jm, ok := h["journalMode"].(string); !ok || !strings.EqualFold(jm, "wal") {
		t.Fatalf("journal mode: want wal, got %v", h["journalMode"])
	}
	// Customers with ledger rows reconcile (harness creates none before
	// this test's server booted fresh, so zero drift is expected).
	if int64(h["customerDriftCount"].(float64)) != 0 {
		t.Fatalf("customer drift on a fresh server: %v", h["customerDrift"])
	}
}
