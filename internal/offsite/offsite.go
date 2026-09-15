// Package offsite pushes encrypted database snapshots to any S3-compatible
// endpoint (Cloudflare R2, Backblaze B2, MinIO, AWS S3). No new module
// dependencies: AES-GCM + scrypt come from golang.org/x/crypto (already
// vendored), SigV4 signing and the S3 REST calls are hand-rolled stdlib.
package offsite

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
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

// Config is everything needed to address one bucket.
type Config struct {
	Endpoint  string // e.g. https://<account>.r2.cloudflarestorage.com
	Bucket    string
	Region    string // "auto" works on R2; real AWS region elsewhere
	AccessKey string
	SecretKey string
	Prefix    string // key prefix, e.g. shop hostname; no leading/trailing "/"
}

// ObjectKey is one listed remote object.
type ObjectKey struct {
	Name         string
	LastModified string
}

// SnapshotKey builds a safe key name: prefix/pos-YYYYMMDD-HHMMSS.db.enc
// (safe chars only, so no path escaping is needed in signing).
func SnapshotKey(prefix string, t time.Time) string {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		prefix = "shop"
	}
	return prefix + "/pos-" + t.Format("20060102-150405") + ".db.enc"
}

// EncryptFile encrypts srcPath with passphrase; returns the ciphertext path
// (srcPath + ".enc"). Wrong passphrases fail at decrypt time (GCM tag).
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

// pctEncode percent-encodes per RFC 3986 (spaces to %20, slashes to %2F).
func pctEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// signV4 attaches SigV4 header auth. query holds already-sorted raw params.
func signV4(req *http.Request, payloadHash string, cfg Config, query url.Values, t time.Time) {
	region := cfg.Region
	if region == "" {
		region = "auto"
	}
	amzDate := t.UTC().Format("20060102T150405Z")
	dateStamp := t.UTC().Format("20060102")
	host := req.URL.Host

	var qparts []string
	for k, vs := range query {
		for _, v := range vs {
			qparts = append(qparts, pctEncode(k)+"="+pctEncode(v))
		}
	}
	sort.Strings(qparts)
	canonicalQuery := strings.Join(qparts, "&")

	canonicalHeaders := "host:" + host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := req.Method + "\n" + req.URL.EscapedPath() + "\n" +
		canonicalQuery + "\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + payloadHash

	scope := dateStamp + "/" + region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" +
		hex.EncodeToString(sha256Of(canonicalRequest))

	kSecret := hmacSHA256([]byte("AWS4"+cfg.SecretKey), dateStamp)
	kRegion := hmacSHA256(kSecret, region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, toSign))

	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+cfg.AccessKey+"/"+scope+
		", SignedHeaders="+signedHeaders+", Signature="+signature)
	if req.URL.RawQuery != canonicalQuery {
		req.URL.RawQuery = canonicalQuery
	}
}

func sha256Of(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func hmacSHA256(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

var httpClient = &http.Client{Timeout: 2 * time.Minute}

func doSigned(ctx context.Context, cfg Config, method, key string, query url.Values, body io.Reader, size int64) (*http.Response, error) {
	endpoint := strings.TrimSuffix(cfg.Endpoint, "/")
	u := endpoint + "/" + cfg.Bucket + "/" + strings.TrimPrefix(key, "/")
	var payloadHash string
	if body == nil {
		payloadHash = hex.EncodeToString(sha256Of(""))
	} else {
		h := sha256.New()
		tee, err := io.ReadAll(body)
		if err != nil {
			return nil, err
		}
		h.Write(tee)
		payloadHash = hex.EncodeToString(h.Sum(nil))
		body = bytes.NewReader(tee)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if size >= 0 && body != nil {
		req.ContentLength = size
	}
	now := time.Now()
	// Encode query onto the URL before signing (signV4 rewrites canonical form).
	q := req.URL.Query()
	for k, vs := range query {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	req.URL.RawQuery = q.Encode()
	signV4(req, payloadHash, cfg, query, now)
	return httpClient.Do(req)
}

// PutObject uploads one key.
func PutObject(ctx context.Context, cfg Config, key string, body io.Reader, size int64) error {
	resp, err := doSigned(ctx, cfg, "PUT", key, nil, body, size)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("PUT %s: status %d", key, resp.StatusCode)
	}
	return nil
}

// ListObjects lists keys under prefix (newest handling is caller-side).
func ListObjects(ctx context.Context, cfg Config, prefix string) ([]ObjectKey, error) {
	q := url.Values{}
	q.Set("list-type", "2")
	q.Set("prefix", prefix)
	q.Set("max-keys", "1000")
	resp, err := doSigned(ctx, cfg, "GET", "", q, nil, -1)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("LIST: status %d", resp.StatusCode)
	}
	var out struct {
		Contents []struct {
			Key          string `xml:"Key"`
			LastModified string `xml:"LastModified"`
		} `xml:"Contents"`
	}
	if err := xml.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	keys := make([]ObjectKey, 0, len(out.Contents))
	for _, c := range out.Contents {
		keys = append(keys, ObjectKey{Name: c.Key, LastModified: c.LastModified})
	}
	return keys, nil
}

// DeleteObject removes one key.
func DeleteObject(ctx context.Context, cfg Config, key string) error {
	resp, err := doSigned(ctx, cfg, "DELETE", key, nil, nil, -1)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		return fmt.Errorf("DELETE %s: status %d", key, resp.StatusCode)
	}
	return nil
}

// GetObject downloads one key (caller closes).
func GetObject(ctx context.Context, cfg Config, key string) (io.ReadCloser, error) {
	resp, err := doSigned(ctx, cfg, "GET", key, nil, nil, -1)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: status %d", key, resp.StatusCode)
	}
	return resp.Body, nil
}

// LocalBackupDir is where snapshots land before upload.
func LocalBackupDir() string { return "backups" }
