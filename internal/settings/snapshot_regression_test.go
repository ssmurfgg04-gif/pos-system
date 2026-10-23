package settings

// snapshot_regression_test.go — regression for the shop-floor
// "store name never saves" report. Root cause: cloud-vault tills carry
// runtime bookkeeping rows in the settings table (config_synced_at,
// paystack_mode, the update_manifest_* mirror, offsite_api_key,
// sync_device_secret). The Settings page round-trips the whole GET
// snapshot, so every save re-sent those keys and the API rejected the
// whole PUT with 400 "unknown setting key: …" / "value too long for
// key: update_manifest_assets". Test tills never had the rows, so the
// suite stayed green while every real till failed.

import (
        "path/filepath"
        "strings"
        "testing"

        "posapp/internal/database"
)

// seededStore boots a settings store over a DB that already carries the
// runtime bookkeeping rows a real cloud-connected till accumulates.
func seededStore(t *testing.T) (*Store, *database.DB) {
        t.Helper()
        dir := t.TempDir()
        db, err := database.Open("sqlite", filepath.Join(dir, "test.db"), "")
        if err != nil {
                t.Fatalf("db: %v", err)
        }
        t.Cleanup(func() { _ = db.Close() })
        if err := db.Migrate(); err != nil {
                t.Fatalf("migrate: %v", err)
        }
        // Exactly what applyCloudConfig / the sync engine leave behind on a
        // production till (update_manifest_assets is ~1KB in the vault).
        bootRows := map[string]string{
                "config_synced_at":      "2026-10-23T09:15:00Z",
                "paystack_mode":         "live",
                "update_manifest_tag":   "v1.3.6",
                "update_manifest_notes": "maintenance release",
                "update_manifest_sig":   "MEUCIQDxxxxxxxxxxxxxxxxxxxxxxx==",
                "update_manifest_assets": `[
  {"name":"ledgerpos-setup-windows-x64.exe","url":"https://example.com/dl.exe","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
  {"name":"ledgerpos-linux-x64.tar.xz","url":"https://example.com/dl.tar.xz","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
]`,
                "offsite_api_key":    "sbp_secret_offsite",
                "sync_device_secret": "device-secret-plain",
                "sync_service_key":   "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.service",
                "device_id":          "dev-test-1234",
        }
        for k, v := range bootRows {
                if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, k, v); err != nil {
                        t.Fatalf("seed %s: %v", k, err)
                }
        }
        st, err := New(db)
        if err != nil {
                t.Fatalf("settings: %v", err)
        }
        return st, db
}

func TestSnapshotHidesRuntimeBookkeeping(t *testing.T) {
        st, _ := seededStore(t)

        snap := st.Snapshot()

        // Runtime bookkeeping must never reach the API: the Settings page
        // round-trips the snapshot, and any of these keys 400s the save.
        for _, k := range []string{
                "config_synced_at", "paystack_mode", "offsite_api_key",
                "update_manifest_tag", "update_manifest_notes",
                "update_manifest_sig", "update_manifest_assets",
                "device_id", "jwt_secret",
        } {
                if _, ok := snap[k]; ok {
                        t.Errorf("snapshot leaks runtime key %q", k)
                }
        }
}

func TestSnapshotMasksCredentialClassKeys(t *testing.T) {
        st, _ := seededStore(t)

        snap := st.Snapshot()

        // Service/api credentials are masked even though they are
        // runtime-delivered (defense in depth: nothing credential-shaped
        // leaves the machine in plaintext).
        if got, _ := snap["sync_service_key"].(string); got != MaskToken {
                t.Errorf("sync_service_key = %q, want mask token", got)
        }
}

func TestUpdateSurvivesStaleSnapshotEcho(t *testing.T) {
        st, db := seededStore(t)

        // The exact PUT a real till sends: the admin edits the store name in
        // the Settings page and the form posts back the whole stale snapshot
        // (bookkeeping echoes included). The save must succeed and leave the
        // bookkeeping untouched.
        echo := map[string]string{
                "store_name":             "Creative Divine Concepts",
                "app_name":               "Point of Sale",
                "config_synced_at":       "2026-10-23T09:15:00Z",
                "paystack_mode":          "live",
                "update_manifest_tag":    "v1.3.6",
                "update_manifest_assets": strings.Repeat("x", 900),
                "offsite_api_key":        "sbp_secret_offsite",
                "sync_device_secret":     MaskToken,
                "sync_service_key":       MaskToken,
                "jwt_secret":             "attacker-chosen",
        }
        changed, err := st.Update(echo)
        if err != nil {
                t.Fatalf("Update with stale echo: %v", err)
        }
        found := false
        for _, k := range changed {
                if k == "store_name" {
                        found = true
                }
        }
        if !found {
                t.Fatalf("store_name not in changed keys: %v", changed)
        }
        if got := st.Get("store_name"); got != "Creative Divine Concepts" {
                t.Fatalf("store_name = %q, want the admin's edit", got)
        }

        // Bookkeeping must be untouched by the round-trip.
        var syncedAt, tag string
        if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'config_synced_at'`).Scan(&syncedAt); err != nil || syncedAt != "2026-10-23T09:15:00Z" {
                t.Fatalf("config_synced_at mutated: %q (%v)", syncedAt, err)
        }
        if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'update_manifest_tag'`).Scan(&tag); err != nil || tag != "v1.3.6" {
                t.Fatalf("update_manifest_tag mutated: %q (%v)", tag, err)
        }
        // The mask echo must never clear or replace the device secret. With a
        // vault key configured (package-level state from other tests), the
        // boot migration also re-encrypts the once-plaintext row — either way
        // the read path must yield the original plaintext.
        if got := st.Get("sync_device_secret"); got != "device-secret-plain" {
                t.Fatalf("sync_device_secret = %q, want the original plaintext", got)
        }
        var devRaw string
        if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'sync_device_secret'`).Scan(&devRaw); err != nil {
                t.Fatalf("raw read: %v", err)
        }
        if devRaw != "device-secret-plain" && !isEncrypted(devRaw) {
                t.Fatalf("sync_device_secret stored as %q, want plaintext or vault ciphertext", devRaw)
        }
        // jwt_secret is not writable through Update at all (defense in depth
        // under the API allowlist).
        if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'jwt_secret'`).Scan(&devRaw); err == nil && devRaw == "attacker-chosen" {
                t.Fatal("jwt_secret was overwritten through Update")
        }
}

func TestBrandingCarriesStoreRename(t *testing.T) {
        st, _ := seededStore(t)

        if _, err := st.Update(map[string]string{"store_name": "Creative Divine Concepts"}); err != nil {
                t.Fatalf("rename: %v", err)
        }
        b := st.Branding()
        if b["store_name"] != "Creative Divine Concepts" {
                t.Fatalf("Branding store_name = %v", b["store_name"])
        }
}
