// Package config loads process-level configuration from the environment.
// Everything white-label (store name, branding, payments) lives in the
// settings table instead — never hardcoded here.
//
// ENV FILES: on boot the app reads a .env file placed NEXT TO the database
// (desktop: %APPDATA%\LedgerPOS\.env — server: alongside pos.db) and loads
// any variables not already present in the real environment. This is where
// deployment secrets belong, e.g.:
//
//	PAYSTACK_SECRET_KEY=sk_live_…
//	PAYSTACK_PUBLIC_KEY=pk_live_…
//	PAYSTACK_CALLBACK_URL=https://awesomeposs.netlify.app/
//
// Keys in a real environment variable always win over the file.
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Port          string // HTTP port (default 3000)
	DBDriver      string // "sqlite" | "postgres"
	SQLitePath    string
	PostgresDSN   string
	MDNSEnabled   bool
	SeedDemoData  bool
	GinMode       string
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// LoadEnvFile reads KEY=VALUE lines from path into the process
// environment. Existing environment variables are NEVER overwritten, and
// lines are ignored that are blank, comments, or missing "=". Missing file
// is a no-op. Returns whether a file was read.
func LoadEnvFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			continue
		}
		// Strip surrounding quotes (common in hand-edited .env files).
		if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
			v = v[1 : len(v)-1]
		}
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
	return true
}

// LoadEnvFileNear loads the .env that lives next to the given database
// file (the data directory on desktop installs).
func LoadEnvFileNear(dbPath string) {
	if dbPath != "" {
		if LoadEnvFile(filepath.Join(filepath.Dir(dbPath), ".env")) {
			return
		}
	}
	_ = LoadEnvFile(".env")
}

// Load builds the config from environment variables with safe defaults.
func Load() *Config {
	c := &Config{
		Port:         env("PORT", "3000"),
		DBDriver:     env("DB_DRIVER", "sqlite"),
		SQLitePath:   env("DB_PATH", "pos.db"),
		PostgresDSN:  env("POSTGRES_DSN", ""),
		MDNSEnabled:  envBool("MDNS_ENABLED", true),
		// Demo catalog is opt-in: real tills boot clean. SEED_DEMO=true is
		// for demos/tests only — demo data never belongs on a shop machine.
		SeedDemoData: envBool("SEED_DEMO", false),
		GinMode:      env("GIN_MODE", "release"),
	}
	if c.DBDriver != "postgres" {
		c.DBDriver = "sqlite"
	}
	return c
}
