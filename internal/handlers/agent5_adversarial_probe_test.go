package handlers_test

// Agent 5 (T5) — adversarial API + concurrency probes.
//
// These tests deliberately do NOT re-cover ground already pinned by
// handlers_test.go, adversarial_test.go (SQLi surfaces, cross-shop sweep,
// money bounds, signup rate limit, XSS receipt), tenants_test.go
// (isolation, signup lifecycle), split_tender_test.go, stocktake_test.go
// (gift cards incl. double redeem) or stress_test.go (last-unit race,
// sync burst idempotency, login burst health).
//
// New ground: JWT tampering/alg confusion, deep RBAC + self-service IDOR
// battery, cross-username login spraying, PIN lockout persistence/reset,
// order-number allocation under load, multi-unit stock drain, concurrent
// void races, store-credit overdraft races, ws hub auth/leaks/stalls,
// CSV formula injection + hostile imports, payload type confusion, and
// several money-integrity bug reproductions (named TestAgent5Bug*).

import (
        "bytes"
        "crypto/hmac"
        "crypto/sha256"
        "encoding/base64"
        "encoding/json"
        "fmt"
        "mime/multipart"
        "net/http"
        "net/http/httptest"
        "strings"
        "sync"
        "sync/atomic"
        "testing"
        "time"

        "github.com/gin-gonic/gin"
        "github.com/gorilla/websocket"
)

// ---- small local helpers (a5 prefix avoids collisions) ----

func a5SetSetting(t *testing.T, engine *gin.Engine, admin, key, val string) {
        t.Helper()
        w := do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{
                "values": map[string]string{key: val},
        })
        if w.Code != 200 {
                t.Fatalf("set setting %s=%s: %d %s", key, val, w.Code, w.Body.String())
        }
}

func a5CreateProduct(t *testing.T, engine *gin.Engine, admin string, body map[string]any) int64 {
        t.Helper()
        w := do(t, engine, "POST", "/api/v1/products", admin, body)
        if w.Code != 201 {
                t.Fatalf("create product %v: %d %s", body, w.Code, w.Body.String())
        }
        return int64(dataMap(t, w)["id"].(float64))
}

func a5StockOf(t *testing.T, engine *gin.Engine, token string, name string) int {
        t.Helper()
        prods := productIDs(t, engine, token, name)
        return int(prods[0]["stockQty"].(float64))
}

func a5CustomerPoints(t *testing.T, engine *gin.Engine, admin string, cid int64) int64 {
        t.Helper()
        w := do(t, engine, "GET", "/api/v1/customers", admin, nil)
        if w.Code != 200 {
                t.Fatalf("customers: %d", w.Code)
        }
        list, _ := decode(t, w)["data"].([]any)
        for _, it := range list {
                m := it.(map[string]any)
                if int64(m["id"].(float64)) == cid {
                        return int64(m["loyaltyPoints"].(float64))
                }
        }
        t.Fatalf("customer %d not found", cid)
        return 0
}

// a5RawJWT hand-builds an HS256 token with an attacker-chosen secret.
func a5RawJWT(t *testing.T, payload string, secret string) string {
        t.Helper()
        b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
        head := b64([]byte(`{"alg":"HS256","typ":"JWT"}`))
        mac := hmac.New(sha256.New, []byte(secret))
        mac.Write([]byte(head + "." + payload))
        return head + "." + payload + "." + b64(mac.Sum(nil))
}

// a5TamperPayload swaps the JWT payload (keeps the original signature).
func a5TamperPayload(t *testing.T, token, newPayloadJSON string) string {
        t.Helper()
        parts := strings.Split(token, ".")
        if len(parts) != 3 {
                t.Fatalf("unexpected token shape: %q", token)
        }
        return parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(newPayloadJSON)) + "." + parts[2]
}

func a5Code(t *testing.T, engine *gin.Engine, method, path, token string, body any) int {
        t.Helper()
        return do(t, engine, method, path, token, body).Code
}

func a5CSVFile(t *testing.T, content string) (bytes.Buffer, string) {
        t.Helper()
        var buf bytes.Buffer
        mw := multipart.NewWriter(&buf)
        fw, err := mw.CreateFormFile("file", "products.csv")
        if err != nil {
                t.Fatalf("formfile: %v", err)
        }
        if _, err := fw.Write([]byte(content)); err != nil {
                t.Fatalf("write csv: %v", err)
        }
        mw.Close()
        return buf, mw.FormDataContentType()
}

// a5DoRaw issues a request with an explicit content type.
func a5DoRaw(t *testing.T, engine *gin.Engine, method, path, token, contentType string, body []byte) *httptest.ResponseRecorder {
        t.Helper()
        req := httptest.NewRequest(method, path, bytes.NewReader(body))
        if token != "" {
                req.Header.Set("Authorization", "Bearer "+token)
        }
        if contentType != "" {
                req.Header.Set("Content-Type", contentType)
        }
        w := httptest.NewRecorder()
        engine.ServeHTTP(w, req)
        return w
}

func a5DialWS(t *testing.T, srv *httptest.Server, bearer string) (*websocket.Conn, *http.Response, error) {
        t.Helper()
        hdr := http.Header{}
        if bearer != "" {
                hdr.Set("Authorization", "Bearer "+bearer)
        }
        d := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
        return d.Dial("ws://"+srv.Listener.Addr().String()+"/api/v1/ws", hdr)
}

// a5WSAuth performs the hub's first-message token handshake.
func a5WSAuth(t *testing.T, conn *websocket.Conn, token string) error {
        t.Helper()
        msg, _ := json.Marshal(map[string]string{"token": token})
        return conn.WriteMessage(websocket.TextMessage, msg)
}

// a5ReadEvent reads one ws text message (with deadline). Returns type+raw.
func a5ReadEvent(t *testing.T, conn *websocket.Conn, within time.Duration) (string, string, error) {
        t.Helper()
        _ = conn.SetReadDeadline(time.Now().Add(within))
        _, raw, err := conn.ReadMessage()
        if err != nil {
                return "", "", err
        }
        var m struct {
                Type string `json:"type"`
        }
        _ = json.Unmarshal(raw, &m)
        return m.Type, string(raw), nil
}

// ---- 1. JWT & token attacks ----

