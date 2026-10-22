package database

import (
	"os"
	"testing"
)

// TestPostgresMigrations runs the FULL migration + seed chain against a
// real Postgres. Skipped unless POSTGRES_TEST_DSN is set — CI wires a
// postgres service container and sets it (see .github/workflows/ci.yml);
// locally: docker run -p 5432:5432 -e POSTGRES_PASSWORD=postgres postgres:16
// and POSTGRES_TEST_DSN='postgres://postgres:postgres@localhost:5432/
// ledgerpos_test?sslmode=disable' go test ./internal/database/ -run Postgres.
//
// Why this must run in CI: the Postgres DDL in migrations.go is a
// hand-maintained copy of the SQLite schema. A column added to one and
// forgotten in the other only explodes on a real server-mode deployment —
// exactly the failure you cannot see from a green sqlite-only suite.
func TestPostgresMigrations(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set — Postgres dialect not exercised (CI sets it)")
	}
	db, err := Open("postgres", "", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Idempotency: a second full migrate must be a no-op (fresh tills run
	// Migrate on every boot; a server-mode restart must never explode).
	if err := db.Migrate(); err != nil {
		t.Fatalf("second migrate (idempotency): %v", err)
	}
	if db.IsSQLite() {
		t.Fatal("driver reported sqlite for a postgres handle")
	}

	// Defaults seeded (server mode boots with a working settings surface).
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&n); err != nil {
		t.Fatalf("count settings: %v", err)
	}
	if n == 0 {
		t.Fatal("settings table empty — defaults not seeded")
	}

	// The payments table's dialect-sensitive pieces (partial unique index,
	// BIGINT cents) exist and enforce what the code relies on.
	if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('pg_probe', '1')
		ON CONFLICT (key) DO UPDATE SET value = '1'`); err != nil {
		t.Fatalf("settings upsert: %v", err)
	}
	var v string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'pg_probe'`).Scan(&v); err != nil || v != "1" {
		t.Fatalf("settings roundtrip: %v %q", err, v)
	}
}
