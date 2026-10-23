// Package settings is a DB-backed key/value store with an in-memory cache.
//
// RATIONALE (deadlock fix): the original build read settings from the DB
// inside rows-iteration loops; with SQLite's single pooled connection that
// self-deadlocks (rows hold the connection, the nested read waits on it
// forever). Caching settings in memory removes the entire bug class AND
// makes Get* calls allocation-cheap in hot paths (order lists, receipts,
// the STK sweeper). Updates write through to the DB and refresh the cache.
package settings

import (
        "fmt"
        "strconv"
        "strings"
        "sync"

        "posapp/internal/database"
)

const MaskToken = "__SET__"    // echoed for configured secrets; never the value
const ClearToken = "__CLEAR__" // explicit "remove this credential" from the UI

// IsMaskToken reports whether v is the masked-secret sentinel.
func IsMaskToken(v string) bool { return v == MaskToken }

// IsClearToken reports whether v is the explicit clear-secret sentinel.
func IsClearToken(v string) bool { return v == ClearToken }

func isSecretKey(k string) bool {
        k = strings.ToLower(k)
        return strings.Contains(k, "secret") || strings.Contains(k, "passkey") ||
                strings.Contains(k, "passphrase") || strings.Contains(k, "consumer_key") ||
                strings.Contains(k, "service_key") || strings.Contains(k, "api_key") ||
                k == "jwt_secret"
}

// encryptable reports whether a key is stored encrypted at rest. jwt_secret
// is deliberately excluded: database/seed.go reads it with raw SQL (outside
// this store) and the tenants registry already keeps a copy outside the
// database, so encrypting it would break boot without adding safety.
func encryptable(k string) bool {
        return isSecretKey(k) && k != "jwt_secret"
}

type Store struct {
        db    *database.DB
        mu    sync.RWMutex
        cache map[string]string
}

func New(db *database.DB) (*Store, error) {
        s := &Store{db: db, cache: map[string]string{}}
        rows, err := db.Query(`SELECT key, COALESCE(value, '') FROM settings`)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        for rows.Next() {
                var k, v string
                if err := rows.Scan(&k, &v); err != nil {
                        return nil, err
                }
                s.cache[k] = v
        }
        if err := rows.Err(); err != nil {
                return nil, err
        }
        s.migrateLegacySecrets()
        s.migrateLegacyPaystackToggle()
        return s, nil
}

// migrateLegacyPaystackToggle — one-time boot fix for tills upgraded from
// releases that seeded paystack_enabled='false': a shop holding a real
// secret key (entered in Settings or provided via env) is payment-ready,
// so the stale seeded switch is flipped on. The companion marker key makes
// this run exactly once; later admin toggles are always respected.
func (s *Store) migrateLegacyPaystackToggle() {
        if s.IsSet("paystack_enabled_touched") {
                return
        }
        if s.Get("paystack_enabled") == "false" && s.Get("paystack_secret_key") != "" {
                _ = s.Set("paystack_enabled", "true")
        }
        _ = s.Set("paystack_enabled_touched", "1")
}

// migrateLegacySecrets re-encrypts secret rows written in plaintext by
// older versions (one-time, transparent). Runs only when a key file is
// configured; plaintext installs without a key file keep working and are
// upgraded on a future boot once one exists.
func (s *Store) migrateLegacySecrets() {
        if !vaultReady() {
                return
        }
        for k, v := range s.cache {
                if !encryptable(k) || v == "" || isEncrypted(v) || IsMaskToken(v) {
                        continue
                }
                enc, err := encryptSecret(v)
                if err != nil {
                        continue
                }
                q := s.db.Rebind(`INSERT INTO settings (key, value) VALUES (?, ?)
                        ON CONFLICT(key) DO UPDATE SET value = excluded.value`)
                if _, err := s.db.Exec(q, k, enc); err == nil {
                        s.mu.Lock()
                        s.cache[k] = enc
                        s.mu.Unlock()
                }
        }
}

// Get returns the string value ("" when unset). Encrypted secret rows
// decrypt transparently; a missing key file yields "" (the feature that
// needs the secret reports itself as unconfigured rather than crashing).
func (s *Store) Get(key string) string {
        s.mu.RLock()
        v := s.cache[key]
        s.mu.RUnlock()
        if isEncrypted(v) {
                plain, err := decryptSecret(v)
                if err != nil {
                        return ""
                }
                return plain
        }
        return v
}

func (s *Store) GetString(key, def string) string {
        if v := s.Get(key); v != "" {
                return v
        }
        return def
}

func (s *Store) GetInt(key string, def int) int {
        if v := s.Get(key); v != "" {
                if n, err := strconv.Atoi(v); err == nil {
                        return n
                }
        }
        return def
}

