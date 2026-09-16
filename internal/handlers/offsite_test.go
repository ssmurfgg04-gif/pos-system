package handlers_test

import (
        "bytes"
        "context"
        "encoding/json"
        "io"
        "net/http"
        "net/http/httptest"
        "os"
        "path/filepath"
        "strings"
        "testing"

        "posapp/internal/database"
        "posapp/internal/offsite"
        "posapp/internal/settings"
)

// Upload flow against a stub Supabase: encrypted POST (never plaintext),
// batch prune beyond keep, audit row written.
func TestOffsiteUploadFlow(t *testing.T) {
        dir := t.TempDir()
        db, err := database.Open("sqlite", filepath.Join(dir, "up.db"), "")
        if err != nil {
                t.Fatal(err)
        }
        t.Cleanup(func() { _ = db.Close() })
        if err := db.Migrate(); err != nil {
                t.Fatal(err)
        }
        if err := db.Seed(true); err != nil {
                t.Fatal(err)
        }
        st, err := settings.New(db)
        if err != nil {
                t.Fatal(err)
        }

        var puts int
        var lastBody []byte
        var deleted []string
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                switch {
                case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/storage/v1/object/list/"):
                        w.Write([]byte(`[{"name":"shop/old1.db.enc","updated_at":"2026-09-01T02:00:00Z"},{"name":"shop/old2.db.enc","updated_at":"2026-09-02T02:00:00Z"}]`))
                case r.Method == "POST":
                        puts++
                        lastBody, _ = io.ReadAll(r.Body)
                        if r.Header.Get("Authorization") != "Bearer SK" {
                                w.WriteHeader(401)
                                return
                        }
                        w.Write([]byte(`{"Key":"shop/k"}`))
                case r.Method == "DELETE":
                        var del struct {
                                Prefixes []string `json:"prefixes"`
                        }
                        body, _ := io.ReadAll(r.Body)
                        _ = json.Unmarshal(body, &del)
                        deleted = append(deleted, del.Prefixes...)
                        w.Write([]byte(`[]`))
                default:
                        w.WriteHeader(400)
                }
        }))
        defer srv.Close()

        st.Set("offsite_enabled", "true")
        st.Set("offsite_endpoint", srv.URL)
        st.Set("offsite_bucket", "b")
        st.Set("offsite_secret_key", "SK")
        st.Set("offsite_prefix", "shop")
        st.Set("offsite_keep", "1")
        st.Set("offsite_passphrase", "correct horse")

        var audits int
        w := offsite.NewWorker(st, func(action, entity, entityID, details string) {
                if action == "BACKUP_UPLOADED" {
                        audits++
                }
        })
        snap := filepath.Join(dir, "snap.db")
        plain := []byte("fake-database-bytes-for-upload-test")
        if err := os.WriteFile(snap, plain, 0o600); err != nil {
                t.Fatal(err)
        }
        ctx := context.Background()
        key, err := w.UploadNow(ctx, snap)
        if err != nil {
                t.Fatalf("upload: %v", err)
        }
        if !strings.HasPrefix(key, "shop/pos-") || !strings.HasSuffix(key, ".db.enc") {
                t.Fatalf("bad key %q", key)
        }
        if puts != 1 {
                t.Fatalf("expected 1 upload POST, got %d", puts)
        }
        if bytes.Equal(lastBody, plain) {
                t.Fatal("upload body must be encrypted, not plaintext")
        }
        if len(deleted) != 1 || deleted[0] != "shop/old1.db.enc" {
                t.Fatalf("keep=1 with 2 old keys should batch-delete old1, deleted %v", deleted)
        }
        if audits != 1 {
                t.Fatalf("expected 1 BACKUP_UPLOADED audit, got %d", audits)
        }
        enabled, pending, lastOk, lastErr, _ := w.Status()
        if !enabled || pending != 0 || lastOk != key || lastErr != "" {
                t.Fatalf("status wrong: enabled=%v pending=%d ok=%q err=%q", enabled, pending, lastOk, lastErr)
        }
}

// Disabled worker refuses with a configuration error (never silent).
func TestOffsiteDisabledRefuses(t *testing.T) {
        dir := t.TempDir()
        db, err := database.Open("sqlite", filepath.Join(dir, "off.db"), "")
        if err != nil {
                t.Fatal(err)
        }
        t.Cleanup(func() { _ = db.Close() })
        if err := db.Migrate(); err != nil {
                t.Fatal(err)
        }
        if err := db.Seed(true); err != nil {
                t.Fatal(err)
        }
        st, err := settings.New(db)
        if err != nil {
                t.Fatal(err)
        }
        w := offsite.NewWorker(st, func(action, entity, entityID, details string) {})
        if _, err := w.UploadNow(context.Background(), filepath.Join(dir, "x.db")); err == nil {
                t.Fatal("disabled uploader must refuse")
        }
        enabled, _, _, _, _ := w.Status()
        if enabled {
                t.Fatal("fresh seed must leave off-site disabled")
        }
}
