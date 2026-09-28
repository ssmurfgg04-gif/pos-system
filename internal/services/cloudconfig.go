package services

// cloudconfig.go — the central key vault client. Payment and deployment
// config (Paystack keys, callback URL, currency, feature switches) lives
// in the CLOUD's app_config table — never in the binary, never typed per
// machine, never in a per-shop .env. Rotation is one SQL UPDATE in the
// cloud; every till picks it up at its next boot / refresh with zero site
// visits.
//
// Fetch paths, in order of trust:
//   1. sync_device_config RPC — device-gated (device_id + secret_hash must
//      match an approved, non-revoked sync_devices row). Returns ALL rows,
//      including secrets (the Paystack secret key) over TLS.
//   2. anon REST read — RLS exposes only is_secret=false rows. Works for
//      fresh/unregistered devices so public config arrives immediately;
//      secrets require approval.
//
// Apply path: rows are lowercase settings keys (paystack_secret_key, …)
// and land straight in the local settings store (secrets encrypted at
// rest by the settings vault). Real environment variables still win at
// read time (GetPaystack checks env first), preserving headless overrides.
// The settings store doubles as the offline snapshot, so a till that
// loses connectivity keeps charging.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"posapp/internal/cloudmeta"
	"posapp/internal/settings"
)

// cloudConfigInterval is how often a running till re-fetches the vault.
// Boot always fetches; the loop covers long-running appliances.
const cloudConfigInterval = 6 * time.Hour

// vaultInjectedEnv records env vars this process received FROM the vault
// (not from the real environment). Rotation updates them in place — the
// alternative is a till charging on a revoked key until restart.
var vaultInjectedEnv = map[string]bool{}

// cloudConfigSkip lists settings keys the cloud may NOT overwrite — local
// identity/policy that must never be mutated from the outside.
var cloudConfigSkip = map[string]bool{
	"jwt_secret":               true, // machine-local auth root
	"sync_device_secret":       true, // machine identity
	"paystack_enabled_touched": true, // one-time migration marker
}

type cloudConfigResult struct {
	OK     bool              `json:"ok"`
	Error  string            `json:"error"`
	Config map[string]string `json:"config"`
}

// RefreshCloudConfig fetches the vault and applies it. Never fatal: the
// loop logs errors and a live till keeps its last-known config.
func (s *Service) RefreshCloudConfig() error {
	var applied int
	// 1) Public rows over anon REST — immediate, even before registration.
	if rows, err := fetchPublicConfig(); err != nil {
		log.Printf("[config] public fetch: %v", err)
	} else {
		applied += s.applyCloudConfig(rows)
	}
	// 2) Full rows (incl. secrets) via the device-gated RPC.
	c, ok := s.syncConfig()
	if ok {
		if err := s.fetchDeviceConfig(c); err != nil {
			return fmt.Errorf("device config: %w", err)
		}
	}
	_ = applied
	return nil
}

// fetchDeviceConfig pulls the approved-device config (secrets included).
func (s *Service) fetchDeviceConfig(c *syncClient) error {
	var out cloudConfigResult
	err := rpcCall(c.base, "sync_device_config", map[string]any{
		"p_device_id":   c.device,
		"p_secret_hash": c.secretHash,
	}, &out)
	if err == nil && !out.OK && strings.Contains(out.Error, "not recognized") {
		// First contact after a factory reset / re-clone: register, retry.
		if rerr := c.registerCloud(s, s.appVersion); rerr == nil {
			err = rpcCall(c.base, "sync_device_config", map[string]any{
				"p_device_id":   c.device,
				"p_secret_hash": c.secretHash,
			}, &out)
		} else {
			return fmt.Errorf("register for config: %w", rerr)
		}
	}
	if err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("%s", out.Error)
	}
	s.applyCloudConfig(out.Config)
	return nil
}

