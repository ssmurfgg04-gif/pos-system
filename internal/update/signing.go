package update

// signing.go — ed25519 signatures for the cloud OTA manifest.
//
// THREAT MODEL (why sha256 alone is not enough): the manifest lives in the
// anon-readable app_config table. TLS protects the pipe, but whoever can
// write those rows (a leaked dashboard token, a compromised owner account)
// controls BOTH the asset URL and its sha256 — a malicious binary verifies
// perfectly. The signature closes that: the signing key lives with the
// release process, the PUBLIC key is baked into every till binary, and an
// auto-OTA install requires a valid signature over the exact manifest
// bytes. GitHub-fallback releases stay manual-install-only (unchanged).
//
// Payload format (deliberately trivial and version-tagged):
//     "update_manifest_v1\n" + tag + "\n" + assetsJSON
// signing the RAW stored strings (not a re-canonicalized structure) means
// the sig covers exactly what the owner signed — any edit to either row
// invalidates it.
//
// Transition: when UpdateSigningPubKey() returns "" (no key embedded),
// verification is skipped with a loud log line — that state exists only
// for fleets that predate signing. This fleet ships a key, so manifests
// must carry update_manifest_sig.

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"strings"
)

// signingPayloadVersion guards the payload format so an old binary can
// never mis-verify a newer envelope.
const signingPayloadVersion = "update_manifest_v1"

// embeddedSigningPubKey is the fleet's manifest-signing PUBLIC key
// (base64 std ed25519). Public data — safe in the binary. Rotate only by
// shipping a release (tills trust the key compiled into them).
const embeddedSigningPubKey = "XDXBwPx5mDrjJabYhDzOEyHI7ILepLqFeaS1aTqQ4Co="

// UpdateSigningPubKey resolves the verification key: the embedded const
// wins; LEDGERPOS_UPDATE_PUB_KEY env overrides (tests, key rotation
// rehearsals). Empty = signing not enforced (transition mode).
func UpdateSigningPubKey() (ed25519.PublicKey, bool) {
	b64 := strings.TrimSpace(os.Getenv("LEDGERPOS_UPDATE_PUB_KEY"))
	if b64 == "" {
		b64 = embeddedSigningPubKey
	}
	if b64 == "" {
		return nil, false
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, false
	}
	return ed25519.PublicKey(raw), true
}

// ManifestSigningPayload builds the exact byte string the owner signs
// (and the till verifies): version, tag, assets — newline separated.
func ManifestSigningPayload(tag, assetsJSON string) []byte {
	return []byte(signingPayloadVersion + "\n" + tag + "\n" + assetsJSON)
}

// VerifyManifestSignature checks a base64 ed25519 signature over the
// manifest payload with the given key.
func VerifyManifestSignature(pub ed25519.PublicKey, tag, assetsJSON, sigB64 string) bool {
	if pub == nil || strings.TrimSpace(sigB64) == "" {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, ManifestSigningPayload(tag, assetsJSON), sig)
}

// ErrUnsignedManifest is returned when a manifest lacks a valid signature
// while signing is enforced.
var ErrUnsignedManifest = errors.New("update manifest is unsigned or has a bad signature — auto-OTA refused")
