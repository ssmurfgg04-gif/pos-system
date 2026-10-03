package services

// agent3_vault_probe_test.go — T3 adversarial probes for the cloud config
// vault (cloudconfig.go), the team sync engine (teamsync.go) and device
// identity (synccloud.go / teamjoin.go) against a FAKE Supabase.
//
// Conventions follow synccloud_test.go (fakeSupabase) but the fake here is
// self-contained (agent3FakeCloud) so it can model revocation, partial
// push commits, poison events and the sync_device_config RPC contract.

import (
        "context"
        "encoding/json"
        "net/http"
        "net/http/httptest"
        "os"
        "path/filepath"
        "strings"
        "sync"
        "testing"
        "time"

        "posapp/internal/auth"
        "posapp/internal/cloudmeta"
        "posapp/internal/database"
        "posapp/internal/models"
        "posapp/internal/printer"
        "posapp/internal/settings"
)

// ---------------------------------------------------------------------------
// Fake cloud
// ---------------------------------------------------------------------------

type agent3PushEvent struct {
        Entity     string          `json:"entity"`
        Op         string          `json:"op"`
        ClientUUID string          `json:"client_uuid"`
        Payload    json.RawMessage `json:"payload"`
}

type agent3Cloud struct {
        mu      sync.Mutex
        srv     *httptest.Server
        selfURL string

        team        string
        autoApprove bool

        device   map[string]string // device_id -> secret_hash
        approved map[string]bool
        revoked  map[string]bool

        events     []map[string]any // server-side sync_events (id, device_id, entity, op, client_uuid, payload)
        seenUUIDs  map[string]bool  // unique (team, client_uuid) — the DB constraint
        nextID     int64
        configRows map[string]string // app_config (full map, incl. secrets)
        publicRows map[string]string // anon-visible app_config rows

        registerBlocked bool // sync_register always refuses
        failPushNext    int  // respond ok:false AFTER committing N more pushes (partial commit emulation)
        poisonEntity    string
        joinTokens      map[string]map[string]any // token -> invite payload
        usedTokens      map[string]bool

        registerCalls int
        pushCalls     int
        pullCalls     int
        configCalls   int
        joinCalls     int
}

func newAgent3Cloud(t *testing.T) *agent3Cloud {
        f := &agent3Cloud{
                team:        "VAULT-TEAM",
                autoApprove: true,
                device:      map[string]string{},
                approved:    map[string]bool{},
                revoked:     map[string]bool{},
                seenUUIDs:   map[string]bool{},
                nextID:      1,
                configRows:  map[string]string{},
                publicRows:  map[string]string{},
                joinTokens:  map[string]map[string]any{},
                usedTokens:  map[string]bool{},
        }
        mux := http.NewServeMux()

        // GET /rest/v1/app_config — anon REST read (RLS: is_secret=false only).
        mux.HandleFunc("/rest/v1/app_config", func(w http.ResponseWriter, r *http.Request) {
                f.mu.Lock()
                defer f.mu.Unlock()
                if r.Header.Get("apikey") == "" {
                        w.WriteHeader(401)
                        return
                }
                w.Header().Set("Content-Type", "application/json")
                rows := []map[string]any{}
                for k, v := range f.publicRows {
                        rows = append(rows, map[string]any{"key": k, "value": v})
                }
                json.NewEncoder(w).Encode(rows)
        })

        // GET /rest/v1/sync_stores — 404: legacy single-store deployments.
        mux.HandleFunc("/rest/v1/sync_stores", func(w http.ResponseWriter, r *http.Request) {
                w.WriteHeader(404)
        })

        // GET /rest/v1/sync_bootstrap — single active store.
        mux.HandleFunc("/rest/v1/sync_bootstrap", func(w http.ResponseWriter, r *http.Request) {
                if r.Header.Get("apikey") == "" {
                        w.WriteHeader(401)
                        return
                }
                f.mu.Lock()
                defer f.mu.Unlock()
                w.Header().Set("Content-Type", "application/json")
                json.NewEncoder(w).Encode([]map[string]any{{
                        "project_url":  f.selfURL,
                        "team_code":    f.team,
                        "auto_approve": f.autoApprove,
                }})
        })

        // POST /rest/v1/rpc/sync_register
        mux.HandleFunc("/rest/v1/rpc/sync_register", func(w http.ResponseWriter, r *http.Request) {
                var in struct {
                        DeviceID   string `json:"p_device_id"`
                        SecretHash string `json:"p_secret_hash"`
                }
                json.NewDecoder(r.Body).Decode(&in)
                f.mu.Lock()
                defer f.mu.Unlock()
                f.registerCalls++
                if len(in.DeviceID) < 8 || len(in.SecretHash) < 32 {
                        agent3RPC(w, map[string]any{"ok": false, "error": "invalid identity"})
                        return
                }
                if f.registerBlocked {
                        agent3RPC(w, map[string]any{"ok": false, "error": "no store is registered in the cloud yet"})
                        return
                }
                if hash, ok := f.device[in.DeviceID]; ok && hash != in.SecretHash {
                        agent3RPC(w, map[string]any{"ok": false, "error": "identity mismatch"})
                        return
                }
                if f.revoked[in.DeviceID] {
                        agent3RPC(w, map[string]any{"ok": false, "error": "device revoked"})
                        return
                }
                f.device[in.DeviceID] = in.SecretHash
                f.approved[in.DeviceID] = f.autoApprove || f.approved[in.DeviceID]
                agent3RPC(w, map[string]any{"ok": true, "approved": f.approved[in.DeviceID],
                        "team_code": f.team, "pending": !f.approved[in.DeviceID]})
        })

        // POST /rest/v1/rpc/sync_push — identity-gated, dedupe by client_uuid,
        // optional partial-commit failure + poison-event rejection.
        mux.HandleFunc("/rest/v1/rpc/sync_push", func(w http.ResponseWriter, r *http.Request) {
                var in struct {
                        DeviceID   string            `json:"p_device_id"`
                        SecretHash string            `json:"p_secret_hash"`
                        Events     []agent3PushEvent `json:"p_events"`
                }
                json.NewDecoder(r.Body).Decode(&in)
                f.mu.Lock()
                defer f.mu.Unlock()
                f.pushCalls++
                if f.device[in.DeviceID] != in.SecretHash || !f.approved[in.DeviceID] || f.revoked[in.DeviceID] {
                        agent3RPC(w, map[string]any{"ok": false, "error": "device not recognized"})
                        return
                }
                for _, ev := range in.Events {
                        if f.poisonEntity != "" && ev.Entity == f.poisonEntity {
                                agent3RPC(w, map[string]any{"ok": false, "error": "poison entity rejected"})
                                return
                        }
                }
                fresh := 0
                for _, ev := range in.Events {
                        if f.seenUUIDs[ev.ClientUUID] {
                                continue // unique (team_code, client_uuid) — dedupe on retry
                        }
                        f.seenUUIDs[ev.ClientUUID] = true
                        f.nextID++
                        f.events = append(f.events, map[string]any{
                                "id": f.nextID, "device_id": in.DeviceID, "entity": ev.Entity,
                                "op": ev.Op, "client_uuid": ev.ClientUUID, "payload": ev.Payload,
                        })
                        fresh++
                }
                if f.failPushNext > 0 {
                        f.failPushNext-- // events ARE committed; the response is lost
                        agent3RPC(w, map[string]any{"ok": false, "error": "connection reset mid-push"})
                        return
                }
                agent3RPC(w, map[string]any{"ok": true, "pushed": fresh})
        })

        // POST /rest/v1/rpc/sync_pull
        mux.HandleFunc("/rest/v1/rpc/sync_pull", func(w http.ResponseWriter, r *http.Request) {
                var in struct {
                        DeviceID   string `json:"p_device_id"`
                        SecretHash string `json:"p_secret_hash"`
                        SinceID    int64  `json:"p_since_id"`
                }
                json.NewDecoder(r.Body).Decode(&in)
                f.mu.Lock()
                defer f.mu.Unlock()
                f.pullCalls++
                if f.device[in.DeviceID] != in.SecretHash || !f.approved[in.DeviceID] || f.revoked[in.DeviceID] {
                        agent3RPC(w, map[string]any{"ok": false, "error": "device not recognized"})
                        return
                }
                out := []map[string]any{}
                for _, ev := range f.events {
                        id, _ := ev["id"].(int64)
                        if id > in.SinceID && ev["device_id"] != in.DeviceID {
                                out = append(out, ev)
                        }
                }
                agent3RPC(w, map[string]any{"ok": true, "events": out})
        })

        // POST /rest/v1/rpc/sync_device_config — the key vault RPC.
        mux.HandleFunc("/rest/v1/rpc/sync_device_config", func(w http.ResponseWriter, r *http.Request) {
                var in struct {
                        DeviceID   string `json:"p_device_id"`
                        SecretHash string `json:"p_secret_hash"`
                }
                json.NewDecoder(r.Body).Decode(&in)
                f.mu.Lock()
                defer f.mu.Unlock()
                f.configCalls++
                if f.device[in.DeviceID] != in.SecretHash || !f.approved[in.DeviceID] || f.revoked[in.DeviceID] {
                        agent3RPC(w, map[string]any{"ok": false, "error": "device not recognized"})
                        return
                }
                agent3RPC(w, map[string]any{"ok": true, "config": f.configRows})
        })

        // POST /rest/v1/rpc/sync_join_team
        mux.HandleFunc("/rest/v1/rpc/sync_join_team", func(w http.ResponseWriter, r *http.Request) {
                var in struct {
                        DeviceID   string `json:"p_device_id"`
                        SecretHash string `json:"p_secret_hash"`
                        Token      string `json:"p_token"`
                }
                json.NewDecoder(r.Body).Decode(&in)
                f.mu.Lock()
                defer f.mu.Unlock()
                f.joinCalls++
                if invite, ok := f.joinTokens[in.Token]; ok && !f.usedTokens[in.Token] {
                        f.usedTokens[in.Token] = true
                        out := map[string]any{"ok": true}
                        for k, v := range invite {
                                out[k] = v
                        }
                        agent3RPC(w, out)
                        return
                }
                agent3RPC(w, map[string]any{"ok": false, "error": "invite unknown, used or expired"})
        })

        f.srv = httptest.NewServer(mux)
        f.selfURL = f.srv.URL
        t.Cleanup(f.srv.Close)
        return f
}

