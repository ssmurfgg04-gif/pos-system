package handlers_test

// v180_batches_test.go — the v1.8.0 pair: fractional (weighable) milli-unit
// quantities and batch/expiry tracking with FEFO allocation. Everything
// drives the real HTTP surface of a booted server (same harness as
// v170_ledger_test.go), because the guarantees that matter live at the
// seam: a sale must price round(price × milli / 1000) exactly like the
// frontend, deduct stock in milli, drain the NEAREST-EXPIRY lot first, and
// keep SUM(lots) == shelf at every step.

import (
	"fmt"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// createFractionProduct makes a kg-sold product; stockKg in whole kilos.
func createFractionProduct(t *testing.T, engine *gin.Engine, token, name string, priceCents int64, stockKg int64) int64 {
	t.Helper()
	stock := stockKg * 1000
	w := do(t, engine, "POST", "/api/v1/products", token, map[string]any{
		"name": name, "sku": "V180-" + name, "priceCents": priceCents,
		"stockQtyMilli": stock, "unit": "kg", "allowFraction": true,
	})
	if w.Code != 201 && w.Code != 200 {
		t.Fatalf("create fraction product: %d %s", w.Code, w.Body.String())
	}
	return int64(dataMap(t, w)["id"].(float64))
}

// createBatchProduct makes a batch-tracked product with an OPENING lot.
func createBatchProduct(t *testing.T, engine *gin.Engine, token, name string, priceCents, costCents, stockUnits int64) int64 {
	t.Helper()
	w := do(t, engine, "POST", "/api/v1/products", token, map[string]any{
		"name": name, "sku": "V180-" + name, "priceCents": priceCents,
		"costCents": costCents, "stockQtyMilli": stockUnits * 1000, "trackBatches": true,
	})
	if w.Code != 201 && w.Code != 200 {
		t.Fatalf("create batch product: %d %s", w.Code, w.Body.String())
	}
	return int64(dataMap(t, w)["id"].(float64))
}

func batchesOf(t *testing.T, engine *gin.Engine, token string, productID int64) []map[string]any {
	t.Helper()
	w := do(t, engine, "GET", "/api/v1/products/"+itoa64(productID)+"/batches", token, nil)
	if w.Code != 200 {
		t.Fatalf("batches: %d %s", w.Code, w.Body.String())
	}
	rows, ok := decode(t, w)["data"].([]any)
	if !ok {
		t.Fatalf("batches: no rows")
	}
	out := []map[string]any{}
	for _, r := range rows {
		out = append(out, r.(map[string]any))
	}
	return out
}

func productStockMilli(t *testing.T, engine *gin.Engine, token string, productID int64) int64 {
	t.Helper()
	w := do(t, engine, "GET", "/api/v1/products", token, nil)
	if w.Code != 200 {
		t.Fatalf("products: %d", w.Code)
	}
	for _, it := range decode(t, w)["data"].([]any) {
		m := it.(map[string]any)
		if int64(m["id"].(float64)) == productID {
			return int64(m["stockQtyMilli"].(float64))
		}
	}
	t.Fatalf("product %d not found", productID)
	return 0
}

func expiryReport(t *testing.T, engine *gin.Engine, token string, days string) map[string]any {
	t.Helper()
	w := do(t, engine, "GET", "/api/v1/reports/expiry?days="+days, token, nil)
	if w.Code != 200 {
		t.Fatalf("expiry report: %d %s", w.Code, w.Body.String())
	}
	return dataMap(t, w)
}

// TestV180FractionalSale: 0.25 kg of a 200.00/kg product prices at exactly
// 5000 cents (the server formula matches the frontend golden), the shelf
// drops by 250 milli, and a whole-unit product refuses fractional quantities.
func TestV180FractionalSale(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)
	pid := createFractionProduct(t, engine, admin, "SugarKg", 20000, 10)

	order := checkoutLines(t, engine, cashier, []map[string]any{
		{"productId": pid, "qtyMilli": 250},
	}, nil)
	items := order["items"].([]any)
	it := items[0].(map[string]any)
	if got := int64(it["qtyMilli"].(float64)); got != 250 {
		t.Fatalf("line qtyMilli: want 250, got %v", it["qtyMilli"])
	}
	if got := int64(it["lineTotalCents"].(float64)); got != 5000 {
		t.Fatalf("line total: want 5000 (20000 × 0.25), got %v", it["lineTotalCents"])
	}
	if got := productStockMilli(t, engine, admin, pid); got != 10000-250 {
		t.Fatalf("stock after sale: want 9750 milli, got %d", got)
	}

	// Whole-unit product rejects fractional quantities (0.3 of a bar of soap).
	wpid := createCostedProduct(t, engine, admin, "SoapBar", 5000, 3000, 20)
	w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
		"items": []map[string]any{{"productId": wpid, "qtyMilli": 1500}}, "paymentMethod": "cash",
	})
	if w.Code == 201 || w.Code == 200 {
		t.Fatalf("fractional qty on whole-unit product must be rejected, got %d %s", w.Code, w.Body.String())
	}

	// Insufficient stock speaks in milli.
	w = do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
		"items": []map[string]any{{"productId": pid, "qtyMilli": 100000}}, "paymentMethod": "cash",
	})
	if w.Code != 409 && w.Code != 422 {
		t.Fatalf("over-sell must 409/422, got %d %s", w.Code, w.Body.String())
	}
}

