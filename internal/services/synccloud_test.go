package services

// synccloud_test.go — the zero-config identity flow against a fake Supabase:
// a till must discover its team from the sync_bootstrap row, register with
// its device secret, and push/pull only through the identity-checked RPCs.

import (
        "encoding/json"
        "net/http"
        "net/http/httptest"
        "path/filepath"
        "strings"
        "sync"
        "testing"

        "posapp/internal/database"
        "posapp/internal/settings"
)

type fakeSupabase struct {
        mu       sync.Mutex
        srv      *httptest.Server
        selfURL  string
        device   map[string]string // device_id -> secret_hash (registered)
        approved map[string]bool
        events   []map[string]any
        nextID   int64
}

func newFakeSupabase(t *testing.T) *fakeSupabase {
        f := &fakeSupabase{
                device:   map[string]string{},
                approved: map[string]bool{},
                nextID:   1,
        }
        mux := http.NewServeMux()

        // GET /rest/v1/sync_bootstrap
        mux.HandleFunc("/rest/v1/sync_bootstrap", func(w http.ResponseWriter, r *http.Request) {
                if r.Header.Get("apikey") == "" {
                        w.WriteHeader(401)
                        return
                }
                w.Header().Set("Content-Type", "application/json")
                json.NewEncoder(w).Encode([]map[string]any{{
                        "project_url":  f.selfURL,
                        "team_code":    "TEST-TEAM",
                        "auto_approve": true,
                }})
        })

        // POST /rest/v1/rpc/sync_register
        mux.HandleFunc("/rest/v1/rpc/sync_register", func(w http.ResponseWriter, r *http.Request) {
                var in struct {
                        DeviceID   string `json:"p_device_id"`
                        SecretHash string `json:"p_secret_hash"`
                        Name       string `json:"p_device_name"`
                        Version    string `json:"p_app_version"`
                }
                json.NewDecoder(r.Body).Decode(&in)
                f.mu.Lock()
                defer f.mu.Unlock()
                if len(in.DeviceID) < 8 || len(in.SecretHash) < 32 {
                        json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid identity"})
                        return
                }
                if hash, ok := f.device[in.DeviceID]; ok && hash != in.SecretHash {
                        json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "identity mismatch"})
                        return
                }
                f.device[in.DeviceID] = in.SecretHash
                f.approved[in.DeviceID] = true // auto_approve
                json.NewEncoder(w).Encode(map[string]any{"ok": true, "approved": true, "team_code": "TEST-TEAM"})
        })

        // POST /rest/v1/rpc/sync_push
        mux.HandleFunc("/rest/v1/rpc/sync_push", func(w http.ResponseWriter, r *http.Request) {
                var in struct {
                        DeviceID   string           `json:"p_device_id"`
                        SecretHash string           `json:"p_secret_hash"`
                        Events     []map[string]any `json:"p_events"`
                }
                json.NewDecoder(r.Body).Decode(&in)
                f.mu.Lock()
                defer f.mu.Unlock()
                if f.device[in.DeviceID] != in.SecretHash || !f.approved[in.DeviceID] {
                        json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "device not recognized"})
                        return
                }
                for _, ev := range in.Events {
                        ev["device_id"] = in.DeviceID // server stamps identity
                        f.nextID++
                        ev["id"] = f.nextID
                        f.events = append(f.events, ev)
                }
                json.NewEncoder(w).Encode(map[string]any{"ok": true, "pushed": len(in.Events)})
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
                if f.device[in.DeviceID] != in.SecretHash || !f.approved[in.DeviceID] {
                        json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "device not recognized"})
                        return
                }
                out := []map[string]any{}
                for _, ev := range f.events {
                        id, _ := ev["id"].(int64)
                        if id > in.SinceID && ev["device_id"] != in.DeviceID {
                                out = append(out, ev)
                        }
                }
                json.NewEncoder(w).Encode(map[string]any{"ok": true, "events": out})
        })

        f.srv = httptest.NewServer(mux)
        f.selfURL = f.srv.URL
        t.Cleanup(f.srv.Close)
        return f
}

func newSyncTestService(t *testing.T) *Service {
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
        return &Service{db: db, settings: st, appVersion: "test"}
}