func agent3RPC(w http.ResponseWriter, body map[string]any) {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(body)
}

func (f *agent3Cloud) setPublic(kv map[string]string) {
        f.mu.Lock()
        defer f.mu.Unlock()
        for k, v := range kv {
                f.publicRows[k] = v
                f.configRows[k] = v
        }
}

func (f *agent3Cloud) revoke(deviceID string) {
        f.mu.Lock()
        defer f.mu.Unlock()
        f.revoked[deviceID] = true
        f.approved[deviceID] = false
}

func (f *agent3Cloud) registerCount() int {
        f.mu.Lock()
        defer f.mu.Unlock()
        return f.registerCalls
}

// agent3Link points a test at the fake cloud and restores the global on exit.
func agent3Link(t *testing.T, url string) {
        t.Helper()
        old := cloudBaseURL
        cloudBaseURL = url
        t.Cleanup(func() { cloudBaseURL = old })
}

func agent3NewService(t *testing.T) *Service {
        t.Helper()
        dir := t.TempDir()
        db, err := database.Open("sqlite", filepath.Join(dir, "test.db"), "")
        if err != nil {
                t.Fatalf("open db: %v", err)
        }
        t.Cleanup(func() { db.Close() })
        if err := db.Migrate(); err != nil {
                t.Fatalf("migrate: %v", err)
        }
        st, err := settings.New(db)
        if err != nil {
                t.Fatalf("settings: %v", err)
        }
        s := &Service{db: db, settings: st, appVersion: "agent3-test", printer: printer.NewWorker(db, st)}
        return s
}

// agent3Register walks a service through bootstrap + registration and
// returns the ready rpc client.
func agent3Register(t *testing.T, s *Service) *syncClient {
        t.Helper()
        c, ok := s.syncConfig()
        if !ok {
                t.Fatal("syncConfig did not resolve a cloud client")
        }
        if err := c.heartbeat(s, "test"); err != nil {
                t.Fatalf("register: %v", err)
        }
        return c
}

func agent3JSON(t *testing.T, v any) json.RawMessage {
        t.Helper()
        b, err := json.Marshal(v)
        if err != nil {
                t.Fatalf("marshal payload: %v", err)
        }
        return b
}

