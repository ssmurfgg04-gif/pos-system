package offsite

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
)

func TestEncryptDecryptRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "pos.db")
	plain := bytes.Repeat([]byte("0123456789ABCDEF"), 65536) // 1MB patterned
	if err := os.WriteFile(src, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	enc, err := EncryptFile(src, "correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if enc == src {
		t.Fatal("ciphertext must be a separate file")
	}
	raw, _ := os.ReadFile(enc)
	if bytes.Equal(raw, plain) {
		t.Fatal("ciphertext must differ from plaintext")
	}
	dst := filepath.Join(dir, "out.db")
	if err := DecryptFile(enc, "correct horse", dst); err != nil {
		t.Fatal(err)
	}
	back, _ := os.ReadFile(dst)
	if !bytes.Equal(back, plain) {
		t.Fatal("round trip mismatch")
	}
	if err := DecryptFile(enc, "wrong passphrase", filepath.Join(dir, "bad.db")); err == nil {
		t.Fatal("wrong passphrase must fail")
	}
	// Tampered magic must fail.
	tampered := filepath.Join(dir, "tampered.enc")
	traw := append([]byte("XXXX"), raw[4:]...)
	os.WriteFile(tampered, traw, 0o600)
	if err := DecryptFile(tampered, "correct horse", filepath.Join(dir, "bad2.db")); err == nil {
		t.Fatal("tampered magic must fail")
	}
}

func TestUploadListDownloadDeleteAgainstStub(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotUpsert string
	var gotBody []byte
	var gotListPrefix string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
	 switch {
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/storage/v1/object/list/"):
			var q struct {
				Prefix string `json:"prefix"`
			}
			_ = json.Unmarshal(body, &q)
			gotListPrefix = q.Prefix
			w.Write([]byte(`[{"name":"shop/pos-20260915-020000.db.enc","updated_at":"2026-09-15T02:00:00Z"}]`))
		case r.Method == "POST":
			gotUpsert = r.Header.Get("x-upsert")
			w.Write([]byte(`{"Key":"shop/k"}`))
		case r.Method == "GET":
			w.Write([]byte("bytes"))
		case r.Method == "DELETE":
			var del struct {
				Prefixes []string `json:"prefixes"`
			}
			_ = json.Unmarshal(body, &del)
			if len(del.Prefixes) == 0 {
				t.Error("delete must send {prefixes:[...]}")
			}
			w.Write([]byte(`[]`))
		default:
			w.WriteHeader(400)
		}
	}))
	defer srv.Close()
	cfg := Config{ProjectURL: srv.URL, Bucket: "b", Key: "SK"}
	ctx := context.Background()

	if err := UploadObject(ctx, cfg, "shop/k", strings.NewReader("data"), 4); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotPath != "/storage/v1/object/b/shop/k" || string(gotBody) != "data" {
		t.Fatalf("upload wrong: %s %s %q", gotMethod, gotPath, gotBody)
	}
	if gotUpsert != "true" {
		t.Fatal("upload must set x-upsert for retry idempotency")
	}
	if gotAuth != "Bearer SK" {
		t.Fatalf("Bearer auth wrong: %q", gotAuth)
	}

	keys, err := ListObjects(ctx, cfg, "shop/")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Name != "shop/pos-20260915-020000.db.enc" {
		t.Fatalf("list wrong: %+v", keys)
	}
	if gotListPrefix != "shop/" {
		t.Fatalf("list prefix wrong: %q", gotListPrefix)
	}

	rc, err := DownloadObject(ctx, cfg, "shop/k")
	if err != nil {
		t.Fatal(err)
	}
	dl, _ := io.ReadAll(rc)
	rc.Close()
	if string(dl) != "bytes" {
		t.Fatalf("download wrong: %q", dl)
	}

	if err := DeleteObjects(ctx, cfg, []string{"shop/k"}); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" || gotPath != "/storage/v1/object/b" {
		t.Fatalf("delete wrong: %s %s", gotMethod, gotPath)
	}
	if err := DeleteObjects(ctx, cfg, nil); err != nil {
		t.Fatal("empty delete must be a no-op")
	}
}

func TestAuthHeadersRequired(t *testing.T) {
	ctx := context.Background()
	bad := Config{ProjectURL: "http://x", Bucket: "b"}
	if err := UploadObject(ctx, bad, "k", strings.NewReader("x"), 1); err == nil {
		t.Fatal("missing key must fail before any network")
	}
}
