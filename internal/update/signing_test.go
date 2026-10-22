package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testKeypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return pub, priv
}

const testAssets = `[{"name":"ledgerpos-setup-windows-x64.exe","url":"https://github.com/x/releases/download/v1.4.0/a.exe","sha256":"aa"},{"name":"ledgerpos-linux-x64.tar.xz","url":"https://github.com/x/releases/download/v1.4.0/b.tar.xz","sha256":"bb"}]`

// TestManifestSignatureRoundTrip — the owner signs (tag, assets) with the
// CLI's exact payload format; the till verifies the same bytes.
func TestManifestSignatureRoundTrip(t *testing.T) {
	pub, priv := testKeypair(t)
	sig := ed25519.Sign(priv, ManifestSigningPayload("v1.4.0", testAssets))
	if !VerifyManifestSignature(pub, "v1.4.0", testAssets, base64.StdEncoding.EncodeToString(sig)) {
		t.Fatal("valid signature refused")
	}
	// Any edit to either row invalidates the signature.
	if VerifyManifestSignature(pub, "v1.4.1", testAssets, base64.StdEncoding.EncodeToString(sig)) {
		t.Fatal("signature verified across a CHANGED tag")
	}
	if VerifyManifestSignature(pub, "v1.4.0", testAssets+" ", base64.StdEncoding.EncodeToString(sig)) {
		t.Fatal("signature verified across a changed assets byte")
	}
	// Garbage / empty signatures never pass.
	if VerifyManifestSignature(pub, "v1.4.0", testAssets, "AAAA") {
		t.Fatal("garbage signature accepted")
	}
	if VerifyManifestSignature(pub, "v1.4.0", testAssets, "") {
		t.Fatal("empty signature accepted")
	}
}

// TestEmbeddedKeyParses — the shipped const must be a valid base64
// ed25519 public key, or every fleet binary would silently drop into
// transition mode (no verification at all).
func TestEmbeddedKeyParses(t *testing.T) {
	t.Setenv("LEDGERPOS_UPDATE_PUB_KEY", "")
	pub, ok := UpdateSigningPubKey()
	if !ok {
		t.Fatal("embedded key missing — signing would NOT be enforced in production")
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Fatalf("embedded key size = %d", len(pub))
	}
}

// manifestCloudFake serves an app_config-shaped manifest with the given
// signature row value.
func manifestCloudFake(t *testing.T, sig string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/rest/v1/app_config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		rows := []map[string]string{
			{"key": "update_manifest_tag", "value": "v1.4.0"},
			{"key": "update_manifest_assets", "value": testAssets},
		}
		if sig != "" {
			rows = append(rows, map[string]string{"key": "update_manifest_sig", "value": sig})
		}
		json.NewEncoder(w).Encode(rows)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestFetchCloudManifestSigMaterial — the fetch returns the raw signing
// material; missing sig row = empty string. Verification decisions stay
// with the caller (checker enforces when a key is compiled in).
func TestFetchCloudManifestSigMaterial(t *testing.T) {
	pub, priv := testKeypair(t)
	good := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, ManifestSigningPayload("v1.4.0", testAssets)))

	// unsigned manifest: sig empty → verify false (checker would refuse)
	m, err := FetchCloudManifest(context.Background(), manifestCloudFake(t, "").URL)
	if err != nil {
		t.Fatalf("fetch unsigned: %v", err)
	}
	if m.Sig != "" || m.Tag != "v1.4.0" || m.AssetsJSON != testAssets {
		t.Fatalf("unsigned manifest material = %+v", m)
	}
	if VerifyManifestSignature(pub, m.Tag, m.AssetsJSON, m.Sig) {
		t.Fatal("unsigned manifest verified")
	}

	// signed manifest: verify true
	m2, err := FetchCloudManifest(context.Background(), manifestCloudFake(t, good).URL)
	if err != nil {
		t.Fatalf("fetch signed: %v", err)
	}
	if !VerifyManifestSignature(pub, m2.Tag, m2.AssetsJSON, m2.Sig) {
		t.Fatal("properly signed manifest refused")
	}

	// tampered tag: verify false
	if VerifyManifestSignature(pub, "v1.4.1", m2.AssetsJSON, m2.Sig) {
		t.Fatal("signature verified across a changed tag")
	}
}
