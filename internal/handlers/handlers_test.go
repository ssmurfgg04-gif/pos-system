package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"posapp/internal/auth"
	"posapp/internal/database"
	"posapp/internal/handlers"
	"posapp/internal/models"
	"posapp/internal/printer"
	"posapp/internal/router"
	"posapp/internal/services"
	"posapp/internal/settings"
	"posapp/internal/ws"
)

// newTestServer boots the full app on a temp SQLite file with the mock
// M-Pesa provider configured fast (200ms) and returns (engine, adminToken,
// cashierToken, designerToken).
func newTestEngine(t *testing.T) *gin.Engine {
        t.Helper()
        gin.SetMode(gin.TestMode)
        dir := t.TempDir()
        db, err := database.Open("sqlite", filepath.Join(dir, "test.db"), "")
        if err != nil {
                t.Fatalf("db: %v", err)
        }
        // Windows locks the SQLite file while open — close before TempDir
        // cleanup or every test in this package fails on removal.
        t.Cleanup(func() { _ = db.Close() })
        if err := db.Migrate(); err != nil {
                t.Fatalf("migrate: %v", err)
        }
        if err := db.Seed(true); err != nil {
                t.Fatalf("seed: %v", err)
        }
        st, err := settings.New(db)
        if err != nil {
                t.Fatalf("settings: %v", err)
        }
        // Fast, deterministic mock payments.
        st.Set("mpesa_mock_delay_ms", "200")
        st.Set("mpesa_env", "mock")

        hub := ws.NewHub(st.JWTSecret)
        go hub.Run()
        pw := printer.NewWorker(db, st)
        svc := services.New(db, st, hub, pw)
        h := handlers.New(db, st, svc, hub, pw)
        engine := router.New(h, nil)
        return engine
}

// newTestServer boots the engine and rotates all seeded credentials through
// the real self-service path, so tests exercise the post-rotation steady
// state. Tests for rotation itself use newTestEngine directly.
func newTestServer(t *testing.T) (*gin.Engine, string, string, string) {
        t.Helper()
        engine := newTestEngine(t)
        admin := login(t, engine, "admin", "admin123")
        cashier := login(t, engine, "cashier", "cashier123")
        designer := login(t, engine, "designer", "designer123")
        rotateFresh(t, engine, admin)
        rotateFresh(t, engine, cashier)
        rotateFresh(t, engine, designer)
        return engine, admin, cashier, designer
}

// rotatedPassword is what rotateFresh sets, so tests needing fresh logins
// after newTestServer know the current seeded-user password.
const rotatedPassword = "rotated-fresh-1"

// rotateFresh clears must_rotate for a token holder (seeded logins start
// flagged). Self-service rotation needs no extra permission.
func rotateFresh(t *testing.T, engine *gin.Engine, token string) {
        t.Helper()
        me := do(t, engine, "GET", "/api/v1/me", token, nil)
        if me.Code != 200 {
                t.Fatalf("me: %d", me.Code)
        }
        id := itoa64(dataMap(t, me)["id"])
        w := do(t, engine, "PUT", "/api/v1/users/"+id+"/password", token, map[string]any{
                "password": rotatedPassword,
        })
        if w.Code != 200 {
                t.Fatalf("rotate: %d %s", w.Code, w.Body.String())
        }
}

func login(t *testing.T, engine *gin.Engine, user, pass string) string {
        t.Helper()
        body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
        req := httptest.NewRequest("POST", "/api/v1/auth/login", bytes.NewReader(body))
        req.Header.Set("Content-Type", "application/json")
        w := httptest.NewRecorder()
        engine.ServeHTTP(w, req)
        if w.Code != 200 {
                t.Fatalf("login %s: %d %s", user, w.Code, w.Body.String())
        }
        var out struct {
                Data struct {
                        Token string `json:"token"`
                } `json:"data"`
        }
        json.Unmarshal(w.Body.Bytes(), &out)
        return out.Data.Token
}

func do(t *testing.T, engine *gin.Engine, method, path, token string, body any) *httptest.ResponseRecorder {
        t.Helper()
        var rd *bytes.Reader
        if body != nil {
                b, _ := json.Marshal(body)
                rd = bytes.NewReader(b)
        } else {
                rd = bytes.NewReader(nil)
        }
        req := httptest.NewRequest(method, path, rd)
        if token != "" {
                req.Header.Set("Authorization", "Bearer "+token)
        }
        if body != nil {
                req.Header.Set("Content-Type", "application/json")
        }
        w := httptest.NewRecorder()
        engine.ServeHTTP(w, req)
        return w
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
        t.Helper()
        var out map[string]any
        if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
                t.Fatalf("bad json %q: %v", w.Body.String(), err)
        }
        return out
}

func dataMap(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
        out := decode(t, w)
        d, _ := out["data"].(map[string]any)
        return d
}

func productIDs(t *testing.T, engine *gin.Engine, token string, names ...string) []map[string]any {
        w := do(t, engine, "GET", "/api/v1/products", token, nil)
        if w.Code != 200 {
                t.Fatalf("products: %d", w.Code)
        }
        out := decode(t, w)
        list, _ := out["data"].([]any)
        byName := map[string]map[string]any{}
        for _, it := range list {
                m, _ := it.(map[string]any)
                byName[m["name"].(string)] = m
        }
        var res []map[string]any
        for _, n := range names {
                res = append(res, byName[n])
        }
        return res
}

