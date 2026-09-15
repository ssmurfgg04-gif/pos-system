package offsite

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestSigV4DeterministicAndBound(t *testing.T) {
	cfg := Config{Endpoint: "https://x.r2.cloudflarestorage.com", Bucket: "b", Region: "auto", AccessKey: "AK", SecretKey: "SK"}
	mk := func() *http.Request {
		r, _ := http.NewRequest("PUT", "https://x.r2.cloudflarestorage.com/b/k", strings.NewReader("hello"))
		return r
	}
	fixed := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	r1, r2 := mk(), mk()
	signV4(r1, "hash1", cfg, nil, fixed)
	signV4(r2, "hash1", cfg, nil, fixed)
	if r1.Header.Get("Authorization") != r2.Header.Get("Authorization") {
		t.Fatal("signing must be deterministic")
	}
	if !strings.Contains(r1.Header.Get("Authorization"), "Credential=AK/20260915/auto/s3/aws4_request") {
		t.Fatalf("credential scope wrong: %s", r1.Header.Get("Authorization"))
	}
	r3 := mk()
	signV4(r3, "different", cfg, nil, fixed)
	if r3.Header.Get("Authorization") == r1.Header.Get("Authorization") {
		t.Fatal("payload hash must bind the signature")
	}
}

func TestPutAndListAgainstStub(t *testing.T) {
	var gotMethod, gotKey, gotQuery, gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotKey, gotQuery, gotAuth = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		if r.Method == "GET" {
			w.Write([]byte(`<ListBucketResult><Contents><Key>b/shop/pos-20260915-020000.db.enc</Key><LastModified>2026-09-15T02:00:00Z</LastModified></Contents></ListBucketResult>`))
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cfg := Config{Endpoint: srv.URL, Bucket: "b", Region: "auto", AccessKey: "AK", SecretKey: "SK"}
	ctx := context.Background()
	if err := PutObject(ctx, cfg, "shop/k", strings.NewReader("data"), 4); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "PUT" || gotKey != "/b/shop/k" || string(gotBody) != "data" {
		t.Fatalf("PUT wrong: %s %s %q", gotMethod, gotKey, gotBody)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("missing SigV4 header: %q", gotAuth)
	}
	keys, err := ListObjects(ctx, cfg, "shop/")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Name != "b/shop/pos-20260915-020000.db.enc" {
		t.Fatalf("list wrong: %+v (query %q)", keys, gotQuery)
	}
	if !strings.Contains(gotQuery, "prefix=shop%2F") {
		t.Fatalf("prefix must be RFC3986-encoded in query: %q", gotQuery)
	}
	if err := DeleteObject(ctx, cfg, "shop/k"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "DELETE" {
		t.Fatalf("expected DELETE, got %s", gotMethod)
	}
}
