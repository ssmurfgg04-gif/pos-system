package handlers_test

import (
        "encoding/json"
        "fmt"
        "testing"
)

// Regression tests for the stocktake count→apply chain (shop-floor review:
// one unreproducible 500 on count, and a silent-no-op class where a product
// missing from the session snapshot returned 200 without recording anything
// — a data loss with a green light on a stock path). These pin the loud,
// fail-closed behaviour.
func TestStocktakeCountApplyRegression(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)

        // ---- 1. Product created AFTER the session opened must be rejected, not
        // silently ignored. ----
        w := do(t, engine, "POST", "/api/v1/stock-counts", admin, map[string]any{"note": "regression"})
        if w.Code != 201 {
                t.Fatalf("open: %d %s", w.Code, w.Body.String())
        }
        var opened struct {
                Data struct {
                        ID int64 `json:"id"`
                } `json:"data"`
        }
        json.Unmarshal(w.Body.Bytes(), &opened)
        id := opened.Data.ID

        w = do(t, engine, "POST", "/api/v1/products", admin, map[string]any{
                "name": "Late Arrival SKU", "sku": "LATE-1", "priceCents": 10000,
                "categoryId": 1, "stockQty": 25, "costCents": 4000,
        })
        if w.Code != 201 {
                t.Fatalf("create late product: %d %s", w.Code, w.Body.String())
        }
        var created struct {
                Data struct {
                        ID int64 `json:"id"`
                } `json:"data"`
        }
        json.Unmarshal(w.Body.Bytes(), &created)
        if created.Data.ID == 0 {
                t.Fatalf("no late product id: %s", w.Body.String())
        }

        w = do(t, engine, "PUT", fmt.Sprintf("/api/v1/stock-counts/%d/lines", id), admin,
                map[string]any{"productId": created.Data.ID, "countedQty": 25})
        if w.Code != 422 {
                t.Fatalf("late product count: got %d, want 422 (silent no-op is forbidden): %s",
                        w.Code, w.Body.String())
        }

        // ---- 2. Zero is a legitimate count ("shelf is empty") and must apply. ----
        prods := productIDs(t, engine, admin, "Classic Cotton Tee, Black")
        pid := int64(prods[0]["id"].(float64))
        w = do(t, engine, "PUT", fmt.Sprintf("/api/v1/stock-counts/%d/lines", id), admin,
                map[string]any{"productId": pid, "countedQty": 0})
        if w.Code != 200 {
                t.Fatalf("zero count rejected: %d %s", w.Code, w.Body.String())
        }
        w = do(t, engine, "POST", fmt.Sprintf("/api/v1/stock-counts/%d/complete", id), admin,
                map[string]any{"apply": true})
        if w.Code != 200 {
                t.Fatalf("apply with zero count: %d %s", w.Code, w.Body.String())
        }
        prods = productIDs(t, engine, admin, "Classic Cotton Tee, Black")
        if got := int(prods[0]["stockQty"].(float64)); got != 0 {
                t.Fatalf("stock after zero-count apply = %d, want 0", got)
        }

        // ---- 3. Apply is terminal: a second complete must be rejected. ----
        w = do(t, engine, "POST", fmt.Sprintf("/api/v1/stock-counts/%d/complete", id), admin,
                map[string]any{"apply": true})
        if w.Code != 422 {
                t.Fatalf("double apply: got %d, want 422", w.Code)
        }

        // ---- 4. Close-without-counting: fresh session, immediate apply →
        // variance 0, no product touched. ----
        w = do(t, engine, "POST", "/api/v1/stock-counts", admin, map[string]any{"note": "empty close"})
        if w.Code != 201 {
                t.Fatalf("second open: %d %s", w.Code, w.Body.String())
        }
        json.Unmarshal(w.Body.Bytes(), &opened)
        id2 := opened.Data.ID
        w = do(t, engine, "POST", fmt.Sprintf("/api/v1/stock-counts/%d/complete", id2), admin,
                map[string]any{"apply": true})
        if w.Code != 200 {
                t.Fatalf("empty apply: %d %s", w.Code, w.Body.String())
        }
        var done struct {
                Data struct {
                        Status             string `json:"status"`
                        VarianceUnits      int64  `json:"varianceUnits"`
                        VarianceValueCents int64  `json:"varianceValueCents"`
                        LinesCounted       int64  `json:"linesCounted"`
                } `json:"data"`
        }
        json.Unmarshal(w.Body.Bytes(), &done)
        if done.Data.Status != "DONE" || done.Data.VarianceUnits != 0 || done.Data.LinesCounted != 0 {
                t.Fatalf("empty close payload wrong: %+v", done.Data)
        }

        // ---- 5. Omitting countedQty clears a previously counted line. ----
        w = do(t, engine, "POST", "/api/v1/stock-counts", admin, map[string]any{"note": "clear check"})
        if w.Code != 201 {
                t.Fatalf("third open: %d %s", w.Code, w.Body.String())
        }
        json.Unmarshal(w.Body.Bytes(), &opened)
        id2 = opened.Data.ID
        w = do(t, engine, "PUT", fmt.Sprintf("/api/v1/stock-counts/%d/lines", id2), admin,
                map[string]any{"productId": pid, "countedQty": 7})
        if w.Code != 200 {
                t.Fatalf("recount on fresh session: %d %s", w.Code, w.Body.String())
        }
        w = do(t, engine, "PUT", fmt.Sprintf("/api/v1/stock-counts/%d/lines", id2), admin,
                map[string]any{"productId": pid}) // countedQty omitted → clear
        if w.Code != 200 {
                t.Fatalf("clear count line: %d %s", w.Code, w.Body.String())
        }
        w = do(t, engine, "GET", fmt.Sprintf("/api/v1/stock-counts/%d", id2), admin, nil)
        if w.Code != 200 {
                t.Fatalf("get session: %d %s", w.Code, w.Body.String())
        }
        var detail struct {
                Data struct {
                        LinesCounted int64 `json:"linesCounted"`
                } `json:"data"`
        }
        json.Unmarshal(w.Body.Bytes(), &detail)
        if detail.Data.LinesCounted != 0 {
                t.Fatalf("clear did not decrement linesCounted: %d", detail.Data.LinesCounted)
        }
        // And applying leaves stock untouched (the clear worked).
        w = do(t, engine, "POST", fmt.Sprintf("/api/v1/stock-counts/%d/complete", id2), admin,
                map[string]any{"apply": true})
        if w.Code != 200 {
                t.Fatalf("apply after clear: %d %s", w.Code, w.Body.String())
        }
        prods = productIDs(t, engine, admin, "Classic Cotton Tee, Black")
        if got := int(prods[0]["stockQty"].(float64)); got != 0 {
                t.Fatalf("stock moved after clear+apply: %d, want 0 (previous apply's value)", got)
        }
}