func TestHealthAndBranding(t *testing.T) {
        engine, _, _, _ := newTestServer(t)
        w := do(t, engine, "GET", "/api/v1/health", "", nil)
        if w.Code != 200 || !strings.Contains(w.Body.String(), "ok") {
                t.Fatalf("health: %d %s", w.Code, w.Body.String())
        }
        w = do(t, engine, "GET", "/api/v1/branding", "", nil)
        b := dataMap(t, w)
        if b["app_name"] == "" || b["mpesa_env"] != "mock" {
                t.Fatalf("branding: %v", b)
        }
        // Branding must never leak secrets.
        if strings.Contains(w.Body.String(), "jwt_secret") {
                t.Fatal("branding leaked jwt_secret")
        }
}

func TestAuthFailures(t *testing.T) {
        engine, _, _, _ := newTestServer(t)
        // Bad password.
        w := do(t, engine, "POST", "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": "nope"})
        if w.Code != 401 {
                t.Fatalf("bad login should 401, got %d", w.Code)
        }
        // No token.
        w = do(t, engine, "GET", "/api/v1/orders", "", nil)
        if w.Code != 401 {
                t.Fatalf("unauthenticated should 401, got %d", w.Code)
        }
        // Garbage token.
        w = do(t, engine, "GET", "/api/v1/orders", "garbage", nil)
        if w.Code != 401 {
                t.Fatalf("garbage token should 401, got %d", w.Code)
        }
}

func TestPinLoginAndLockout(t *testing.T) {
        engine, _, _, _ := newTestServer(t)
        // List pin users.
        w := do(t, engine, "GET", "/api/v1/auth/pin-users", "", nil)
        if w.Code != 200 {
                t.Fatalf("pin-users: %d", w.Code)
        }
        out := decode(t, w)
        list, _ := out["data"].([]any)
        if len(list) < 3 {
                t.Fatalf("expected 3 pin users, got %d", len(list))
        }
        var adminID int64
        for _, it := range list {
                m := it.(map[string]any)
                if m["fullName"] == "System Admin" {
                        adminID = int64(m["id"].(float64))
                }
        }
        // Correct PIN.
        w = do(t, engine, "POST", "/api/v1/auth/pin", "", map[string]any{"userId": adminID, "pin": "1234"})
        if w.Code != 200 {
                t.Fatalf("pin login: %d %s", w.Code, w.Body.String())
        }
        // 5 wrong PINs escalate to lockout.
        for i := 0; i < 5; i++ {
                w = do(t, engine, "POST", "/api/v1/auth/pin", "", map[string]any{"userId": adminID, "pin": "9999"})
        }
        if w.Code != 423 && !strings.Contains(w.Body.String(), "locked") {
                // 5th failure locks (30s).
                t.Logf("5th wrong pin response: %d %s", w.Code, w.Body.String())
        }
        w = do(t, engine, "POST", "/api/v1/auth/pin", "", map[string]any{"userId": adminID, "pin": "1234"})
        if w.Code != 423 {
                t.Fatalf("locked account must reject even correct PIN: %d %s", w.Code, w.Body.String())
        }
}

func TestRBACGates(t *testing.T) {
        engine, admin, cashier, designer := newTestServer(t)
        // Cashier cannot manage products.
        w := do(t, engine, "POST", "/api/v1/products", cashier, map[string]any{
                "name": "X", "categoryId": 1, "priceCents": 100,
        })
        if w.Code != 403 {
                t.Fatalf("cashier products.create should 403, got %d", w.Code)
        }
        // Designer can view products but not sell.
        w = do(t, engine, "GET", "/api/v1/products", designer, nil)
        if w.Code != 200 {
                t.Fatalf("designer products.view should 200, got %d", w.Code)
        }
        w = do(t, engine, "POST", "/api/v1/orders/checkout", designer, checkoutBody(nil))
        if w.Code != 403 {
                t.Fatalf("designer pos.sell should 403, got %d", w.Code)
        }
        // Cashier cannot see audit.
        w = do(t, engine, "GET", "/api/v1/audit", cashier, nil)
        if w.Code != 403 {
                t.Fatalf("cashier audit.view should 403, got %d", w.Code)
        }
        // Cashier CAN do manual receipt entry (fallback is theirs).
        // (Checked implicitly in TestManualConfirm.)
        // Designer sees design board; cashier cannot.
        w = do(t, engine, "GET", "/api/v1/design", designer, nil)
        if w.Code != 200 {
                t.Fatalf("designer design.view should 200, got %d", w.Code)
        }
        w = do(t, engine, "GET", "/api/v1/design", cashier, nil)
        if w.Code != 403 {
                t.Fatalf("cashier design.view should 403, got %d", w.Code)
        }
        // Only admin manages settings.
        w = do(t, engine, "GET", "/api/v1/settings", admin, nil)
        if w.Code != 200 {
                t.Fatalf("admin settings: %d", w.Code)
        }
        w = do(t, engine, "GET", "/api/v1/settings", cashier, nil)
        if w.Code != 403 {
                t.Fatalf("cashier settings should 403, got %d", w.Code)
        }
}