func TestAgent5JWTTampering(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)

        // Baseline: a valid token works.
        if c := a5Code(t, engine, "GET", "/api/v1/products", admin, nil); c != 200 {
                t.Fatalf("valid token should work, got %d", c)
        }

        // (a) Payload tampering — cashier id promoted to admin id, signature kept.
        forged := a5TamperPayload(t, admin, `{"uid":1,"iat_ms":9999999999999}`)
        if c := a5Code(t, engine, "GET", "/api/v1/products", forged, nil); c != 401 {
                t.Errorf("tampered payload must 401, got %d", c)
        }
        // (b) alg=none (unsigned).
        b64 := base64.RawURLEncoding.EncodeToString
        none := b64([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
                b64([]byte(`{"uid":1}`)) + "."
        if c := a5Code(t, engine, "GET", "/api/v1/products", none, nil); c != 401 {
                t.Errorf("alg=none token must 401, got %d", c)
        }
        // (c) HS256 signed with an attacker secret.
        evil := a5RawJWT(t, b64([]byte(`{"uid":1,"shop":""}`)), "attacker-secret")
        if c := a5Code(t, engine, "GET", "/api/v1/products", evil, nil); c != 401 {
                t.Errorf("attacker-signed token must 401, got %d", c)
        }
        // (d) Header tricks: alg confusion to HS384/HS512 with attacker secret.
        for _, alg := range []string{"HS384", "HS512"} {
                head := b64([]byte(fmt.Sprintf(`{"alg":%q,"typ":"JWT"}`, alg)))
                payload := b64([]byte(`{"uid":1}`))
                mac := hmac.New(sha256.New, []byte("attacker-secret")) // wrong digest length on purpose
                mac.Write([]byte(head + "." + payload))
                tok := head + "." + payload + "." + b64(mac.Sum(nil))
                if c := a5Code(t, engine, "GET", "/api/v1/products", tok, nil); c != 401 {
                        t.Errorf("alg %s with wrong key must 401, got %d", alg, c)
                }
        }
        // (e) Deactivated user's still-valid token must be rejected (403).
        w := do(t, engine, "POST", "/api/v1/users", admin, map[string]any{
                "username": "victim9", "password": "victim9-pass", "roleId": 2,
        })
        if w.Code != 201 {
                t.Fatalf("create user: %d %s", w.Code, w.Body.String())
        }
        vid := int64(dataMap(t, w)["id"].(float64))
        // CreateUser arms must_rotate: rotate through the self-service path,
        // re-login, THEN the token is a normal working session.
        if c := a5Code(t, engine, "PUT", "/api/v1/users/"+itoa64(vid)+"/password", admin,
                map[string]any{"password": "victim9-temp"}); c != 200 {
                t.Fatalf("pre-rotate victim: %d", c)
        }
        vtok := login(t, engine, "victim9", "victim9-temp")
        if c := a5Code(t, engine, "PUT", "/api/v1/users/"+itoa64(vid)+"/password", vtok,
                map[string]any{"password": "victim9-pass"}); c != 200 {
                t.Fatalf("victim self-rotate: %d", c)
        }
        vtok = login(t, engine, "victim9", "victim9-pass")
        if c := a5Code(t, engine, "DELETE", "/api/v1/users/"+itoa64(vid), admin, nil); c != 200 {
                t.Fatalf("deactivate: %d", c)
        }
        if c := a5Code(t, engine, "GET", "/api/v1/products", vtok, nil); c != 403 {
                t.Errorf("deactivated user token must 403, got %d", c)
        }
        // (f) Authorization header shape games.
        for _, h := range []string{"", "Bearer", "Bearer ", "bearer " + admin, "Bearer" + admin, "Token " + admin} {
                req := httptest.NewRequest("GET", "/api/v1/products", nil)
                if h != "" {
                        req.Header.Set("Authorization", h)
                }
                w := httptest.NewRecorder()
                engine.ServeHTTP(w, req)
                if w.Code != 401 {
                        t.Errorf("Authorization %q should 401, got %d", h, w.Code)
                }
        }
}

// ---- 2. RBAC + self-service IDOR battery (extends TestRBACGates) ----

func TestAgent5PermissionBypassBattery(t *testing.T) {
        engine, admin, cashier, designer := newTestServer(t)

        me := do(t, engine, "GET", "/api/v1/me", admin, nil)
        adminID := int64(dataMap(t, me)["id"].(float64))

        cashierForbidden := []struct {
                method, path string
                body         any
        }{
                {"GET", "/api/v1/users", nil},
                {"POST", "/api/v1/users", map[string]any{"username": "nope1", "password": "nope1pass", "roleId": 2}},
                {"DELETE", "/api/v1/users/" + itoa64(adminID), nil},
                {"GET", "/api/v1/roles", nil},
                {"POST", "/api/v1/roles", map[string]any{"name": "X", "permissions": []string{"pos.sell"}}},
                {"PUT", "/api/v1/settings", map[string]any{"values": map[string]string{"store_name": "Hax"}}},
                {"POST", "/api/v1/system/backup", nil},
                {"GET", "/api/v1/system/backups", nil},
                {"POST", "/api/v1/system/update/install", nil},
                {"GET", "/api/v1/products/export", nil},
                {"POST", "/api/v1/products/import", nil},
                {"POST", "/api/v1/customers", map[string]any{"name": "Nope"}},
                {"POST", "/api/v1/customers/1/adjustments", map[string]any{"amountCents": 1, "note": "x"}},
                {"POST", "/api/v1/gift-cards/redeem", map[string]any{"code": "GC-XXXX-YYYY", "customerId": 1}},
                {"GET", "/api/v1/stock-takes", nil},
                {"POST", "/api/v1/purchase-orders", map[string]any{"supplierId": 1}},
                {"GET", "/api/v1/reports/daily", nil},
                {"GET", "/api/v1/reports/monthly.csv", nil},
                {"POST", "/api/v1/void-reasons", map[string]any{"label": "x"}},
                {"GET", "/api/v1/team-sync", nil},
                {"GET", "/api/v1/products/low-stock", nil},
                {"GET", "/api/v1/print-jobs", nil},
                {"POST", "/api/v1/settings/test-print", nil},
                // Self-service IDOR: cashier rotating SOMEONE ELSE's credentials.
                {"PUT", "/api/v1/users/" + itoa64(adminID) + "/password", map[string]any{"password": "hijacked-pass-1"}},
                {"PUT", "/api/v1/users/" + itoa64(adminID) + "/pin", map[string]any{"pin": "4321"}},
        }
        for _, c := range cashierForbidden {
                if code := a5Code(t, engine, c.method, c.path, cashier, c.body); code != 403 {
                        t.Errorf("cashier %s %s must 403, got %d", c.method, c.path, code)
                }
        }

        designerForbidden := []struct {
                method, path string
                body         any
        }{
                {"POST", "/api/v1/orders/1/void", map[string]any{"reason": "x"}},
                {"POST", "/api/v1/orders/1/manual", map[string]any{"receiptCode": "ABCD123456"}},
                {"POST", "/api/v1/orders/checkout", checkoutBody(nil)},
                {"GET", "/api/v1/users", nil},
                {"POST", "/api/v1/products", map[string]any{"name": "Nope", "categoryId": 1, "priceCents": 1}},
                {"POST", "/api/v1/shifts/open", map[string]any{"openingFloatCents": 0}},
                {"GET", "/api/v1/customers", nil},
                {"PUT", "/api/v1/users/" + itoa64(adminID) + "/password", map[string]any{"password": "hijacked-pass-2"}},
        }
        for _, c := range designerForbidden {
                if code := a5Code(t, engine, c.method, c.path, designer, c.body); code != 403 {
                        t.Errorf("designer %s %s must 403, got %d", c.method, c.path, code)
                }
        }

        // Cross-shop user mutation: shop B's owner targeting shop A's user id.
        t.Setenv("ALLOW_SIGNUP", "true")
        tokB, _ := signup(t, engine, "bobby5", "bobby5pass", "Bobby Shop")
        w := do(t, engine, "POST", "/api/v1/users", admin, map[string]any{
                "username": "lucy5", "password": "lucy5-pass", "roleId": 2,
        })
        if w.Code != 201 {
                t.Fatalf("create lucy: %d %s", w.Code, w.Body.String())
        }
        lucyID := int64(dataMap(t, w)["id"].(float64))
        if code := a5Code(t, engine, "DELETE", "/api/v1/users/"+itoa64(lucyID), tokB, nil); code != 404 {
                t.Errorf("cross-shop user delete must 404, got %d", code)
        }
        if code := a5Code(t, engine, "PUT", "/api/v1/users/"+itoa64(lucyID)+"/password", tokB, map[string]any{"password": "stolen-pass-1"}); code != 404 {
                t.Errorf("cross-shop password reset must 404, got %d", code)
        }

        // SwitchShop to a shop you are not a member of.
        if code := a5Code(t, engine, "POST", "/api/v1/auth/switch", cashier, map[string]any{"shopId": "sh-deadbeef"}); code != 403 && code != 404 {
                t.Errorf("switch to foreign shop must 403/404, got %d", code)
        }
}

// ---- 3. Login spraying + PIN lockout hardening ----

// The login limiter is keyed ONLY by username: distinct usernames are never
// throttled globally, so password spraying across a user list is unthrottled
// (each account still limited to 10/min). This pins the per-username half;
// the cross-username gap is reported in the worklog, not asserted away.
func TestAgent5LoginSprayPerUsernameOnly(t *testing.T) {
        engine, _, _, _ := newTestServer(t)
        // 12 distinct usernames, one bad password each: all must be plain 401s.
        for i := 0; i < 12; i++ {
                w := do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
                        "username": fmt.Sprintf("sprayagent%d", i), "password": "guess-1",
                })
                if w.Code == 429 {
                        t.Fatalf("unexpected 429 on first attempt for distinct username %d", i)
                }
                if w.Code != 401 {
                        t.Fatalf("unknown user should 401, got %d", w.Code)
                }
        }
        // 11 rapid guesses against ONE username: the 11th must 429.
        sawLimit := false
        for i := 0; i < 11; i++ {
                w := do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
                        "username": "sprayagent0", "password": "guess-2",
                })
                if w.Code == 429 {
                        sawLimit = true
                }
        }
        if !sawLimit {
                t.Fatal("per-username limiter never engaged after 11 guesses")
        }
}

// Extends TestPinLoginAndLockout: the lock survives further wrong attempts,
// blocks the CORRECT pin, and an admin PIN reset clears it.
func TestAgent5PINLockoutPersistenceAndReset(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)
        w := do(t, engine, "GET", "/api/v1/auth/pin-users", "", nil)
        if w.Code != 200 {
                t.Fatalf("pin-users: %d", w.Code)
        }
        var designerID int64
        for _, it := range decode(t, w)["data"].([]any) {
                m := it.(map[string]any)
                if m["fullName"] == "Designer Demo" {
                        designerID = int64(m["id"].(float64))
                }
        }
        if designerID == 0 {
                t.Fatal("designer pin user missing")
        }
        // 5 wrong PINs → escalating lock.
        for i := 0; i < 5; i++ {
                w = do(t, engine, "POST", "/api/v1/auth/pin", "", map[string]any{"userId": designerID, "pin": "9876"})
        }
        if w.Code != 423 {
                t.Fatalf("5th wrong pin should lock (423), got %d %s", w.Code, w.Body.String())
        }
        // Wrong attempt while locked: still 423 (not 401, no counter reset path).
        if c := a5Code(t, engine, "POST", "/api/v1/auth/pin", "", map[string]any{"userId": designerID, "pin": "9876"}); c != 423 {
                t.Errorf("attempt while locked must stay 423, got %d", c)
        }
        // Correct PIN must NOT bypass the lock.
        if c := a5Code(t, engine, "POST", "/api/v1/auth/pin", "", map[string]any{"userId": designerID, "pin": "3333"}); c != 423 {
                t.Errorf("correct pin while locked must 423, got %d", c)
        }
        // Default/well-known PIN rejected on reset.
        w = do(t, engine, "PUT", "/api/v1/users/"+itoa64(designerID)+"/pin", admin, map[string]any{"pin": "0000"})
        if w.Code != 400 {
                t.Errorf("default PIN must be rejected, got %d", w.Code)
        }
        // Admin reset clears the lock; new PIN works immediately.
        w = do(t, engine, "PUT", "/api/v1/users/"+itoa64(designerID)+"/pin", admin, map[string]any{"pin": "9988"})
        if w.Code != 200 {
                t.Fatalf("pin reset: %d %s", w.Code, w.Body.String())
        }
        w = do(t, engine, "POST", "/api/v1/auth/pin", "", map[string]any{"userId": designerID, "pin": "9988"})
        if w.Code != 200 {
                t.Fatalf("login after reset: %d %s", w.Code, w.Body.String())
        }
}

