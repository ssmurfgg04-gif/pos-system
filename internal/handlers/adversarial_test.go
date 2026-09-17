package handlers_test

import (
        "net/url"
        "strings"
        "testing"
)

// Injection strings thrown at every text input in this file. Query params
// go through QueryEscape exactly like a browser would — the server still
// sees the raw quotes, semicolons, and comment markers after decoding.
var injections = []string{
        `' OR '1'='1`,
        `'; DROP TABLE users; --`,
        `" OR ""="`,
        `' UNION SELECT * FROM users --`,
        `1; DELETE FROM products`,
        `%`,
        `_%_`,
        "\\",
}

// TestSQLInjectionSurfaces fires classic payloads at every text-accepting
// endpoint: nothing may error 500 (malformed SQL), drop tables, or leak rows.
func TestSQLInjectionSurfaces(t *testing.T) {
        t.Setenv("ALLOW_SIGNUP", "true")
        engine, admin, _, _ := newTestServer(t)
        tokA, _ := signup(t, engine, "mallory", "mallory12345", "Mallory Shop")

        for _, inj := range injections {
                q := url.QueryEscape(inj)
                for _, path := range []string{
                        "/api/v1/products?search=" + q,
                        "/api/v1/customers?search=" + q,
                        "/api/v1/suppliers?search=" + q,
                        "/api/v1/orders?search=" + q,
                } {
                        w := do(t, engine, "GET", path, admin, nil)
                        if w.Code == 500 {
                                t.Fatalf("500 on search %q", inj)
                        }
                }
                // Login bypass attempts (JSON body is marshaled safely —
                // placeholders, never concat — which is exactly the point).
                w := do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
                        "username": inj, "password": "whatever",
                })
                if w.Code != 401 && w.Code != 400 {
                        t.Fatalf("injection login %q should 401/400, got %d", inj, w.Code)
                }
                // Stored payloads must not break writes or reads.
                w = do(t, engine, "POST", "/api/v1/customers", tokA, map[string]any{
                        "name": inj, "creditLimitCents": 1000,
                })
                if w.Code != 201 && w.Code != 400 {
                        t.Fatalf("customer name %q: unexpected %d", inj, w.Code)
                }
        }
        // Tables intact after the barrage: seeded login still works.
        // (newTestServer rotates seeded passwords — use the rotated one.)
        w := do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
                "username": "admin", "password": rotatedPassword,
        })
        if w.Code != 200 {
                t.Fatalf("login after injection barrage: %d", w.Code)
        }
        tok := dataMap(t, w)["token"].(string)
        w = do(t, engine, "GET", "/api/v1/products", tok, nil)
        if w.Code != 200 {
                t.Fatalf("products after injection barrage: %d", w.Code)
        }
}

