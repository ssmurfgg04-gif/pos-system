package database

// migration_v21_test.go — v1.6.2: live tills ran roles created BEFORE
// orders.notify existed, so the WhatsApp contact interface came up
// admin-only (the cyber-café machines' complaint). v21 unions the key
// back into the system roles (Cashier above all) — union-only, custom
// roles and admin edits stay untouched.
import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestV21BackfillGrantsNotifyToSystemRoles(t *testing.T) {
	dir := t.TempDir()
	db, err := Open("sqlite", filepath.Join(dir, "v21.db"), "")
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Seed(true); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Simulate the LIVE fleet: a till DB whose Cashier role predates the
	// notify key, and a custom role the admin built by hand.
	if _, err := db.Exec(`UPDATE roles SET permissions = ? WHERE name = 'Cashier'`,
		`["pos.sell","pos.void","orders.view","payments.manual"]`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO roles (name, description, is_system, permissions) VALUES ('Night Till','custom',0,'["pos.sell"]')`); err != nil {
		t.Fatal(err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	extra := map[string][]string{
		"Owner":      {"orders.notify"},
		"Admin":      {"orders.notify"},
		"Front Desk": {"orders.notify"},
		"Branding":   {"orders.notify"},
		"Cyber":      {"orders.notify"},
		"Cashier":    {"orders.notify"},
	}
	if err := unionRolePerms(db, tx, extra); err != nil {
		t.Fatalf("v21 union: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	permsOf := func(name string) map[string]bool {
		t.Helper()
		var raw string
		if err := db.QueryRow(`SELECT permissions FROM roles WHERE name = ?`, name).Scan(&raw); err != nil {
			t.Fatalf("role %s: %v", name, err)
		}
		var list []string
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			t.Fatalf("json %s: %v", name, err)
		}
		set := map[string]bool{}
		for _, p := range list {
			set[p] = true
		}
		return set
	}

	if !permsOf("Cashier")["orders.notify"] {
		t.Fatal("v21 must grant orders.notify to the Cashier role (the till complaint)")
	}
	for _, name := range []string{"Owner", "Admin", "Front Desk", "Branding", "Cyber"} {
		if !permsOf(name)["orders.notify"] {
			t.Fatalf("v21 must keep %s able to contact customers", name)
		}
	}
	if permsOf("Night Till")["orders.notify"] {
		t.Fatal("v21 is union-only for SYSTEM roles — custom roles stay as the admin set them")
	}
	if permsOf("Designer")["orders.notify"] {
		t.Fatal("Designer intentionally keeps no customer-contact permission")
	}
}