// ---- 4. Concurrency: order numbers, stock drain, void races, credit ----

func TestAgent5OrderNumberUniquenessUnderLoad(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        pid := a5CreateProduct(t, engine, admin, map[string]any{
                "sku": "A5-SEQ", "name": "A5 Seq", "categoryId": 1,
                "priceCents": 100, "stockQty": 10000, "trackStock": true,
        })
        const n = 40
        type res struct {
                code   int
                number string
        }
        results := make([]res, n)
        start := make(chan struct{})
        var wg sync.WaitGroup
        for i := 0; i < n; i++ {
                wg.Add(1)
                go func(i int) {
                        defer wg.Done()
                        <-start
                        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                                "items":         []map[string]any{{"productId": pid, "qty": 1}},
                                "paymentMethod": "cash",
                                "clientUuid":    fmt.Sprintf("a5-seq-%d", i),
                        })
                        num := ""
                        if w.Code == 201 {
                                num, _ = dataMap(t, w)["number"].(string)
                        } else {
                                t.Errorf("racer %d: checkout %d %s", i, w.Code, w.Body.String())
                        }
                        results[i] = res{w.Code, num}
                }(i)
        }
        close(start)
        wg.Wait()
        seen := map[string]bool{}
        for i, r := range results {
                if r.code != 201 {
                        t.Fatalf("racer %d failed: %d", i, r.code)
                }
                if !strings.HasPrefix(r.number, "ORD") || len(r.number) != 15 { // ORD+YYYYMMDD+4-digit seq
                        t.Fatalf("bad order number %q", r.number)
                }
                if seen[r.number] {
                        t.Fatalf("duplicate order number allocated: %s", r.number)
                }
                seen[r.number] = true
        }
        if stock := a5StockOf(t, engine, cashier, "A5 Seq"); stock != 10000-n {
                t.Fatalf("stock after %d sales: %d", n, stock)
        }
}

func TestAgent5ParallelDrainNoNegativeStock(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        const stock = 10
        const racers = 30
        a5CreateProduct(t, engine, admin, map[string]any{
                "sku": "A5-DRAIN", "name": "A5 Drain", "categoryId": 1,
                "priceCents": 100, "stockQty": stock, "trackStock": true,
        })
        pid := int64(productIDs(t, engine, cashier, "A5 Drain")[0]["id"].(float64))
        var ok, conflict, other atomic.Int32
        start := make(chan struct{})
        var wg sync.WaitGroup
        for i := 0; i < racers; i++ {
                wg.Add(1)
                go func(i int) {
                        defer wg.Done()
                        <-start
                        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                                "items":         []map[string]any{{"productId": pid, "qty": 1}},
                                "paymentMethod": "cash",
                                "clientUuid":    fmt.Sprintf("a5-drain-%d", i),
                        })
                        switch {
                        case w.Code == 201:
                                ok.Add(1)
                        case w.Code == 409:
                                conflict.Add(1)
                        default:
                                other.Add(1)
                                t.Errorf("drain %d: code %d %s", i, w.Code, w.Body.String())
                        }
                }(i)
        }
        close(start)
        wg.Wait()
        if int(ok.Load()) != stock {
                t.Fatalf("exactly %d sales must succeed, got %d (conflicts %d, other %d)",
                        stock, ok.Load(), conflict.Load(), other.Load())
        }
        if s := a5StockOf(t, engine, cashier, "A5 Drain"); s != 0 {
                t.Fatalf("stock must end at 0, got %d", s)
        }
}

func TestAgent5ConcurrentVoidSingleRestore(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        a5CreateProduct(t, engine, admin, map[string]any{
                "sku": "A5-VOID", "name": "A5 Void", "categoryId": 1,
                "priceCents": 100, "stockQty": 5, "trackStock": true,
        })
        pid := int64(productIDs(t, engine, cashier, "A5 Void")[0]["id"].(float64))
        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items":         []map[string]any{{"productId": pid, "qty": 2}},
                "paymentMethod": "cash", "clientUuid": "a5-void-order",
        })
        if w.Code != 201 {
                t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
        }
        oid := int64(dataMap(t, w)["id"].(float64))
        if s := a5StockOf(t, engine, cashier, "A5 Void"); s != 3 {
                t.Fatalf("pre-void stock: %d", s)
        }
        // 10 racing voids: exactly one wins; stock restored exactly once.
        var wins, conflicts, other atomic.Int32
        start := make(chan struct{})
        var wg sync.WaitGroup
        for i := 0; i < 10; i++ {
                wg.Add(1)
                go func(i int) {
                        defer wg.Done()
                        <-start
                        r := do(t, engine, "POST", fmt.Sprintf("/api/v1/orders/%d/void", oid), cashier,
                                map[string]any{"reason": fmt.Sprintf("race-%d", i)})
                        switch {
                        case r.Code == 200:
                                wins.Add(1)
                        case r.Code == 409:
                                conflicts.Add(1)
                        default:
                                other.Add(1)
                                t.Errorf("void %d: code %d %s", i, r.Code, r.Body.String())
                        }
                }(i)
        }
        close(start)
        wg.Wait()
        if wins.Load() != 1 {
                t.Fatalf("exactly one void must win, got %d wins / %d conflicts / %d other",
                        wins.Load(), conflicts.Load(), other.Load())
        }
        if s := a5StockOf(t, engine, cashier, "A5 Void"); s != 5 {
                t.Fatalf("stock after void race: %d (want 5 — restored exactly once)", s)
        }
}

func TestAgent5CreditOverdrawUnderConcurrency(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        a5CreateProduct(t, engine, admin, map[string]any{
                "sku": "A5-CRED", "name": "A5 Cred", "categoryId": 1,
                "priceCents": 50000, "stockQty": 0, "trackStock": false,
        })
        pid := int64(productIDs(t, engine, cashier, "A5 Cred")[0]["id"].(float64))
        w := do(t, engine, "POST", "/api/v1/customers", admin, map[string]any{"name": "A5 Cred Cust"})
        if w.Code != 201 {
                t.Fatalf("customer: %d %s", w.Code, w.Body.String())
        }
        cid := int64(dataMap(t, w)["id"].(float64))
        w = do(t, engine, "POST", fmt.Sprintf("/api/v1/customers/%d/credit-topup", cid), admin,
                map[string]any{"amountCents": 100000, "note": "seed"}) // buys exactly 2
        if w.Code != 200 {
                t.Fatalf("topup: %d %s", w.Code, w.Body.String())
        }
        // NOTE: losers of this race currently surface as HTTP 500
        // ("not enough store credit" is not mapped in mapErr) — the known
        // status-code bug is pinned separately; here we only require that
        // the BALANCE can never go negative and at most 2 sales land.
        var ok atomic.Int32
        start := make(chan struct{})
        var wg sync.WaitGroup
        for i := 0; i < 6; i++ {
                wg.Add(1)
                go func(i int) {
                        defer wg.Done()
                        <-start
                        r := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                                "items":         []map[string]any{{"productId": pid, "qty": 1}},
                                "paymentMethod": "credit",
                                "customerId":    cid,
                                "clientUuid":    fmt.Sprintf("a5-cred-%d", i),
                        })
                        if r.Code == 201 {
                                ok.Add(1)
                        } else if r.Code < 400 || r.Code >= 600 {
                                t.Errorf("credit racer %d: unexpected code %d %s", i, r.Code, r.Body.String())
                        }
                }(i)
        }
        close(start)
        wg.Wait()
        if ok.Load() > 2 {
                t.Fatalf("overdraw: %d credit sales succeeded against a 2-sale balance", ok.Load())
        }
        w = do(t, engine, "GET", "/api/v1/customers", admin, nil)
        var credit int64
        for _, it := range decode(t, w)["data"].([]any) {
                m := it.(map[string]any)
                if int64(m["id"].(float64)) == cid {
                        credit = int64(m["storeCreditCents"].(float64))
                }
        }
        if credit < 0 {
                t.Fatalf("store credit went negative: %d", credit)
        }
        if int(ok.Load()) != 2 || credit != 0 {
                t.Logf("credit race: %d sales, final credit %d", ok.Load(), credit)
        }
}

