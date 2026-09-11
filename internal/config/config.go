// Package config loads process-level configuration from the environment.
// Everything white-label (store name, branding, payments) lives in the
// settings table instead — never hardcoded here.
package config

import (
	"os"
	"strconv"
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

// Load builds the config from environment variables with safe defaults.
func Load() *Config {
	c := &Config{
		Port:         env("PORT", "3000"),
		DBDriver:     env("DB_DRIVER", "sqlite"),
		SQLitePath:   env("DB_PATH", "pos.db"),
		PostgresDSN:  env("POSTGRES_DSN", ""),
		MDNSEnabled:  envBool("MDNS_ENABLED", true),
		SeedDemoData: envBool("SEED_DEMO", true),
		GinMode:      env("GIN_MODE", "release"),
	}
	if c.DBDriver != "postgres" {
		c.DBDriver = "sqlite"
	}
	return c
}