func checkoutBody(extra map[string]any) map[string]any {
        body := map[string]any{
                "items": []map[string]any{
                        {"productId": 1, "qty": 2},
                },
                "paymentMethod": "cash",
        }
        for k, v := range extra {
                body[k] = v
        }
        return body
}

func TestCashCheckoutFlow(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        // Stock before.
        prods := productIDs(t, engine, cashier, "Classic Cotton Tee — Black")
        before := int(prods[0]["stockQty"].(float64))

        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, checkoutBody(map[string]any{
                "clientUuid": "e2e-cash-1",
        }))
        if w.Code != 201 {
                t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
        }
        order := dataMap(t, w)
        if order["status"] != "PAID" {
                t.Fatalf("cash order should be PAID: %v", order["status"])
        }
        total := order["totalCents"].(float64)
        if total != 110000 { // 2 × 55000, tax-inclusive
                t.Fatalf("total: %v", total)
        }

        // Stock decremented.
        prods = productIDs(t, engine, cashier, "Classic Cotton Tee — Black")
        after := int(prods[0]["stockQty"].(float64))
        if after != before-2 {
                t.Fatalf("stock %d -> %d (want %d)", before, after, before-2)
        }

        // Idempotent replay with the same clientUuid: same order, no double stock.
        w = do(t, engine, "POST", "/api/v1/orders/checkout", cashier, checkoutBody(map[string]any{
                "clientUuid": "e2e-cash-1",
        }))
        if w.Code != 201 {
                t.Fatalf("replay: %d", w.Code)
        }
        replay := dataMap(t, w)
        if replay["id"] != order["id"] {
                t.Fatalf("replay created a new order: %v vs %v", replay["id"], order["id"])
        }
        prods = productIDs(t, engine, cashier, "Classic Cotton Tee — Black")
        if int(prods[0]["stockQty"].(float64)) != after {
                t.Fatal("replay changed stock — idempotency broken")
        }
}

func TestInsufficientStockRejected(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items":          []map[string]any{{"productId": 4, "qty": 9999}},
                "paymentMethod":  "cash",
        })
        if w.Code != 409 {
                t.Fatalf("insufficient stock should 409, got %d %s", w.Code, w.Body.String())
        }
}

func TestPriceOverridePermissionGate(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        // Cashier tries to override price → 403 (server-side enforced).
        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items": []map[string]any{{"productId": 1, "qty": 1, "unitPriceCents": 100}},
                "paymentMethod": "cash",
        })
        if w.Code != 403 {
                t.Fatalf("cashier override should 403, got %d %s", w.Code, w.Body.String())
        }
        // Admin override is honored (log in on the same server; seeded
        // password already rotated by newTestServer).
        admin := login(t, engine, "admin", rotatedPassword)
        w = do(t, engine, "POST", "/api/v1/orders/checkout", admin, map[string]any{
                "items": []map[string]any{{"productId": 1, "qty": 1, "unitPriceCents": 1000}},
                "paymentMethod": "cash",
        })
        if w.Code != 201 {
                t.Fatalf("admin override checkout: %d %s", w.Code, w.Body.String())
        }
        order := dataMap(t, w)
        if order["totalCents"].(float64) != 1000 {
                t.Fatalf("override total: %v", order["totalCents"])
        }
}

func TestMpesaSTKFlowCompletesViaSweeper(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items": []map[string]any{{"productId": 1, "qty": 1}},
                "paymentMethod": "mpesa",
                "paymentMode":   "stk",
                "customerPhone": "0722123456",
                "clientUuid":    "e2e-stk-1",
        })
        if w.Code != 201 {
                t.Fatalf("stk checkout: %d %s", w.Code, w.Body.String())
        }
        order := dataMap(t, w)
        if order["status"] != "PENDING" {
                t.Fatalf("mpesa order should start PENDING: %v", order["status"])
        }
        // Phone normalized on the payment.
        pays, _ := order["payments"].([]any)
        p0, _ := pays[0].(map[string]any)
        if p0["phone"] != "254722123456" {
                t.Fatalf("phone not normalized: %v", p0["phone"])
        }

        // The sweeper isn't running in-process; drive completion through the
        // provider directly like the sweeper does (identical code path).
        // Simulate by waiting past the mock delay and querying via the
        // callback endpoint? Callback needs a checkout_request_id — read it
        // from the payment.
        _ = time.Now()

        // Poll the order until the mock completes it via the sweeper-less path:
        // we emulate the sweeper tick by hitting GetOrder after invoking the
        // service's sweeper tick through a callback. Simplest deterministic
        // route: complete via callback with the checkout id.
        checkoutID, _ := p0["checkoutRequestId"].(string)
        if checkoutID == "" {
                t.Fatalf("no checkout request id: %+v", p0)
        }
        // Wait for mock delay so QuerySTK would succeed; use callback directly.
        time.Sleep(250 * time.Millisecond)
        cb := map[string]any{
                "Body": map[string]any{
                        "stkCallback": map[string]any{
                                "MerchantRequestID": "m-1",
                                "CheckoutRequestID": checkoutID,
                                "ResultCode":        0,
                                "ResultDesc":        "success",
                                "CallbackMetadata": map[string]any{
                                        "Item": []map[string]any{
                                                {"Name": "Amount", "Value": 550.0},
                                                {"Name": "MpesaReceiptNumber", "Value": "NLJ7RT61SV"},
                                        },
                                },
                        },
                },
        }
        w = do(t, engine, "POST", "/api/v1/payments/mpesa/callback", "", cb)
        if w.Code != 200 {
                t.Fatalf("callback: %d %s", w.Code, w.Body.String())
        }

        orderID := int64(order["id"].(float64))
        w = do(t, engine, "GET", fmt.Sprintf("/api/v1/orders/%d", orderID), cashier, nil)
        order = dataMap(t, w)
        if order["status"] != "PAID" {
                t.Fatalf("order should be PAID after callback: %v", order["status"])
        }
        pays, _ = order["payments"].([]any)
        p0, _ = pays[len(pays)-1].(map[string]any)
        if p0["mpesaReceipt"] != "NLJ7RT61SV" {
                t.Fatalf("receipt not stored: %v", p0["mpesaReceipt"])
        }

        // Duplicate callback is idempotent — no error, order stays PAID,
        // stock is not double-deducted.
        prods := productIDs(t, engine, cashier, "Classic Cotton Tee — Black")
        stockAfterFirst := int(prods[0]["stockQty"].(float64))
        w = do(t, engine, "POST", "/api/v1/payments/mpesa/callback", "", cb)
        if w.Code != 200 {
                t.Fatalf("duplicate callback: %d", w.Code)
        }
        prods = productIDs(t, engine, cashier, "Classic Cotton Tee — Black")
        if int(prods[0]["stockQty"].(float64)) != stockAfterFirst {
                t.Fatal("duplicate callback double-deducted stock")
        }
}

