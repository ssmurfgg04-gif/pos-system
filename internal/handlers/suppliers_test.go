package handlers_test

import (
        "testing"

        "github.com/gin-gonic/gin"
)

// prodState reads live cost/stock for one product (never hardcode seeds).
func prodState(t *testing.T, engine *gin.Engine, token string, id int64) (costCents int64, stock int) {
        t.Helper()
        w := do(t, engine, "GET", "/api/v1/products", token, nil)
        if w.Code != 200 {
                t.Fatalf("products: %d", w.Code)
        }
        for _, it := range decode(t, w)["data"].([]any) {
                m := it.(map[string]any)
                if int64(m["id"].(float64)) == id {
                        return int64(m["costCents"].(float64)), int(m["stockQty"].(float64))
                }
        }
        t.Fatalf("product %d not found", id)
        return 0, 0
}

// PO lifecycle: create → receive posts stock + averages cost; double-receive
// 409s; cancel of a received order 409s.
func TestSupplierPOLifecycle(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)

        w := do(t, engine, "POST", "/api/v1/suppliers", admin, map[string]any{
                "name": "Nairobi Wholesalers", "phone": "0722000000",
        })
        if w.Code != 201 {
                t.Fatalf("supplier: %d %s", w.Code, w.Body.String())
        }
        sid := int64(dataMap(t, w)["id"].(float64))

        costBefore, stockBefore := prodState(t, engine, admin, 1)

        w = do(t, engine, "POST", "/api/v1/purchase-orders", admin, map[string]any{
                "supplierId": sid,
                "items":      []map[string]any{{"productId": 1, "qty": 10, "costCents": 40000}},
                "note":       "test receive",
        })
        if w.Code != 201 {
                t.Fatalf("create PO: %d %s", w.Code, w.Body.String())
        }
        po := dataMap(t, w)
        if po["status"] != "PENDING" {
                t.Fatalf("new PO must be PENDING, got %v", po["status"])
        }
        poid := itoa64(po["id"])

        w = do(t, engine, "POST", "/api/v1/purchase-orders/"+poid+"/receive", admin, nil)
        if w.Code != 200 {
                t.Fatalf("receive: %d %s", w.Code, w.Body.String())
        }
        if dataMap(t, w)["status"] != "RECEIVED" {
                t.Fatal("received PO must be RECEIVED")
        }

        newStock := stockBefore + 10
        wantCost := (int64(stockBefore)*costBefore + 10*40000 + int64(newStock)/2) / int64(newStock)
        costAfter, stockAfter := prodState(t, engine, admin, 1)
        if stockAfter != newStock {
                t.Fatalf("stock should be %d, got %d", newStock, stockAfter)
        }
        if costAfter != wantCost {
                t.Fatalf("cost should average to %d, got %d", wantCost, costAfter)
        }

        w = do(t, engine, "POST", "/api/v1/purchase-orders/"+poid+"/receive", admin, nil)
        if w.Code != 409 {
                t.Fatalf("double receive should 409, got %d", w.Code)
        }
        w = do(t, engine, "POST", "/api/v1/purchase-orders/"+poid+"/cancel", admin, map[string]any{"reason": "x"})
        if w.Code != 409 {
                t.Fatalf("cancel of received PO should 409, got %d", w.Code)
        }
}

// Stock take: create snapshots expected, count edits, apply writes stock.
func TestStockTakeApply(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)

        _, stockBefore := prodState(t, engine, admin, 1)
        w := do(t, engine, "POST", "/api/v1/stock-takes", admin, map[string]any{
                "productIds": []int64{1}, "note": "test take",
        })
        if w.Code != 201 {
                t.Fatalf("create take: %d %s", w.Code, w.Body.String())
        }
        take := dataMap(t, w)
        items := take["items"].([]any)
        if len(items) != 1 || int64(items[0].(map[string]any)["expectedQty"].(float64)) != int64(stockBefore) {
                t.Fatalf("take should snapshot stock %d: %v", stockBefore, take)
        }
        takeID := itoa64(take["id"])

        counted := stockBefore + 5
        w = do(t, engine, "POST", "/api/v1/stock-takes/"+takeID+"/count", admin, map[string]any{
                "counts": map[string]int{"1": counted},
        })
        if w.Code != 200 {
                t.Fatalf("count: %d %s", w.Code, w.Body.String())
        }
        w = do(t, engine, "POST", "/api/v1/stock-takes/"+takeID+"/apply", admin, nil)
        if w.Code != 200 {
                t.Fatalf("apply: %d %s", w.Code, w.Body.String())
        }
        if dataMap(t, w)["status"] != "APPLIED" {
                t.Fatal("applied take must be APPLIED")
        }
        _, stockAfter := prodState(t, engine, admin, 1)
        if stockAfter != counted {
                t.Fatalf("stock should be counted %d, got %d", counted, stockAfter)
        }
        w = do(t, engine, "POST", "/api/v1/stock-takes/"+takeID+"/apply", admin, nil)
        if w.Code != 409 {
                t.Fatalf("double apply should 409, got %d", w.Code)
        }
}
