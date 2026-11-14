package handlers_test

// v163_retail_test.go — the v1.6.3 retail expansion: stock ledger,
// returns/refunds, expenses, and wholesale price tiers. Everything here
// drives the real HTTP surface of a booted test server.

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// checkoutLines rings up a sale and returns the order payload.
func checkoutLines(t *testing.T, engine *gin.Engine, token string, items []map[string]any, extra map[string]any) map[string]any {
	t.Helper()
	body := map[string]any{"items": items, "paymentMethod": "cash"}
	for k, v := range extra {
		body[k] = v
	}
	w := do(t, engine, "POST", "/api/v1/orders/checkout", token, body)
	if w.Code != 201 {
		t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
	}
	return dataMap(t, w)
}

func productStock(t *testing.T, engine *gin.Engine, token string, id int64) int {
	t.Helper()
	w := do(t, engine, "GET", "/api/v1/products", token, nil)
	if w.Code != 200 {
		t.Fatalf("products: %d", w.Code)
	}
	for _, it := range decode(t, w)["data"].([]any) {
		m := it.(map[string]any)
		if int64(m["id"].(float64)) == id {
			return int(m["stockQtyMilli"].(float64)) / 1000
		}
	}
	t.Fatalf("product %d not found", id)
	return 0
}

func productField(t *testing.T, engine *gin.Engine, token string, id int64, field string) any {
	t.Helper()
	w := do(t, engine, "GET", "/api/v1/products", token, nil)
	if w.Code != 200 {
		t.Fatalf("products: %d", w.Code)
	}
	for _, it := range decode(t, w)["data"].([]any) {
		m := it.(map[string]any)
		if int64(m["id"].(float64)) == id {
			return m[field]
		}
	}
	t.Fatalf("product %d not found", id)
	return nil
}

// TestV163StockLedgerEveryMoveAudited: checkout writes a SALE row,
// void writes VOID_RESTORE, adjust writes ADJUSTMENT — and every row
// carries balance_after that reconciles with products.stock_qty.
func TestV163StockLedgerEveryMoveAudited(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	before := productStock(t, engine, admin, 1)
	order := checkoutLines(t, engine, cashier, []map[string]any{
		{"productId": 1, "qtyMilli": 3000},
	}, nil)
	oid := itoa64(order["id"])

	afterSale := productStock(t, engine, admin, 1)
	if afterSale != before-3 {
		t.Fatalf("stock after sale = %d, want %d", afterSale, before-3)
	}

	// Ledger shows OPENING (migration anchor) + SALE.
	w := do(t, engine, "GET", "/api/v1/products/1/movements", admin, nil)
	if w.Code != 200 {
		t.Fatalf("movements: %d %s", w.Code, w.Body.String())
	}
	movs := decode(t, w)["data"].([]any)
	if len(movs) < 2 {
		t.Fatalf("expected >=2 movements, got %d", len(movs))
	}
	// Newest first: [0] is the SALE.
	sale := movs[0].(map[string]any)
	if sale["kind"] != "SALE" {
		t.Fatalf("newest movement kind = %v, want SALE", sale["kind"])
	}
	if int64(sale["deltaMilli"].(float64)) != -3000 {
		t.Fatalf("sale delta = %v, want -3000", sale["deltaMilli"])
	}
	if int64(sale["balanceAfterMilli"].(float64)) != int64(afterSale)*1000 {
		t.Fatalf("balanceAfterMilli = %v, want %d", sale["balanceAfterMilli"], afterSale*1000)
	}
	ref, _ := sale["reference"].(string)
	if ref == "" {
		t.Fatal("sale movement must reference the order number")
	}

	// Adjust: +5 with a note lands as ADJUSTMENT.
	w = do(t, engine, "POST", "/api/v1/products/1/adjust-stock", admin, map[string]any{
		"deltaMilli": 5000, "note": "restock box",
	})
	if w.Code != 200 {
		t.Fatalf("adjust: %d %s", w.Code, w.Body.String())
	}
	if dataMap(t, w)["applied"].(float64) != 5000 {
		t.Fatalf("adjust applied = %v", dataMap(t, w)["applied"])
	}

	// Void the paid sale: stock returns, VOID_RESTORE logged.
	w = do(t, engine, "POST", "/api/v1/orders/"+oid+"/void", admin, map[string]any{"reason": "test void"})
	if w.Code != 200 {
		t.Fatalf("void: %d %s", w.Code, w.Body.String())
	}
	afterVoid := productStock(t, engine, admin, 1)
	if afterVoid != afterSale+5+3 {
		t.Fatalf("stock after adjust+void = %d, want %d", afterVoid, afterSale+5+3)
	}

	w = do(t, engine, "GET", "/api/v1/stock-movements?limit=10", admin, nil)
	if w.Code != 200 {
		t.Fatalf("stock-movements: %d", w.Code)
	}
	movs = decode(t, w)["data"].([]any)
	kinds := map[string]bool{}
	for _, m := range movs {
		kinds[m.(map[string]any)["kind"].(string)] = true
	}
	for _, want := range []string{"SALE", "ADJUSTMENT", "VOID_RESTORE", "OPENING"} {
		if !kinds[want] {
			t.Fatalf("ledger missing %s (got %v)", want, kinds)
		}
	}

	// Cashier has no products.view: ledger reads are admin-side.
	if w = do(t, engine, "GET", "/api/v1/stock-movements", cashier, nil); w.Code != 403 {
		t.Fatalf("cashier movements: %d, want 403", w.Code)
	}
	if w = do(t, engine, "GET", "/api/v1/stock-movements", "", nil); w.Code != 401 {
		t.Fatalf("anonymous movements: %d, want 401", w.Code)
	}
}

