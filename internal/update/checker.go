package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"posapp/internal/settings"
)

// Repo is the releases home (matches scripts/create_release.py).
const Repo = "ssmurfgg04-gif/pos-system"

// Checker caches the latest known release (boot + daily refresh + manual).
type Checker struct {
	current string
	repo    string
	store   *settings.Store

	mu        sync.Mutex
	available bool
	latest    string
	notes     string
	assetURL  string
	assetName string
	assetSHA  string
	checkedAt string
	lastErr   string
	staged    string
}

// NewChecker builds the checker; current is main.version ("dev" disables).
func NewChecker(current, repo string, st *settings.Store) *Checker {
	return &Checker{current: current, repo: repo, store: st}
}

// Status is the JSON shape for GET /system/update.
func (c *Checker) Status() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{
		"current":         c.current,
		"latest":          c.latest,
		"notes":           c.notes,
		"url":             c.assetURL,
		"updateAvailable": c.available,
		"checkedAt":       c.checkedAt,
		"lastError":       c.lastErr,
		"staged":          c.staged != "",
	}
}

// Refresh checks once. The cloud manifest (app_config in the fleet
// project) is the primary channel — the owner publishes/pulls updates
// with a SQL UPDATE; GitHub Releases is the fallback when the cloud has
// no manifest or is unreachable.
func (c *Checker) Refresh(ctx context.Context) {
	if c.store.Get("update_channel") == "off" {
		return
	}
	rel, err := CheckCloudManifest(ctx, "")
	if err != nil {
		log.Printf("[update] cloud manifest: %v — falling back to GitHub", err)
		apiBase := c.store.Get("update_api_base")
		if apiBase == "" {
			apiBase = "https://api.github.com"
		}
		rel, err = Check(ctx, apiBase, c.repo)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checkedAt = time.Now().Format(time.RFC3339)
	if err != nil {
		c.lastErr = err.Error()
		log.Printf("[update] check failed: %v", err)
		return
	}
	c.lastErr = ""
	c.latest = rel.Tag
	c.notes = rel.Notes
	c.available = IsNewer(c.current, rel.Tag)
	if asset := CurrentPlatformAsset(rel); asset.URL != "" {
		c.assetURL = asset.URL
		c.assetName = asset.Name
		c.assetSHA = asset.SHA256
	}
}

// StartLoop refreshes at boot and every 24h until ctx ends.
func (c *Checker) StartLoop(ctx context.Context) {
	go c.Refresh(ctx)
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.Refresh(ctx)
		}
	}
}

// Download fetches the picked asset into the OS temp dir; remembers the
// path. Cloud-manifest assets are SHA-256 verified — a corrupted or
// tampered download is deleted and refused, never staged for install.
func (c *Checker) Download(ctx context.Context) (string, error) {
	c.mu.Lock()
	url, name, wantSHA := c.assetURL, c.assetName, c.assetSHA
	c.mu.Unlock()
	if url == "" {
		return "", fmt.Errorf("no staged update available (check first)")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	dl := &http.Client{Timeout: 10 * time.Minute}
	resp, err := dl.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download: status %d", resp.StatusCode)
	}
	dst := filepath.Join(os.TempDir(), name)
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, hasher), resp.Body); err != nil {
		f.Close()
		os.Remove(dst)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(dst)
		return "", err
	}
	if wantSHA != "" {
		if got := hex.EncodeToString(hasher.Sum(nil)); !strings.EqualFold(got, wantSHA) {
			os.Remove(dst)
			return "", fmt.Errorf("download failed integrity check (sha256 mismatch) — update refused")
		}
	}
	c.mu.Lock()
	c.staged = dst
	c.mu.Unlock()
	return dst, nil
}

// Staged returns the downloaded asset path ("", if none).
func (c *Checker) Staged() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.staged
}