// ---- 5. Money-integrity bug reproductions (TestAgent5Bug*) ----

// BUG: the M-Pesa callback path completes a payment on an already-VOIDED
// order. completePayment only guards the ORDER transition (status='PENDING')
// but flips the payment row to COMPLETED unconditionally (guard is
// status != 'COMPLETED', which a VOIDED payment passes), mints gift cards,
// and broadcasts ORDER_PAID — with no discrepancy flag and no re-open.
func TestAgent5BugCallbackCompletesVoidedOrder(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        a5SetSetting(t, engine, admin, "mpesa_mock_delay_ms", "60000")
        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items":         []map[string]any{{"productId": 1, "qty": 1}},
                "paymentMethod": "mpesa", "paymentMode": "stk",
                "customerPhone": "0722123456", "clientUuid": "a5-resurrect",
        })
        if w.Code != 201 {
                t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
        }
        order := dataMap(t, w)
        oid := int64(order["id"].(float64))
        pays, _ := order["payments"].([]any)
        p0, _ := pays[0].(map[string]any)
        coID, _ := p0["checkoutRequestId"].(string)
        if coID == "" {
                t.Fatalf("no checkout id: %v", p0)
        }
        if c := a5Code(t, engine, "POST", fmt.Sprintf("/api/v1/orders/%d/void", oid), cashier,
                map[string]any{"reason": "cashier cancelled"}); c != 200 {
                t.Fatalf("void: %d", c)
        }
        // The customer paid anyway; Daraja calls back after the void.
        cb := map[string]any{
                "Body": map[string]any{"stkCallback": map[string]any{
                        "MerchantRequestID": "m-9", "CheckoutRequestID": coID,
                        "ResultCode": 0, "ResultDesc": "success",
                        "CallbackMetadata": map[string]any{"Item": []map[string]any{
                                {"Name": "Amount", "Value": 550.0},
                                {"Name": "MpesaReceiptNumber", "Value": "QA11BB22CC"},
                        }},
                }},
        }
        if c := a5Code(t, engine, "POST", "/api/v1/payments/mpesa/callback", "", cb); c != 200 {
                t.Fatalf("callback: %d", c)
        }
        w = do(t, engine, "GET", fmt.Sprintf("/api/v1/orders/%d", oid), cashier, nil)
        order = dataMap(t, w)
        if order["status"] != "VOIDED" {
                t.Fatalf("order must stay VOIDED, got %v", order["status"])
        }
        pays, _ = order["payments"].([]any)
        p0, _ = pays[0].(map[string]any)
        if p0["status"] == "COMPLETED" {
                t.Errorf("BUG T5-Money-1: payment resurrected to COMPLETED on a VOIDED order "+
                        "(status=%v discrepancy=%v receipt=%v) — money shows collected on a cancelled sale",
                        p0["status"], order["discrepancy"], p0["mpesaReceipt"])
        }
        if order["discrepancy"] == true {
                t.Errorf("BUG T5-Money-1b: unexpected discrepancy flag — should have been the tell-tale")
        }
}

// BUG: loyalty points EARNED on a paid order survive voiding it. The void
// refunds redeemed points but never claws back earned ones, so repeated
// buy→void cycles mint spendable points with zero net sales.
func TestAgent5BugLoyaltyKeptOnVoid(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        a5SetSetting(t, engine, admin, "loyalty_earn_per_cents", "100") // 1 pt / KES 1
        w := do(t, engine, "POST", "/api/v1/customers", admin, map[string]any{"name": "A5 Loyalty"})
        if w.Code != 201 {
                t.Fatalf("customer: %d", w.Code)
        }
        cid := int64(dataMap(t, w)["id"].(float64))
        w = do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items":         []map[string]any{{"productId": 1, "qty": 1}},
                "paymentMethod": "mpesa", "paymentMode": "manual",
                "customerId": cid, "clientUuid": "a5-loyal-1",
        })
        if w.Code != 201 {
                t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
        }
        oid := int64(dataMap(t, w)["id"].(float64))
        if c := a5Code(t, engine, "POST", fmt.Sprintf("/api/v1/orders/%d/manual", oid), cashier,
                map[string]any{"receiptCode": "QK1B2C3D4E"}); c != 200 {
                t.Fatalf("manual confirm: %d", c)
        }
        earned := a5CustomerPoints(t, engine, admin, cid)
        if earned != 550 {
                t.Fatalf("expected 550 earned points after KES 550 sale, got %d", earned)
        }
        if c := a5Code(t, engine, "POST", fmt.Sprintf("/api/v1/orders/%d/void", oid), cashier,
                map[string]any{"reason": "refunded"}); c != 200 {
                t.Fatalf("void: %d", c)
        }
        after := a5CustomerPoints(t, engine, admin, cid)
        if after != 0 {
                t.Errorf("BUG T5-Money-2: voided order keeps earned loyalty points (%d) — "+
                        "buy/void cycles mint points with no net sales", after)
        }
}

// BUG (functional): immediate cash/credit checkouts never award loyalty
// points even for a customer-bearing order — the earn only lives inside
// completePayment (async paths), contradicting the code comment claiming
// "cash, M-Pesa, credit, and tab settles alike".
func TestAgent5BugCashCheckoutEarnsNoLoyalty(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        a5SetSetting(t, engine, admin, "loyalty_earn_per_cents", "100")
        w := do(t, engine, "POST", "/api/v1/customers", admin, map[string]any{"name": "A5 Cash Cust"})
        if w.Code != 201 {
                t.Fatalf("customer: %d", w.Code)
        }
        cid := int64(dataMap(t, w)["id"].(float64))
        if c := a5Code(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items":         []map[string]any{{"productId": 1, "qty": 1}},
                "paymentMethod": "cash", "customerId": cid, "clientUuid": "a5-loyal-cash",
        }); c != 201 {
                t.Fatalf("cash checkout: %d", c)
        }
        if pts := a5CustomerPoints(t, engine, admin, cid); pts == 0 {
                t.Errorf("BUG T5-Money-3: cash sale for a customer earned 0 loyalty points " +
                        "(earn path skipped for immediate methods)")
        }
}

// BUG: a 0.00-priced product can never be sold — the discount sanity check
// (discount >= subtotal → reject) fires on subtotal 0 with discount 0,
// producing "discount out of range (0 to -1)".
func TestAgent5BugZeroPriceCheckoutRejected(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        pid := a5CreateProduct(t, engine, admin, map[string]any{
                "sku": "A5-FREE", "name": "A5 Freebie", "categoryId": 1,
                "priceCents": 0, "stockQty": 5, "trackStock": true,
        })
        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items":         []map[string]any{{"productId": pid, "qty": 1}},
                "paymentMethod": "cash", "clientUuid": "a5-free-1",
        })
        if w.Code != 201 {
                t.Errorf("BUG T5-Money-4: zero-priced product checkout rejected with %d %s "+
                        "(free items / samples cannot be rung up at all)", w.Code, w.Body.String())
        }
}

