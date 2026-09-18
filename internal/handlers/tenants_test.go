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
        // Same username in a different shop is allowed (owner with two shops).
        w := do(t, engine, "POST", "/api/v1/auth/signup", "", map[string]any{
                "username": "Alice", "password": "other12345", "shopName": "Copy Shop",
        })
        if w.Code != 201 {
                t.Fatalf("same username in new shop should succeed, got %d %s", w.Code, w.Body.String())
        }
        copyShop := dataMap(t, w)["shop"].(map[string]any)["id"].(string)
        if copyShop == shopA {
                t.Fatal("second shop must have different id")
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

// Shop-1 staff story: the admin activates the seeded cashier template,
// hires more staff under them, and deactivates leavers. Everything stays
// inside the one shop; rotation is enforced at every handoff.
func TestStaffLifecycle(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)

        // Seeded cashier activates with the rotated password, rotates to
        // their own, then sells.
        w := do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
                "username": "cashier", "password": rotatedPassword,
        })
        if w.Code != 200 {
                t.Fatalf("cashier login: %d %s", w.Code, w.Body.String())
        }
        if dataMap(t, w)["user"].(map[string]any)["mustRotate"] != false {
                t.Fatal("rotated cashier must be clear")
        }
        cashierTok := dataMap(t, w)["token"].(string)
        w = do(t, engine, "POST", "/api/v1/orders/checkout", cashierTok, map[string]any{
                "items": []map[string]any{{"productId": 1, "qty": 1}},
                "paymentMethod": "cash", "clientUuid": "staff-1",
        })
        if w.Code != 201 {
                t.Fatalf("cashier sale: %d %s", w.Code, w.Body.String())
        }

        // Admin hires a second cashier with a PIN; they enter via PIN,
        // rotate, and sell.
        w = do(t, engine, "POST", "/api/v1/users", admin, map[string]any{
                "username": "cashier2", "fullName": "Cashier Two",
                "password": "cashier2-temp", "pin": "4444", "roleId": 2,
        })
        if w.Code != 201 {
                t.Fatalf("hire: %d %s", w.Code, w.Body.String())
        }
        c2id := int64(dataMap(t, w)["id"].(float64))
        w = do(t, engine, "POST", "/api/v1/auth/pin", "", map[string]any{"userId": c2id, "pin": "4444"})
        if w.Code != 200 {
                t.Fatalf("pin login: %d %s", w.Code, w.Body.String())
        }
        if dataMap(t, w)["user"].(map[string]any)["mustRotate"] != true {
                t.Fatal("fresh hire must rotate on first login")
        }
        c2tok := dataMap(t, w)["token"].(string)
        w = do(t, engine, "PUT", "/api/v1/users/"+itoa64(c2id)+"/password", c2tok, map[string]any{"password": "cashier2-own"})
        if w.Code != 200 {
                t.Fatalf("self rotate: %d", w.Code)
        }

        // Leaver deactivated: both password and PIN paths die.
        w = do(t, engine, "DELETE", "/api/v1/users/"+itoa64(c2id), admin, nil)
        if w.Code != 200 {
                t.Fatalf("deactivate: %d", w.Code)
        }
        w = do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
                "username": "cashier2", "password": "cashier2-own",
        })
        if w.Code == 200 {
                t.Fatal("deactivated user must not log in")
        }
        w = do(t, engine, "POST", "/api/v1/auth/pin", "", map[string]any{"userId": c2id, "pin": "4444"})
        if w.Code == 200 {
                t.Fatal("deactivated user must not PIN in")
        }
}

// Changing credentials kills outstanding sessions: the old token 401s,
// a fresh login works.
func TestPasswordChangeKillsOldToken(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)
        me := do(t, engine, "GET", "/api/v1/me", admin, nil)
        adminID := int64(dataMap(t, me)["id"].(float64))
        w := do(t, engine, "PUT", "/api/v1/users/"+itoa64(adminID)+"/password", admin, map[string]any{
                "password": "brand-new-password-1",
        })
        if w.Code != 200 {
                t.Fatalf("rotate: %d", w.Code)
        }
        w = do(t, engine, "GET", "/api/v1/me", admin, nil)
        if w.Code != 401 {
                t.Fatalf("old token should die on password change, got %d", w.Code)
        }
        w = do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
                "username": "admin", "password": "brand-new-password-1",
        })
        if w.Code != 200 {
                t.Fatalf("fresh login: %d", w.Code)
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