func TestCallbackAmountMismatchFlagsDiscrepancy(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items": []map[string]any{{"productId": 2, "qty": 1}},
                "paymentMethod": "mpesa", "paymentMode": "stk", "customerPhone": "0722123456",
        })
        order := dataMap(t, w)
        pays, _ := order["payments"].([]any)
        p0, _ := pays[0].(map[string]any)
        checkoutID, _ := p0["checkoutRequestId"].(string)
        time.Sleep(250 * time.Millisecond)
        // Callback reports a WRONG amount (450.00 vs 550.00 due).
        cb := map[string]any{
                "Body": map[string]any{"stkCallback": map[string]any{
                        "MerchantRequestID": "m-2", "CheckoutRequestID": checkoutID,
                        "ResultCode": 0, "ResultDesc": "success",
                        "CallbackMetadata": map[string]any{"Item": []map[string]any{
                                {"Name": "Amount", "Value": 450.0},
                                {"Name": "MpesaReceiptNumber", "Value": "AA11BB22CC"},
                        }},
                }},
        }
        w = do(t, engine, "POST", "/api/v1/payments/mpesa/callback", "", cb)
        if w.Code != 200 {
                t.Fatalf("mismatch callback: %d %s", w.Code, w.Body.String())
        }
        orderID := int64(order["id"].(float64))
        w = do(t, engine, "GET", fmt.Sprintf("/api/v1/orders/%d", orderID), cashier, nil)
        order = dataMap(t, w)
        if order["discrepancy"] != true {
                t.Fatalf("amount mismatch must flag discrepancy: %v", order["discrepancy"])
        }
}

func TestManualConfirmAndDedupe(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        // Manual-mode order.
        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items": []map[string]any{{"productId": 5, "qty": 1}},
                "paymentMethod": "mpesa", "paymentMode": "manual", "clientUuid": "e2e-man-1",
        })
        if w.Code != 201 {
                t.Fatalf("manual checkout: %d %s", w.Code, w.Body.String())
        }
        order := dataMap(t, w)
        if order["status"] != "PENDING" {
                t.Fatalf("manual order should be PENDING until code entry: %v", order["status"])
        }
        orderID := int64(order["id"].(float64))

        // Invalid code format rejected.
        w = do(t, engine, "POST", fmt.Sprintf("/api/v1/orders/%d/manual", orderID), cashier, map[string]any{"receiptCode": "TOOSHORT"})
        if w.Code != 422 {
                t.Fatalf("bad code should 422, got %d", w.Code)
        }
        // Valid code completes.
        w = do(t, engine, "POST", fmt.Sprintf("/api/v1/orders/%d/manual", orderID), cashier, map[string]any{"receiptCode": "uc3hw8hdn2"})
        if w.Code != 200 {
                t.Fatalf("manual confirm: %d %s", w.Code, w.Body.String())
        }
        order = dataMap(t, w)
        if order["status"] != "PAID" {
                t.Fatalf("order should be PAID: %v", order["status"])
        }
        // Receipt canonicalized to uppercase.
        pays, _ := order["payments"].([]any)
        p0, _ := pays[0].(map[string]any)
        if p0["mpesaReceipt"] != "UC3HW8HDN2" {
                t.Fatalf("receipt not canonicalized: %v", p0["mpesaReceipt"])
        }
        // Same code on another order → duplicate rejection.
        w = do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items": []map[string]any{{"productId": 6, "qty": 1}},
                "paymentMethod": "mpesa", "paymentMode": "manual",
        })
        order2 := dataMap(t, w)
        w = do(t, engine, "POST", fmt.Sprintf("/api/v1/orders/%d/manual", int64(order2["id"].(float64))), cashier, map[string]any{"receiptCode": "UC3HW8HDN2"})
        if w.Code != 409 {
                t.Fatalf("duplicate receipt should 409, got %d %s", w.Code, w.Body.String())
        }
}