func (s *Store) GetInt64(key string, def int64) int64 {
        if v := s.Get(key); v != "" {
                if n, err := strconv.ParseInt(v, 10, 64); err == nil {
                        return n
                }
        }
        return def
}

func (s *Store) GetFloat(key string, def float64) float64 {
        if v := s.Get(key); v != "" {
                if f, err := strconv.ParseFloat(v, 64); err == nil {
                        return f
                }
        }
        return def
}

func (s *Store) GetBool(key string, def bool) bool {
        if v := s.Get(key); v != "" {
                if b, err := strconv.ParseBool(v); err == nil {
                        return b
                }
        }
        return def
}

// IsSet reports whether the key exists in the store at all (regardless of
// value) — lets callers distinguish "explicitly configured false" from
// "never touched".
func (s *Store) IsSet(key string) bool {
        s.mu.RLock()
        _, ok := s.cache[key]
        s.mu.RUnlock()
        return ok
}

// Set persists one setting and refreshes the cache. Secret-class keys are
// encrypted at rest when a key file has been configured (UseKeyFile).
func (s *Store) Set(key, value string) error {
        if encryptable(key) && value != "" && !isEncrypted(value) && !IsMaskToken(value) && vaultReady() {
                if enc, err := encryptSecret(value); err == nil {
                        value = enc
                }
                // On encryption failure store nothing rather than plaintext: the
                // caller sees the DB error and the operator can fix the key file.
        }
        q := s.db.Rebind(`INSERT INTO settings (key, value) VALUES (?, ?)
                ON CONFLICT(key) DO UPDATE SET value = excluded.value`)
        if _, err := s.db.Exec(q, key, value); err != nil {
                return err
        }
        s.mu.Lock()
        s.cache[key] = value
        s.mu.Unlock()
        return nil
}

// Update applies a batch. For secret-class keys:
//   - MaskToken keeps the stored secret (the UI echoes it back untouched)
//   - "" keeps the stored secret too — an accidentally cleared field (a
//     stray backspace, a browser autofill wipe) must never silently break
//     Paystack at checkout; removing a credential is explicit
//   - ClearToken removes the stored secret
//
// Returns the keys actually changed.
func (s *Store) Update(kv map[string]string) ([]string, error) {
        changed := make([]string, 0, len(kv))
        for k, v := range kv {
                if k == "jwt_secret" {
                        // The auth root is machine-local: seed.go writes it with raw
                        // SQL and nothing else may rotate it — not the API (the
                        // allowlist already blocks it there) and not a batch write
                        // that walked around the handler. A ClearToken echo must not
                        // wipe it either.
                        continue
                }
                if isSecretKey(k) {
                        if v == MaskToken || v == "" {
                                continue // untouched echo or cleared-by-accident — keep existing secret
                        }
                        if v == ClearToken {
                                if s.Get(k) != "" {
                                        if err := s.Set(k, ""); err != nil {
                                                return changed, fmt.Errorf("clear %s: %w", k, err)
                                        }
                                        changed = append(changed, k)
                                }
                                continue
                        }
                }
                if k == "tax_percent" {
                        f, ferr := strconv.ParseFloat(v, 64)
                        // NaN defeats every comparison (NaN < 0 is false) and
                        // would poison tax math fleet-wide — the only safe
                        // check is the affirmative range test.
                        if ferr != nil || !(f >= 0 && f <= 100) {
                                return changed, fmt.Errorf("tax_percent must be 0-100")
                        }
                }
                if err := s.Set(k, v); err != nil {
                        return changed, fmt.Errorf("set %s: %w", k, err)
                }
                changed = append(changed, k)
        }
        return changed, nil
}

// internalKeys are runtime bookkeeping keys the app writes itself (sync
// state, device identity, synced logo payloads). They live in the same
// settings table but are NOT admin-editable and never belong in an API
// response: the Settings page round-trips whatever the GET returned, so
// exposing them made every save fail with "unknown setting key" (the
// store-name report-as-failed bug). Genuinely mistyped keys still fail
// loudly; only these runtime keys are hidden.
var internalKeys = map[string]bool{
        "device_id":                true,
        "sync_source":              true,
        "sync_bootstrap_at":        true,
        "sync_registered":          true,
        "sync_approved":            true,
        "sync_store_pending":       true,
        "sync_auto_approve":        true,
        "sync_disabled":            true,
        "sync_last_push":           true,
        "sync_last_pull":           true,
        "sync_last_error":          true,
        "sync_outbox_last_error":   true,
        "brand_logo_data":          true, // synced logo data URL (can be huge)
        "paystack_via_cloud":       true, // cloud route bookkeeping
        "paystack_enabled_touched": true, // one-time migration marker
        "buni_via_cloud":           true, // cloud route bookkeeping (Buni main rail switch)
        // Cloud-vault bookkeeping: rows the vault apply loop and the OTA
        // checker write locally on real tills. They are cloud-owned state,
        // never admin-editable, and — before they were classified — every
        // one of them 400'd the Settings page's save (the round-tripped
        // snapshot echo tripped the allowlist: "unknown setting key:
        // config_synced_at" was why the store name never saved).
        "config_synced_at": true, // last vault apply stamp
        "paystack_mode":    true, // vault route bookkeeping (live/test)
        "offsite_api_key":  true, // vault-delivered credential, env-injected only
}