// Money-integrity guardrails that DO hold: discount boundaries and the
// overpayment/negative-payment walls.
func TestAgent5DiscountAndRefundBoundaries(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        a5CreateProduct(t, engine, admin, map[string]any{
                "sku": "A5-DISC", "name": "A5 Disc", "categoryId": 1,
                "priceCents": 10000, "stockQty": 10, "trackStock": true,
        })
        pid := int64(productIDs(t, engine, cashier, "A5 Disc")[0]["id"].(float64))
        // Discount == subtotal must be rejected (no 100%-off orders).
        if c := a5Code(t, engine, "POST", "/api/v1/orders/checkout", admin, map[string]any{
                "items":         []map[string]any{{"productId": pid, "qty": 1}},
                "paymentMethod": "cash", "discountCents": 10000, "clientUuid": "a5-d1",
        }); c != 422 {
                t.Errorf("100%% discount must 422, got %d", c)
        }
        // Negative discount rejected.
        if c := a5Code(t, engine, "POST", "/api/v1/orders/checkout", admin, map[string]any{
                "items":         []map[string]any{{"productId": pid, "qty": 1}},
                "paymentMethod": "cash", "discountCents": -1, "clientUuid": "a5-d2",
        }); c != 422 {
                t.Errorf("negative discount must 422, got %d", c)
        }
        // Cashier without payments.apply_discount cannot discount at all.
        if c := a5Code(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items":         []map[string]any{{"productId": pid, "qty": 1}},
                "paymentMethod": "cash", "discountCents": 100, "clientUuid": "a5-d3",
        }); c != 403 {
                t.Errorf("cashier discount must 403, got %d", c)
        }
        // subtotal-1 is the legal ceiling and produces total 1.
        w := do(t, engine, "POST", "/api/v1/orders/checkout", admin, map[string]any{
                "items":         []map[string]any{{"productId": pid, "qty": 1}},
                "paymentMethod": "cash", "discountCents": 9999, "clientUuid": "a5-d4",
        })
        if w.Code != 201 {
                t.Fatalf("max legal discount: %d %s", w.Code, w.Body.String())
        }
        o := dataMap(t, w)
        if o["totalCents"].(float64) != 1 {
                t.Fatalf("discounted total: %v", o["totalCents"])
        }
        pays, _ := o["payments"].([]any)
        if len(pays) == 0 {
                t.Fatal("PAID order must carry at least one payment row")
        }
        // Tab refunds: overpayment and negative walk-in payments are refused.
        w = do(t, engine, "POST", "/api/v1/customers", admin, map[string]any{"name": "A5 Tab", "creditLimitCents": 100000})
        cid := int64(dataMap(t, w)["id"].(float64))
        if c := a5Code(t, engine, "POST", fmt.Sprintf("/api/v1/customers/%d/payments", cid), cashier,
                map[string]any{"amountCents": -500}); c != 422 && c != 400 {
                t.Errorf("BUG T5-Money-5a: negative walk-in payment should 4xx, got %d (mapErr gap → 500?)", c)
        }
        if c := a5Code(t, engine, "POST", fmt.Sprintf("/api/v1/customers/%d/payments", cid), cashier,
                map[string]any{"amountCents": 1}); c == 200 {
                t.Errorf("payment against zero balance (overpayment) must be rejected, got 200")
        }
        // Insufficient store credit currently maps to HTTP 500 (mapErr lacks
        // ErrNoStoreCredit / ErrLoyaltyPoints) — misleading 5xx on client errors.
        a5CreateProduct(t, engine, admin, map[string]any{
                "sku": "A5-NOFC", "name": "A5 NoFund", "categoryId": 1,
                "priceCents": 100000, "stockQty": 0, "trackStock": false,
        })
        p2 := int64(productIDs(t, engine, cashier, "A5 NoFund")[0]["id"].(float64))
        w = do(t, engine, "POST", "/api/v1/orders/checkout", admin, map[string]any{
                "items":         []map[string]any{{"productId": p2, "qty": 1}},
                "paymentMethod": "credit", "customerId": cid, "clientUuid": "a5-nofund",
        })
        if w.Code >= 500 {
                t.Errorf("BUG T5-Money-6: checkout with empty store credit returned %d (client error mapped to 5xx): %s",
                        w.Code, w.Body.String())
        }
}

// ---- 6. CSV formula injection + hostile imports ----

func TestAgent5CSVFormulaInjectionExport(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)
        names := []string{"=1+1", "+SUM(A1)", "-2", "@xplo"}
        for i, n := range names {
                a5CreateProduct(t, engine, admin, map[string]any{
                        "sku": fmt.Sprintf("A5-F%d", i), "name": n, "categoryId": 1,
                        "priceCents": 100, "costCents": 50, "stockQty": 1, "trackStock": true,
                })
        }
        a5CreateProduct(t, engine, admin, map[string]any{
                "sku": "A5-FQ", "name": `Has,Comma "Quote"` + "\nNewline", "categoryId": 1,
                "priceCents": 100, "stockQty": 1, "trackStock": true,
        })
        w := do(t, engine, "GET", "/api/v1/products/export", admin, nil)
        if w.Code != 200 {
                t.Fatalf("export: %d", w.Code)
        }
        csv := w.Body.String()
        for _, n := range names {
                if !strings.Contains(csv, "'"+n) {
                        t.Errorf("formula-shaped name %q not neutralized with leading quote in export", n)
                }
        }
        if !strings.Contains(csv, `"Has,Comma ""Quote""`+"\n"+`Newline"`) {
                t.Errorf("comma/quote/newline name not properly quoted:\n%s", csv)
        }
        // Sanity: clean cell stays clean.
        if !strings.Contains(csv, "Classic Cotton Tee — Black") {
                t.Errorf("clean name missing from export")
        }
}

func TestAgent5CSVImportHostility(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)
        header := "sku,barcode,name,category,price,cost,stock,track_stock,active\n"
        row := "IMP-1,4001234500999,Import Widget,Cats,10.00,5.00,3,true,true\n"

        // (a) Oversized file (>4MB) rejected, not OOM'd.
        big := &bytes.Buffer{}
        big.WriteString(header)
        for big.Len() < 5*1024*1024 {
                big.WriteString(row)
        }
        body, ctype := a5CSVFile(t, big.String())
        w := a5DoRaw(t, engine, "POST", "/api/v1/products/import", admin, ctype, body.Bytes())
        if w.Code != 400 {
                t.Errorf("5MB import must 400, got %d", w.Code)
        }
        // (b) Too many rows (max 2000 data rows).
        many := &bytes.Buffer{}
        many.WriteString(header)
        for i := 0; i < 2100; i++ {
                fmt.Fprintf(many, "M-%d,,Row %d,Cats,1,1,1,true,true\n", i, i)
        }
        body, ctype = a5CSVFile(t, many.String())
        w = a5DoRaw(t, engine, "POST", "/api/v1/products/import", admin, ctype, body.Bytes())
        if w.Code != 400 {
                t.Errorf("2100-row import must 400, got %d", w.Code)
        }
        // (c) Negative price rejected.
        body, ctype = a5CSVFile(t, header+"NEG-1,,Bad,Cats,-5.00,0,1,true,true\n")
        w = a5DoRaw(t, engine, "POST", "/api/v1/products/import", admin, ctype, body.Bytes())
        if w.Code != 400 {
                t.Errorf("negative price import must 400, got %d", w.Code)
        }
        // (d) Absurd stock rejected.
        body, ctype = a5CSVFile(t, header+"BIG-1,,Big,Cats,1.00,0,1000000001,true,true\n")
        w = a5DoRaw(t, engine, "POST", "/api/v1/products/import", admin, ctype, body.Bytes())
        if w.Code != 400 {
                t.Errorf("absurd stock import must 400, got %d", w.Code)
        }
        // (e) Formula-shaped names are stored literally as DATA (no eval server-side).
        body, ctype = a5CSVFile(t, header+`FOM-1,,"=HYPERLINK(""http://evil"",""win"")",Cats,1.00,0,1,true,true`+"\n")
        w = a5DoRaw(t, engine, "POST", "/api/v1/products/import", admin, ctype, body.Bytes())
        if w.Code != 200 {
                t.Errorf("formula-shaped name should import as data, got %d %s", w.Code, w.Body.String())
        }
        // (f) Garbage binary (not CSV) → clean 400, no 500.
        w = a5DoRaw(t, engine, "POST", "/api/v1/products/import", admin, "text/csv", []byte{0x00, 0xFF, 0xFE, 0x00, 0x01})
        if w.Code != 200 && w.Code != 400 {
                t.Errorf("binary junk import: unexpected %d", w.Code)
        }
}

// ---- 7. Payload hostility: sizes, types, filters, unicode ----

func TestAgent5PayloadTypeConfusionAndLimits(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        a5CreateProduct(t, engine, admin, map[string]any{
                "sku": "A5-TYPE", "name": "A5 Type", "categoryId": 1,
                "priceCents": 100, "stockQty": 100000, "trackStock": true,
        })
        pid := int64(productIDs(t, engine, cashier, "A5 Type")[0]["id"].(float64))

        // Type confusion: wrong JSON types must bind-fail as 4xx, never 500.
        bad := []map[string]any{
                {"items": []map[string]any{{"productId": pid, "qty": "3"}}, "paymentMethod": "cash", "clientUuid": "a5-t1"},
                {"items": []map[string]any{{"productId": pid, "qty": 1.5}}, "paymentMethod": "cash", "clientUuid": "a5-t2"},
                {"items": []map[string]any{{"productId": -7, "qty": 1}}, "paymentMethod": "cash", "clientUuid": "a5-t3"},
                {"items": "not-an-array", "paymentMethod": "cash", "clientUuid": "a5-t4"},
                {"items": []map[string]any{{"productId": pid, "qty": 1}}, "paymentMethod": "cash ", "clientUuid": "a5-t5"},
                {"items": []map[string]any{{"productId": pid, "qty": 1, "unitPriceCents": -900}}, "paymentMethod": "cash", "clientUuid": "a5-t6"},
        }
        for i, b := range bad {
                w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, b)
                if w.Code < 400 || w.Code >= 500 {
                        t.Errorf("type-confusion case %d should 4xx, got %d %s", i, w.Code, w.Body.String())
                }
        }
        // Line-count bomb: 600 lines > MaxOrderLines(500).
        lines := make([]map[string]any, 600)
        for i := range lines {
                lines[i] = map[string]any{"productId": pid, "qty": 1}
        }
        if c := a5Code(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items": lines, "paymentMethod": "cash", "clientUuid": "a5-t7",
        }); c != 422 && c != 400 {
                t.Errorf("600-line cart must 4xx, got %d", c)
        }
        // Deeply nested JSON bomb → clean 400 (json depth limit), no crash.
        deep := strings.Repeat("[", 200000) + strings.Repeat("]", 200000)
        req := httptest.NewRequest("POST", "/api/v1/orders/checkout", strings.NewReader(
                `{"items":`+deep+`,"paymentMethod":"cash"}`))
        req.Header.Set("Authorization", "Bearer "+cashier)
        req.Header.Set("Content-Type", "application/json")
        w := httptest.NewRecorder()
        engine.ServeHTTP(w, req)
        if w.Code >= 500 {
                t.Errorf("deep-nesting bomb must not 5xx, got %d", w.Code)
        }
        // Oversized body (8MB password) — accepted parse, still a clean 401.
        huge := strings.Repeat("A", 8<<20)
        w = do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
                "username": "admin", "password": huge,
        })
        if w.Code >= 500 {
                t.Errorf("8MB body must not 5xx, got %d", w.Code)
        }
        // Half-megabyte note: currently stored verbatim (unbounded) — must at
        // minimum not 5xx; 201 documents the unbounded-note finding.
        w = do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items":         []map[string]any{{"productId": pid, "qty": 1}},
                "paymentMethod": "cash", "clientUuid": "a5-t8",
                "note": strings.Repeat("N", 500<<10),
        })
        if w.Code >= 500 {
                t.Errorf("500KB note must not 5xx, got %d", w.Code)
        }
        if w.Code == 201 {
                t.Logf("CONFIRMED: 500KB order note accepted and stored unbounded (amplified to receipts/ws broadcasts)")
        }
}