// TestCrossShopSweep replays one shop's IDs against another shop's token
// across every resource type: all must 404 (never 200, never чужой data).
func TestCrossShopSweep(t *testing.T) {
        t.Setenv("ALLOW_SIGNUP", "true")
        engine, _, _, _ := newTestServer(t)
        tokA, _ := signup(t, engine, "sweepa", "sweepa12345", "Sweep A")
        tokB, _ := signup(t, engine, "sweepb", "sweepb12345", "Sweep B")

        mk := func(method, path string, token string, body any) int {
                return do(t, engine, method, path, token, body).Code
        }
        // Alice-side fixtures.
        var aliceProd, aliceCust, aliceSup, alicePO, aliceTake int64
        w := do(t, engine, "POST", "/api/v1/products", tokA, map[string]any{
                "sku": "SW1", "name": "Sweep Widget", "categoryId": 1,
                "priceCents": 1000, "costCents": 400, "stockQty": 5, "trackStock": true,
        })
        if w.Code != 201 {
                t.Fatalf("seed product: %d", w.Code)
        }
        aliceProd = int64(dataMap(t, w)["id"].(float64))
        w = do(t, engine, "POST", "/api/v1/customers", tokA, map[string]any{"name": "Sweep Debtor", "creditLimitCents": 50000})
        aliceCust = int64(dataMap(t, w)["id"].(float64))
        w = do(t, engine, "POST", "/api/v1/suppliers", tokA, map[string]any{"name": "Sweep Supply"})
        aliceSup = int64(dataMap(t, w)["id"].(float64))
        w = do(t, engine, "POST", "/api/v1/purchase-orders", tokA, map[string]any{
                "supplierId": aliceSup,
                "items":      []map[string]any{{"productId": aliceProd, "qty": 1, "costCents": 100}},
        })
        alicePO = int64(dataMap(t, w)["id"].(float64))
        w = do(t, engine, "POST", "/api/v1/stock-takes", tokA, map[string]any{"productIds": []int64{aliceProd}})
        aliceTake = int64(dataMap(t, w)["id"].(float64))
        w = do(t, engine, "POST", "/api/v1/orders/checkout", tokA, map[string]any{
                "items": []map[string]any{{"productId": aliceProd, "qty": 1}},
                "paymentMethod": "cash", "clientUuid": "sweep-1",
        })
        aliceOrder := int64(dataMap(t, w)["id"].(float64))

        // Bob's token against every Alice ID: reads 404, writes 404/403.
        reads := []string{
                "/api/v1/orders/" + itoa64(aliceOrder),
                "/api/v1/orders/" + itoa64(aliceOrder) + "/receipt",
                "/api/v1/customers/" + itoa64(aliceCust) + "/ledger",
                "/api/v1/purchase-orders/" + itoa64(alicePO),
                "/api/v1/stock-takes/" + itoa64(aliceTake),
        }
        for _, p := range reads {
                if code := mk("GET", p, tokB, nil); code != 404 {
                        t.Fatalf("cross-shop GET %s should 404, got %d", p, code)
                }
        }
        writes := []struct {
                method, path string
                body         any
        }{
                {"PUT", "/api/v1/products/" + itoa64(aliceProd), map[string]any{"name": "Hijacked", "categoryId": 1}},
                {"POST", "/api/v1/orders/" + itoa64(aliceOrder) + "/void", map[string]any{"reason": "x"}},
                {"POST", "/api/v1/purchase-orders/" + itoa64(alicePO) + "/receive", nil},
                {"POST", "/api/v1/stock-takes/" + itoa64(aliceTake) + "/apply", nil},
                {"POST", "/api/v1/customers/" + itoa64(aliceCust) + "/payments", map[string]any{"amountCents": 1}},
        }
        for _, c := range writes {
                if code := mk(c.method, c.path, tokB, c.body); code != 404 && code != 403 {
                        t.Fatalf("cross-shop %s %s should 404/403, got %d", c.method, c.path, code)
                }
        }
        // Sanity: Alice's own reads still work.
        if code := mk("GET", "/api/v1/orders/"+itoa64(aliceOrder), tokA, nil); code != 200 {
                t.Fatalf("own order should 200, got %d", code)
        }
}

// TestMalformedMoneyNumbers rejects non-positive and absurd quantities.
func TestMalformedMoneyNumbers(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        for _, body := range []map[string]any{
                {"items": []map[string]any{{"productId": 1, "qty": 0}}, "paymentMethod": "cash", "clientUuid": "m0"},
                {"items": []map[string]any{{"productId": 1, "qty": -5}}, "paymentMethod": "cash", "clientUuid": "mneg"},
                {"items": []map[string]any{{"productId": 999999, "qty": 1}}, "paymentMethod": "cash", "clientUuid": "midx"},
        } {
                w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, body)
                if w.Code < 400 || w.Code >= 500 {
                        t.Fatalf("malformed checkout should 4xx, got %d %s", w.Code, w.Body.String())
                }
        }
}