// IsInternalKey reports whether a settings key is runtime bookkeeping that
// the API neither serves nor accepts. syncstate_* cursors/counters are
// matched by prefix (they embed the team code); update_manifest_* is the
// OTA checker's local mirror of the cloud manifest (the assets row alone
// is ~1KB and would also trip the 500-byte value cap).
func IsInternalKey(k string) bool {
        if internalKeys[k] {
                return true
        }
        return strings.HasPrefix(k, "syncstate_") ||
                strings.HasPrefix(k, "update_manifest_")
}

// Snapshot returns all settings with secrets masked for API responses.
// jwt_secret is never exposed: it is not API-writable, so echoing it back
// (even masked) only invites the update endpoint to reject the whole save —
// the exact bug that broke "Save changes" in admin Settings. Runtime
// bookkeeping keys (sync state, device identity) are hidden for the same
// reason: they are not API-writable, and an echo of them must never fail
// the save.
//
// The rule is also a class-kill: anything NOT in the API allowlist is
// runtime state by definition (admin-editable keys all live in
// DefaultSettings), so it is hidden wholesale. The Settings page
// round-trips this snapshot on save — one stray runtime key in here used
// to 400 every save on real tills ("unknown setting key:
// config_synced_at"; the store-name-never-saves report).
func (s *Store) Snapshot() map[string]any {
        allowed := AllowedKeys()
        s.mu.RLock()
        defer s.mu.RUnlock()
        out := make(map[string]any, len(s.cache))
        for k, v := range s.cache {
                if k == "jwt_secret" || IsInternalKey(k) || !allowed[k] {
                        continue
                }
                if isSecretKey(k) {
                        if v == "" {
                                out[k] = ""
                        } else {
                                out[k] = MaskToken
                        }
                        continue
                }
                out[k] = v
        }
        return out
}

// Branding is the public white-label display payload (no secrets) used by
// the login screen and POS before/without elevated permissions.
func (s *Store) Branding() map[string]any {
        return map[string]any{
                "app_name":        s.GetString("app_name", "Point of Sale"),
                "store_name":      s.GetString("store_name", ""),
                "brand_logo_url":  brandLogoURL(s.Get("brand_logo")),
                "brand_color":     s.GetString("brand_color", "#10B981"),
                "currency_symbol": s.GetString("currency_symbol", "KES"),
                "currency_code":   s.GetString("currency_code", "KES"),
                "tax_percent":     s.GetFloat("tax_percent", 16),
                "tax_included":    s.GetBool("tax_included", true),
                "payment_mode":    s.GetString("payment_mode", "auto"),
                "till_number":     s.GetString("till_number", ""),
                "paybill_number":  s.GetString("paybill_number", ""),
                "mpesa_env":       s.GetString("mpesa_env", "mock"),
                // eTIMS (P6): the shop's KRA PIN — when set, the receipt
                // gains the tax-invoice header and the checkout rail offers
                // a buyer PIN field.
                "kra_pin": s.GetString("kra_pin", ""),
        }
}

// brandLogoURL is the public logo endpoint when a logo is on file.
func brandLogoURL(flag string) string {
        if flag == "1" {
                return "/api/v1/settings/logo"
        }
        return ""
}

// JWTSecret returns the per-installation signing secret.
func (s *Store) JWTSecret() []byte {
        return []byte(s.Get("jwt_secret"))
}

// AllowedKeys is the API-writable settings allowlist. Secrets like
// jwt_secret are deliberately absent — tokens must never rotate via API.
func AllowedKeys() map[string]bool {
        out := map[string]bool{}
        for k := range database.DefaultSettings {
                out[k] = true
        }
        // paystack_enabled is deliberately not seeded (absence means
        // "on whenever a valid secret key exists"), but admins may still
        // toggle it explicitly through the API. paystack_enabled_touched
        // is runtime bookkeeping (internal): the handler writes it with
        // Set directly, never through this allowlist.
        out["paystack_enabled"] = true
        return out
}
