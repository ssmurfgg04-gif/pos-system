package handlers_test

// v134_regression_test.go — handler-level regression tests for the v1.3.4
// fixes:
//
//  1. Settings saves survive runtime bookkeeping keys in the payload (the
//     store-name report-as-failed bug) while genuinely unknown keys still
//     fail loudly, and runtime keys never round-trip back out.
//  2. The owner-approval dialog answers 422 (never 401) on bad portal
//     credentials, so the session-expiry interceptor cannot wipe the
//     owner's token mid-task (the kick-out bug).
//  3. Owner-level password logins self-heal the cloud portal credential,
//     so the owner gate works without a prior password SAVE. Cashier
//     logins never publish.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"posapp/internal/database"
	"posapp/internal/handlers"
	"posapp/internal/printer"
	"posapp/internal/router"
	"posapp/internal/services"
	"posapp/internal/settings"
	"posapp/internal/tenants"
	"posapp/internal/ws"
)

// wireCloudEngine mirrors wireTestEngine but hands back the settings store
// and seeds the cloud-link settings to point at fakeURL (a fake Supabase),
// so cloud RPC paths resolve in-process.
func wireCloudEngine(t *testing.T, fakeURL string) (*gin.Engine, *database.DB, *settings.Store) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	db, err := database.Open("sqlite", filepath.Join(dir, "test.db"), "")
	if err != nil {
		t.Fatalf("db: %v", err)
	}
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
	st.Set("mpesa_mock_delay_ms", "200")
	st.Set("mpesa_env", "mock")
	// Cloud link pointing at the fake: cached bootstrap path (sync_source
	// "cloud" + fresh stamp) resolves sync_endpoint/sync_team_code.
	st.Set("sync_source", "cloud")
	st.Set("sync_endpoint", fakeURL)
	st.Set("sync_team_code", "TEST-TEAM")
	st.Set("sync_bootstrap_at", time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	st.Set("sync_registered", "1")
	st.Set("sync_approved", "1")
	st.Set("sync_enabled", "true")

	hub := ws.NewHub(st.JWTSecret)
	go hub.Run()
	reg, err := tenants.Load(dir)
	if err != nil {
		t.Fatalf("tenants: %v", err)
	}
	dbPath := filepath.Join(dir, "test.db")
	shop, err := reg.CreateShop("Test Shop", dbPath, "2026-09-16T00:00:00Z")
	if err != nil {
		t.Fatalf("shop: %v", err)
	}
	for _, u := range []string{"admin", "cashier"} {
		if err := reg.RegisterUser(u, shop.ID); err != nil {
			t.Fatalf("register %s: %v", u, err)
		}
	}
	secret := reg.EnsureJWTSecret(st.Get("jwt_secret"))
	dbPool := tenants.NewPool(reg, "sqlite", "")
	dbPool.Inject(shop.ID, db)
	shopPool := services.NewShopPool(dbPool, hub, nil)
	svc, err := shopPool.Service(shop.ID)
	if err != nil {
		t.Fatalf("shop service: %v", err)
	}
	pw := printer.NewWorker(db, svc.Settings())
	shopPool.SetPrinter(pw)
	h := handlers.New(db, svc.Settings(), svc, hub, pw)
	h.Tenants = reg
	h.Shops = shopPool
	h.DefaultShop = shop.ID
	h.MasterSecret = []byte(secret)
	t.Cleanup(func() { shopPool.CloseAll() })
	engine := router.New(h, nil)
	return engine, db, st
}

// rotatedLogin signs a seeded user in and rotates the password through the
// real self-service path (newTestServer's dance), returning a steady-state
// token. The cloud harness boots fresh seeds, so rotation happens here.
func rotatedLogin(t *testing.T, engine *gin.Engine, user, seeded string) string {
	t.Helper()
	tok := login(t, engine, user, seeded)
	rotateFresh(t, engine, tok)
	return login(t, engine, user, rotatedPassword)
}

// fakeCloudRPC records portal_set_credentials + owner_mint_capability and
// answers with fixed bodies. It only serves the two RPCs the tests drive.
type fakeCloudRPC struct {
	mu       sync.Mutex
	srv      *httptest.Server
	portal   []map[string]any
	mints    []map[string]any
	mintBody map[string]any
}

func (f *fakeCloudRPC) srvURL() string { return f.srv.URL }

func newFakeCloudRPC(t *testing.T, mintBody map[string]any) *fakeCloudRPC {
	t.Helper()
	f := &fakeCloudRPC{mintBody: mintBody}
	mux := http.NewServeMux()
	handle := func(name string, rec func(map[string]any)) {
		mux.HandleFunc("/rest/v1/rpc/"+name, func(w http.ResponseWriter, r *http.Request) {
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			f.mu.Lock()
			rec(in)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if name == "owner_mint_capability" && f.mintBody != nil {
				json.NewEncoder(w).Encode(f.mintBody)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		})
	}
	handle("portal_set_credentials", func(in map[string]any) { f.portal = append(f.portal, in) })
	handle("owner_mint_capability", func(in map[string]any) { f.mints = append(f.mints, in) })
	srv := httptest.NewServer(mux)
	f.srv = srv
	t.Cleanup(srv.Close)
	return f
}

func (f *fakeCloudRPC) portalCalls() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.portal...)
}