func TestAgent5UnicodeHomoglyphUsernames(t *testing.T) {
        t.Setenv("ALLOW_SIGNUP", "true")
        engine, _, _, _ := newTestServer(t)
        bad := []string{
                "аdmin",                 // Cyrillic homoglyph 'а'
                "us😀er",                 // emoji
                "sp ace",                // space
                "tab\tx",                // control
                "new\nline",             // control
                "../../etc/passwd",      // traversal chars
                `quo"te`,                // quote
                "adm in\x00",            // NUL
                strings.Repeat("a", 33), // over-long
                "администратор",         // full unicode
        }
        for _, u := range bad {
                w := do(t, engine, "POST", "/api/v1/auth/signup", "", map[string]any{
                        "username": u, "password": "valid-pass-1", "shopName": "Unicode Shop",
                })
                if w.Code != 400 {
                        t.Errorf("hostile username %q must 400, got %d", u, w.Code)
                }
                // Login path must stay a clean 401 (no 500, no enumeration timing leak).
                w = do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
                        "username": u, "password": "valid-pass-1",
                })
                if w.Code >= 500 {
                        t.Errorf("hostile username login %q must not 5xx, got %d", u, w.Code)
                }
        }
        // Positive control still works.
        w := do(t, engine, "POST", "/api/v1/auth/signup", "", map[string]any{
                "username": "agent5-ok", "password": "valid-pass-1", "shopName": "Valid Shop",
        })
        if w.Code != 201 {
                t.Errorf("valid ASCII username must 201, got %d", w.Code)
        }
}

func TestAgent5OrdersFilterHostility(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        if c := a5Code(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items":         []map[string]any{{"productId": 1, "qty": 1}},
                "paymentMethod": "cash", "clientUuid": "a5-filt",
        }); c != 201 {
                t.Fatalf("seed order: %d", c)
        }
        paths := []string{
                "/api/v1/orders?status=PENDING%27%20OR%20%271%27%3D%271",
                "/api/v1/orders?search=%25",       // LIKE wildcard — filter no-op, not injectable
                "/api/v1/orders?search=%5F%25%5F", // wildcard soup
                "/api/v1/orders?cashierId=1%3BDROP%20TABLE%20orders",
                "/api/v1/orders?from=2026-01-01%27%20OR%20%271%27%3D%271&to=2026-12-31",
                "/api/v1/orders?limit=999999999",
                "/api/v1/orders?limit=-1",
                "/api/v1/orders?offset=-999",
                "/api/v1/orders?limit=50&offset=99999999999999999999",
                "/api/v1/orders?status=NOT_A_STATUS",
        }
        for _, p := range paths {
                w := do(t, engine, "GET", p, cashier, nil)
                if w.Code >= 500 {
                        t.Errorf("hostile filter %s must not 5xx, got %d %s", p, w.Code, w.Body.String())
                }
        }
        // Table still intact.
        if c := a5Code(t, engine, "GET", "/api/v1/orders", cashier, nil); c != 200 {
                t.Errorf("orders list after filter barrage: %d", c)
        }
}

// ---- 8. WebSocket hub: auth handshake, cross-shop leak, slow clients ----

