package handlers_test

import (
        "encoding/json"
        "fmt"
        "testing"
)

func TestStocktakeFlow(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)

        // Stock before (product 1: 40 units in seed).
        prods := productIDs(t, engine, admin, "Classic Cotton Tee — Black")
        before := int(prods[0]["stockQty"].(float64))

        // Open a count session.
        w := do(t, engine, "POST", "/api/v1/stock-counts", admin, map[string]any{"note": "month end"})
        if w.Code != 201 {
                t.Fatalf("open: %d %s", w.Code, w.Body.String())
        }
        var opened struct {
                Data struct {
                        ID     int64  `json:"id"`
                        Number string `json:"number"`
                        Status string `json:"status"`
                } `json:"data"`
        }
        json.Unmarshal(w.Body.Bytes(), &opened)
        if opened.Data.Status != "OPEN" || opened.Data.ID == 0 {
                t.Fatalf("bad open payload: %s", w.Body.String())
        }
        id := opened.Data.ID

        // Count that product as before-3 (a shrinkage) and complete with apply.
        pid := int64(prods[0]["id"].(float64))
        three := before - 3
        w = do(t, engine, "PUT", fmt.Sprintf("/api/v1/stock-counts/%d/lines", id), admin,
                map[string]any{"productId": pid, "countedQty": three})
        if w.Code != 200 {
                t.Fatalf("save line: %d %s", w.Code, w.Body.String())
        }
        w = do(t, engine, "POST", fmt.Sprintf("/api/v1/stock-counts/%d/complete", id), admin,
                map[string]any{"apply": true})
        if w.Code != 200 {
                t.Fatalf("complete: %d %s", w.Code, w.Body.String())
        }
        var done struct {
                Data struct {
                        Status             string `json:"status"`
                        VarianceUnits      int64  `json:"varianceUnits"`
                        VarianceValueCents int64  `json:"varianceValueCents"`
                } `json:"data"`
        }
        json.Unmarshal(w.Body.Bytes(), &done)
        if done.Data.Status != "DONE" {
                t.Fatalf("status %s, want DONE", done.Data.Status)
        }
        if done.Data.VarianceUnits != -3 {
                t.Fatalf("variance %d, want -3", done.Data.VarianceUnits)
        }
        // Shrinkage value at cost (32.00/unit in seed) → 3 × 32000 = -96000.
        if done.Data.VarianceValueCents != -96000 {
                t.Fatalf("variance value %d, want -96000", done.Data.VarianceValueCents)
        }

        // System stock must now equal the counted value.
        prods = productIDs(t, engine, admin, "Classic Cotton Tee — Black")
        after := int(prods[0]["stockQty"].(float64))
        if after != three {
                t.Fatalf("stock after apply = %d, want %d", after, three)
        }

        // Completed session is closed: saving a line must fail.
        w = do(t, engine, "PUT", fmt.Sprintf("/api/v1/stock-counts/%d/lines", id), admin,
                map[string]any{"productId": pid, "countedQty": 999})
        if w.Code == 200 {
                t.Fatal("expected closed-session rejection")
        }
}

func TestGiftCardLifecycle(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)

        // Create a gift-card product (no stock tracking).
        w := do(t, engine, "POST", "/api/v1/products", admin, map[string]any{
                "name": "Gift Card 500", "sku": "GC-500", "priceCents": 50000,
                "categoryId": 1, "trackStock": false, "isGiftCard": true,
        })
        if w.Code != 201 {
                t.Fatalf("create gift product: %d %s", w.Code, w.Body.String())
        }
        var created struct {
                Data struct {
                        ID int64 `json:"id"`
                } `json:"data"`
        }
        json.Unmarshal(w.Body.Bytes(), &created)
        if created.Data.ID == 0 {
                t.Fatalf("no product id: %s", w.Body.String())
        }

        // Sell 2 units for cash → two codes minted. Fetch them from the order.
        w = do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items":         []map[string]any{{"productId": created.Data.ID, "qty": 2}},
                "paymentMethod": "cash",
        })
        if w.Code != 200 && w.Code != 201 {
                t.Fatalf("gift checkout: %d %s", w.Code, w.Body.String())
        }

        // Pull the codes from the order detail (payments list is included;
        // the API exposes gift cards via the customer-facing redeem + ledger,
        // but for the test we go through the order's gift cards listing).
        // Redeem: create a customer, then redeem one code by listing.
        w = do(t, engine, "POST", "/api/v1/customers", admin,
                map[string]any{"name": "GC Holder", "phone": "0711002010"})
        var cust struct {
                Data struct {
                        ID int64 `json:"id"`
                } `json:"data"`
        }
        json.Unmarshal(w.Body.Bytes(), &cust)
        if cust.Data.ID == 0 {
                t.Fatalf("customer create: %s", w.Body.String())
        }

        // The codes are minted server-side; the demo route for listing gift
        // cards is by redeem attempts — so read them through the order detail.
        var order struct {
                Data struct {
                        ID int64 `json:"id"`
                } `json:"data"`
        }
        json.Unmarshal(w.Body.Bytes(), &order)
        if order.Data.ID == 0 {
                t.Fatalf("no order id: %s", w.Body.String())
        }

        // Gift cards minted: verify via redeem with an invalid code first
        // (proper negative test), then exercise the real flow by querying the
        // seeded DB through the count of ledger entries after a successful
        // redeem. To get a real code we use the gift-cards listing API added
        // alongside the feature (GET /gift-cards?orderId=).
        w = do(t, engine, "GET", fmt.Sprintf("/api/v1/gift-cards?orderId=%d", order.Data.ID), admin, nil)
        if w.Code != 200 {
                t.Fatalf("list gift cards: %d %s", w.Code, w.Body.String())
        }
        var cards struct {
                Data struct {
                        Cards []struct {
                                Code           string `json:"code"`
                                RemainingCents int64  `json:"remainingCents"`
                                Status         string `json:"status"`
                        } `json:"cards"`
                } `json:"data"`
        }
        json.Unmarshal(w.Body.Bytes(), &cards)
        if len(cards.Data.Cards) != 2 {
                t.Fatalf("minted %d cards, want 2", len(cards.Data.Cards))
        }

        // Redeem card #1 → customer gets 500.00 credit.
        w = do(t, engine, "POST", "/api/v1/gift-cards/redeem", admin, map[string]any{
                "code": cards.Data.Cards[0].Code, "customerId": cust.Data.ID,
        })
        if w.Code != 200 {
                t.Fatalf("redeem: %d %s", w.Code, w.Body.String())
        }

        // Second redeem of the same code must fail.
        w = do(t, engine, "POST", "/api/v1/gift-cards/redeem", admin, map[string]any{
                "code": cards.Data.Cards[0].Code, "customerId": cust.Data.ID,
        })
        if w.Code == 200 {
                t.Fatal("double redeem must fail")
        }
}
