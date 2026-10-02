package database

// purge_test.go — P1: the v13 demo-account purge must disable ONLY the
// accounts still holding the public seed credentials, keep rotated
// accounts alive, preserve all history (deactivation, not deletion), and
// journal every action for the documented rollback.

import (
	"path/filepath"
	"testing"

	"posapp/internal/hash"
)

func TestV13PurgeDisablesOnlyUnrotatedDemoAccounts(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("sqlite", filepath.Join(dir, "purge.db"), "")
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	defer db.Close()
	// Simulate a REAL v1.1.x database: demo users seeded, one of them
	// later rotated by its human (the owner took over 'admin').
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	if err := db.Seed(true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// The owner rotated admin's password AND PIN via the app (the hashes
	// change — metadata alone would not fool bcrypt verification).
	newPw, err := hash.Password("rotated-pass-1")
	if err != nil {
		t.Fatal(err)
	}
	newPin, err := hash.Password("9870")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET password_hash = ?, pin_hash = ?, must_rotate = 0 WHERE username = 'admin'`, newPw, newPin); err != nil {
		t.Fatal(err)
	}

	// Run the v13 Go step directly (SQL half is idempotent CREATE IF NOT
	// EXISTS; the purge hook is what needs proving).
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := purgeDemoUsersV13(db, tx); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// cashier/designer (unrotated) are disabled; admin (rotated) survives.
	var active int
	if err := db.QueryRow(`SELECT is_active FROM users WHERE username='admin'`).Scan(&active); err != nil || active != 1 {
		t.Fatal("rotated admin account must be left untouched")
	}
	for _, u := range []string{"cashier", "designer"} {
		if err := db.QueryRow(`SELECT is_active FROM users WHERE username=?`, u).Scan(&active); err != nil || active != 0 {
			t.Fatalf("unrotated demo account %s must be disabled", u)
		}
	}
	// History intact: rows still exist (deactivated, never deleted).
	var total int
	db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&total)
	if total != 3 {
		t.Fatalf("user rows = %d, want 3 (deactivation, not deletion)", total)
	}
	// Journal present for the rollback procedure.
	var journal int
	db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='DEMO_ACCOUNT_DISABLED'`).Scan(&journal)
	if journal != 2 {
		t.Fatalf("audit journal rows = %d, want 2", journal)
	}

	// Idempotent: running the purge again changes nothing.
	tx2, _ := db.Begin()
	if err := purgeDemoUsersV13(db, tx2); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='DEMO_ACCOUNT_DISABLED'`).Scan(&journal)
	if journal != 2 {
		t.Fatalf("journal after re-run = %d, want 2 (idempotent)", journal)
	}
}

func TestV13PurgeGuardrailReactivatesLastLogin(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("sqlite", filepath.Join(dir, "guard.db"), "")
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	// A box that ONLY ever used the demo accounts (nothing rotated).
	if err := db.Seed(true); err != nil {
		t.Fatal(err)
	}
	tx, _ := db.Begin()
	if err := purgeDemoUsersV13(db, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// The guardrail re-enables demo admin with forced rotation so the box
	// still has exactly one working login that must rotate immediately.
	var active, rotate int
	db.QueryRow(`SELECT is_active, must_rotate FROM users WHERE username='admin'`).Scan(&active, &rotate)
	if active != 1 || rotate != 1 {
		t.Fatalf("guardrail admin active=%d rotate=%d, want 1/1", active, rotate)
	}
	var others int
	db.QueryRow(`SELECT COUNT(*) FROM users WHERE username != 'admin' AND is_active = 1`).Scan(&others)
	if others != 0 {
		t.Fatalf("other demo accounts still active: %d", others)
	}
}