func TestCloudIdentityZeroConfig(t *testing.T) {
        fake := newFakeSupabase(t)
        oldURL := cloudBaseURL
        cloudBaseURL = fake.srv.URL
        t.Cleanup(func() { cloudBaseURL = oldURL })

        s := newSyncTestService(t)

        // 1. A fresh till resolves its team from the database with NO setup.
        c, ok := s.syncConfig()
        if !ok {
                t.Fatal("cloud bootstrap did not resolve a client")
        }
        if c.mode != "rpc" || c.team != "TEST-TEAM" {
                t.Fatalf("mode=%s team=%s, want rpc/TEST-TEAM", c.mode, c.team)
        }
        if s.settings.Get("sync_enabled") != "true" {
                t.Fatal("cloud discovery should auto-enable sync")
        }

        // 2. Heartbeat registers the device (approved via auto_approve).
        if err := c.heartbeat(s, "test"); err != nil {
                t.Fatalf("register: %v", err)
        }
        if !s.settings.GetBool("sync_registered", false) || !s.settings.GetBool("sync_approved", false) {
                t.Fatal("device should be registered + approved")
        }
        // Secret must exist locally; only its hash ever leaves.
        if s.deviceSecret() == "" {
                t.Fatal("device secret missing")
        }

        // 3. Emit → push moves the event; identity is stamped server-side.
        s.Emit("product", "upsert", map[string]any{"sku": "T1", "name": "Test"})
        pushed, err := c.push(s)
        if err != nil || pushed != 1 {
                t.Fatalf("push: %d %v", pushed, err)
        }

        // 4. A second till pulls the event and applies it.
        s2 := newSyncTestService(t)
        c2, ok := s2.syncConfig()
        if !ok {
                t.Fatal("second till bootstrap failed")
        }
        if err := c2.heartbeat(s2, "test"); err != nil {
                t.Fatalf("register 2: %v", err)
        }
        applied, err := c2.pull(s2)
        if err != nil || applied != 1 {
                t.Fatalf("pull: %d %v", applied, err)
        }

        // 5. Pull again: idempotent (cursor advanced, nothing re-applied).
        if applied, err := c2.pull(s2); err != nil || applied != 0 {
                t.Fatalf("second pull: %d %v", applied, err)
        }
}

func TestCloudIdentityRejectsWrongSecret(t *testing.T) {
        fake := newFakeSupabase(t)
        oldURL := cloudBaseURL
        cloudBaseURL = fake.srv.URL
        t.Cleanup(func() { cloudBaseURL = oldURL })

        s := newSyncTestService(t)
        c, ok := s.syncConfig()
        if !ok {
                t.Fatal("bootstrap failed")
        }
        if err := c.heartbeat(s, "test"); err != nil {
                t.Fatalf("register: %v", err)
        }
        // Sabotage the stored secret hash: the server must refuse pushes.
        c.secretHash = strings.Repeat("0", 64)
        s.Emit("product", "upsert", map[string]any{"sku": "T2"})
        if _, err := c.push(s); err == nil || !strings.Contains(err.Error(), "not recognized") {
                t.Fatalf("expected rejection, got %v", err)
        }
}

func TestManualConfigWinsOverCloud(t *testing.T) {
        newFakeSupabase(t)
        oldURL := cloudBaseURL
        cloudBaseURL = "http://127.0.0.1:1" // cloud unreachable — must not matter
        t.Cleanup(func() { cloudBaseURL = oldURL })

        s := newSyncTestService(t)
        _ = s.settings.Set("sync_enabled", "true")
        _ = s.settings.Set("sync_source", "manual")
        _ = s.settings.Set("sync_endpoint", "https://manual.example.com")
        _ = s.settings.Set("sync_service_key", "manual-key")
        _ = s.settings.Set("sync_team_code", "MAN-1")

        c, ok := s.syncConfig()
        if !ok {
                t.Fatal("manual config should resolve")
        }
        if c.mode != "rest" || c.base != "https://manual.example.com/rest/v1" {
                t.Fatalf("manual client wrong: %+v", c)
        }
}

func TestUseCloudRevertsManual(t *testing.T) {
        fake := newFakeSupabase(t)
        oldURL := cloudBaseURL
        cloudBaseURL = fake.srv.URL
        t.Cleanup(func() { cloudBaseURL = oldURL })

        s := newSyncTestService(t)
        _ = s.settings.Set("sync_source", "manual")
        _ = s.settings.Set("sync_endpoint", "https://manual.example.com")
        _ = s.settings.Set("sync_service_key", "k")
        _ = s.settings.Set("sync_team_code", "MAN-1")

        if err := s.TeamSyncUseCloud(); err != nil {
                t.Fatalf("use-cloud: %v", err)
        }
        if s.settings.Get("sync_source") != "cloud" {
                t.Fatalf("source=%q, want cloud", s.settings.Get("sync_source"))
        }
        if s.settings.Get("sync_team_code") != "TEST-TEAM" {
                t.Fatalf("team=%q, want TEST-TEAM", s.settings.Get("sync_team_code"))
        }
}
