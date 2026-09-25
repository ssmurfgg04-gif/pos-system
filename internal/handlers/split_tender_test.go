package handlers_test

import (
        "encoding/json"
        "fmt"
        "testing"
)

// splitOrderPayments extracts status and payment rows from a checkout
// payload (the order sits directly under "data").
func splitOrderPayments(t *testing.T, body []byte) (string, []map[string]any) {
        t.Helper()
        var env struct {
                Data struct {
                        Number   string `json:"number"`
                        Status   string `json:"status"`
                        Payments []struct {
                                Method      string  `json:"method"`
                                Status      string  `json:"status"`
                                AmountCents float64 `json:"amountCents"`
                        } `json:"payments"`
                } `json:"data"`
        }
        if err := json.Unmarshal(body, &env); err != nil {
                t.Fatalf("order decode: %v", err)
        }
        pays := []map[string]any{}
        for _, p := range env.Data.Payments {
                pays = append(pays, map[string]any{
                        "method": p.Method, "status": p.Status, "amountCents": p.AmountCents,
                })
        }
        return env.Data.Status, pays
}

func TestSplitTenderCashPlusCredit(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)

        // Create a customer and load prepaid credit.
        w := do(t, engine, "POST", "/api/v1/customers", admin,
                map[string]any{"name": "Split Tester", "phone": "0711002003"})
        if w.Code != 201 {
                t.Fatalf("create customer: %d %s", w.Code, w.Body.String())
        }
        var created struct {
                Data struct {
                        ID int64 `json:"id"`
                } `json:"data"`
        }
        json.Unmarshal(w.Body.Bytes(), &created)
        cid := created.Data.ID
        w = do(t, engine, "POST", fmt.Sprintf("/api/v1/customers/%d/credit-topup", cid), admin,
                map[string]any{"amountCents": 100000, "note": "split test"})
        if w.Code != 200 {
                t.Fatalf("credit topup: %d %s", w.Code, w.Body.String())
        }

        // Split: 60.00 cash + 50.00 credit (product 1 x2 = 110.00 in seed).
        w = do(t, engine, "POST", "/api/v1/orders/checkout", cashier, checkoutBody(map[string]any{
                "paymentMethod": "cash",
                "customerId":    cid,
                "splitPayments": []map[string]any{
                        {"method": "cash", "amountCents": 60000},
                        {"method": "credit", "amountCents": 50000},
                },
        }))
        if w.Code != 200 && w.Code != 201 {
                t.Fatalf("split checkout: %d %s", w.Code, w.Body.String())
        }
        status, pays := splitOrderPayments(t, w.Body.Bytes())
        if status != "PAID" {
                t.Fatalf("status %s, want PAID for all-instant split", status)
        }
        if len(pays) != 2 {
                t.Fatalf("payment rows %d, want 2 legs", len(pays))
        }
        seen := map[string]map[string]any{}
        for _, p := range pays {
                seen[p["method"].(string)] = p
                if p["status"] != "COMPLETED" {
                        t.Fatalf("leg %v not completed", p)
                }
        }
        if seen["cash"]["amountCents"].(float64) != 60000 || seen["credit"]["amountCents"].(float64) != 50000 {
                t.Fatalf("leg amounts wrong: %v", pays)
        }

        // Store credit must have dropped by exactly the credit leg.
        w = do(t, engine, "GET", "/api/v1/customers", admin, nil)
        var list struct {
                Data struct {
                        Customers []struct {
                                ID               int64   `json:"id"`
                                StoreCreditCents float64 `json:"storeCreditCents"`
                        } `json:"customers"`
                } `json:"data"`
        }
        json.Unmarshal(w.Body.Bytes(), &list)
        for _, c := range list.Data.Customers {
                if c.ID == cid && c.StoreCreditCents != 50000 {
                        t.Fatalf("credit after = %v, want 50000", c.StoreCreditCents)
                }
        }
}

func TestSplitTenderRejectsBadSums(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        cases := []map[string]any{
                { // sum too small
                        "splitPayments": []map[string]any{{"method": "cash", "amountCents": 1000}},
                },
                { // two async legs
                        "splitPayments": []map[string]any{
                                {"method": "mpesa", "amountCents": 55000, "phone": "254712345678"},
                                {"method": "paystack", "amountCents": 55000, "email": "x@y.z"},
                        },
                },
                { // negative leg (sum deliberately matches — the leg check must fire)
                        "splitPayments": []map[string]any{
                                {"method": "cash", "amountCents": 160000},
                                {"method": "cash", "amountCents": -50000},
                        },
                },
        }
        for i, extra := range cases {
                body := checkoutBody(map[string]any{"paymentMethod": "cash"})
                for k, v := range extra {
                        body[k] = v
                }
                w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, body)
                if w.Code == 200 {
                        t.Fatalf("case %d: expected rejection, got %d %s", i, w.Code, w.Body.String())
                }
        }
}

func TestSplitTenderWithAsyncMpesaLeg(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        // cash 90.00 + mpesa 20.00 (mock STK auto-completes in ~200ms).
        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, checkoutBody(map[string]any{
                "paymentMethod": "cash",
                "splitPayments": []map[string]any{
                        {"method": "cash", "amountCents": 90000},
                        {"method": "mpesa", "amountCents": 20000, "phone": "254712345678"},
                },
        }))
        if w.Code != 200 && w.Code != 201 {
                t.Fatalf("split checkout: %d %s", w.Code, w.Body.String())
        }
        // The cash leg must be COMPLETED immediately; order still PENDING
        // (or already PAID once the fast mock settled — both legal).
        status, pays := splitOrderPayments(t, w.Body.Bytes())
        if status != "PENDING" && status != "PAID" {
                t.Fatalf("status %s, want PENDING or PAID", status)
        }
        cashDone := false
        for _, p := range pays {
                if p["method"] == "cash" && p["status"] == "COMPLETED" {
                        cashDone = true
                }
        }
        if !cashDone {
                t.Fatalf("cash leg missing/completed: %v", pays)
        }
}