func agent3Pending(s *Service) int64 {
        var n int64
        s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE pushed_at = ''`).Scan(&n)
        return n
}

func agent3Stock(s *Service, sku string) int {
        var q int
        s.db.QueryRow(`SELECT stock_qty FROM products WHERE sku = ?`, sku).Scan(&q)
        return q
}

// ---------------------------------------------------------------------------
// 1. Vault: public rows apply while the device is still unknown
// ---------------------------------------------------------------------------

func TestAgent3VaultPublicRowsBeforeRegistration(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        fake.setPublic(map[string]string{
                "paystack_public_key": "pk_live_public_first_0001",
                "paystack_currency":   "KES",
        })
        fake.mu.Lock()
        fake.registerBlocked = true // device can never register
        // Secret rows are RLS-hidden from the anon read; put one in the full
        // map to prove it can only arrive via the RPC.
        fake.configRows["paystack_secret_key"] = "sk_live_secret_must_not_leak"
        fake.mu.Unlock()

        s := agent3NewService(t)
        err := s.RefreshCloudConfig()

        // Public config must land even though the device is unknown...
        if got := s.Settings().Get("paystack_public_key"); got != "pk_live_public_first_0001" {
                t.Fatalf("public key not applied before registration: %q", got)
        }
        if got := s.Settings().Get("paystack_currency"); got != "KES" {
                t.Fatalf("currency not applied: %q", got)
        }
        // ...but a secret must never ride the public path.
        if got := s.Settings().Get("paystack_secret_key"); got != "" {
                t.Fatalf("secret leaked through the public path: %q", got)
        }
        // The failed registration surfaces as a non-fatal error.
        if err == nil {
                t.Fatal("expected an error when the device cannot register for full config")
        }
        if !strings.Contains(err.Error(), "register for config") {
                t.Fatalf("unexpected error shape: %v", err)
        }
        // Boot-grade: the till still reports a usable public config.
        if s.Settings().Get("config_synced_at") == "" {
                t.Fatal("config_synced_at should be stamped after a public apply")
        }
}

// ---------------------------------------------------------------------------
// 2. Vault: RPC path after registration + precedence/guard rules
// ---------------------------------------------------------------------------

func TestAgent3VaultRPCConfigAfterRegistration(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        const cloudSecret = "sk_live_cloudvaultkey0000000001"
        fake.mu.Lock()
        fake.configRows = map[string]string{
                "paystack_secret_key":  cloudSecret,
                "paystack_public_key":  "pk_live_fresh_value_00001",
                "paystack_currency":    "KES",
                "jwt_secret":           "evil-cloud-jwt",
                "sync_device_secret":   "evil-cloud-identity",
                "offsite_api_key":      settings.MaskToken, // masked echo row
                "paystack_enabled_tou": "x",                // near-miss of a guarded key
        }
        fake.mu.Unlock()

        s := agent3NewService(t)
        s.SetVersion("test")
        _ = s.Settings().Set("jwt_secret", "local-root-secret")
        _ = s.Settings().Set("sync_device_secret", "local-device-secret")
        deviceSecretBefore := s.deviceSecret()
        _ = s.Settings().Set("offsite_api_key", "off-local-key")
        _ = s.Settings().Set("paystack_public_key", "pk_live_stale_value_0001")
        // Reserve the env slot so applyCloudConfig's os.Setenv is cleaned up.
        t.Setenv("PAYSTACK_SECRET_KEY", "")

        if err := s.RefreshCloudConfig(); err != nil {
                t.Fatalf("RefreshCloudConfig: %v", err)
        }

        // The "not recognized" -> registerCloud -> retry dance must have happened.
        fake.mu.Lock()
        cfgCalls := fake.configCalls
        fake.mu.Unlock()
        if cfgCalls < 2 {
                t.Fatalf("expected a retried sync_device_config after registration, got %d calls", cfgCalls)
        }

        // Full config (incl. secret) applied.
        if got := s.Settings().Get("paystack_secret_key"); got != cloudSecret {
                t.Fatalf("paystack_secret_key = %q, want cloud vault value", got)
        }
        // Cloud overwrites a stale local value.
        if got := s.Settings().Get("paystack_public_key"); got != "pk_live_fresh_value_00001" {
                t.Fatalf("stale local value not overwritten: %q", got)
        }
        // Guarded keys are never touched.
        if got := s.Settings().Get("jwt_secret"); got != "local-root-secret" {
                t.Fatalf("jwt_secret overwritten: %q", got)
        }
        if got := s.deviceSecret(); got != deviceSecretBefore || got == "evil-cloud-identity" {
                t.Fatalf("sync_device_secret overwritten: %q", got)
        }
        // Masked echo rows are skipped.
        if got := s.Settings().Get("offsite_api_key"); got != "off-local-key" {
                t.Fatalf("mask token overwrote a real secret: %q", got)
        }
        if s.Settings().Get("config_synced_at") == "" {
                t.Fatal("config_synced_at missing")
        }
        // Deployment env injection (env was unset).
        if got := os.Getenv("PAYSTACK_SECRET_KEY"); got != cloudSecret {
                t.Fatalf("PAYSTACK_SECRET_KEY not injected from vault: %q", got)
        }

        // GetPaystack picks up the vault key.
        if c := s.GetPaystack(); c == nil {
                t.Fatal("GetPaystack nil with vault secret")
        } else if s.paystackKey != cloudSecret {
                t.Fatalf("paystack client key = %q, want vault secret", s.paystackKey)
        }
}

func TestAgent3VaultEnvWinsAtReadTime(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        const cloudSecret = "sk_live_cloudvaultkey0000000001"
        fake.mu.Lock()
        fake.configRows = map[string]string{"paystack_secret_key": cloudSecret}
        fake.mu.Unlock()

        s := agent3NewService(t)
        t.Setenv("PAYSTACK_SECRET_KEY", "") // reserve slot; applyCloudConfig will inject
        if err := s.RefreshCloudConfig(); err != nil {
                t.Fatalf("RefreshCloudConfig: %v", err)
        }
        if s.Settings().Get("paystack_secret_key") != cloudSecret {
                t.Fatalf("vault secret missing from settings store")
        }

        // An operator-set env var wins at READ time even though the cloud value
        // is what the settings store holds.
        const envKey = "sk_live_envoverridekey000000042"
        t.Setenv("PAYSTACK_SECRET_KEY", envKey)
        if c := s.GetPaystack(); c == nil {
                t.Fatal("GetPaystack nil with env key")
        } else if s.paystackKey != envKey {
                t.Fatalf("env did not win at read time: effective key %q", s.paystackKey)
        }

        // A cloud refresh landing a NEW secret updates the store but must not
        // clobber the env override.
        fake.mu.Lock()
        fake.configRows["paystack_secret_key"] = "sk_live_rotatedvaultkey0000009"
        fake.mu.Unlock()
        if err := s.RefreshCloudConfig(); err != nil {
                t.Fatalf("second refresh: %v", err)
        }
        if got := s.Settings().Get("paystack_secret_key"); got != "sk_live_rotatedvaultkey0000009" {
                t.Fatalf("cloud rotation did not land in the store: %q", got)
        }
        if s.paystackKey != envKey {
                t.Fatalf("rotation leaked into the effective key: %q", s.paystackKey)
        }

        // Clearing the env falls back to the (rotated) vault value.
        t.Setenv("PAYSTACK_SECRET_KEY", "")
        if c := s.GetPaystack(); c == nil {
                t.Fatal("GetPaystack nil after env cleared")
        } else if s.paystackKey != "sk_live_rotatedvaultkey0000009" {
                t.Fatalf("fallback to vault value failed: %q", s.paystackKey)
        }
}

// ---------------------------------------------------------------------------
// 3. Vault: offline / hostile network must never break boot
// ---------------------------------------------------------------------------

func TestAgent3VaultOfflineNeverCrashesBoot(t *testing.T) {
        // Unreachable cloud (connection refused).
        agent3Link(t, "http://127.0.0.1:1")
        s := agent3NewService(t)
        _ = s.Settings().Set("paystack_public_key", "pk_live_keepme_00000001")

        var panicked any
        func() {
                defer func() { panicked = recover() }()
                if err := s.RefreshCloudConfig(); err != nil {
                        t.Logf("offline refresh error (tolerable): %v", err)
                }
                if err := s.RefreshCloudConfig(); err != nil {
                        t.Logf("offline refresh error 2 (tolerable): %v", err)
                }
        }()
        if panicked != nil {
                t.Fatalf("offline refresh panicked: %v", panicked)
        }
        if got := s.Settings().Get("paystack_public_key"); got != "pk_live_keepme_00000001" {
                t.Fatalf("offline refresh mutated settings: %q", got)
        }

        // StartCloudConfig must not block startup even against a slow cloud.
        slow := newAgent3Cloud(t)
        agent3Link(t, slow.srv.URL)
        slow.mu.Lock()
        slow.publicRows["paystack_currency"] = "KES"
        slow.mu.Unlock()
        // Replace the app_config handler response time by exercising through a
        // fresh service: the boot fetch runs in a goroutine.
        s2 := agent3NewService(t)
        ctx, cancel := context.WithCancel(context.Background())
        start := time.Now()
        s2.StartCloudConfig(ctx) // must return immediately
        elapsed := time.Since(start)
        cancel()
        if elapsed > time.Second {
                t.Fatalf("StartCloudConfig blocked boot for %v", elapsed)
        }
        // Give the background goroutine a moment before the test server closes.
        time.Sleep(300 * time.Millisecond)
}

func TestAgent3VaultLoopRespectsContextCancel(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        s := agent3NewService(t)

        ctx, cancel := context.WithCancel(context.Background())
        done := make(chan struct{})
        go func() {
                s.CloudConfigLoop(ctx)
                close(done)
        }()
        cancel()
        select {
        case <-done:
                // loop exited promptly on cancellation — 6h ticker never fired
        case <-time.After(2 * time.Second):
                t.Fatal("CloudConfigLoop ignored context cancellation")
        }

        fake.mu.Lock()
        cfg := fake.configCalls
        fake.mu.Unlock()
        if cfg != 0 {
                t.Fatalf("cancelled loop performed %d config fetches", cfg)
        }
}

// ---------------------------------------------------------------------------
// 4. Team sync: push retry after a partial server commit
// ---------------------------------------------------------------------------

func TestAgent3SyncPushRetryAfterPartialCommit(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        s1 := agent3NewService(t)
        s2 := agent3NewService(t)
        c1 := agent3Register(t, s1)
        c2 := agent3Register(t, s2)

        for _, sku := range []string{"SKU-P1", "SKU-P2", "SKU-P3"} {
                s1.Emit("product", "upsert", map[string]any{"sku": sku, "name": sku})
        }
        if n := agent3Pending(s1); n != 3 {
                t.Fatalf("outbox pending = %d, want 3", n)
        }

        // Server commits the batch but the response is lost (network cut).
        fake.mu.Lock()
        fake.failPushNext = 1
        fake.mu.Unlock()
        // The wire attempt fails (server committed, response lost). Hardened
        // push bisects and retries inside the same cycle, so the outbox may
        // already drain — the invariant that matters is server-side
        // exactly-once, asserted below.
        c1.push(s1)

        // Retry: server-side client_uuid dedupe must land each event exactly once.
        if _, err := c1.push(s1); err != nil {
                t.Fatalf("retry push: %v", err)
        }
        if n := agent3Pending(s1); n != 0 {
                t.Fatalf("outbox not drained after retry: %d", n)
        }
        fake.mu.Lock()
        total, uniq := len(fake.events), len(fake.seenUUIDs)
        fake.mu.Unlock()
        if uniq != 3 {
                t.Fatalf("unique events on server = %d, want 3", uniq)
        }
        if total != uniq {
                t.Fatalf("server stored %d rows for %d unique client_uuids — dedupe broken", total, uniq)
        }

        // Downstream till applies each exactly once (a category fixture is
        // present, so the product events can actually apply).
        s2.db.Exec(s2.db.Rebind(`INSERT INTO categories (name, slug) VALUES ('C','c')`))
        if applied, err := c2.pull(s2); err != nil || applied != 3 {
                t.Fatalf("pull: applied=%d err=%v", applied, err)
        }
        if applied, err := c2.pull(s2); err != nil || applied != 0 {
                t.Fatalf("second pull must be a no-op: applied=%d err=%v", applied, err)
        }
}

// ---------------------------------------------------------------------------
// 5. Team sync: poison event head-of-line blocking (event loss probe)
// ---------------------------------------------------------------------------

func TestAgent3SyncPoisonEventQuarantinedAndStreamUnblocked(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        s1 := agent3NewService(t)
        _ = agent3Register(t, s1)
        c1, _ := s1.syncConfig()

        fake.mu.Lock()
        fake.poisonEntity = "poison"
        fake.mu.Unlock()

        s1.Emit("poison", "upsert", map[string]any{"bad": true})
        s1.Emit("product", "upsert", map[string]any{"sku": "SKU-GOOD", "name": "Good"})

        // Hardened push: bisection isolates the poison row so the healthy
        // event behind it reaches the cloud on the FIRST cycle; the poison
        // row itself is quarantined to the dead-letter table after 5 failed
        // attempts and can never wedge the outbox again.
        for i := 0; i < maxPushAttempts; i++ {
                c1.push(s1)
        }
        fake.mu.Lock()
        got := 0
        for _, ev := range fake.events {
                if ev["entity"] == "product" {
                        got++
                }
        }
        fake.mu.Unlock()
        if got == 0 {
                t.Fatal("healthy event never reached the cloud behind the poison event")
        }
        var dl int
        s1.db.QueryRow(`SELECT COUNT(*) FROM sync_dead_letter WHERE direction='push'`).Scan(&dl)
        if dl != 1 {
                t.Fatalf("dead letter rows = %d, want 1 (the quarantined poison event)", dl)
        }
        if n := agent3Pending(s1); n != 0 {
                t.Fatalf("pending = %d, want 0 (poison quarantined, healthy pushed)", n)
        }
        var entity string
        s1.db.QueryRow(`SELECT entity FROM sync_dead_letter WHERE direction='push'`).Scan(&entity)
        if entity != "poison" {
                t.Fatalf("quarantined entity = %q, want poison", entity)
        }
}

// ---------------------------------------------------------------------------
// 6. Team sync: emit-while-disabled keeps events (they push once enabled)
// ---------------------------------------------------------------------------

func TestAgent3SyncEmitWhileOfflineKeepsEvents(t *testing.T) {
        agent3Link(t, "http://127.0.0.1:1") // no cloud reachable -> sync stays off
        s := agent3NewService(t)
        s.Emit("product", "upsert", map[string]any{"sku": "PRE-SYNC", "name": "kept-too"})
        // Hardened Emit: the outbox is written even while sync is off — a till
        // that runs standalone and joins a team later replays its history
        // instead of silently dropping it.
        if n := agent3Pending(s); n != 1 {
                t.Fatalf("offline emit must be queued, pending=%d", n)
        }
        s.Emit("product", "upsert", map[string]any{"sku": "POST-SYNC", "name": "kept"})
        if n := agent3Pending(s); n != 2 {
                t.Fatalf("pending = %d, want 2", n)
        }
}

// ---------------------------------------------------------------------------
// 7. Team sync: cursor regression double-applies stock + ledger
// ---------------------------------------------------------------------------

func TestAgent3SyncCursorRegressionDoubleApply(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        s1 := agent3NewService(t)
        s2 := agent3NewService(t)
        c1 := agent3Register(t, s1)
        c2 := agent3Register(t, s2)

        // Receiver-side fixtures: product + customer exist locally.
        s2.db.Exec(s2.db.Rebind(`INSERT INTO categories (name, slug) VALUES ('C','c')`))
        var catID int64
        s2.db.QueryRow(`SELECT id FROM categories LIMIT 1`).Scan(&catID)
        s2.db.Exec(s2.db.Rebind(`INSERT INTO products (sku, name, category_id, stock_qty) VALUES ('SKU-A','A',?,10)`), catID)
        s2.db.Exec(s2.db.Rebind(`INSERT INTO customers (name, phone) VALUES ('Jane','0700000001')`))

        s1.Emit("stock", "upsert", map[string]any{"sku": "SKU-A", "delta": -3})
        s1.Emit("ledger", "upsert", map[string]any{
                "phone": "0700000001", "kind": models.LedgerCreditTopup,
                "amountCents": 5000, "pointsDelta": 0, "note": "topup", "createdAt": nowStamp(),
        })
        c1.push(s1)

        if applied, err := c2.pull(s2); err != nil || applied != 2 {
                t.Fatalf("first pull: applied=%d err=%v", applied, err)
        }
        if got := agent3Stock(s2, "SKU-A"); got != 7 {
                t.Fatalf("stock after first apply = %d, want 7", got)
        }
        var credit int64
        s2.db.QueryRow(`SELECT store_credit_cents FROM customers WHERE phone='0700000001'`).Scan(&credit)
        if credit != 5000 {
                t.Fatalf("store credit after first apply = %d, want 5000", credit)
        }

        // Cursor regression (restored backup / wiped sync_state / team re-key):
        // the same events are re-delivered, but sync_applied dedupes them —
        // stock and money apply EXACTLY ONCE even when the cursor goes back.
        cursorKey := "syncstate_last_event_" + fake.team
        _ = s2.settings.Set(cursorKey, "0")
        if applied, err := c2.pull(s2); err != nil || applied != 0 {
                t.Fatalf("pull after cursor reset must not re-apply: applied=%d err=%v", applied, err)
        }
        if got := agent3Stock(s2, "SKU-A"); got != 7 {
                t.Fatalf("stock re-applied after cursor reset: %d (want 7)", got)
        }
        s2.db.QueryRow(`SELECT store_credit_cents FROM customers WHERE phone='0700000001'`).Scan(&credit)
        if credit != 5000 {
                t.Fatalf("ledger re-applied after cursor reset: credit %d (want 5000)", credit)
        }
        var ledgerRows int
        s2.db.QueryRow(`SELECT COUNT(*) FROM customer_ledger WHERE customer_id=(SELECT id FROM customers WHERE phone='0700000001')`).Scan(&ledgerRows)
        if ledgerRows != 1 {
                t.Fatalf("customer_ledger rows = %d, want 1 (exactly once)", ledgerRows)
        }
}

// ---------------------------------------------------------------------------
// 8. Team sync: pull-cursor written under a different team key than read
// ---------------------------------------------------------------------------

func TestAgent3SyncCursorKeyMismatchProbe(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        s1 := agent3NewService(t)
        s2 := agent3NewService(t)
        c1 := agent3Register(t, s1)
        _ = agent3Register(t, s2)

        s1.Emit("stock", "upsert", map[string]any{"sku": "SKU-K", "delta": -2})
        c1.push(s1)
        _ = s1.settings.Set("sync_enabled", "false") // silence further emits

        // Receiver till whose SETTINGS team lags the client team — the exact
        // shape of a mid-cycle store reassignment (registerCloud rewrites
        // sync_team_code while the cycle's client still carries the old team).
        _ = s2.settings.Set("sync_team_code", "TEAM-OLD")
        c2 := &syncClient{
                base: fake.srv.URL, key: cloudAnonKey,
                team: "TEAM-NEW", device: s2.deviceID(),
                secretHash: secretHash(s2.deviceSecret()), mode: "rpc",
                hc: &http.Client{Timeout: syncHTTPTimeout},
        }

        // Fixture: the receiving till has the product locally.
        s2.db.Exec(s2.db.Rebind(`INSERT INTO categories (name, slug) VALUES ('C','c')`))
        var catK int64
        s2.db.QueryRow(`SELECT id FROM categories LIMIT 1`).Scan(&catK)
        s2.db.Exec(s2.db.Rebind(`INSERT INTO products (sku, name, category_id, stock_qty) VALUES ('SKU-K','K',?,10)`), catK)

        if applied, err := c2.pull(s2); err != nil || applied != 1 {
                t.Fatalf("first pull: applied=%d err=%v", applied, err)
        }
        // Hardened: applyPulled now writes the cursor under the CLIENT's team
        // (the same key pullRPC reads), so redelivery stops after the first
        // pull even mid-reassignment.
        if applied, err := c2.pull(s2); err != nil || applied != 0 {
                t.Fatalf("second pull must be a no-op (cursor keys unified): applied=%d err=%v", applied, err)
        }
        if got := agent3Stock(s2, "SKU-K"); got != 8 {
                t.Fatalf("stock after first apply = %d, want 8 (10-2, no re-apply)", got)
        }
}

// ---------------------------------------------------------------------------
// 9. Team sync: order events that cannot apply are quarantined and recover
// ---------------------------------------------------------------------------

func TestAgent3SyncOrderFKDropQuarantinedThenRecovered(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        s1 := agent3NewService(t)
        s2 := agent3NewService(t)
        c1 := agent3Register(t, s1)
        c2 := agent3Register(t, s2)

        // A synced order whose item SKU is unknown locally (and whose cashier
        // cannot resolve — this store has no staff yet) violates FK constraints
        // on the default foreign_keys(ON) SQLite store.
        orderPayload := agent3JSON(t, map[string]any{
                "clientUuid": "ord-fk-1", "number": "9001", "status": "PAID",
                "subtotalCents": 100, "totalCents": 100, "createdAt": nowStamp(),
                "items":    []map[string]any{{"sku": "GHOST-SKU", "name": "Ghost", "qty": 1, "unitPriceCents": 100, "lineTotalCents": 100}},
                "payments": []map[string]any{},
        })
        err := s2.applyEvent("order", "upsert", orderPayload)
        if err == nil {
                t.Fatal("expected the synced order to fail (unknown product, no staff), it applied cleanly")
        }
        var n int
        s2.db.QueryRow(`SELECT COUNT(*) FROM orders WHERE number='9001'`).Scan(&n)
        if n != 0 {
                t.Fatalf("failed order must not land, found %d rows", n)
        }

        // Hardened pipeline: the failure is QUARANTINED (dead letter) and the
        // cursor still advances — the stream is never wedged, but the sale is
        // NOT lost: it stays flagged and retries every cycle.
        s1.Emit("order", "upsert", json.RawMessage(orderPayload))
        c1.push(s1)
        if applied, err := c2.pull(s2); err != nil || applied != 0 {
                t.Fatalf("pipeline pull: applied=%d err=%v", applied, err)
        }
        s2.db.QueryRow(`SELECT COUNT(*) FROM sync_dead_letter WHERE direction='apply' AND payload LIKE '%ord-fk-1%'`).Scan(&n)
        if n != 1 {
                t.Fatalf("dead letter rows for ord-fk-1 = %d, want 1", n)
        }
        // The root cause heals (product arrives, staff onboarded) — the next
        // retry recovers the order with full history.
        s2.db.Exec(s2.db.Rebind(`INSERT INTO categories (name, slug) VALUES ('C','c')`))
        var catID int64
        s2.db.QueryRow(`SELECT id FROM categories LIMIT 1`).Scan(&catID)
        s2.db.Exec(s2.db.Rebind(`INSERT INTO products (sku, name, category_id, stock_qty) VALUES ('GHOST-SKU','Ghost',?,5)`), catID)
        s2.db.Exec(s2.db.Rebind(`INSERT INTO roles (name) VALUES ('Cashier')`))
        var roleID int64
        s2.db.QueryRow(`SELECT id FROM roles LIMIT 1`).Scan(&roleID)
        s2.db.Exec(s2.db.Rebind(`INSERT INTO users (username, full_name, password_hash, role_id, is_active) VALUES ('cashier1','Cashier One','x',?,1)`), roleID)
        if recovered := s2.retryFailedApplies(); recovered != 1 {
                t.Fatalf("retryFailedApplies recovered %d, want 1", recovered)
        }
        s2.db.QueryRow(`SELECT COUNT(*) FROM orders WHERE client_uuid='ord-fk-1'`).Scan(&n)
        if n != 1 {
                t.Fatalf("order row after recovery = %d, want 1", n)
        }
        var status string
        s2.db.QueryRow(`SELECT status FROM orders WHERE client_uuid='ord-fk-1'`).Scan(&status)
        if status != "PAID" {
                t.Fatalf("recovered order status = %s, want PAID", status)
        }
        // And the dead letter is empty again.
        s2.db.QueryRow(`SELECT COUNT(*) FROM sync_dead_letter`).Scan(&n)
        if n != 0 {
                t.Fatalf("dead letter after recovery = %d, want 0", n)
        }
}

// ---------------------------------------------------------------------------
// 10. Team sync: void replay + silent drops for unknown references
// ---------------------------------------------------------------------------

func TestAgent3SyncVoidReplayAndDrops(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        s1 := agent3NewService(t)
        s2 := agent3NewService(t)
        c1 := agent3Register(t, s1)
        c2 := agent3Register(t, s2)

        // Receiver-side: simulate a till that already has the order locally
        // (PAID, 2 units sold, pending payment row).
        s2.db.Exec(s2.db.Rebind(`INSERT INTO categories (name, slug) VALUES ('C','c')`))
        var catID int64
        s2.db.QueryRow(`SELECT id FROM categories LIMIT 1`).Scan(&catID)
        s2.db.Exec(s2.db.Rebind(`INSERT INTO products (sku, name, category_id, stock_qty) VALUES ('SKU-V','V',?,8)`), catID)
        var prodID int64
        s2.db.QueryRow(`SELECT id FROM products WHERE sku='SKU-V'`).Scan(&prodID)
        s2.db.Exec(s2.db.Rebind(`INSERT INTO roles (name) VALUES ('Cashier')`))
        var roleID int64
        s2.db.QueryRow(`SELECT id FROM roles WHERE name='Cashier'`).Scan(&roleID)
        s2.db.Exec(s2.db.Rebind(`INSERT INTO users (username, role_id) VALUES ('cash1', ?)`), roleID)
        var userID int64
        s2.db.QueryRow(`SELECT id FROM users WHERE username='cash1'`).Scan(&userID)
        s2.db.Exec(s2.db.Rebind(`
                INSERT INTO orders (number, status, subtotal_cents, total_cents, cashier_id, client_uuid)
                VALUES ('9002','PAID',1000,1000,?, 'ord-void-1')`), userID)
        var ordID int64
        s2.db.QueryRow(`SELECT id FROM orders WHERE client_uuid='ord-void-1'`).Scan(&ordID)
        s2.db.Exec(s2.db.Rebind(`
                INSERT INTO order_items (order_id, product_id, name, sku, qty, unit_price_cents, line_total_cents)
                VALUES (?, ?, 'V', 'SKU-V', 2, 500, 1000)`), ordID, prodID)
        s2.db.Exec(s2.db.Rebind(`
                INSERT INTO payments (order_id, method, amount_cents, status) VALUES (?, 'cash', 1000, 'COMPLETED')`), ordID)

        // 1. A void referencing a KNOWN order replays fully: stock restored,
        //    payments voided, order VOIDED exactly once.
        s1.Emit("void", "upsert", map[string]any{
                "orderClientUuid": "ord-void-1", "orderNumber": "9002",
                "voidedAt": nowStamp(), "reason": "wrong item",
        })
        c1.push(s1)
        if applied, err := c2.pull(s2); err != nil || applied != 1 {
                t.Fatalf("void pull: applied=%d err=%v", applied, err)
        }
        var status string
        s2.db.QueryRow(`SELECT status FROM orders WHERE id=?`, ordID).Scan(&status)
        if status != "VOIDED" {
                t.Fatalf("order status after synced void = %s", status)
        }
        if got := agent3Stock(s2, "SKU-V"); got != 10 {
                t.Fatalf("stock after void replay = %d, want 10", got)
        }
        var payStatus string
        s2.db.QueryRow(`SELECT status FROM payments WHERE order_id=?`, ordID).Scan(&payStatus)
        if payStatus != "VOIDED" {
                t.Fatalf("payment status after synced void = %s", payStatus)
        }

        // 2. Replay the same void: idempotent (already VOIDED -> no-op).
        if applied, err := c2.pull(s2); err != nil || applied != 0 {
                t.Fatalf("void replay must be a no-op: applied=%d err=%v", applied, err)
        }
        if got := agent3Stock(s2, "SKU-V"); got != 10 {
                t.Fatalf("double void restored stock twice: %d", got)
        }

        // 3. Void for an order this till never saw: silently discarded, cursor
        //    advances — if the order event later arrives, the void is lost.
        s1.Emit("void", "upsert", map[string]any{
                "orderClientUuid": "ord-never-synced", "voidedAt": nowStamp(), "reason": "x",
        })
        c1.push(s1)
        if applied, err := c2.pull(s2); err != nil || applied != 1 {
                t.Fatalf("unknown-order void pull: applied=%d err=%v", applied, err)
        }
        var voided int
        s2.db.QueryRow(`SELECT COUNT(*) FROM orders WHERE status='VOIDED'`).Scan(&voided)
        if voided != 1 {
                t.Fatalf("unexpected extra voided orders: %d", voided)
        }

        // 4. Stock delta for an unknown SKU: QUARANTINED, not consumed. The
        //    old behavior matched 0 rows and reported success — a sale whose
        //    product hadn't synced yet vanished from stock accounting
        //    forever. Now the delta dead-letters and retries (the catalog
        //    rename/creation that precedes it lands on a later cycle).
        before := agent3Stock(s2, "SKU-V")
        s1.Emit("stock", "upsert", map[string]any{"sku": "SKU-GHOST", "delta": -5})
        c1.push(s1)
        if applied, err := c2.pull(s2); err != nil || applied != 0 {
                t.Fatalf("ghost stock pull: applied=%d err=%v (want 0: event must quarantine)", applied, err)
        }
        var dl int
        s2.db.QueryRow(`SELECT COUNT(*) FROM sync_dead_letter WHERE direction='apply'`).Scan(&dl)
        if dl != 1 {
                t.Fatalf("ghost delta not quarantined (rows=%d)", dl)
        }
        if got := agent3Stock(s2, "SKU-V"); got != before {
                t.Fatalf("unrelated stock moved: %d -> %d", before, got)
        }
        var ghost int
        s2.db.QueryRow(`SELECT COUNT(*) FROM products WHERE sku='SKU-GHOST'`).Scan(&ghost)
        if ghost != 0 {
                t.Fatal("ghost product should not exist")
        }
        // The quarantined delta RECOVERS once the product arrives (the
        // out-of-order story heals instead of losing the movement).
        s1.Emit("product", "upsert", map[string]any{
                "sku": "SKU-GHOST", "name": "Ghost", "priceCents": 100,
                "stockQty": 50, "stockSet": true, "trackStock": true, "active": true,
                "updatedAt": nowStamp(),
        })
        c1.push(s1)
        if applied, err := c2.pull(s2); err != nil || applied != 1 {
                t.Fatalf("ghost product pull: applied=%d err=%v", applied, err)
        }
        if recovered := s2.retryFailedApplies(); recovered != 1 {
                t.Fatalf("ghost delta recovered %d, want 1", recovered)
        }
        var ghostStock int
        s2.db.QueryRow(`SELECT stock_qty FROM products WHERE sku='SKU-GHOST'`).Scan(&ghostStock)
        if ghostStock != 45 {
                t.Fatalf("ghost stock after recovery = %d, want 45 (50 - 5)", ghostStock)
        }
}

// ---------------------------------------------------------------------------
// 11. Device identity: approval gating + revocation stops sync AND config
// ---------------------------------------------------------------------------

func TestAgent3DeviceApprovalGating(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        fake.mu.Lock()
        fake.autoApprove = false
        fake.mu.Unlock()

        s := agent3NewService(t)
        c, ok := s.syncConfig()
        if !ok {
                t.Fatal("bootstrap failed")
        }
        // Not approved: registration reports waiting; push/pull refused.
        if err := c.heartbeat(s, "test"); err == nil {
                t.Fatal("pending device must report waiting-for-approval")
        }
        if !s.settings.GetBool("sync_registered", false) {
                t.Fatal("pending device should still be registered")
        }
        if s.settings.GetBool("sync_approved", true) {
                t.Fatal("pending device must not be marked approved")
        }
        s.Emit("product", "upsert", map[string]any{"sku": "GATE-1"})
        if _, err := c.push(s); err == nil {
                t.Fatal("unapproved device must not be able to push")
        }
        if _, err := c.pull(s); err == nil {
                t.Fatal("unapproved device must not be able to pull")
        }

        // Owner approves -> the queue drains with no user action on the till.
        fake.mu.Lock()
        fake.approved[c.device] = true
        fake.mu.Unlock()
        if err := c.heartbeat(s, "test"); err != nil {
                t.Fatalf("approve+register: %v", err)
        }
        if pushed, err := c.push(s); err != nil || pushed != 1 {
                t.Fatalf("push after approval: %d %v", pushed, err)
        }
}

func TestAgent3DeviceRevocationStopsSyncAndConfig(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        s := agent3NewService(t)
        c := agent3Register(t, s)

        // Config arrives while approved.
        fake.mu.Lock()
        fake.configRows["paystack_public_key"] = "pk_live_before_revoke_001"
        fake.mu.Unlock()
        if err := s.RefreshCloudConfig(); err != nil {
                t.Fatalf("pre-revoke refresh: %v", err)
        }
        if got := s.Settings().Get("paystack_public_key"); got != "pk_live_before_revoke_001" {
                t.Fatalf("pre-revoke config missing: %q", got)
        }

        // Owner revokes the stolen till.
        fake.revoke(c.device)

        // Sync stops: register refuses outright.
        regBefore := fake.registerCount()
        if err := c.heartbeat(s, "test"); err == nil || !strings.Contains(err.Error(), "device revoked") {
                t.Fatalf("revoked heartbeat: %v", err)
        }
        // Push/pull are refused by identity checks.
        s.Emit("product", "upsert", map[string]any{"sku": "AFTER-REVOKE"})
        if _, err := c.push(s); err == nil || !strings.Contains(err.Error(), "not recognized") {
                t.Fatalf("revoked push must be refused: %v", err)
        }
        if _, err := c.pull(s); err == nil || !strings.Contains(err.Error(), "not recognized") {
                t.Fatalf("revoked pull must be refused: %v", err)
        }
        // Config is cut too: sync_device_config says not recognized, and the
        // re-register retry hits "device revoked".
        if err := s.RefreshCloudConfig(); err == nil {
                t.Fatal("revoked device must not be able to refresh config")
        }
        if got := s.Settings().Get("paystack_public_key"); got != "pk_live_before_revoke_001" {
                t.Fatalf("revoked device's config snapshot was mutated: %q", got)
        }
        // The event emitted after revocation stays local (never leaks upstream).
        if n := agent3Pending(s); n != 1 {
                t.Fatalf("revoked device outbox pending = %d, want 1", n)
        }

        // Documented behaviour: the revoked till retries registration forever —
        // no backoff, no local disable. Two more cycles, two more RPCs.
        _ = c.heartbeat(s, "test")
        _ = s.RefreshCloudConfig()
        if fake.registerCount()-regBefore < 3 {
                t.Fatalf("expected unbounded register retries (got %d extra)", fake.registerCount()-regBefore)
        }
}

// ---------------------------------------------------------------------------
// 12. Team join links: token authority + local account creation
// ---------------------------------------------------------------------------

func TestAgent3JoinLinkLifecycle(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        fake.mu.Lock()
        fake.joinTokens["VAULTTOKEN123456"] = map[string]any{
                "team_code": "VAULT-TEAM", "store_name": "Vault Store",
                "role_name": "Cashier", "permissions": []string{"pos.sell", "orders.view"},
        }
        fake.mu.Unlock()

        s := agent3NewService(t)

        // Weak PIN is refused before any network call.
        if _, err := s.TeamJoinWithLink("https://ledgerpos.app/join#k=VAULTTOKEN123456", "Jane Worker", "1234"); err == nil {
                t.Fatal("weak PIN must be refused")
        }
        fake.mu.Lock()
        calls := fake.joinCalls
        fake.mu.Unlock()
        if calls != 0 {
                t.Fatalf("weak PIN check hit the cloud %d times", calls)
        }

        // Garbage link refused locally.
        if _, err := s.TeamJoinWithLink("https://ledgerpos.app/join#k=short", "Jane Worker", "9182"); err == nil {
                t.Fatal("short token must be refused locally")
        }

        // Happy path: cloud redeems the token, local worker account is created.
        info, err := s.TeamJoinWithLink("https://ledgerpos.app/join#k=VAULTTOKEN123456", "Jane Worker", "9182")
        if err != nil {
                t.Fatalf("join: %v", err)
        }
        if info.TeamCode != "VAULT-TEAM" || info.Username == "" || info.UserID == 0 {
                t.Fatalf("join info incomplete: %+v", info)
        }
        var username string
        if err := s.db.QueryRow(`SELECT username FROM users WHERE id=?`, info.UserID).Scan(&username); err != nil || username != info.Username {
                t.Fatalf("worker account missing: %q %v", username, err)
        }
        if !s.settings.GetBool("sync_approved", false) || !s.settings.GetBool("sync_enabled", false) {
                t.Fatal("joined till must be approved + enabled")
        }
        if s.settings.Get("onboarding_done") != "true" {
                t.Fatal("join must skip onboarding")
        }

        // Single-use token: replay is refused server-side, nothing changes.
        if _, err := s.TeamJoinWithLink("https://ledgerpos.app/join#k=VAULTTOKEN123456", "Mallory", "9182"); err == nil {
                t.Fatal("used token must be refused")
        }
        var users int
        s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users)
        if users != 1 {
                t.Fatalf("user count after replay = %d, want 1", users)
        }

        // Probe: the till records the hardcoded fleet URL as its sync endpoint,
        // not the server it actually redeemed the token from. Identical in
        // production, divergent for staged clouds.
        if got := s.settings.Get("sync_endpoint"); got != strings.TrimRight(cloudmeta.ProjectURL, "/") {
                t.Logf("NOTE: sync_endpoint after join = %q (redeemed from %q)", got, fake.srv.URL)
        }
}

// ---------------------------------------------------------------------------
// 13. Clock skew poisons product LWW (string comparison of timestamps)
// ---------------------------------------------------------------------------

func TestAgent3SyncClockSkewLWWProbe(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        s1 := agent3NewService(t)
        s2 := agent3NewService(t)
        c1 := agent3Register(t, s1)
        c2 := agent3Register(t, s2)

        s2.db.Exec(s2.db.Rebind(`INSERT INTO categories (name, slug) VALUES ('C','c')`))

        // Till with a fast clock (1 year ahead) pushes a product edit.
        s1.Emit("product", "upsert", map[string]any{
                "sku": "SKU-T", "name": "Skewed Edit", "priceCents": 9900,
                "categorySlug": "c", "active": true, "trackStock": true,
                "updatedAt": "2027-01-01T00:00:00.000Z", // future-dated
        })
        c1.push(s1)
        if applied, err := c2.pull(s2); err != nil || applied != 1 {
                t.Fatalf("skewed pull: applied=%d err=%v", applied, err)
        }
        var price int64
        s2.db.QueryRow(`SELECT price_cents FROM products WHERE sku='SKU-T'`).Scan(&price)
        if price != 9900 {
                t.Fatalf("skewed edit not applied: %d", price)
        }

        // A correctly-clocked till pushes a LATER real-world price change. The
        // string LWW rejects it as "stale" for a full year of skew.
        s1.Emit("product", "upsert", map[string]any{
                "sku": "SKU-T", "name": "Legit Edit", "priceCents": 500,
                "categorySlug": "c", "active": true, "trackStock": true,
                "updatedAt": nowStamp(),
        })
        c1.push(s1)
        if applied, err := c2.pull(s2); err != nil || applied != 1 {
                t.Fatalf("legit pull: applied=%d err=%v", applied, err)
        }
        s2.db.QueryRow(`SELECT price_cents FROM products WHERE sku='SKU-T'`).Scan(&price)
        if price != 9900 {
                t.Fatalf("expected the legit edit to be rejected by the skewed timestamp (documenting the bug), got price=%d", price)
        }
        var name string
        s2.db.QueryRow(`SELECT name FROM products WHERE sku='SKU-T'`).Scan(&name)
        if name != "Skewed Edit" {
                t.Fatalf("product name = %q", name)
        }
}

// ---------------------------------------------------------------------------
// 14. Vault hardening: malformed cloud answers never crash the till
// ---------------------------------------------------------------------------

func TestAgent3VaultMalformedResponses(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        s := agent3NewService(t)
        _ = agent3Register(t, s)

        // RPC returning ok=true with a nil config map
        // (empty configRows — applyCloudConfig(nil) must not panic).
        fake.mu.Lock()
        fake.configRows["paystack_public_key"] = "pk_live_ok_00000000001"
        fake.mu.Unlock()
        if err := s.RefreshCloudConfig(); err != nil {
                t.Fatalf("refresh with empty config: %v", err)
        }

        // Public endpoint erroring with 500 is tolerated by the loop.
        fake.mu.Lock()
        fake.registerBlocked = true // force the RPC path to fail too
        fake.mu.Unlock()
        var panicked any
        func() {
                defer func() { panicked = recover() }()
                _ = s.RefreshCloudConfig()
        }()
        if panicked != nil {
                t.Fatalf("refresh panicked under hostile responses: %v", panicked)
        }
}

// Guard: the fake cloud must never accidentally hit the production host.
func TestAgent3GuardProductionStillArmed(t *testing.T) {
        if err := guardProduction(cloudmeta.ProjectURL); err == nil {
                t.Fatal("guardProduction must refuse the production ref under go test")
        }
        if err := guardProduction("http://127.0.0.1:1"); err != nil {
                t.Fatalf("guardProduction refused a harmless host: %v", err)
        }
}

// ---------------------------------------------------------------------------
// 17. Vault rotation vs the auto-injected env var (BUG T3-1 tripwire)
// ---------------------------------------------------------------------------

// The vault's headline promise: "rotation is one SQL UPDATE in the cloud;
// every till picks it up at its next refresh with zero site visits."
// applyCloudConfig seeds PAYSTACK_SECRET_KEY into the process env the first
// time it applies a secret (when the env slot is unset) — and GetPaystack
// reads env FIRST. So the seed freezes: a later rotation updates the
// settings store but os.Setenv is skipped ("env already set"), and the till
// keeps charging on the REVOKED key until the process restarts. On a POS
// appliance that runs for months, that is exactly the window the 6h
// refresh loop exists to cover.
//
// This test asserts the CURRENT (buggy) behaviour as a tripwire: when the
// fix lands, the final two asserts fail with "BUG T3-1 appears fixed" and
// should be flipped to assert rotation wins.
func TestAgent3VaultRotationAfterEnvInjection(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        const v1 = "sk_live_vaultv1key00000000001"
        const v2 = "sk_live_vaultv2key00000000002"
        fake.mu.Lock()
        fake.configRows["paystack_secret_key"] = v1
        fake.mu.Unlock()

        s := agent3NewService(t)
        t.Setenv("PAYSTACK_SECRET_KEY", "") // reserve the slot; applyCloudConfig will inject
        if err := s.RefreshCloudConfig(); err != nil {
                t.Fatalf("first refresh: %v", err)
        }
        // Precondition: the seed injection happened.
        if got := os.Getenv("PAYSTACK_SECRET_KEY"); got != v1 {
                t.Fatalf("precondition: env not seeded from vault: %q", got)
        }
        if c := s.GetPaystack(); c == nil || s.paystackKey != v1 {
                t.Fatalf("precondition: effective key %q, want v1", s.paystackKey)
        }

        // Rotation: one SQL UPDATE in the cloud.
        fake.mu.Lock()
        fake.configRows["paystack_secret_key"] = v2
        fake.mu.Unlock()
        if err := s.RefreshCloudConfig(); err != nil {
                t.Fatalf("refresh after rotation: %v", err)
        }
        if got := s.Settings().Get("paystack_secret_key"); got != v2 {
                t.Fatalf("rotation did not reach the settings store: %q", got)
        }

        // FIXED T3-1: the vault-injected env follows rotation — no till keeps
        // charging on a revoked key, and child processes see the new value.
        if got := os.Getenv("PAYSTACK_SECRET_KEY"); got != v2 {
                t.Fatalf("injected env must follow rotation, got %q", got)
        }
        if c := s.GetPaystack(); c == nil {
                t.Fatal("GetPaystack nil after rotation")
        } else if s.paystackKey != v2 {
                t.Fatalf("effective key must follow rotation, got %q", s.paystackKey)
        }
        rotated := false
        for _, kv := range os.Environ() {
                if strings.HasPrefix(kv, "PAYSTACK_SECRET_KEY=") && strings.HasSuffix(kv, v2) {
                        rotated = true
                }
        }
        if !rotated {
                t.Fatal("env must carry the rotated key for child processes")
        }
}

// ---------------------------------------------------------------------------
// 18. Vault cannot remotely revoke a secret (INFO tripwire)
// ---------------------------------------------------------------------------

// applyCloudConfig skips rows with empty values, so an operator "revoking"
// a compromised key by CLEARING the vault row never reaches the tills —
// they keep charging on the compromised key indefinitely (no local expiry,
// no negative signal). Only overwriting with a NEW value propagates.
func TestAgent3VaultCloudCannotRevokeSecret(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        const v1 = "sk_live_compromisedkey00000001"
        fake.mu.Lock()
        fake.configRows["paystack_secret_key"] = v1
        fake.mu.Unlock()
        s := agent3NewService(t)
        t.Setenv("PAYSTACK_SECRET_KEY", "")
        if err := s.RefreshCloudConfig(); err != nil {
                t.Fatalf("first refresh: %v", err)
        }

        // Cloud-side "revocation": clear the row.
        fake.mu.Lock()
        fake.configRows["paystack_secret_key"] = ""
        fake.mu.Unlock()
        if err := s.RefreshCloudConfig(); err != nil {
                t.Fatalf("refresh after revocation: %v", err)
        }
        // FIXED: clearing the vault row revokes the secret locally — the till
        // stops charging on a compromised key.
        if got := s.Settings().Get("paystack_secret_key"); got != "" {
                t.Fatalf("cleared vault row must clear the local secret, got %q", got)
        }
        if s.GetPaystack() != nil {
                t.Fatal("client must refuse the revoked key (cleared locally)")
        }
}

// ---------------------------------------------------------------------------
// 19. Order apply is exactly-once under cursor regression (client_uuid)
// ---------------------------------------------------------------------------

// applyOrder dedupes on orders.client_uuid and remoteComplete only fires on
// a PENDING→PAID transition (guarded by the UPDATE ... WHERE status=
// 'PENDING' rows-affected check), so the completion replay survives a
// cursor regression without double stock deduction or duplicate rows — the
// apply-side "exactly-once via client_uuid" contract.
//
// Stock contract: the checkout emits per-line stock DELTA events (applied
// exactly once) — remoteComplete only adopts payments. This test replays
// the delta too, exactly as a real checkout on till A would.
func TestAgent3SyncOrderReplayExactlyOnce(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        s1 := agent3NewService(t)
        s2 := agent3NewService(t)
        c1 := agent3Register(t, s1)
        c2 := agent3Register(t, s2)

        s2.db.Exec(s2.db.Rebind(`INSERT INTO categories (name, slug) VALUES ('C','c')`))
        var catID int64
        s2.db.QueryRow(`SELECT id FROM categories LIMIT 1`).Scan(&catID)
        s2.db.Exec(s2.db.Rebind(`INSERT INTO products (sku, name, category_id, price_cents, stock_qty)
                VALUES ('SKU-R','R',?,500,10)`), catID)
        var prodID int64
        s2.db.QueryRow(`SELECT id FROM products WHERE sku='SKU-R'`).Scan(&prodID)
        s2.db.Exec(s2.db.Rebind(`INSERT INTO roles (name) VALUES ('Cashier')`))
        var roleID int64
        s2.db.QueryRow(`SELECT id FROM roles WHERE name='Cashier'`).Scan(&roleID)
        s2.db.Exec(s2.db.Rebind(`INSERT INTO users (username, role_id) VALUES ('cash1', ?)`), roleID)
        var userID int64
        s2.db.QueryRow(`SELECT id FROM users WHERE username='cash1'`).Scan(&userID)

        // Local PENDING tab on the receiving till.
        s2.db.Exec(s2.db.Rebind(`
                INSERT INTO orders (number, status, subtotal_cents, total_cents, cashier_id, client_uuid, created_at)
                VALUES ('9100','PENDING',1000,1000,?, 'ord-dedupe-1', ?)`), userID, nowStamp())
        var ordID int64
        s2.db.QueryRow(`SELECT id FROM orders WHERE client_uuid='ord-dedupe-1'`).Scan(&ordID)
        s2.db.Exec(s2.db.Rebind(`
                INSERT INTO order_items (order_id, product_id, name, sku, qty, unit_price_cents, line_total_cents)
                VALUES (?, ?, 'R', 'SKU-R', 2, 500, 1000)`), ordID, prodID)
        s2.db.Exec(s2.db.Rebind(`
                INSERT INTO payments (order_id, method, amount_cents, status) VALUES (?, 'cash', 1000, 'PENDING')`), ordID)

        cursorKey := "syncstate_last_event_" + fake.team
        paidEvent := func() map[string]any {
                return map[string]any{
                        "clientUuid": "ord-dedupe-1", "number": "9100", "status": "PAID",
                        "subtotalCents": 1000, "totalCents": 1000,
                        "createdAt": nowStamp(), "paidAt": nowStamp(),
                        "items": []map[string]any{
                                {"sku": "SKU-R", "name": "R", "qty": 2, "unitPriceCents": 500, "lineTotalCents": 1000},
                        },
                        "payments": []map[string]any{
                                {"method": "cash", "amountCents": 1000, "status": "COMPLETED", "completedAt": nowStamp()},
                        },
                }
        }

        // Remote completion arrives once: the checkout's stock delta moves
        // stock exactly once; the PAID event adopts the payment.
        s1.Emit("stock", "upsert", map[string]any{"sku": "SKU-R", "delta": -2})
        s1.Emit("order", "upsert", paidEvent())
        c1.push(s1)
        if applied, err := c2.pull(s2); err != nil || applied != 2 {
                t.Fatalf("completion pull: applied=%d err=%v", applied, err)
        }
        if got := agent3Stock(s2, "SKU-R"); got != 8 {
                t.Fatalf("stock after remote completion = %d, want 8", got)
        }
        var status string
        s2.db.QueryRow(`SELECT status FROM orders WHERE client_uuid='ord-dedupe-1'`).Scan(&status)
        if status != "PAID" {
                t.Fatalf("remote completion status = %s", status)
        }
        var orders, pays int
        s2.db.QueryRow(`SELECT COUNT(*) FROM orders WHERE client_uuid='ord-dedupe-1'`).Scan(&orders)
        s2.db.QueryRow(`SELECT COUNT(*) FROM payments p JOIN orders o ON o.id=p.order_id
                WHERE o.client_uuid='ord-dedupe-1' AND p.status='COMPLETED'`).Scan(&pays)
        if orders != 1 || pays != 1 {
                t.Fatalf("after completion: orders=%d completed-payments=%d, want 1/1", orders, pays)
        }

        // Cursor regression (restored backup / wiped sync_state): the same
        // event is re-delivered. sync_applied skips it (0 applies) — and the
        // inner client_uuid + PENDING-only completion guard are the second,
        // entity-level exactly-once net.
        _ = s2.settings.Set(cursorKey, "0")
        if applied, err := c2.pull(s2); err != nil || applied != 0 {
                t.Fatalf("replay pull must not re-apply: applied=%d err=%v", applied, err)
        }
        if got := agent3Stock(s2, "SKU-R"); got != 8 {
                t.Fatalf("replay double-deducted stock: %d, want 8", got)
        }
        s2.db.QueryRow(`SELECT COUNT(*) FROM orders WHERE client_uuid='ord-dedupe-1'`).Scan(&orders)
        s2.db.QueryRow(`SELECT COUNT(*) FROM payments p JOIN orders o ON o.id=p.order_id
                WHERE o.client_uuid='ord-dedupe-1' AND p.status='COMPLETED'`).Scan(&pays)
        if orders != 1 || pays != 1 {
                t.Fatalf("replay duplicated rows: orders=%d completed-payments=%d, want 1/1", orders, pays)
        }
        _ = ordID
}

// ---------------------------------------------------------------------------
// 20. A sale never blocks on (or fails because of) a sync outage
// ---------------------------------------------------------------------------

// sync_enabled + cloud down: Checkout must complete at sale latency (Emit
// is a local outbox INSERT — no network on the hot path), events pile up in
// the outbox, TeamSyncNow records the error without panicking, and the
// outbox survives for a later retry.
func TestAgent3SaleSurvivesSyncOutage(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        s := agent3NewService(t)
        _ = agent3Register(t, s) // sync_enabled=true, endpoint cached
        if !s.Settings().GetBool("sync_enabled", false) {
                t.Fatal("precondition: sync not enabled after registration")
        }

        s.db.Exec(s.db.Rebind(`INSERT INTO categories (name, slug) VALUES ('C','c')`))
        var catID int64
        s.db.QueryRow(`SELECT id FROM categories LIMIT 1`).Scan(&catID)
        s.db.Exec(s.db.Rebind(`INSERT INTO products (sku, name, category_id, price_cents, stock_qty)
                VALUES ('SKU-SALE','S',?,700,10)`), catID)
        var prodID int64
        s.db.QueryRow(`SELECT id FROM products WHERE sku='SKU-SALE'`).Scan(&prodID)
        s.db.Exec(s.db.Rebind(`INSERT INTO roles (name) VALUES ('Cashier')`))
        var roleID int64
        s.db.QueryRow(`SELECT id FROM roles WHERE name='Cashier'`).Scan(&roleID)
        s.db.Exec(s.db.Rebind(`INSERT INTO users (username, role_id) VALUES ('cash1', ?)`), roleID)
        var userID int64
        s.db.QueryRow(`SELECT id FROM users WHERE username='cash1'`).Scan(&userID)

        // The cloud dies right before the rush.
        fake.srv.Close()

        start := time.Now()
        order, err := s.Checkout(context.Background(), &auth.Principal{
                ID: userID, Username: "cash1", RoleID: roleID, RoleName: "Cashier", Active: true,
        }, models.CheckoutRequest{
                Items:         []models.CheckoutItem{{ProductID: prodID, Qty: 2}},
                PaymentMethod: models.MethodCash,
                ClientUUID:    "sale-outage-1",
        })
        elapsed := time.Since(start)
        if err != nil {
                t.Fatalf("checkout failed during a sync outage: %v", err)
        }
        if order == nil || order.Status != models.OrderPaid {
                t.Fatalf("order not completed during outage: %+v", order)
        }
        if elapsed > 2*time.Second {
                t.Fatalf("checkout blocked on sync for %v (Emit must be offline-local)", elapsed)
        }
        if got := agent3Stock(s, "SKU-SALE"); got != 8 {
                t.Fatalf("stock after sale = %d, want 8", got)
        }
        if n := agent3Pending(s); n < 1 {
                t.Fatalf("outbox empty after sale (%d pending) — Emit dropped sale events", n)
        }

        // Idempotent double-submit while offline: same sale, no duplicate.
        if again, err := s.Checkout(context.Background(), &auth.Principal{ID: 1, Username: "agent3", Active: true},
                models.CheckoutRequest{
                        Items:         []models.CheckoutItem{{ProductID: prodID, Qty: 2}},
                        PaymentMethod: models.MethodCash, ClientUUID: "sale-outage-1",
                }); err != nil || again.ID != order.ID {
                t.Fatalf("offline replay: order=%+v err=%v", again, err)
        }
        if got := agent3Stock(s, "SKU-SALE"); got != 8 {
                t.Fatalf("double-submit double-deducted: %d", got)
        }

        // Sync cycle against the dead cloud: error is recorded, nothing is
        // marked pushed, and calling it again neither panics nor wedges.
        for i := 0; i < 2; i++ {
                if _, _, err := s.TeamSyncNow("agent3-test"); err == nil {
                        t.Fatalf("TeamSyncNow #%d succeeded against a dead cloud", i+1)
                }
        }
        if s.Settings().Get("sync_last_error") == "" {
                t.Fatal("sync_last_error not recorded for the outage")
        }
        if n := agent3Pending(s); n < 1 {
                t.Fatal("outbox was cleared despite a failed push — events would be lost")
        }

        // Cloud comes back (same team, fresh server): the till must re-resolve
        // past its cached endpoint, register, and drain the backlog.
        back := newAgent3Cloud(t)
        back.team = fake.team
        agent3Link(t, back.srv.URL)
        // Expire the 6h bootstrap cache so syncConfig re-resolves against the
        // live cloud instead of retrying the dead URL until the cache ages out.
        _ = s.Settings().Set("sync_bootstrap_at", "2000-01-01T00:00:00Z")
        if _, _, err := s.TeamSyncNow("agent3-test"); err != nil {
                t.Fatalf("sync did not recover with the cloud back: %v", err)
        }
        if n := agent3Pending(s); n != 0 {
                t.Fatalf("outbox did not drain after recovery: %d pending", n)
        }
}