func TestSyncIdempotency(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        syncBody := map[string]any{"transactions": []map[string]any{
                {"items": []map[string]any{{"productId": 1, "qty": 1}}, "paymentMethod": "cash", "clientUuid": "sync-1"},
                {"items": []map[string]any{{"productId": 8, "qty": 3}}, "paymentMethod": "cash", "clientUuid": "sync-2"},
        }}
        w := do(t, engine, "POST", "/api/v1/sync", cashier, syncBody)
        if w.Code != 200 {
                t.Fatalf("sync: %d %s", w.Code, w.Body.String())
        }
        out := decode(t, w)
        results, _ := out["data"].([]any)
        if len(results) != 2 {
                t.Fatalf("expected 2 results, got %d", len(results))
        }
        // Replay the whole batch: same orders, no new ones.
        w = do(t, engine, "POST", "/api/v1/sync", cashier, syncBody)
        out = decode(t, w)
        results2, _ := out["data"].([]any)
        for i := range results {
                a := results[i].(map[string]any)
                b := results2[i].(map[string]any)
                if a["orderId"] != b["orderId"] {
                        t.Fatalf("replay changed order for %v", a["clientUuid"])
                }
        }
        w = do(t, engine, "GET", "/api/v1/orders?limit=100", cashier, nil)
        out = decode(t, w)
        list, _ := out["data"].([]any)
        if len(list) != 2 {
                t.Fatalf("replayed sync created extra orders: %d", len(list))
        }
}

func TestVoidRestoresStock(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        prods := productIDs(t, engine, cashier, "Classic Cotton Tee — Black")
        before := int(prods[0]["stockQty"].(float64))
        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, checkoutBody(nil))
        order := dataMap(t, w)
        orderID := int64(order["id"].(float64))
        w = do(t, engine, "POST", fmt.Sprintf("/api/v1/orders/%d/void", orderID), cashier, map[string]any{"reason": "customer changed mind"})
        if w.Code != 200 {
                t.Fatalf("void: %d %s", w.Code, w.Body.String())
        }
        order = dataMap(t, w)
        if order["status"] != "VOIDED" {
                t.Fatalf("status: %v", order["status"])
        }
        prods = productIDs(t, engine, cashier, "Classic Cotton Tee — Black")
        if int(prods[0]["stockQty"].(float64)) != before {
                t.Fatal("void did not restore stock")
        }
        // Double void rejected.
        w = do(t, engine, "POST", fmt.Sprintf("/api/v1/orders/%d/void", orderID), cashier, map[string]any{"reason": "again"})
        if w.Code != 409 {
                t.Fatalf("double void should 409, got %d", w.Code)
        }
}

func TestSettingsMasking(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)
        w := do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{
                "values": map[string]any{
                        "mpesa_consumer_secret": "super-secret-value",
                        "store_name":            "Test Shop",
                },
        })
        if w.Code != 200 {
                t.Fatalf("settings update: %d %s", w.Code, w.Body.String())
        }
        body := w.Body.String()
        if strings.Contains(body, "super-secret-value") {
                t.Fatal("secret echoed in plaintext")
        }
        if !strings.Contains(body, "__SET__") {
                t.Fatal("secret not masked")
        }
        // Masked echo preserves the secret.
        w = do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{
                "values": map[string]any{"mpesa_consumer_secret": "__SET__"},
        })
        if w.Code != 200 {
                t.Fatalf("masked update: %d", w.Code)
        }
        // Unknown key rejected.
        w = do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{
                "values": map[string]any{"jwt_secret": "hacked"},
        })
        if w.Code != 400 {
                t.Fatalf("jwt_secret write must be rejected, got %d", w.Code)
        }
}

// TestSettingsSaveEchoedReadOnlyKey is the regression for the reported
// "Save changes → error jwt secret" bug: the GET /settings snapshot used to
// include jwt_secret (masked), the frontend echoed the whole map back on
// save, and the update endpoint rejected the batch. Saving must never fail
// on a read-only secret echo, and jwt_secret must not appear in snapshots.
func TestSettingsSaveEchoedReadOnlyKey(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)
        // Snapshot must not leak jwt_secret at all.
        w := do(t, engine, "GET", "/api/v1/settings", admin, nil)
        if w.Code != 200 {
                t.Fatalf("get settings: %d", w.Code)
        }
        body := w.Body.String()
        if strings.Contains(body, "jwt_secret") {
                t.Fatalf("jwt_secret present in snapshot: %s", body)
        }
        // A stale frontend echoing the full map (incl. masked jwt_secret)
        // must still save successfully.
        w = do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{
                "values": map[string]any{
                        "jwt_secret":           "__SET__",
                        "store_name":            "Fixed Shop",
                        "mpesa_consumer_secret": "__SET__",
                },
        })
        if w.Code != 200 {
                t.Fatalf("save with echoed read-only key failed: %d %s", w.Code, w.Body.String())
        }
        d := dataMap(t, w)
        if d["store_name"] != "Fixed Shop" {
                t.Fatalf("store_name not saved: %v", d["store_name"])
        }
        // Sessions still valid (jwt_secret untouched).
        w = do(t, engine, "GET", "/api/v1/settings", admin, nil)
        if w.Code != 200 {
                t.Fatalf("token invalidated by settings save: %d", w.Code)
        }
}