func TestAgent5WSAuthHandshake(t *testing.T) {
        engine, _, cashier, _ := newTestServer(t)
        srv := httptest.NewServer(engine)
        defer srv.Close()

        // (a) No Authorization header → upgrade refused.
        if _, _, err := a5DialWS(t, srv, ""); err == nil {
                t.Error("ws upgrade without bearer token must fail")
        }
        // (b) Valid bearer + garbage first message → ERROR frame then close.
        conn, _, err := a5DialWS(t, srv, cashier)
        if err != nil {
                t.Fatalf("dial: %v", err)
        }
        defer conn.Close()
        if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"token":"garbage"}`)); err != nil {
                t.Fatalf("write: %v", err)
        }
        typ, _, err := a5ReadEvent(t, conn, 3*time.Second)
        if err != nil || typ != "ERROR" {
                t.Errorf("garbage hub token must produce ERROR frame, got type=%q err=%v", typ, err)
        }
        // (c) Valid hub token accepted (server stays silent, connection open).
        conn2, _, err := a5DialWS(t, srv, cashier)
        if err != nil {
                t.Fatalf("dial2: %v", err)
        }
        defer conn2.Close()
        if err := a5WSAuth(t, conn2, cashier); err != nil {
                t.Fatalf("auth write: %v", err)
        }
        _ = conn2.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
        if _, _, err := conn2.ReadMessage(); err == nil {
                t.Error("valid auth must not produce an immediate frame")
        }
}

// BUG: the hub is box-shared — ANY valid token from ANY shop receives EVERY
// shop's ORDER_PAID / ORDER_VOIDED broadcasts (customer names, totals,
// payment phone numbers). Shop A's cashier watching shop B's sales is a
// cross-tenant data leak on a multi-shop box.
func TestAgent5BugWSCrossShopEventLeak(t *testing.T) {
        t.Setenv("ALLOW_SIGNUP", "true")
        engine, _, _, _ := newTestServer(t)
        tokA, _ := signup(t, engine, "wsa5", "wsa5pass1", "WS Shop A")
        tokB, _ := signup(t, engine, "wsb5", "wsb5pass1", "WS Shop B")

        // Stock shop B with its own product.
        a5CreateProduct(t, engine, tokB, map[string]any{
                "sku": "WS-B1", "name": "WS B Widget", "categoryId": 1,
                "priceCents": 12345, "stockQty": 10, "trackStock": true,
        })
        pidB := int64(productIDs(t, engine, tokB, "WS B Widget")[0]["id"].(float64))

        srv := httptest.NewServer(engine)
        defer srv.Close()

        connA, _, err := a5DialWS(t, srv, tokA)
        if err != nil {
                t.Fatalf("dial A: %v", err)
        }
        defer connA.Close()
        if err := a5WSAuth(t, connA, tokA); err != nil {
                t.Fatalf("auth A: %v", err)
        }

        // Shop B sells; shop A's socket must hear NOTHING about it.
        if c := a5Code(t, engine, "POST", "/api/v1/orders/checkout", tokB, map[string]any{
                "items":         []map[string]any{{"productId": pidB, "qty": 1}},
                "paymentMethod": "cash", "customerName": "Secret B Customer",
                "clientUuid": "a5-wsleak",
        }); c != 201 {
                t.Fatalf("shop B checkout: %d", c)
        }
        typ, raw, err := a5ReadEvent(t, connA, 2*time.Second)
        if err == nil && (typ == "ORDER_PAID" || strings.Contains(raw, "WS B Widget") || strings.Contains(raw, "Secret B Customer")) {
                t.Errorf("BUG T5-WS-1: shop A socket received shop B's broadcast (type=%s, body=%.200s)", typ, raw)
        }
}

// Broadcasts with a stalled (non-reading) client must never stall the HTTP
// request path — big orders are pushed, the buffer fills, the hub drops the
// client, and checkouts keep completing.
func TestAgent5WSBroadcastNeverStalls(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        pid := a5CreateProduct(t, engine, admin, map[string]any{
                "sku": "A5-STALL", "name": "A5 Stall", "categoryId": 1,
                "priceCents": 100, "stockQty": 100, "trackStock": true,
        })
        srv := httptest.NewServer(engine)
        defer srv.Close()

        // Two clients authenticate and then stop reading entirely.
        stalled := make([]*websocket.Conn, 2)
        for i := range stalled {
                c, _, err := a5DialWS(t, srv, cashier)
                if err != nil {
                        t.Fatalf("dial %d: %v", i, err)
                }
                defer c.Close()
                if err := a5WSAuth(t, c, cashier); err != nil {
                        t.Fatalf("auth %d: %v", i, err)
                }
                stalled[i] = c
        }
        // Three half-megabyte-note orders → three ~500KB ORDER_PAID broadcasts
        // that the stalled clients never drain.
        begin := time.Now()
        for i := 0; i < 3; i++ {
                started := time.Now()
                w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                        "items":         []map[string]any{{"productId": pid, "qty": 1}},
                        "paymentMethod": "cash", "clientUuid": fmt.Sprintf("a5-stall-%d", i),
                        "note": strings.Repeat("S", 500<<10),
                })
                if w.Code != 201 {
                        t.Fatalf("stall checkout %d: %d %s", i, w.Code, w.Body.String())
                }
                if d := time.Since(started); d > 5*time.Second {
                        t.Fatalf("checkout %d blocked %.1fs behind slow ws clients", i, d.Seconds())
                }
        }
        // The engine is still healthy afterwards.
        if c := a5Code(t, engine, "GET", "/api/v1/health", "", nil); c != 200 {
                t.Fatalf("health after broadcast storm: %d", c)
        }
        t.Logf("3 × 500KB broadcasts against stalled clients took %s total", time.Since(begin))
}

// ---- 9. T5 retry: sharper cross-shop writes, void exact-once, zero-value pay ----

// Extends TestCrossShopSweep with the state-changing paths the sweep never
// touched: checkout with a foreign product id, tab checkout / settle /
// manual-confirm / stk-retry / void against foreign orders and customers,
// foreign stock mutation, held-sale discards, search-scoping, and daily
// report isolation. Database-per-tenant says every one of these fails
// closed (4xx) and every report shows ONLY this shop's numbers.
func TestAgent5CrossShopWriteBattery(t *testing.T) {
        t.Setenv("ALLOW_SIGNUP", "true")
        engine, admin, _, _ := newTestServer(t)
        tokB, _ := signup(t, engine, "a5cswb", "a5cswbpass1", "A5 CSW Shop B")

        // Shop A (default tenant) fixtures.
        pid := a5CreateProduct(t, engine, admin, map[string]any{
                "sku": "A5-CSW", "name": "A5 CSW Widget", "categoryId": 1,
                "priceCents": 2500, "stockQty": 7, "trackStock": true,
        })
        w := do(t, engine, "POST", "/api/v1/customers", admin, map[string]any{
                "name": "A5 CSW Debtor", "creditLimitCents": 900000,
        })
        if w.Code != 201 {
                t.Fatalf("customer: %d %s", w.Code, w.Body.String())
        }
        cid := int64(dataMap(t, w)["id"].(float64))
        w = do(t, engine, "POST", "/api/v1/orders/checkout", admin, map[string]any{
                "items":         []map[string]any{{"productId": pid, "qty": 1}},
                "paymentMethod": "account", "customerId": cid, "clientUuid": "a5-csw-tab",
        })
        if w.Code != 201 {
                t.Fatalf("tab checkout: %d %s", w.Code, w.Body.String())
        }
        tabOrder := dataMap(t, w)
        tabOID := int64(tabOrder["id"].(float64))
        tabNumber, _ := tabOrder["number"].(string)
        w = do(t, engine, "POST", "/api/v1/held-sales", admin, map[string]any{
                "refName": "a5-csw-hold",
                "cart": map[string]any{
                        "items":         []map[string]any{{"productId": pid, "qty": 2}},
                        "paymentMethod": "cash", // nested CheckoutRequest binds strictly
                },
        })
        if w.Code != 201 {
                t.Fatalf("hold: %d %s", w.Code, w.Body.String())
        }
        heldID := int64(dataMap(t, w)["id"].(float64))

        // Shop B replaying shop A's ids through every write/execute path.
        // 4xx = fails closed (good). <400 = cross-tenant write (FATAL).
        // >=500 = still closed, but a mapErr gap worth reporting.
        probes := []struct {
                name   string
                method string
                path   string
                body   any
        }{
                {"checkout with foreign product", "POST", "/api/v1/orders/checkout", map[string]any{
                        "items": []map[string]any{{"productId": pid, "qty": 1}},
                        "paymentMethod": "cash", "clientUuid": "a5-csw-x1",
                }},
                {"tab checkout on foreign customer", "POST", "/api/v1/orders/checkout", map[string]any{
                        "items": []map[string]any{{"productId": 1, "qty": 1}},
                        "paymentMethod": "account", "customerId": cid, "clientUuid": "a5-csw-x2",
                }},
                {"credit checkout on foreign customer", "POST", "/api/v1/orders/checkout", map[string]any{
                        "items": []map[string]any{{"productId": 1, "qty": 1}},
                        "paymentMethod": "credit", "customerId": cid, "clientUuid": "a5-csw-x3",
                }},
                {"loyalty redeem against foreign customer", "POST", "/api/v1/orders/checkout", map[string]any{
                        "items": []map[string]any{{"productId": 1, "qty": 1}},
                        "paymentMethod": "cash", "customerId": cid, "redeemPoints": 10, "clientUuid": "a5-csw-x4",
                }},
                {"adjust foreign stock", "POST", "/api/v1/products/" + itoa64(pid) + "/adjust-stock", map[string]any{"delta": -7}},
                {"deactivate foreign product", "DELETE", "/api/v1/products/" + itoa64(pid), nil},
                {"discard foreign held sale", "DELETE", "/api/v1/held-sales/" + itoa64(heldID), nil},
                {"settle foreign tab", "POST", "/api/v1/orders/" + itoa64(tabOID) + "/settle", map[string]any{"method": "cash"}},
                {"manual-confirm foreign order", "POST", "/api/v1/orders/" + itoa64(tabOID) + "/manual", map[string]any{"receiptCode": "QB2C3D4E5F"}},
                {"stk-retry foreign order", "POST", "/api/v1/orders/" + itoa64(tabOID) + "/stkpush", map[string]any{"phone": "0722123456"}},
                {"void foreign order", "POST", "/api/v1/orders/" + itoa64(tabOID) + "/void", map[string]any{"reason": "foreign"}},
                {"walk-in payment to foreign customer", "POST", "/api/v1/customers/" + itoa64(cid) + "/payments", map[string]any{"amountCents": 100}},
                {"credit topup on foreign customer", "POST", "/api/v1/customers/" + itoa64(cid) + "/credit-topup", map[string]any{"amountCents": 100}},
        }
        for _, pr := range probes {
                w := do(t, engine, pr.method, pr.path, tokB, pr.body)
                if w.Code < 400 {
                        t.Errorf("CROSS-SHOP LEAK: %s succeeded (%d): %s", pr.name, w.Code, w.Body.String())
                        continue
                }
                if w.Code >= 500 {
                        t.Errorf("cross-shop %s returned %d (mapErr gap, should be 4xx): %s",
                                pr.name, w.Code, w.Body.String())
                }
        }

        // Search scoping: B cannot surface A's rows by number/name.
        for _, q := range []string{
                "/api/v1/orders?search=" + tabNumber,
                "/api/v1/products?search=A5%20CSW",
                "/api/v1/customers?search=A5%20CSW%20Debtor",
                "/api/v1/suppliers?search=A5",
        } {
                w := do(t, engine, "GET", q, tokB, nil)
                if w.Code != 200 {
                        t.Errorf("B search %s: %d", q, w.Code)
                        continue
                }
                if rows, _ := decode(t, w)["data"].([]any); len(rows) != 0 {
                        t.Errorf("CROSS-SHOP LEAK: B search %s returned %d foreign rows", q, len(rows))
                }
        }

        // Shop A's state is untouched: stock, PENDING tab, held sale intact.
        if s := a5StockOf(t, engine, admin, "A5 CSW Widget"); s != 7 {
                t.Fatalf("A stock disturbed: %d (want 7)", s)
        }
        w = do(t, engine, "GET", "/api/v1/orders/"+itoa64(tabOID), admin, nil)
        if w.Code != 200 || dataMap(t, w)["status"] != "PENDING" {
                t.Fatalf("A tab disturbed: %d %v", w.Code, dataMap(t, w)["status"])
        }
        w = do(t, engine, "GET", "/api/v1/held-sales", admin, nil)
        if w.Code != 200 {
                t.Fatalf("held list: %d", w.Code)
        }
        var heldFound bool
        for _, it := range decode(t, w)["data"].([]any) {
                if int64(it.(map[string]any)["id"].(float64)) == heldID {
                        heldFound = true
                }
        }
        if !heldFound {
                t.Fatal("A's held sale was discarded by shop B")
        }

        // Report isolation: A settles its own tab, then A's daily report shows
        // the sale and B's shows zeros — no cross-tenant totals, no ?id params.
        if c := a5Code(t, engine, "POST", "/api/v1/orders/"+itoa64(tabOID)+"/settle", admin,
                map[string]any{"method": "cash"}); c != 200 {
                t.Fatalf("A settle own tab: %d", c)
        }
        w = do(t, engine, "GET", "/api/v1/reports/daily", admin, nil)
        if w.Code != 200 {
                t.Fatalf("A daily: %d", w.Code)
        }
        a := decode(t, w)["data"].(map[string]any)
        if int64(a["salesCents"].(float64)) != 2500 || a["ordersPaid"].(float64) != 1 {
                t.Errorf("A daily report wrong: sales=%v paid=%v", a["salesCents"], a["ordersPaid"])
        }
        w = do(t, engine, "GET", "/api/v1/reports/daily", tokB, nil)
        if w.Code != 200 {
                t.Fatalf("B daily: %d", w.Code)
        }
        b := decode(t, w)["data"].(map[string]any)
        if b["salesCents"].(float64) != 0 || b["ordersPaid"].(float64) != 0 || b["cashCents"].(float64) != 0 {
                t.Errorf("CROSS-SHOP LEAK: B daily report shows A's money: %v", b)
        }
        // B attacking report params with A's ids changes nothing.
        for _, q := range []string{
                "/api/v1/reports/daily?cashierId=" + itoa64(1),
                "/api/v1/reports/daily?date=2026-01-01&shopId=x",
                "/api/v1/reports/monthly?customerId=" + itoa64(cid),
        } {
                w := do(t, engine, "GET", q, tokB, nil)
                if w.Code != 200 {
                        t.Errorf("B report %s: %d", q, w.Code)
                        continue
                }
                if d, ok := decode(t, w)["data"].(map[string]any); ok {
                        if sc, ok := d["salesCents"].(float64); ok && sc != 0 {
                                t.Errorf("CROSS-SHOP LEAK: B report %s shows sales %v", q, sc)
                        }
                }
        }
}

// Void must be exactly-once for BOTH side effects: stock restore and
// redeemed-points refund. Second void on the same order is rejected and
// must move nothing.
func TestAgent5VoidExactlyOnceStockAndPoints(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        a5SetSetting(t, engine, admin, "loyalty_earn_per_cents", "100") // 1 pt / KES 1
        pid := a5CreateProduct(t, engine, admin, map[string]any{
                "sku": "A5-VOID1", "name": "A5 VoidOnce", "categoryId": 1,
                "priceCents": 50000, "stockQty": 5, "trackStock": true,
        })
        w := do(t, engine, "POST", "/api/v1/customers", admin, map[string]any{"name": "A5 VoidOnce Cust"})
        if w.Code != 201 {
                t.Fatalf("customer: %d %s", w.Code, w.Body.String())
        }
        cid := int64(dataMap(t, w)["id"].(float64))

        // Order 1: paid via manual M-Pesa → earns 500 points (50000/100).
        w = do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items": []map[string]any{{"productId": pid, "qty": 1}},
                "paymentMethod": "mpesa", "paymentMode": "manual",
                "customerId": cid, "clientUuid": "a5-vo-1",
        })
        if w.Code != 201 {
                t.Fatalf("checkout1: %d %s", w.Code, w.Body.String())
        }
        oid1 := int64(dataMap(t, w)["id"].(float64))
        if c := a5Code(t, engine, "POST", "/api/v1/orders/"+itoa64(oid1)+"/manual", cashier,
                map[string]any{"receiptCode": "QA1B2C3D4E"}); c != 200 {
                t.Fatalf("manual confirm: %d", c)
        }
        if pts := a5CustomerPoints(t, engine, admin, cid); pts != 500 {
                t.Fatalf("pre-redeem points: %d (want 500)", pts)
        }

        // Order 2: admin redeems 400 points (40000 cents, under the 50% cap
        // of the 100000-cent subtotal) → stock 5→3, points 500→100.
        w = do(t, engine, "POST", "/api/v1/orders/checkout", admin, map[string]any{
                "items": []map[string]any{{"productId": pid, "qty": 2}},
                "paymentMethod": "cash", "customerId": cid, "redeemPoints": 400,
                "clientUuid": "a5-vo-2",
        })
        if w.Code != 201 {
                t.Fatalf("redeem checkout: %d %s", w.Code, w.Body.String())
        }
        oid2 := int64(dataMap(t, w)["id"].(float64))
        // Cash checkouts EARN loyalty too (fixed T5-Money-3): 500 - 400
        // redeemed + 600 earned (60000 cents / 100) = 700. The void below
        // must reverse the earn AND refund the redemption, landing on 500.
        if pts := a5CustomerPoints(t, engine, admin, cid); pts != 700 {
                t.Fatalf("post-redeem points: %d (want 700 — cash earns loyalty now)", pts)
        }
        if s := a5StockOf(t, engine, cashier, "A5 VoidOnce"); s != 2 {
                t.Fatalf("post-sale stock: %d (want 2)", s)
        }

        // Void once: stock restored to 5, points refunded to exactly 500.
        w = do(t, engine, "POST", "/api/v1/orders/"+itoa64(oid2)+"/void", cashier,
                map[string]any{"reason": "changed mind"})
        if w.Code != 200 {
                t.Fatalf("void: %d %s", w.Code, w.Body.String())
        }
        if s := a5StockOf(t, engine, cashier, "A5 VoidOnce"); s != 4 {
                t.Fatalf("stock after void: %d (want 4)", s)
        }
        if pts := a5CustomerPoints(t, engine, admin, cid); pts != 500 {
                t.Fatalf("points after void: %d (want exactly 500 — single refund)", pts)
        }

        // Second void on the SAME order: 4xx, moves nothing.
        if c := a5Code(t, engine, "POST", "/api/v1/orders/"+itoa64(oid2)+"/void", cashier,
                map[string]any{"reason": "again"}); c < 400 || c >= 500 {
                t.Fatalf("double void must 4xx, got %d", c)
        }
        if s := a5StockOf(t, engine, cashier, "A5 VoidOnce"); s != 4 {
                t.Fatalf("stock after double void: %d (want 4 — no double restore)", s)
        }
        if pts := a5CustomerPoints(t, engine, admin, cid); pts != 500 {
                t.Fatalf("points after double void: %d (want 500 — no double refund)", pts)
        }
        // The other PAID order is untouched and stock/points are consistent.
        w = do(t, engine, "GET", "/api/v1/orders/"+itoa64(oid1), cashier, nil)
        if w.Code != 200 || dataMap(t, w)["status"] != "PAID" {
                t.Fatalf("order1 disturbed: %d %v", w.Code, dataMap(t, w)["status"])
        }
}

// A forged/malformed Daraja success callback that carries NO Amount item
// (zero-amount evidence) must never silently complete a payment as clean.
// completePayment only flags discrepancy when amountCents > 0 and differs —
// zero slips through the check entirely.
func TestAgent5BugZeroAmountCallbackMarksPaidSilently(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)
        a5SetSetting(t, engine, admin, "mpesa_mock_delay_ms", "60000")
        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                "items":         []map[string]any{{"productId": 1, "qty": 1}},
                "paymentMethod": "mpesa", "paymentMode": "stk",
                "customerPhone": "0722123456", "clientUuid": "a5-zero-cb",
        })
        if w.Code != 201 {
                t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
        }
        order := dataMap(t, w)
        oid := int64(order["id"].(float64))
        pays, _ := order["payments"].([]any)
        p0, _ := pays[0].(map[string]any)
        coID, _ := p0["checkoutRequestId"].(string)
        if coID == "" {
                t.Fatalf("no checkout id: %v", p0)
        }
        // Success callback WITHOUT any Amount metadata item.
        cb := map[string]any{
                "Body": map[string]any{"stkCallback": map[string]any{
                        "MerchantRequestID": "m-z", "CheckoutRequestID": coID,
                        "ResultCode": 0, "ResultDesc": "success",
                        "CallbackMetadata": map[string]any{"Item": []map[string]any{
                                {"Name": "MpesaReceiptNumber", "Value": "QZ9BB22CC9"},
                        }},
                }},
        }
        if c := a5Code(t, engine, "POST", "/api/v1/payments/mpesa/callback", "", cb); c != 200 {
                t.Fatalf("callback: %d", c)
        }
        w = do(t, engine, "GET", "/api/v1/orders/"+itoa64(oid), cashier, nil)
        order = dataMap(t, w)
        pays, _ = order["payments"].([]any)
        p0, _ = pays[0].(map[string]any)
        if order["status"] == "PAID" && order["discrepancy"] != true {
                t.Errorf("BUG T5-Money-7: zero-amount success callback marked order PAID with "+
                        "discrepancy=false (payment status=%v receipt=%v) — completed with no money evidence and no tell-tale",
                        p0["status"], p0["mpesaReceipt"])
        }
        // The order must ALSO not have deducted stock silently as fully-paid
        // when nothing was proven collected — if it did deduct, discrepancy
        // is the only honest signal (asserted above).
        if order["status"] == "PAID" {
                t.Logf("order was completed by zero-evidence callback; stock now %d", a5StockOf(t, engine, cashier, "Classic Cotton Tee — Black"))
        }
}