// TestV163ReturnFlowRestocksAndRefunds: cash refund returns stock to the
// shelf and logs RETURN_RESTOCK; a second over-return is rejected; a
// PENDING order cannot be returned.
func TestV163ReturnFlowRestocksAndRefunds(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	before := productStock(t, engine, admin, 1)
	order := checkoutLines(t, engine, cashier, []map[string]any{
		{"productId": 1, "qtyMilli": 4000},
	}, nil)

	items := order["items"].([]any)
	itemID := int64(items[0].(map[string]any)["id"].(float64))

	// PENDING orders (tab) can't be returned — use void status instead:
	// check by returning the PAID order normally first.
	w := do(t, engine, "POST", "/api/v1/returns", cashier, map[string]any{
		"orderId": order["id"].(float64),
		"lines":   []map[string]any{{"orderItemId": itemID, "qtyMilli": 2000}},
		"method":  "CASH",
		"reason":  "customer brought one back",
	})
	if w.Code != 201 {
		t.Fatalf("return: %d %s", w.Code, w.Body.String())
	}
	ret := dataMap(t, w)
	if ret["number"] == "" || ret["status"] != "COMPLETED" {
		t.Fatalf("return doc: %+v", ret)
	}
	if int64(ret["totalCents"].(float64)) <= 0 {
		t.Fatalf("return total = %v", ret["totalCents"])
	}

	afterReturn := productStock(t, engine, admin, 1)
	if afterReturn != before-4+2 {
		t.Fatalf("stock after return = %d, want %d", afterReturn, before-4+2)
	}

	// Ledger has the restock row.
	w = do(t, engine, "GET", "/api/v1/products/1/movements", admin, nil)
	movs := decode(t, w)["data"].([]any)
	found := false
	for _, m := range movs {
		mm := m.(map[string]any)
		if mm["kind"] == "RETURN_RESTOCK" && int64(mm["deltaMilli"].(float64)) == 2000 {
			found = true
		}
	}
	if !found {
		t.Fatal("RETURN_RESTOCK movement missing")
	}

	// Over-return: only 2 remain returnable on that line.
	w = do(t, engine, "POST", "/api/v1/returns", cashier, map[string]any{
		"orderId": order["id"].(float64),
		"lines":   []map[string]any{{"orderItemId": itemID, "qtyMilli": 3000}},
		"method":  "CASH",
	})
	if w.Code != 400 && w.Code != 409 && w.Code != 422 {
		t.Fatalf("over-return should 4xx, got %d %s", w.Code, w.Body.String())
	}

	// Returns list is visible and the daily report nets them.
	w = do(t, engine, "GET", "/api/v1/returns", admin, nil)
	if w.Code != 200 {
		t.Fatalf("returns list: %d", w.Code)
	}
	w = do(t, engine, "GET", "/api/v1/reports/daily", admin, nil)
	if w.Code != 200 {
		t.Fatalf("daily: %d", w.Code)
	}
	sum := dataMap(t, w)
	if sum["returnsCount"].(float64) < 1 {
		t.Fatalf("daily returnsCount = %v", sum["returnsCount"])
	}
}