// TestV180FEFOAllocation: goods-in two dated lots, sell across both — the
// nearest expiry drains first, the allocation rows freeze the split, a void
// puts the goods back on their original lots, and SUM(lots) == shelf at
// every step.
func TestV180FEFOAllocation(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)
	pid := createBatchProduct(t, engine, admin, "CookingOil", 35000, 26000, 0)

	today := time.Now().UTC()
	near := today.AddDate(0, 0, 10).Format("2006-01-02")
	far := today.AddDate(0, 0, 60).Format("2006-01-02")

	// Goods-in without a PO: the common duka flow.
	w := do(t, engine, "POST", "/api/v1/products/"+itoa64(pid)+"/batches", admin, map[string]any{
		"batchNo": "OIL-FAR", "expiryDate": far, "qtyMilli": 10000, "costCents": 26000,
	})
	if w.Code != 201 {
		t.Fatalf("goods-in far: %d %s", w.Code, w.Body.String())
	}
	farBatch := int64(dataMap(t, w)["id"].(float64))
	w = do(t, engine, "POST", "/api/v1/products/"+itoa64(pid)+"/batches", admin, map[string]any{
		"batchNo": "OIL-NEAR", "expiryDate": near, "qtyMilli": 5000, "costCents": 24000,
	})
	if w.Code != 201 {
		t.Fatalf("goods-in near: %d %s", w.Code, w.Body.String())
	}
	nearBatch := int64(dataMap(t, w)["id"].(float64))

	if got := productStockMilli(t, engine, admin, pid); got != 15000 {
		t.Fatalf("shelf after goods-in: want 15000 milli, got %d", got)
	}

	// Sell 12 units: FEFO takes all 5 from NEAR, then 7 from FAR.
	order := checkoutLines(t, engine, cashier, []map[string]any{
		{"productId": pid, "qtyMilli": 12000},
	}, nil)
	items := order["items"].([]any)
	itemID := int64(items[0].(map[string]any)["id"].(float64))

	lots := batchesOf(t, engine, admin, pid)
	byID := map[int64]map[string]any{}
	for _, b := range lots {
		byID[int64(b["id"].(float64))] = b
	}
	if got := int64(byID[nearBatch]["qtyRemainingMilli"].(float64)); got != 0 {
		t.Fatalf("near-expiry lot must drain to 0 first, got %d", got)
	}
	if byID[nearBatch]["status"] != "EXHAUSTED" {
		t.Fatalf("near lot status = %v, want EXHAUSTED", byID[nearBatch]["status"])
	}
	if got := int64(byID[farBatch]["qtyRemainingMilli"].(float64)); got != 3000 {
		t.Fatalf("far lot must keep 3000 milli, got %d", got)
	}
	if got := productStockMilli(t, engine, admin, pid); got != 3000 {
		t.Fatalf("shelf after sale: want 3000 milli, got %d", got)
	}

	// Batch-driven oversell refuses even when a RECALLED lot holds goods.
	w = do(t, engine, "POST", "/api/v1/batches/"+itoa64(farBatch)+"/recall", admin, map[string]any{"reason": "supplier recall"})
	if w.Code != 200 {
		t.Fatalf("recall: %d %s", w.Code, w.Body.String())
	}
	if got := productStockMilli(t, engine, admin, pid); got != 0 {
		t.Fatalf("recall must clear the shelf projection, want 0, got %d", got)
	}
	w = do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
		"items": []map[string]any{{"productId": pid, "qtyMilli": 1000}}, "paymentMethod": "cash",
	})
	if w.Code == 201 || w.Code == 200 {
		t.Fatalf("sale from a fully recalled shelf must fail, got %d", w.Code)
	}

	// Restock a fresh lot, sell, then VOID: goods return to their lot.
	w = do(t, engine, "POST", "/api/v1/products/"+itoa64(pid)+"/batches", admin, map[string]any{
		"batchNo": "OIL-NEW", "expiryDate": far, "qtyMilli": 4000, "costCents": 26000,
	})
	if w.Code != 201 {
		t.Fatalf("goods-in new: %d %s", w.Code, w.Body.String())
	}
	newBatch := int64(dataMap(t, w)["id"].(float64))
	order2 := checkoutLines(t, engine, cashier, []map[string]any{
		{"productId": pid, "qtyMilli": 4000},
	}, nil)
	if productStockMilli(t, engine, admin, pid) != 0 {
		t.Fatalf("shelf must be empty after full sale")
	}
	oid := itoa64(order2["id"])
	w = do(t, engine, "POST", "/api/v1/orders/"+oid+"/void", admin, map[string]any{"reason": "wrong sale"})
	if w.Code != 200 {
		t.Fatalf("void: %d %s", w.Code, w.Body.String())
	}
	lots = batchesOf(t, engine, admin, pid)
	for _, b := range lots {
		if int64(b["id"].(float64)) == newBatch {
			if got := int64(b["qtyRemainingMilli"].(float64)); got != 4000 {
				t.Fatalf("void must restock the ORIGINAL lot (4000), got %d", got)
			}
			if b["status"] != "ACTIVE" {
				t.Fatalf("restocked lot must be ACTIVE again, got %v", b["status"])
			}
		}
	}
	if got := productStockMilli(t, engine, admin, pid); got != 4000 {
		t.Fatalf("shelf after void: want 4000 milli, got %d", got)
	}
	_ = itemID
}

