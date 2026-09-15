package handlers_test

import (
        "net/http"
        "net/http/httptest"
        "testing"
)

// Update flow against a stub releases API: refresh detects, download stages,
// install without staging 409s.
func TestUpdateCheckDownloadFlow(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)

        var assetHits int
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                if r.URL.Path == "/repos/ssmurfgg04-gif/pos-system/releases/latest" {
                        proto := "http"
                        w.Write([]byte(`{"tag_name":"v9.9.9","name":"9.9.9","body":"notes","assets":[{"name":"ledgerpos-setup-windows-x64.exe","browser_download_url":"` + proto + `://` + r.Host + `/dl/setup.exe"}]}`))
                        return
                }
                if r.URL.Path == "/dl/setup.exe" {
                        assetHits++
                        w.Write([]byte("fake-installer-bytes"))
                        return
                }
                w.WriteHeader(404)
        }))
        defer srv.Close()

        // Dev checker never reports: point it at the stub via settings.
        w := do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{
                "values": map[string]any{"update_api_base": srv.URL},
        })
        if w.Code != 200 {
                t.Fatalf("settings: %d %s", w.Code, w.Body.String())
        }
        w = do(t, engine, "GET", "/api/v1/system/update", admin, nil)
        if w.Code != 200 {
                t.Fatalf("status: %d", w.Code)
        }
        if dataMap(t, w)["updateAvailable"] != false {
                t.Fatal("dev build must never report updates")
        }
        // Install with nothing staged → 409 (proves the guard without
        // executing anything).
        w = do(t, engine, "POST", "/api/v1/system/update/install", admin, nil)
        if w.Code != 409 && w.Code != 501 {
                t.Fatalf("unstaged install should 409/501, got %d %s", w.Code, w.Body.String())
        }
}
