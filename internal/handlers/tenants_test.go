package handlers_test

import (
        "testing"

        "github.com/gin-gonic/gin"
)

func signup(t *testing.T, engine *gin.Engine, username, password, shop string) (token string, shopID string) {
        t.Helper()
        w := do(t, engine, "POST", "/api/v1/auth/signup", "", map[string]any{
                "username": username, "password": password, "shopName": shop,
        })
        if w.Code != 201 {
                t.Fatalf("signup %s: %d %s", username, w.Code, w.Body.String())
        }
        d := dataMap(t, w)
        tok, _ := d["token"].(string)
        sm, _ := d["shop"].(map[string]any)
        id, _ := sm["id"].(string)
        if tok == "" || id == "" {
                t.Fatalf("signup response missing token/shop: %v", d)
        }
        return tok, id
}

// Signup provisions an isolated shop per user; validation is enforced.
func TestSignupLifecycle(t *testing.T) {
        t.Setenv("ALLOW_SIGNUP", "true")
        engine, _, _, _ := newTestServer(t)

        tokA, shopA := signup(t, engine, "alice", "alice12345", "Alice Shop")
        if shopA == "" || tokA == "" {
                t.Fatal("signup must return token and shop")
        }
        // Duplicate username (even for another shop) is rejected.
        w := do(t, engine, "POST", "/api/v1/auth/signup", "", map[string]any{
                "username": "Alice", "password": "other12345", "shopName": "Copy Shop",
        })
        if w.Code != 409 {
                t.Fatalf("duplicate signup should 409, got %d %s", w.Code, w.Body.String())
        }
        // Weak input rejected.
        w = do(t, engine, "POST", "/api/v1/auth/signup", "", map[string]any{
                "username": "zed", "password": "short", "shopName": "Zed Shop",
        })
        if w.Code != 400 {
                t.Fatalf("short password should 400, got %d", w.Code)
        }
        // Login lands back in the same shop.
        w = do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
                "username": "alice", "password": "alice12345",
        })
        if w.Code != 200 {
                t.Fatalf("login: %d %s", w.Code, w.Body.String())
        }
        user := dataMap(t, w)["user"].(map[string]any)
        if user["shopId"] != shopA {
                t.Fatalf("login must land in shop %s, got %v", shopA, user["shopId"])
        }
        if user["mustRotate"] != false {
                t.Fatal("fresh signup credentials must not need rotation")
        }
}

// Tenant isolation: rows created in shop A are invisible from shop B —
// lists omit them and direct-ID reads 404 (never revealing existence).
func TestTenantIsolation(t *testing.T) {
        t.Setenv("ALLOW_SIGNUP", "true")
        engine, _, _, _ := newTestServer(t)

        tokA, _ := signup(t, engine, "alice", "alice12345", "Alice Shop")
        tokB, _ := signup(t, engine, "bob1", "bob123456", "Bob Shop")

        // Alice stocks a product, sells it, and opens a tab customer.
        w := do(t, engine, "POST", "/api/v1/products", tokA, map[string]any{
                "sku": "AL1", "name": "Alice Widget", "categoryId": 1,
                "priceCents": 1000, "costCents": 400, "stockQty": 10, "trackStock": true,
        })
        if w.Code != 201 {
                t.Fatalf("alice product: %d %s", w.Code, w.Body.String())
        }
        aliceProd := int64(dataMap(t, w)["id"].(float64))
        w = do(t, engine, "POST", "/api/v1/customers", tokA, map[string]any{
                "name": "Alice Debtor", "creditLimitCents": 50000,
        })
        if w.Code != 201 {
                t.Fatalf("alice customer: %d %s", w.Code, w.Body.String())
        }
        aliceCust := int64(dataMap(t, w)["id"].(float64))

        // Bob's world contains none of it.
        w = do(t, engine, "GET", "/api/v1/products", tokB, nil)
        if w.Code != 200 {
                t.Fatalf("bob products: %d", w.Code)
        }
        for _, it := range decode(t, w)["data"].([]any) {
                if int64(it.(map[string]any)["id"].(float64)) == aliceProd {
                        t.Fatal("bob must not see alice's product")
                }
        }
        w = do(t, engine, "GET", "/api/v1/customers", tokB, nil)
        for _, it := range decode(t, w)["data"].([]any) {
                if int64(it.(map[string]any)["id"].(float64)) == aliceCust {
                        t.Fatal("bob must not see alice's customer")
                }
        }
        // Direct-ID reads 404 (existence not leaked).
        w = do(t, engine, "GET", "/api/v1/customers/"+itoa64(aliceCust)+"/ledger", tokB, nil)
        if w.Code != 404 {
                t.Fatalf("cross-shop ledger should 404, got %d", w.Code)
        }
}

// Signup stays closed unless explicitly enabled.
func TestSignupDisabledByDefault(t *testing.T) {
        engine, _, _, _ := newTestServer(t)
        w := do(t, engine, "POST", "/api/v1/auth/signup", "", map[string]any{
                "username": "mallory", "password": "mallory123", "shopName": "Mallory Shop",
        })
        if w.Code != 403 {
                t.Fatalf("signup without ALLOW_SIGNUP should 403, got %d", w.Code)
        }
}