// TestV180DisposalByBatchAndExpiryReport: the expiry report names the lots
// at risk with their value; disposal can target one specific lot and leaves
// the other untouched; system health keeps the batch invariant honest.
func TestV180DisposalByBatchAndExpiryReport(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)
	pid := createBatchProduct(t, engine, admin, "Yoghurt", 12000, 8000, 0)

	today := time.Now().UTC()
	expired := today.AddDate(0, 0, -3).Format("2006-01-02")
	soon := today.AddDate(0, 0, 5).Format("2006-01-02")
	w := do(t, engine, "POST", "/api/v1/products/"+itoa64(pid)+"/batches", admin, map[string]any{
		"batchNo": "YG-OLD", "expiryDate": expired, "qtyMilli": 6000, "costCents": 8000,
	})
	if w.Code != 201 {
		t.Fatalf("goods-in expired: %d %s", w.Code, w.Body.String())
	}
	oldBatch := int64(dataMap(t, w)["id"].(float64))
	w = do(t, engine, "POST", "/api/v1/products/"+itoa64(pid)+"/batches", admin, map[string]any{
		"batchNo": "YG-NEW", "expiryDate": soon, "qtyMilli": 6000, "costCents": 8000,
	})
	if w.Code != 201 {
		t.Fatalf("goods-in soon: %d %s", w.Code, w.Body.String())
	}

	rep := expiryReport(t, engine, admin, "30")
	expiredRows, _ := rep["expired"].([]any)
	soonRows, _ := rep["soon"].([]any)
	// The demo catalog seeds its own dated lots — filter to THIS product.
	var row map[string]any
	for _, r := range expiredRows {
		m := r.(map[string]any)
		if int64(m["productId"].(float64)) == pid {
			row = m
		}
	}
	if row == nil {
		t.Fatalf("expiry report: this product's expired lot missing: %v", expiredRows)
	}
	if row["batchNo"] != "YG-OLD" || int64(row["valueCents"].(float64)) != 48000 {
		t.Fatalf("expired row: want YG-OLD at 48000 cents, got %v", row)
	}
	soonMine := 0
	for _, r := range soonRows {
		if int64(r.(map[string]any)["productId"].(float64)) == pid {
			soonMine++
		}
	}
	if soonMine != 1 {
		t.Fatalf("expiry report: want 1 soon lot for this product, got %d", soonMine)
	}

	// Dispose ONLY the expired lot by id: the shelf keeps the good lot.
	w = do(t, engine, "POST", "/api/v1/products/"+itoa64(pid)+"/dispose", admin, map[string]any{
		"qtyMilli": 6000, "reason": "expired", "note": "fridge died", "batchId": oldBatch,
	})
	if w.Code != 200 {
		t.Fatalf("dispose by batch: %d %s", w.Code, w.Body.String())
	}
	if got := productStockMilli(t, engine, admin, pid); got != 6000 {
		t.Fatalf("shelf after disposal: want 6000 milli, got %d", got)
	}
	lots := batchesOf(t, engine, admin, pid)
	for _, b := range lots {
		if b["batchNo"] == "YG-OLD" && int64(b["qtyRemainingMilli"].(float64)) != 0 {
			t.Fatalf("disposed lot must hold 0, got %v", b["qtyRemainingMilli"])
		}
		if b["batchNo"] == "YG-NEW" && int64(b["qtyRemainingMilli"].(float64)) != 6000 {
			t.Fatalf("sibling lot must be untouched, got %v", b["qtyRemainingMilli"])
		}
	}

	// Health page: batch invariant reconciled, expired lot counted.
	w = do(t, engine, "GET", "/api/v1/system/health", admin, nil)
	if w.Code != 200 {
		t.Fatalf("health: %d %s", w.Code, w.Body.String())
	}
	h := dataMap(t, w)
	if got := int64(h["batchesChecked"].(float64)); got < 1 {
		t.Fatalf("health should check batch products, got %v", h["batchesChecked"])
	}
	if got := int64(h["batchDriftCount"].(float64)); got != 0 {
		t.Fatalf("batch drift must be zero, got %v (%v)", h["batchDriftCount"], h["batchDrift"])
	}
	if got := int64(h["expiredBatches"].(float64)); got != 0 {
		// The expired lot was disposed (remaining 0) — nothing counts.
		t.Fatalf("disposed lot must not count as expired-with-stock, got %v", h["expiredBatches"])
	}
	_ = fmt.Sprint("")
}