// TestMoneyBoundsOverflow tries integer-overflow theft: absurd quantities
// and prices must 400, never wrap into negative totals.
func TestMoneyBoundsOverflow(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        huge := []map[string]any{
                {"items": []map[string]any{{"productId": 1, "qty": 100001}}, "paymentMethod": "cash", "clientUuid": "o1"},
                {"items": []map[string]any{{"productId": 1, "qty": 9007199254740993}}, "paymentMethod": "cash", "clientUuid": "o2"},
        }
        for i, body := range huge {
                w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, body)
                if w.Code != 400 && w.Code != 422 {
                        t.Fatalf("oversize qty %d should 400/422, got %d %s", i, w.Code, w.Body.String())
                }
        }
        // Absurd catalog prices rejected at write time.
        for _, body := range []map[string]any{
                {"sku": "BIG1", "name": "Big", "categoryId": 1, "priceCents": 1000000000001},
                {"sku": "BIGN", "name": "Big Neg", "categoryId": 1, "priceCents": -5},
                {"sku": "BIGS", "name": "Big Stock", "categoryId": 1, "priceCents": 100, "stockQty": 1000000001},
        } {
                w := do(t, engine, "POST", "/api/v1/products", admin, body)
                if w.Code != 400 {
                        t.Fatalf("out-of-range product should 400, got %d %s", w.Code, w.Body.String())
                }
        }
        // Absurd override rejected even with the permission.
        w := do(t, engine, "POST", "/api/v1/orders/checkout", admin, map[string]any{
                "items": []map[string]any{{"productId": 1, "qty": 1, "unitPriceCents": 1000000000001}},
                "paymentMethod": "cash", "clientUuid": "o3",
        })
        if w.Code != 400 && w.Code != 422 {
                t.Fatalf("oversize override should 400/422, got %d", w.Code)
        }
        // Absurd stock adjustment rejected.
        w = do(t, engine, "POST", "/api/v1/products/1/adjust-stock", admin, map[string]any{"delta": 100000001})
        if w.Code != 400 {
                t.Fatalf("oversize adjust should 400, got %d", w.Code)
        }
        // Sane boundary values still work (caps are ceilings, not walls):
        // stock a product deep, then buy exactly the max line quantity.
        w = do(t, engine, "POST", "/api/v1/products", admin, map[string]any{
                "sku": "BULK1", "name": "Bulk Grain", "categoryId": 1,
                "priceCents": 100, "costCents": 50, "stockQty": 200000, "trackStock": true,
        })
        if w.Code != 201 {
                t.Fatalf("bulk product: %d %s", w.Code, w.Body.String())
        }
        bulkID := int64(dataMap(t, w)["id"].(float64))
        w = do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items": []map[string]any{{"productId": bulkID, "qty": 100000}},
                "paymentMethod": "cash", "clientUuid": "o4",
        })
        if w.Code != 201 {
                t.Fatalf("max-qty checkout should 201, got %d %s", w.Code, w.Body.String())
        }
}

// TestSignupRateLimited hammers signup: eventually 429, never 500.
func TestSignupRateLimited(t *testing.T) {
        t.Setenv("ALLOW_SIGNUP", "true")
        engine, _, _, _ := newTestServer(t)
        limited := false
        for i := 0; i < 15; i++ {
                w := do(t, engine, "POST", "/api/v1/auth/signup", "", map[string]any{
                        "username": "flooder",
                        "password": "flooder12345",
                        "shopName": "Flood Shop",
                })
                if w.Code == 429 {
                        limited = true
                        break
                }
                if w.Code >= 500 {
                        t.Fatalf("signup flood should never 500, got %d", w.Code)
                }
        }
        if !limited {
                t.Fatal("signup flood should eventually rate-limit")
        }
}

// TestXSSPayloadRoundTrip stores script payloads and asserts the receipt
// HTML escapes them (html/template auto-escapes; this pins it).
func TestXSSPayloadRoundTrip(t *testing.T) {
        t.Setenv("ALLOW_SIGNUP", "true")
        engine, _, _, _ := newTestServer(t)
        tok, _ := signup(t, engine, "xssowner", "xssowner123", "XSS Shop")
        payload := `<script>alert(1)</script><img src=x onerror=alert(2)>`
        w := do(t, engine, "POST", "/api/v1/products", tok, map[string]any{
                "sku": "XS1", "name": payload, "categoryId": 1,
                "priceCents": 100, "costCents": 10, "stockQty": 5, "trackStock": true,
        })
        if w.Code != 201 {
                t.Fatalf("payload product: %d", w.Code)
        }
        pid := int64(dataMap(t, w)["id"].(float64))
        w = do(t, engine, "POST", "/api/v1/orders/checkout", tok, map[string]any{
                "items": []map[string]any{{"productId": pid, "qty": 1}},
                "paymentMethod": "cash", "clientUuid": "xss-1",
        })
        oid := int64(dataMap(t, w)["id"].(float64))
        w = do(t, engine, "GET", "/api/v1/orders/"+itoa64(oid)+"/receipt", tok, nil)
        if w.Code != 200 {
                t.Fatalf("receipt: %d", w.Code)
        }
        html := w.Body.String()
        if strings.Contains(html, "<script>alert(1)</script>") {
                t.Fatal("receipt HTML must escape script payloads")
        }
        if !strings.Contains(html, "&lt;script&gt;") {
                t.Fatal("receipt HTML should contain the escaped payload")
        }
}