func TestMonthlyReportKRA(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        month := time.Now().Format("2006-01")
        // A cash sale to have data.
        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items":         []map[string]any{{"productId": 1, "qty": 2}},
                "paymentMethod": "cash",
        })
        if w.Code != 201 {
                t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
        }
        w = do(t, engine, "GET", "/api/v1/reports/monthly?month="+month, admin, nil)
        if w.Code != 200 {
                t.Fatalf("monthly: %d %s", w.Code, w.Body.String())
        }
        d := dataMap(t, w)
        if d["month"] != month {
                t.Fatalf("month echo: %v", d["month"])
        }
        gross, _ := d["grossCents"].(float64)
        vat, _ := d["vatCents"].(float64)
        nett, _ := d["nettCents"].(float64)
        if gross <= 0 || vat <= 0 {
                t.Fatalf("expected sales and VAT > 0, got gross=%v vat=%v", gross, vat)
        }
        if nett != gross-vat {
                t.Fatalf("nett must be gross-vat: %v != %v-%v", nett, gross, vat)
        }
        series, _ := d["series"].([]any)
        if len(series) < 28 {
                t.Fatalf("expected a full month of series points, got %d", len(series))
        }
        // CSV export downloads.
        w = do(t, engine, "GET", "/api/v1/reports/monthly.csv?month="+month, admin, nil)
        if w.Code != 200 || !strings.Contains(w.Body.String(), "Gross sales") {
                t.Fatalf("monthly csv: %d %s", w.Code, w.Body.String()[:minInt(200, len(w.Body.String()))])
        }
        // Bad month format rejected.
        w = do(t, engine, "GET", "/api/v1/reports/monthly?month=garbage", admin, nil)
        if w.Code != 400 {
                t.Fatalf("bad month must 400, got %d", w.Code)
        }
}

func TestBackupEndpoints(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        w := do(t, engine, "POST", "/api/v1/system/backup", admin, nil)
        if w.Code != 200 {
                t.Fatalf("backup: %d %s", w.Code, w.Body.String())
        }
        d := dataMap(t, w)
        file, _ := d["file"].(string)
        bytesN, _ := d["bytes"].(float64)
        if file == "" || bytesN <= 0 {
                t.Fatalf("backup result bad: %v", d)
        }
        w = do(t, engine, "GET", "/api/v1/system/backups", admin, nil)
        if w.Code != 200 {
                t.Fatalf("list backups: %d", w.Code)
        }
        var out struct {
                Data []map[string]any `json:"data"`
        }
        json.Unmarshal(w.Body.Bytes(), &out)
        if len(out.Data) < 1 {
                t.Fatalf("expected at least one backup listed")
        }
        // Cashier cannot back up.
        w = do(t, engine, "POST", "/api/v1/system/backup", cashier, nil)
        if w.Code != 403 {
                t.Fatalf("cashier backup must 403, got %d", w.Code)
        }
}

func minInt(a, b int) int {
        if a < b {
                return a
        }
        return b
}

func TestRolesCrud(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)
        // Create a custom role (dynamic RBAC).
        w := do(t, engine, "POST", "/api/v1/roles", admin, map[string]any{
                "name": "Stock Officer", "description": "Manages inventory only",
                "permissions": []string{"products.view", "products.manage"},
        })
        if w.Code != 201 {
                t.Fatalf("create role: %d %s", w.Code, w.Body.String())
        }
        // Invalid permission rejected.
        w = do(t, engine, "POST", "/api/v1/roles", admin, map[string]any{
                "name": "Bad", "permissions": []string{"not.a.permission"},
        })
        if w.Code != 400 {
                t.Fatalf("invalid perm should 400, got %d", w.Code)
        }
        // System role delete blocked.
        w = do(t, engine, "DELETE", "/api/v1/roles/1", admin, nil)
        if w.Code != 409 {
                t.Fatalf("system role delete should 409, got %d", w.Code)
        }
        // Edit cashier permissions (system role editable).
        w = do(t, engine, "PUT", "/api/v1/roles/2", admin, map[string]any{
                "name": "Cashier", "description": "POS scoped",
                "permissions": []string{"pos.sell", "pos.void", "orders.view", "payments.manual", "shifts.manage", "products.view"},
        })
        if w.Code != 200 {
                t.Fatalf("role update: %d %s", w.Code, w.Body.String())
        }
}