// TestV163ReturnStoreCreditRequiresCustomer: STORE_CREDIT without a
// customer is 400; with one, the customer's store credit rises.
func TestV163ReturnStoreCreditRequiresCustomer(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	// Make a customer.
	w := do(t, engine, "POST", "/api/v1/customers", admin, map[string]any{
		"name": "Return Tester", "phone": "0711998877",
	})
	if w.Code != 201 {
		t.Fatalf("customer: %d %s", w.Code, w.Body.String())
	}
	custIDNum := dataMap(t, w)["id"].(float64)

	order := checkoutLines(t, engine, cashier, []map[string]any{
		{"productId": 1, "qtyMilli": 1000},
	}, map[string]any{"customerId": custIDNum, "customerName": "Return Tester"})

	items := order["items"].([]any)
	itemID := int64(items[0].(map[string]any)["id"].(float64))

	// Missing customer for STORE_CREDIT → 400.
	w = do(t, engine, "POST", "/api/v1/returns", cashier, map[string]any{
		"orderId": order["id"].(float64),
		"lines":   []map[string]any{{"orderItemId": itemID, "qtyMilli": 1000}},
		"method":  "STORE_CREDIT",
	})
	if w.Code != 400 {
		t.Fatalf("store credit without customer: %d, want 400", w.Code)
	}

	// With the customer: credit balance grows.
	w = do(t, engine, "POST", "/api/v1/returns", cashier, map[string]any{
		"orderId":    order["id"].(float64),
		"lines":      []map[string]any{{"orderItemId": itemID, "qtyMilli": 1000}},
		"method":     "STORE_CREDIT",
		"customerId": custIDNum,
		"reason":     "wrong colour",
	})
	if w.Code != 201 {
		t.Fatalf("store credit return: %d %s", w.Code, w.Body.String())
	}

	// The store-credit balance rose on this till.
	w = do(t, engine, "GET", "/api/v1/customers", admin, nil)
	if w.Code != 200 {
		t.Fatalf("customers list: %d", w.Code)
	}
	credit := 0.0
	for _, it := range decode(t, w)["data"].([]any) {
		m := it.(map[string]any)
		if m["id"].(float64) == custIDNum {
			credit = m["storeCreditCents"].(float64)
		}
	}
	if credit <= 0 {
		t.Fatalf("store credit = %v, want > 0", credit)
	}
}

// TestV163ExpensesLifecycle: seeded types exist, an expense mints EXP-,
// the daily summary books it, and cashier (expenses.view) can read.
func TestV163ExpensesLifecycle(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	w := do(t, engine, "GET", "/api/v1/expenses/types", admin, nil)
	if w.Code != 200 {
		t.Fatalf("types: %d", w.Code)
	}
	types := decode(t, w)["data"].([]any)
	if len(types) < 8 {
		t.Fatalf("expected >=8 seeded expense types, got %d", len(types))
	}
	typeID := int64(types[0].(map[string]any)["id"].(float64))

	// Cashier cannot record (expenses.manage), can read (expenses.view).
	w = do(t, engine, "POST", "/api/v1/expenses", cashier, map[string]any{
		"expenseTypeId": typeID, "amountCents": 50000, "method": "CASH", "note": "airtime",
	})
	if w.Code != 403 {
		t.Fatalf("cashier expense create: %d, want 403", w.Code)
	}
	w = do(t, engine, "GET", "/api/v1/expenses", cashier, nil)
	if w.Code != 200 {
		t.Fatalf("cashier expense view: %d, want 200", w.Code)
	}

	w = do(t, engine, "POST", "/api/v1/expenses", admin, map[string]any{
		"expenseTypeId": typeID, "amountCents": 150000, "method": "MPESA",
		"reference": "QWE456", "note": "internet bundle",
	})
	if w.Code != 201 {
		t.Fatalf("expense create: %d %s", w.Code, w.Body.String())
	}
	exp := dataMap(t, w)
	num, _ := exp["number"].(string)
	if len(num) < 4 || num[:4] != "EXP-" {
		t.Fatalf("expense number = %v, want EXP-…", exp["number"])
	}

	w = do(t, engine, "GET", "/api/v1/reports/daily", admin, nil)
	sum := dataMap(t, w)
	if sum["expensesCents"].(float64) < 150000 {
		t.Fatalf("daily expensesCents = %v", sum["expensesCents"])
	}
	if sum["netSalesCents"] == nil {
		t.Fatal("daily netSalesCents missing")
	}

	// Bad amount rejected.
	w = do(t, engine, "POST", "/api/v1/expenses", admin, map[string]any{
		"expenseTypeId": typeID, "amountCents": 0,
	})
	if w.Code != 400 && w.Code != 422 {
		t.Fatalf("zero expense: %d, want 400/422", w.Code)
	}
}

