package settings

// update_secrets_test.go — the API save-path semantics for secret-class
// keys: a masked echo keeps the secret, an accidentally emptied field keeps
// the secret (a stray backspace must never silently break Paystack at
// checkout), removal is explicit via ClearToken, and typing a new value
// replaces. Non-secret keys still clear normally on empty.

import (
	"path/filepath"
	"testing"
)

func TestEmptyFieldPreservesStoredSecret(t *testing.T) {
	UseKeyFile(filepath.Join(t.TempDir(), "secret.key"))
	st, _, _ := testStore(t)

	if err := st.Set("paystack_secret_key", "sk_live_keepme"); err != nil {
		t.Fatalf("set: %v", err)
	}
	changed, err := st.Update(map[string]string{"paystack_secret_key": ""})
	if err != nil {
		t.Fatalf("update empty: %v", err)
	}
	for _, k := range changed {
		if k == "paystack_secret_key" {
			t.Fatal("empty value must not report the secret as changed")
		}
	}
	if got := st.Get("paystack_secret_key"); got != "sk_live_keepme" {
		t.Fatalf("empty field erased the stored secret: got %q", got)
	}
}

func TestClearTokenRemovesSecretExplicitly(t *testing.T) {
	UseKeyFile(filepath.Join(t.TempDir(), "secret.key"))
	st, _, _ := testStore(t)

	if err := st.Set("offsite_passphrase", "correct horse battery staple"); err != nil {
		t.Fatalf("set: %v", err)
	}
	changed, err := st.Update(map[string]string{"offsite_passphrase": ClearToken})
	if err != nil {
		t.Fatalf("update clear: %v", err)
	}
	found := false
	for _, k := range changed {
		if k == "offsite_passphrase" {
			found = true
		}
	}
	if !found {
		t.Fatal("explicit clear must report the key as changed")
	}
	if got := st.Get("offsite_passphrase"); got != "" {
		t.Fatalf("ClearToken did not remove the secret: got %q", got)
	}
	// Clearing again is a no-op (nothing changed).
	changed, err = st.Update(map[string]string{"offsite_passphrase": ClearToken})
	if err != nil {
		t.Fatalf("re-clear: %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("re-clear should change nothing, got %v", changed)
	}
}

func TestNewValueReplacesStoredSecret(t *testing.T) {
	UseKeyFile(filepath.Join(t.TempDir(), "secret.key"))
	st, _, _ := testStore(t)

	if err := st.Set("paystack_secret_key", "sk_live_old"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := st.Update(map[string]string{"paystack_secret_key": "sk_live_new"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got := st.Get("paystack_secret_key"); got != "sk_live_new" {
		t.Fatalf("replace failed: got %q", got)
	}
}

func TestNonSecretKeysStillClearOnEmpty(t *testing.T) {
	UseKeyFile(filepath.Join(t.TempDir(), "secret.key"))
	st, _, _ := testStore(t)

	if err := st.Set("store_phone", "0712345678"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := st.Update(map[string]string{"store_phone": ""}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := st.Get("store_phone"); got != "" {
		t.Fatalf("non-secret empty should clear the value, got %q", got)
	}
}