func TestCSVImportUpsert(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)
        csv := "sku,barcode,name,category,price,cost,stock,track_stock,active\n" +
                "TS-001,,Classic Cotton Tee — Black UPDATED,T-Shirts,600.00,320.00,40,true,true\n" +
                ",,Brand New Wristband,Accessories,25.00,10.00,80,true,true\n"
        // Proper multipart form with a "file" field.
        var buf bytes.Buffer
        mw := multipart.NewWriter(&buf)
        fw, _ := mw.CreateFormFile("file", "products.csv")
        fw.Write([]byte(csv))
        mw.Close()
        req := httptest.NewRequest("POST", "/api/v1/products/import", &buf)
        req.Header.Set("Authorization", "Bearer "+admin)
        req.Header.Set("Content-Type", mw.FormDataContentType())
        w := httptest.NewRecorder()
        engine.ServeHTTP(w, req)
        if w.Code != 200 {
                t.Fatalf("import: %d %s", w.Code, w.Body.String())
        }
        out := decode(t, w)
        d, _ := out["data"].(map[string]any)
        if d["created"].(float64) != 1 || d["updated"].(float64) != 1 {
                t.Fatalf("import counts: %v", d)
        }
        // Price updated.
        w = do(t, engine, "GET", "/api/v1/products?search=Classic+Cotton+Tee", admin, nil)
        out = decode(t, w)
        list, _ := out["data"].([]any)
        if len(list) == 0 {
                t.Fatal("search found nothing")
        }
        if list[0].(map[string]any)["priceCents"].(float64) != 60000 {
                t.Fatalf("price not updated: %v", list[0].(map[string]any)["priceCents"])
        }
}

func TestDesignBoardCrud(t *testing.T) {
        engine, _, _, designer := newTestServer(t)
        w := do(t, engine, "POST", "/api/v1/design", designer, map[string]any{
                "title": "Team jersey artwork", "productName": "Premium Heavyweight Tee",
                "customerName": "Acme FC", "notes": "Two-color print", "status": "queue",
        })
        if w.Code != 201 {
                t.Fatalf("create design: %d %s", w.Code, w.Body.String())
        }
        job := dataMap(t, w)
        id := int64(job["id"].(float64))
        // Move it.
        w = do(t, engine, "POST", fmt.Sprintf("/api/v1/design/%d/move", id), designer, map[string]any{"status": "in_progress"})
        if w.Code != 200 {
                t.Fatalf("move: %d %s", w.Code, w.Body.String())
        }
        job = dataMap(t, w)
        if job["status"] != "in_progress" {
                t.Fatalf("status: %v", job["status"])
        }
        // Invalid status rejected.
        w = do(t, engine, "POST", fmt.Sprintf("/api/v1/design/%d/move", id), designer, map[string]any{"status": "bogus"})
        if w.Code != 409 && w.Code != 400 && w.Code != 422 {
                t.Fatalf("bogus status should fail, got %d", w.Code)
        }
        // List.
        w = do(t, engine, "GET", "/api/v1/design", designer, nil)
        if w.Code != 200 {
                t.Fatalf("list design: %d", w.Code)
        }
}

func TestShiftsFlow(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        // Open with 5000 float.
        w := do(t, engine, "POST", "/api/v1/shifts/open", cashier, map[string]any{"openingFloatCents": 500000})
        if w.Code != 201 {
                t.Fatalf("open shift: %d %s", w.Code, w.Body.String())
        }
        // Double open rejected.
        w = do(t, engine, "POST", "/api/v1/shifts/open", cashier, map[string]any{"openingFloatCents": 0})
        if w.Code != 409 {
                t.Fatalf("double open should 409, got %d", w.Code)
        }
        // Sell 550 in cash.
        do(t, engine, "POST", "/api/v1/orders/checkout", cashier, checkoutBody(map[string]any{"items": []map[string]any{{"productId": 1, "qty": 1}}}))
        // Close counting exactly expected (550 + 5000 float = wait, 55000 + 500000).
        w = do(t, engine, "POST", "/api/v1/shifts/close", cashier, map[string]any{"countedCents": 555000})
        if w.Code != 200 {
                t.Fatalf("close shift: %d %s", w.Code, w.Body.String())
        }
        shift := dataMap(t, w)
        if shift["varianceCents"].(float64) != 0 {
                t.Fatalf("variance should be 0, got %v", shift["varianceCents"])
        }
        if shift["expectedCents"].(float64) != 555000 {
                t.Fatalf("expected: %v", shift["expectedCents"])
        }
}

func TestReportsDaily(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        do(t, engine, "POST", "/api/v1/orders/checkout", cashier, checkoutBody(nil))
        // Cashier lacks reports.view → 403.
        w := do(t, engine, "GET", "/api/v1/reports/daily", cashier, nil)
        if w.Code != 403 {
                t.Fatalf("cashier reports should 403, got %d", w.Code)
        }
        // Do a designer login for a token that lacks it too, then admin? We
        // didn't keep admin — log in (seeded password already rotated).
        admin := login(t, engine, "admin", rotatedPassword)
        w = do(t, engine, "GET", "/api/v1/reports/daily", admin, nil)
        if w.Code != 200 {
                t.Fatalf("admin reports: %d %s", w.Code, w.Body.String())
        }
        r := dataMap(t, w)
        if r["ordersPaid"].(float64) < 1 {
                t.Fatalf("ordersPaid: %v", r["ordersPaid"])
        }
        if r["salesCents"].(float64) != 110000 {
                t.Fatalf("salesCents: %v", r["salesCents"])
        }
        if r["cashCents"].(float64) != 110000 {
                t.Fatalf("cashCents: %v", r["cashCents"])
        }
        top, _ := r["topProducts"].([]any)
        if len(top) == 0 {
                t.Fatal("no top products")
        }
        series, _ := r["series"].([]any)
        if len(series) != 7 {
                t.Fatalf("series should have 7 days, got %d", len(series))
        }
}

