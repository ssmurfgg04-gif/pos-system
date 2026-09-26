package settings

// crypt.go — secrets at rest are encrypted with AES-256-GCM using a key
// that lives OUTSIDE the database (a 32-byte key file in the app data
// directory, created with 0600 permissions on first use).
//
// Threat model: the SQLite file (or a backup of it) alone must not reveal
// the Paystack secret key, Daraja passkey or consumer secret. Copying the
// DB off the machine — via a stolen backup, a synced folder, or a curious
// employee with file access — yields ciphertext only.
//
// Values are stored with an "enc:v1:" prefix so plaintext rows written by
// older versions are recognisable: at store creation every legacy secret
// row is re-encrypted in place (transparent one-time upgrade, no action
// needed on existing installs). Reads decrypt transparently; the plaintext
// exists only in process memory, never in API responses (Snapshot masks
// secrets) and never in team-sync events (configSyncWhitelist excludes
// secret-class keys).

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const encPrefix = "enc:v1:"

var (
	vaultMu  sync.RWMutex
	vaultKey []byte
	vaultErr error
)

// UseKeyFile points secret encryption at a per-machine key file. Call it
// once at boot, before any Store is created. The file is created on first
// use with 0600 permissions; an unreadable key file is remembered and
// surfaces on the first secret read/write rather than crashing boot.
func UseKeyFile(path string) {
	vaultMu.Lock()
	defer vaultMu.Unlock()
	vaultKey, vaultErr = nil, nil
	key, err := loadOrCreateKey(path)
	if err != nil {
		vaultErr = fmt.Errorf("secret key file: %w", err)
		return
	}
	vaultKey = key
}

// vaultReady reports whether encryption is active (key loaded).
func vaultReady() bool {
	vaultMu.RLock()
	defer vaultMu.RUnlock()
	return vaultKey != nil
}

// loadOrCreateKey reads (or creates) a 32-byte key file.
func loadOrCreateKey(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("empty key file path")
	}
	if raw, err := os.ReadFile(path); err == nil {
		key, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if derr != nil || len(key) != 32 {
			return nil, fmt.Errorf("%s is not a valid key file", filepath.Base(path))
		}
		return key, nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func encryptSecret(plain string) (string, error) {
	vaultMu.RLock()
	key := vaultKey
	vaultMu.RUnlock()
	if key == nil {
		return "", vaultErr
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nil, nonce, []byte(plain), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(nonce) + ":" +
		base64.StdEncoding.EncodeToString(ct), nil
}

func decryptSecret(stored string) (string, error) {
	vaultMu.RLock()
	key := vaultKey
	vaultMu.RUnlock()
	rest := strings.TrimPrefix(stored, encPrefix)
	if key == nil {
		if vaultErr != nil {
			return "", vaultErr
		}
		return "", errors.New("secret is encrypted but no key file is configured")
	}
	nonceB64, ctB64, ok := strings.Cut(rest, ":")
	if !ok {
		return "", errors.New("malformed encrypted secret")
	}
	nonce, err := base64.StdEncoding.DecodeString(nonceB64)
	if err != nil {
		return "", err
	}
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", errors.New("secret decryption failed — wrong or missing key file")
	}
	return string(plain), nil
}

func isEncrypted(v string) bool { return strings.HasPrefix(v, encPrefix) }
