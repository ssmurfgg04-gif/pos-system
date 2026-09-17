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

const MaskToken = "__SET__" // echoed for configured secrets; never the value

// IsMaskToken reports whether v is the masked-secret sentinel.
func IsMaskToken(v string) bool { return v == MaskToken }

func isSecretKey(k string) bool {
        k = strings.ToLower(k)
        return strings.Contains(k, "secret") || strings.Contains(k, "passkey") ||
                strings.Contains(k, "passphrase") || k == "jwt_secret"
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
        return s, rows.Err()
}

// Get returns the raw string value ("" when unset).
func (s *Store) Get(key string) string {
        s.mu.RLock()
        defer s.mu.RUnlock()
        return s.cache[key]
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

// Set persists one setting and refreshes the cache.
func (s *Store) Set(key, value string) error {
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

// Update applies a batch. Values equal to MaskToken keep the stored secret.
// Returns the keys actually changed.
func (s *Store) Update(kv map[string]string) ([]string, error) {
        changed := make([]string, 0, len(kv))
        for k, v := range kv {
                if isSecretKey(k) && v == MaskToken {
                        continue // masked echo — keep existing secret
                }
                if k == "tax_percent" {
                        f, ferr := strconv.ParseFloat(v, 64)
                        if ferr != nil || f < 0 || f > 100 {
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

// Snapshot returns all settings with secrets masked for API responses.
// jwt_secret is never exposed: it is not API-writable, so echoing it back
// (even masked) only invites the update endpoint to reject the whole save —
// the exact bug that broke "Save changes" in admin Settings.
func (s *Store) Snapshot() map[string]any {
        s.mu.RLock()
        defer s.mu.RUnlock()
        out := make(map[string]any, len(s.cache))
        for k, v := range s.cache {
                if k == "jwt_secret" {
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
        return out
}
