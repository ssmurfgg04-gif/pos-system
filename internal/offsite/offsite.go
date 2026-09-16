// Package offsite pushes encrypted database snapshots to Supabase Storage
// over plain Bearer-auth REST (no SigV4, no clock-skew failures on shop PCs
// with drifting clocks). No new module dependencies: AES-GCM + scrypt come
// from golang.org/x/crypto (already vendored), the rest is stdlib.
//
// Blast radius is one shop: use one free Supabase project per shop, so a
// leaked key opens that shop's bucket only.
package offsite

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"
)

// Magic + layout of encrypted snapshots:
// "LPB1" | salt[16] | nonce[12] | AES-256-GCM ciphertext.
var fileMagic = []byte("LPB1")

const (
	scryptN      = 32768
	scryptR      = 8
	scryptP      = 1
	scryptKeyLen = 32
)

// Config is everything needed to address one bucket. Key is the project's
// service_role secret (masked in our settings API, never logged).
type Config struct {
	ProjectURL string // e.g. https://xyzcompany.supabase.co
	Bucket     string
	Key        string
	Prefix     string // key prefix, e.g. shop hostname; no leading/trailing "/"
}

// ObjectKey is one listed remote object.
type ObjectKey struct {
	Name         string
	LastModified string
}

// SnapshotKey builds a safe key name: prefix/pos-YYYYMMDD-HHMMSS.db.enc
// (safe chars only, so no URL escaping is needed).
func SnapshotKey(prefix string, t time.Time) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "shop"
	}
	return prefix + "/pos-" + t.Format("20060102-150405") + ".db.enc"
}

// EncryptFile encrypts srcPath with passphrase and returns the path of the
// ciphertext file. Wrong passphrases fail at decrypt time (GCM tag).
func EncryptFile(srcPath, passphrase string) (string, error) {
	plain, err := os.ReadFile(srcPath)
	if err != nil {
		return "", err
	}
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return "", err
	}
	key, err := scrypt.Key([]byte(passphrase), salt[:], scryptN, scryptR, scryptP, scryptKeyLen)
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
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	out := make([]byte, 0, len(fileMagic)+len(salt)+len(nonce)+len(plain)+gcm.Overhead())
	out = append(out, fileMagic...)
	out = append(out, salt[:]...)
	out = append(out, nonce[:]...)
	out = gcm.Seal(out, nonce[:], plain, nil)
	dst := srcPath + ".enc"
	if err := os.WriteFile(dst, out, 0o600); err != nil {
		return "", err
	}
	return dst, nil
}

// DecryptFile reverses EncryptFile into dstPath.
func DecryptFile(encPath, passphrase, dstPath string) error {
	raw, err := os.ReadFile(encPath)
	if err != nil {
		return err
	}
	if len(raw) < len(fileMagic)+16+12+1 || !bytes.Equal(raw[:len(fileMagic)], fileMagic) {
		return fmt.Errorf("not a LedgerPOS backup file")
	}
	rest := raw[len(fileMagic):]
	salt, nonce, ct := rest[:16], rest[16:28], rest[28:]
	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return fmt.Errorf("decrypt failed (wrong passphrase?): %w", err)
	}
	return os.WriteFile(dstPath, plain, 0o600)
}

var httpClient = &http.Client{Timeout: 10 * time.Minute}

func storageBase(cfg Config) string {
	return strings.TrimSuffix(cfg.ProjectURL, "/") + "/storage/v1"
}

func authHeaders(cfg Config) (http.Header, error) {
	if cfg.Key == "" {
		return nil, fmt.Errorf("supabase API key required")
	}
	h := http.Header{}
	h.Set("apikey", cfg.Key)
	h.Set("Authorization", "Bearer "+cfg.Key)
	return h, nil
}

// UploadObject PUTs one key (x-upsert so retries overwrite cleanly).
func UploadObject(ctx context.Context, cfg Config, key string, body io.Reader, size int64) error {
	h, err := authHeaders(cfg)
	if err != nil {
		return err
	}
	h.Set("Content-Type", "application/octet-stream")
	h.Set("x-upsert", "true")
	req, err := http.NewRequestWithContext(ctx, "POST",
		storageBase(cfg)+"/object/"+cfg.Bucket+"/"+strings.TrimPrefix(key, "/"), body)
	if err != nil {
		return err
	}
	req.Header = h
	if size >= 0 {
		req.ContentLength = size
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("upload %s: status %d", key, resp.StatusCode)
	}
	return nil
}

// DownloadObject fetches one key from a private bucket (caller closes).
func DownloadObject(ctx context.Context, cfg Config, key string) (io.ReadCloser, error) {
	h, err := authHeaders(cfg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET",
		storageBase(cfg)+"/object/authenticated/"+cfg.Bucket+"/"+strings.TrimPrefix(key, "/"), nil)
	if err != nil {
		return nil, err
	}
	req.Header = h
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: status %d", key, resp.StatusCode)
	}
	return resp.Body, nil
}

// ListObjects lists keys under prefix, newest-first by name (our timestamped
// names sort chronologically). Returned names are used for deletes as-is,
// falling back to prefix-join when the API returns bare filenames.
func ListObjects(ctx context.Context, cfg Config, prefix string) ([]ObjectKey, error) {
	h, err := authHeaders(cfg)
	if err != nil {
		return nil, err
	}
	h.Set("Content-Type", "application/json")
	payload, _ := json.Marshal(map[string]any{
		"prefix": prefix, "limit": 1000, "offset": 0,
		"sortBy": map[string]string{"column": "name", "order": "desc"},
	})
	req, err := http.NewRequestWithContext(ctx, "POST",
		storageBase(cfg)+"/object/list/"+cfg.Bucket, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header = h
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("list: status %d", resp.StatusCode)
	}
	var raw []struct {
		Name         string `json:"name"`
		UpdatedAt    string `json:"updated_at"`
		LastModified string `json:"last_modified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]ObjectKey, 0, len(raw))
	for _, r := range raw {
		name := r.Name
		if !strings.HasPrefix(name, strings.TrimSuffix(prefix, "/")) {
			name = strings.TrimSuffix(prefix, "/") + "/" + strings.TrimPrefix(name, "/")
		}
		when := r.LastModified
		if when == "" {
			when = r.UpdatedAt
		}
		out = append(out, ObjectKey{Name: name, LastModified: when})
	}
	return out, nil
}

// DeleteObjects removes keys in one call (API takes {"prefixes": [...]}).
func DeleteObjects(ctx context.Context, cfg Config, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	h, err := authHeaders(cfg)
	if err != nil {
		return err
	}
	h.Set("Content-Type", "application/json")
	payload, _ := json.Marshal(map[string]any{"prefixes": keys})
	req, err := http.NewRequestWithContext(ctx, "DELETE",
		storageBase(cfg)+"/object/"+cfg.Bucket, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header = h
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("delete: status %d", resp.StatusCode)
	}
	return nil
}

// LocalBackupDir is where snapshots land before upload.
func LocalBackupDir() string { return "backups" }
