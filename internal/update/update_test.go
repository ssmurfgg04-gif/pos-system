package update

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		cur, lat string
		want     bool
	}{
		{"1.1.0", "v1.2.0", true},
		{"v1.2.0", "v1.2.0", false},
		{"1.3.0", "v1.2.0", false},
		{"dev", "v9.9.9", false},
		{"", "v1.0.0", false},
		{"1.2", "v1.2.1", true},
		{"v1.10.0", "v1.9.9", false},
		{"1.0.0", "not-a-version", false},
		{"1.0.0-beta", "v1.0.1", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.cur, c.lat); got != c.want {
			t.Errorf("IsNewer(%q, %q) = %v, want %v", c.cur, c.lat, got, c.want)
		}
	}
}

func TestCheckAgainstStub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/ssmurfgg04-gif/pos-system/releases/latest" {
			t.Errorf("wrong path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"tag_name":"v1.2.0","name":"1.2.0","body":"notes","assets":[{"name":"ledgerpos-setup-windows-x64.exe","browser_download_url":"https://example.com/setup.exe"}]}`))
	}))
	defer srv.Close()
	rel, err := Check(context.Background(), srv.URL, "ssmurfgg04-gif/pos-system")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Tag != "v1.2.0" {
		t.Fatalf("tag = %q", rel.Tag)
	}
	got := PickAsset(rel, "windows")
	if got.URL != "https://example.com/setup.exe" {
		t.Fatalf("asset = %+v", got)
	}
	if PickAsset(rel, "plan9") != (Asset{}) {
		t.Fatal("unknown platform must yield no asset")
	}
}

func TestDownloadStagesBytes(t *testing.T) {
        payload := []byte("fake-installer-bytes")
        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                w.Write(payload)
        }))
        defer srv.Close()
        c := &Checker{current: "1.0.0", assetURL: srv.URL + "/setup.exe", assetName: "setup-test-download.exe"}
        path, err := c.Download(context.Background())
        if err != nil {
                t.Fatal(err)
        }
        defer os.Remove(path)
        got, _ := os.ReadFile(path)
        if !bytes.Equal(got, payload) {
                t.Fatal("staged bytes mismatch")
        }
        if c.Staged() != path {
                t.Fatal("Staged() must return the staged path")
        }
}

func TestCheck404IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()
	if _, err := Check(context.Background(), srv.URL, "x/y"); err == nil {
		t.Fatal("404 must error")
	}
}
