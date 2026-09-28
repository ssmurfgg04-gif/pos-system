package update

// manifest_test.go — the cloud OTA manifest channel: parses tag/notes/
// assets from the app_config REST shape, picks the platform asset with
// its sha256, and refuses to touch the production cloud from tests.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"posapp/internal/cloudmeta"
)

func manifestServer(t *testing.T, tag, notes, assets string, status int) *httptest.Server {
	t.Helper()
	rows := []map[string]any{}
	for k, v := range map[string]string{
		"update_manifest_tag":    tag,
		"update_manifest_notes":  notes,
		"update_manifest_assets": assets,
	} {
		if v != "" {
			rows = append(rows, map[string]any{"key": k, "value": v})
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/rest/v1/app_config") {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("apikey") == "" {
			w.WriteHeader(401)
			return
		}
		if status != 200 {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[`))
		for i, row := range rows {
			if i > 0 {
				w.Write([]byte(`,`))
			}
			w.Write([]byte(`{"key":"` + row["key"].(string) + `","value":`))
			enc, _ := jsonMarshal(row["value"].(string))
			w.Write(enc)
			w.Write([]byte(`}`))
		}
		w.Write([]byte(`]`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckCloudManifestParsesRelease(t *testing.T) {
	assets := `[{"name":"ledgerpos-setup-windows-x64.exe","url":"https://dl.example/win.exe","sha256":"abc123"},
		{"name":"ledgerpos-linux-x64.tar.xz","url":"https://dl.example/linux.tar.xz","sha256":"def456"}]`
	srv := manifestServer(t, "1.1.5", "Notes body", assets, 200)
	old := cloudmeta.BaseURL
	cloudmeta.BaseURL = srv.URL
	t.Cleanup(func() { cloudmeta.BaseURL = old })

	rel, err := CheckCloudManifest(context.Background(), "")
	if err != nil {
		t.Fatalf("CheckCloudManifest: %v", err)
	}
	if rel.Tag != "1.1.5" || rel.Notes != "Notes body" {
		t.Fatalf("tag/notes = %q/%q", rel.Tag, rel.Notes)
	}
	if len(rel.Assets) != 2 {
		t.Fatalf("assets = %d, want 2", len(rel.Assets))
	}
	a := PickAsset(rel, "windows")
	if a.Name != "ledgerpos-setup-windows-x64.exe" || a.SHA256 != "abc123" {
		t.Fatalf("windows asset = %+v", a)
	}
	l := PickAsset(rel, "linux")
	if l.SHA256 != "def456" {
		t.Fatalf("linux asset sha = %q", l.SHA256)
	}
}

func TestCheckCloudManifestMissing(t *testing.T) {
	srv := manifestServer(t, "", "", "", 200)
	old := cloudmeta.BaseURL
	cloudmeta.BaseURL = srv.URL
	t.Cleanup(func() { cloudmeta.BaseURL = old })
	if _, err := CheckCloudManifest(context.Background(), ""); err == nil {
		t.Fatalf("empty manifest must error")
	}
}

func TestCheckCloudManifestServerError(t *testing.T) {
	srv := manifestServer(t, "1.1.5", "n", "[]", 500)
	old := cloudmeta.BaseURL
	cloudmeta.BaseURL = srv.URL
	t.Cleanup(func() { cloudmeta.BaseURL = old })
	if _, err := CheckCloudManifest(context.Background(), ""); err == nil {
		t.Fatalf("server error must propagate so the GitHub fallback engages")
	}
}

func TestCheckCloudManifestRefusesProductionInTests(t *testing.T) {
	if _, err := CheckCloudManifest(context.Background(), cloudmeta.ProjectURL); err == nil {
		t.Fatalf("tests must never fetch the production manifest")
	}
}

// jsonMarshal encodes a string as a JSON string value.
func jsonMarshal(s string) ([]byte, error) {
	return json.Marshal(s)
}