// fetchPublicConfig reads the anon-readable (non-secret) rows. Offline or
// project unreachable → error (caller logs, boot continues).
func fetchPublicConfig() (map[string]string, error) {
	if err := guardProduction(cloudBaseURL); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("select", "key,value")
	base := strings.TrimRight(cloudBaseURL, "/")
	req, err := http.NewRequest("GET", base+"/rest/v1/app_config?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("apikey", cloudmeta.AnonKey)
	req.Header.Set("Authorization", "Bearer "+cloudmeta.AnonKey)
	hc := &http.Client{Timeout: syncHTTPTimeout}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("app_config: status %d", resp.StatusCode)
	}
	var rows []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Key] = r.Value
	}
	return out, nil
}

// applyCloudConfig lands vault rows in the settings store. Returns how
// many rows changed. Cloud is the source of truth; env vars still win at
// read time. Deployment-style env names are also injected into the
// process environment when unset (PAYSTACK_SECRET_KEY & friends for
// server-mode parity and the offsite worker).
func (s *Service) applyCloudConfig(cfg map[string]string) int {
	applied := 0
	for k, v := range cfg {
		k = strings.TrimSpace(k)
		if k == "" || settings.IsMaskToken(v) {
			continue
		}
		env := settingsEnvName(k)
		if v == "" {
			// The vault removed the value (key cleared / credential
			// revoked): stop using it locally too — otherwise a deleted
			// secret keeps charging until someone touches the machine.
			if env != "" && vaultInjectedEnv[env] {
				os.Unsetenv(env)
				delete(vaultInjectedEnv, env)
			}
			if s.settings.Get(k) != "" {
				_ = s.settings.Set(k, "")
				applied++
			}
			continue
		}
		if cloudConfigSkip[k] {
			continue
		}
		if s.settings.Get(k) != v {
			_ = s.settings.Set(k, v)
			applied++
		}
		if env != "" {
			if os.Getenv(env) == "" {
				// First delivery: seed the env for env-first readers.
				_ = os.Setenv(env, v)
				vaultInjectedEnv[env] = true
			} else if vaultInjectedEnv[env] {
				// Rotation of a vault-injected value: update in place. A
				// real environment variable is never touched — the operator
				// set it outside the vault and wins until they remove it.
				_ = os.Setenv(env, v)
			}
		}
	}
	if applied > 0 {
		_ = s.settings.Set("config_synced_at", nowStamp())
		log.Printf("[config] applied %d cloud config update(s)", applied)
	}
	return applied
}

// settingsEnvName maps a settings key to its deployment env var (empty =
// no mapping). Keeping this list explicit avoids surprise overrides.
func settingsEnvName(k string) string {
	switch k {
	case "paystack_secret_key":
		return "PAYSTACK_SECRET_KEY"
	case "paystack_public_key":
		return "PAYSTACK_PUBLIC_KEY"
	case "paystack_callback_url":
		return "PAYSTACK_CALLBACK_URL"
	case "offsite_endpoint":
		return "OFFSITE_ENDPOINT"
	case "offsite_bucket":
		return "OFFSITE_BUCKET"
	case "offsite_api_key":
		return "OFFSITE_API_KEY"
	case "offsite_passphrase":
		return "OFFSITE_PASSPHRASE"
	}
	return ""
}

// CloudConfigLoop refreshes the vault every cloudConfigInterval until ctx
// ends. Never blocks a sale; errors are logged.
func (s *Service) CloudConfigLoop(ctx context.Context) {
	t := time.NewTicker(cloudConfigInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.RefreshCloudConfig(); err != nil {
				log.Printf("[config] refresh: %v", err)
			}
		}
	}
}

// StartCloudConfig performs the boot-time fetch in the background and
// starts the refresh loop. A slow/unreachable cloud never delays startup.
func (s *Service) StartCloudConfig(ctx context.Context) {
	go func() {
		if err := s.RefreshCloudConfig(); err != nil {
			log.Printf("[config] boot fetch: %v", err)
		}
	}()
	go s.CloudConfigLoop(ctx)
}
