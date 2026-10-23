package handlers_test

// settings_roundtrip_regression_test.go — the shop-floor "store name
// never saves" report, at the API boundary. A real cloud-vault till's
// GET /api/v1/settings snapshot contains runtime bookkeeping keys
// (config_synced_at, paystack_mode, the update_manifest_* mirror,
// sync_device_secret). The Settings page posts the whole snapshot back,
// so every save — store name included — died on the allowlist with 400
// "unknown setting key: config_synced_at" (or "value too long" for the
// manifest assets echo). The test server's pristine DB never had those
// rows, so the suite stayed green while production tills failed.

import (
        "net/http"
        "testing"
)

func TestSettingsSaveSurvivesRealTillSnapshotEcho(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)

        // 1. GET the settings snapshot (what the Settings page loads).
        w := do(t, engine, "GET", "/api/v1/settings", admin, nil)
        if w.Code != 200 {
                t.Fatalf("get: %d %s", w.Code, w.Body.String())
        }
        snap := dataMap(t, w)

        // 2. What a real till's snapshot carries beyond the test seed:
        // cloud-vault bookkeeping + the update-manifest mirror + device
        // credentials (masked). The admin only edited the store name.
        stale := map[string]any{
                "config_synced_at":       "2026-10-23T09:15:00Z",
                "paystack_mode":          "live",
                "update_manifest_tag":    "v1.3.6",
                "update_manifest_notes":  "maintenance release",
                "update_manifest_sig":    "MEUCIQDxxxxxxxxxxxxxxxxxxxxxxx==",
                "update_manifest_assets": "[]",
                "offsite_api_key":        "sbp_secret_offsite",
                "sync_device_secret":     "__SET__",
                "sync_service_key":       "__SET__",
        }
        values := map[string]any{}
        for k, v := range snap {
                if _, taken := stale[k]; !taken {
                        values[k] = v
                }
        }
        for k, v := range stale {
                values[k] = v
        }
        // The admin's edit lands last, exactly like the form field over the
        // loaded snapshot.
        values["store_name"] = "Creative Divine Concepts"

        // 3. Save — must succeed end to end.
        w = do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{"values": values})
        if w.Code != 200 {
                t.Fatalf("save with real-till snapshot: %d %s", w.Code, w.Body.String())
        }

        // 4. The edit persisted and the bookkeeping never leaked back out.
        w = do(t, engine, "GET", "/api/v1/settings", admin, nil)
        if w.Code != 200 {
                t.Fatalf("reget: %d", w.Code)
        }
        d := dataMap(t, w)
        if d["store_name"] != "Creative Divine Concepts" {
                t.Fatalf("store_name = %v, want the admin's edit", d["store_name"])
        }
        for _, k := range []string{"config_synced_at", "paystack_mode", "offsite_api_key",
                "update_manifest_tag", "update_manifest_sig", "update_manifest_assets"} {
                if _, ok := d[k]; ok {
                        t.Errorf("snapshot leaks runtime key %q", k)
                }
        }
        // sync_service_key is seeded empty on the test till and the "__SET__"
        // echo must keep it that way — never the raw credential, never a
        // stray mask token stored as a value.
        if got, _ := d["sync_service_key"].(string); got != "" {
                t.Errorf("sync_service_key = %q, want empty (unconfigured, mask echo kept stored value)", got)
        }
}

func TestSettingsSaveStillRejectsGenuinelyUnknownKeys(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)

        w := do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{
                "values": map[string]any{"definitely_not_a_setting": "x"},
        })
        if w.Code != 400 {
                t.Fatalf("mistyped key must fail loudly, got %d %s", w.Code, w.Body.String())
        }
        if got := http.StatusText(w.Code); got == "" {
                t.Fatalf("bad status")
        }
}