// waitFor polls cond for up to 2s (the self-heal publish is async).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestSettingsSaveSurvivesRuntimeKeyEchoes: the Settings page round-trips
// the whole snapshot; runtime bookkeeping keys in the payload must be
// dropped (not 400), the real edit must land, and runtime keys must never
// come back out of the snapshot. A genuinely unknown key still fails.
func TestSettingsSaveSurvivesRuntimeKeyEchoes(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)

	// The exact payload shape the old build produced on a cloud-connected
	// till: a real edit plus echoes of runtime keys the API cannot write.
	body := map[string]any{"values": map[string]string{
		"store_name":                 "Kimani Whole Sale",
		"receipt_footer":             "Thank you for shopping with us",
		"sync_source":                "cloud",
		"device_id":                  "dev-echo",
		"syncstate_last_event_T-001": "41",
		"brand_logo_data":            "data:image/png;base64,AAAA",
		"paystack_via_cloud":         "true",
	}}
	w := do(t, engine, "PUT", "/api/v1/settings", admin, body)
	if w.Code != 200 {
		t.Fatalf("settings save with runtime echoes: code=%d body=%v", w.Code, w.Body.String())
	}
	w = do(t, engine, "GET", "/api/v1/settings", admin, nil)
	if w.Code != 200 {
		t.Fatalf("settings get: code=%d", w.Code)
	}
	snap := dataMap(t, w)
	if snap["store_name"] != "Kimani Whole Sale" {
		t.Fatalf("store_name = %v, want the edit to land", snap["store_name"])
	}
	for _, k := range []string{"sync_source", "device_id", "syncstate_last_event_T-001", "brand_logo_data", "paystack_via_cloud"} {
		if _, present := snap[k]; present {
			t.Fatalf("internal key %q must not appear in the API snapshot", k)
		}
	}
	// A genuinely mistyped key still fails loudly.
	w = do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{"values": map[string]string{
		"definitely_not_a_setting": "1",
	}})
	if w.Code != 400 {
		t.Fatalf("unknown key: code=%d, want 400", w.Code)
	}
}

// TestOwnerApprovalBadCredentialKeepsSession: wrong portal password is a
// 422 the dialog can retry, NOT a 401 that wipes the session (the owner
// used to land on the login page mid-invite).
func TestOwnerApprovalBadCredentialKeepsSession(t *testing.T) {
	fake := newFakeCloudRPC(t, map[string]any{
		"ok": false, "error": "those owner credentials were not recognized for this store",
	})
	engine, _, _ := wireCloudEngine(t, fake.srvURL())
	admin := rotatedLogin(t, engine, "admin", "admin123")

	w := do(t, engine, "POST", "/api/v1/team-sync/owner-approval", admin,
		map[string]any{"username": "admin", "password": "not-my-password"})
	if w.Code != 422 {
		t.Fatalf("owner approval with bad credentials: code=%d body=%v, want 422 (never 401)", w.Code, w.Body.String())
	}
	// The session token must still be valid: the owner keeps working.
	w = do(t, engine, "GET", "/api/v1/settings", admin, nil)
	if w.Code != 200 {
		t.Fatalf("session wiped after 422: settings GET code=%d", w.Code)
	}
}

// TestOwnerLoginPublishesPortalCredential: a successful owner-level login
// self-heals the cloud portal credential (owners whose password predates
// the portal could never pass the owner gate). Cashier logins never publish.
func TestOwnerLoginPublishesPortalCredential(t *testing.T) {
	fake := newFakeCloudRPC(t, nil)
	engine, _, _ := wireCloudEngine(t, fake.srvURL())

	_ = login(t, engine, "admin", "admin123")
	waitFor(t, "portal publish after owner login", func() bool {
		return len(fake.portalCalls()) == 1
	})
	calls := fake.portalCalls()
	if got, _ := calls[0]["p_username"].(string); got != "admin" {
		t.Fatalf("published username = %v, want admin", calls[0]["p_username"])
	}
	if hash, _ := calls[0]["p_password_hash"].(string); len(hash) < 20 || hash[:2] != "$2" {
		t.Fatalf("published credential is not a bcrypt hash: %q", hash)
	}
	if _, hasPlain := calls[0]["p_password"]; hasPlain {
		t.Fatal("the plaintext password must never be published")
	}

	// A cashier login must NOT publish anything.
	_ = login(t, engine, "cashier", "cashier123")
	time.Sleep(300 * time.Millisecond)
	if calls := fake.portalCalls(); len(calls) != 1 {
		t.Fatalf("portal publish calls = %d, want 1 (cashier must not publish)", len(calls))
	}
}