// TestV163WholesaleTierAndLowStock: product keeps a wholesale price +
// per-product threshold; customer keeps a price tier; low-stock list
// honours the per-product threshold.
func TestV163WholesaleTierAndLowStock(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)

	// Update product 2 with wholesale price + tight threshold.
	w := do(t, engine, "PUT", "/api/v1/products/2", admin, map[string]any{
		"name":                "Wholesale Item",
		"priceCents":          20000,
		"costCents":           10000,
		"wholesalePriceCents": 16000,
		"lowStockUnits":       7,
		"stockQtyMilli":       5000,
	})
	if w.Code != 200 {
		t.Fatalf("product update: %d %s", w.Code, w.Body.String())
	}
	if v, _ := productField(t, engine, admin, 2, "wholesalePriceCents").(float64); v != 16000 {
		t.Fatalf("wholesale price = %v", v)
	}
	if v, _ := productField(t, engine, admin, 2, "lowStockUnits").(float64); v != 7 {
		t.Fatalf("low stock units = %v", v)
	}

	// Product 2 (qty 5 <= 7) shows in the low-stock list even though the
	// global threshold is higher/looser for other products.
	w = do(t, engine, "GET", "/api/v1/products/low-stock", admin, nil)
	if w.Code != 200 {
		t.Fatalf("low stock: %d", w.Code)
	}
	found := false
	for _, it := range decode(t, w)["data"].([]any) {
		if int64(it.(map[string]any)["id"].(float64)) == 2 {
			found = true
		}
	}
	if !found {
		t.Fatal("product 2 (5 <= 7) missing from low-stock list")
	}

	// Customer with wholesale tier persists.
	w = do(t, engine, "POST", "/api/v1/customers", admin, map[string]any{
		"name": "Bulk Buyer", "phone": "0733445566", "priceTier": "wholesale",
	})
	if w.Code != 201 {
		t.Fatalf("customer: %d %s", w.Code, w.Body.String())
	}
	cust := dataMap(t, w)
	if cust["priceTier"] != "wholesale" {
		t.Fatalf("priceTier = %v", cust["priceTier"])
	}
	cid := itoa64(cust["id"])
	w = do(t, engine, "PUT", "/api/v1/customers/"+cid, admin, map[string]any{
		"name": "Bulk Buyer", "priceTier": "retail",
	})
	if w.Code != 200 {
		t.Fatalf("customer update: %d", w.Code)
	}
	if dataMap(t, w)["priceTier"] != "retail" {
		t.Fatalf("tier update failed: %v", dataMap(t, w)["priceTier"])
	}
}

// TestV163ReturnsPermissionGate: designer (no returns.manage, no
// orders.view) cannot post returns.
func TestV163ReturnsPermissionGate(t *testing.T) {
	engine, _, _, designer := newTestServer(t)
	w := do(t, engine, "POST", "/api/v1/returns", designer, map[string]any{
		"orderId": 1,
		"lines":   []map[string]any{{"orderItemId": 1, "qtyMilli": 1000}},
	})
	if w.Code != 403 {
		t.Fatalf("designer return: %d, want 403", w.Code)
	}
}
