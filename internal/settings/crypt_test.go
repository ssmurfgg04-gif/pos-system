package settings

// crypt_test.go — encryption-at-rest for secret-class settings: round
// trip, plaintext-invisibility in the DB, legacy migration, mask-token
// handling, and the jwt_secret raw-SQL exclusion.

import (
	"path/filepath"
	"strings"
	"testing"

	"posapp/internal/database"
)

func testStore(t *testing.T) (*Store, *database.DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open("sqlite", filepath.Join(dir, "test.db"), "")
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st, err := New(db)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	return st, db, dir
}

func TestSecretRoundTripEncryptedAtRest(t *testing.T) {
	UseKeyFile(filepath.Join(t.TempDir(), "secret.key"))
	st, db, _ := testStore(t)

	if err := st.Set("paystack_secret_key", "sk_live_abc123"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Read path decrypts transparently.
	if got := st.Get("paystack_secret_key"); got != "sk_live_abc123" {
		t.Fatalf("Get = %q, want the plaintext secret", got)
	}

	// Stored bytes must not contain the secret.
	var raw string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'paystack_secret_key'`).Scan(&raw); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if !isEncrypted(raw) {
		t.Fatalf("stored value is not encrypted: %q", raw)
	}
	if strings.Contains(raw, "sk_live_abc123") {
		t.Fatalf("plaintext secret leaked into the database: %q", raw)
	}

	// API snapshot masks it.
	snap := st.Snapshot()
	if snap["paystack_secret_key"] != MaskToken {
		t.Fatalf("snapshot should mask secrets, got %v", snap["paystack_secret_key"])
	}
}

func TestLegacyPlaintextMigratedOnBoot(t *testing.T) {
	dir := t.TempDir()
	UseKeyFile(filepath.Join(dir, "secret.key"))

	// Simulate an older install: plaintext secret already in the DB.
	st, db, _ := testStore(t)
	q := db.Rebind(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`)
	if _, err := db.Exec(q, "mpesa_passkey", "old-plain-passkey"); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	// Recreate the store: New() runs the migration.
	st2, err := New(db)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_ = st

	var raw string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'mpesa_passkey'`).Scan(&raw); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if !isEncrypted(raw) || strings.Contains(raw, "old-plain-passkey") {
		t.Fatalf("legacy secret was not migrated: %q", raw)
	}
	if got := st2.Get("mpesa_passkey"); got != "old-plain-passkey" {
		t.Fatalf("migrated secret reads back wrong: %q", got)
	}
}

func TestMaskTokenKeepsStoredSecret(t *testing.T) {
	UseKeyFile(filepath.Join(t.TempDir(), "secret.key"))
	st, _, _ := testStore(t)

	if err := st.Set("paystack_secret_key", "sk_live_first"); err != nil {
		t.Fatalf("set: %v", err)
	}
	changed, err := st.Update(map[string]string{"paystack_secret_key": MaskToken})
	if err != nil {
		t.Fatalf("update mask: %v", err)
	}
	for _, k := range changed {
		if k == "paystack_secret_key" {
			t.Fatal("mask token must not overwrite the stored secret")
		}
	}
	if got := st.Get("paystack_secret_key"); got != "sk_live_first" {
		t.Fatalf("stored secret changed: %q", got)
	}
}

func TestJWTSecretNeverEncrypted(t *testing.T) {
	UseKeyFile(filepath.Join(t.TempDir(), "secret.key"))
	st, db, _ := testStore(t)

	// seed.go reads jwt_secret with raw SQL — it must stay plaintext.
	if err := st.Set("jwt_secret", "hexseedvalue"); err != nil {
		t.Fatalf("set: %v", err)
	}
	var raw string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'jwt_secret'`).Scan(&raw); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if isEncrypted(raw) {
		t.Fatal("jwt_secret must not be encrypted — seed.go reads it with raw SQL")
	}
	if got := st.Get("jwt_secret"); got != "hexseedvalue" {
		t.Fatalf("jwt_secret reads back wrong: %q", got)
	}
}

func TestMissingKeyFileYieldsUnconfigured(t *testing.T) {
	// No UseKeyFile call: encrypted rows cannot decrypt → Get returns "".
	st, _, _ := testStore(t)
	if got := st.Get("paystack_secret_key"); got != "" {
		t.Fatalf("expected empty read without a key file, got %q", got)
	}
}