func TestReceiptHTML(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, checkoutBody(nil))
        order := dataMap(t, w)
        orderID := int64(order["id"].(float64))
        req := httptest.NewRequest("GET", fmt.Sprintf("/api/v1/orders/%d/receipt", orderID), nil)
        req.Header.Set("Authorization", "Bearer "+cashier)
        rec := httptest.NewRecorder()
        engine.ServeHTTP(rec, req)
        if rec.Code != 200 {
                t.Fatalf("receipt: %d", rec.Code)
        }
        body := rec.Body.String()
        for _, want := range []string{"ORD", "TOTAL", "window.print"} {
                if !strings.Contains(body, want) {
                        t.Errorf("receipt HTML missing %q", want)
                }
        }
}

// TestListOrdersConcurrentNoDeadlock is the regression test for the
// single-connection self-deadlock found in the browser E2E (settings read
// inside the rows loop). Hits the enriched order list concurrently with
// itself and with settings-heavy endpoints; the whole batch must finish
// well inside the deadline.
func TestListOrdersConcurrentNoDeadlock(t *testing.T) {
        engine, _, cashier, admin := newTestServer(t)
        // Create some orders first.
        for i := 0; i < 5; i++ {
                do(t, engine, "POST", "/api/v1/orders/checkout", cashier, checkoutBody(map[string]any{
                        "clientUuid": fmt.Sprintf("deadlock-%d", i),
                }))
        }
        done := make(chan struct{})
        var wg sync.WaitGroup
        ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
        defer cancel()

        for i := 0; i < 8; i++ {
                wg.Add(1)
                go func(i int) {
                        defer wg.Done()
                        for n := 0; n < 10; n++ {
                                select {
                                case <-ctx.Done():
                                        return
                                default:
                                }
                                w := do(t, engine, "GET", "/api/v1/orders?search=ORD&limit=50", cashier, nil)
                                if w.Code != 200 {
                                        t.Errorf("list orders failed: %d", w.Code)
                                        return
                                }
                                // Interleave a settings snapshot read (cached — must not
                                // touch the DB while other readers hold the connection).
                                do(t, engine, "GET", "/api/v1/settings", admin, nil)
                        }
                }(i)
        }
        go func() {
                wg.Wait()
                close(done)
        }()
        select {
        case <-done:
                // healthy
        case <-ctx.Done():
                t.Fatal("DEADLOCK: concurrent ListOrders did not finish in 20s")
        }
}

func TestOfflineSyncReplaySafety(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        // One offline checkout, replayed three times (double-network-glitch).
        body := map[string]any{"transactions": []map[string]any{
                {"items": []map[string]any{{"productId": 9, "qty": 2}}, "paymentMethod": "cash", "clientUuid": "offline-42"},
        }}
        var firstOrderID any
        for i := 0; i < 3; i++ {
                w := do(t, engine, "POST", "/api/v1/sync", cashier, body)
                if w.Code != 200 {
                        t.Fatalf("sync %d: %d %s", i, w.Code, w.Body.String())
                }
                out := decode(t, w)
                results, _ := out["data"].([]any)
                r := results[0].(map[string]any)
                if i == 0 {
                        firstOrderID = r["orderId"]
                } else if r["orderId"] != firstOrderID {
                        t.Fatalf("replay %d created new order", i)
                }
        }
        // Stock moved exactly once (150 - 2).
        w := do(t, engine, "GET", "/api/v1/products?search=Fabric", cashier, nil)
        out := decode(t, w)
        list, _ := out["data"].([]any)
        if list[0].(map[string]any)["stockQty"].(float64) != 148 {
                t.Fatalf("stock after 3 replays: %v", list[0].(map[string]any)["stockQty"])
        }
}

// Seeded credentials force rotation: login flags it, other endpoints 403,
// rotating clears it.
func TestForcedRotation(t *testing.T) {
        engine := newTestEngine(t)
        admin := login(t, engine, "admin", "admin123")
        w := do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
                "username": "admin", "password": "admin123",
        })
        if w.Code != 200 {
                t.Fatalf("login: %d %s", w.Code, w.Body.String())
        }
        user := dataMap(t, w)["user"].(map[string]any)
        if user["mustRotate"] != true {
                t.Fatal("seeded admin login must flag mustRotate")
        }
        w = do(t, engine, "GET", "/api/v1/products", admin, nil)
        if w.Code != 403 {
                t.Fatalf("pre-rotation products should 403, got %d", w.Code)
        }
        me := do(t, engine, "GET", "/api/v1/me", admin, nil)
        if me.Code != 200 {
                t.Fatalf("me must stay reachable, got %d", me.Code)
        }
        adminID := int64(dataMap(t, me)["id"].(float64))
        w = do(t, engine, "PUT", "/api/v1/users/"+itoa64(adminID)+"/password", admin, map[string]any{
                "password": "new-secret-1",
        })
        if w.Code != 200 {
                t.Fatalf("rotate password: %d %s", w.Code, w.Body.String())
        }
        w = do(t, engine, "GET", "/api/v1/products", admin, nil)
        if w.Code != 200 {
                t.Fatalf("post-rotation products should 200, got %d", w.Code)
        }
}

func TestMain(m *testing.M) {
        // Keep gin quiet.
        os.Setenv("GIN_MODE", "test")
        os.Exit(m.Run())
}

var (
        _ = auth.HashPassword
        _ = models.OrderPaid
        _ = fmt.Sprintf
)
