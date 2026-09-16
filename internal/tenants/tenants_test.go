package tenants

import (
	"path/filepath"
	"testing"
)

func TestRegistryRouting(t *testing.T) {
	dir := t.TempDir()
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.CreateShop("Shop A", filepath.Join(dir, "a.db"), "2026-09-16T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.CreateShop("Shop B", filepath.Join(dir, "b.db"), "2026-09-16T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatal("shop ids must differ")
	}
	if err := r.RegisterUser("Alice", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterUser("alice", b.ID); err == nil {
		t.Fatal("duplicate username (case-insensitive) must fail")
	}
	if err := r.RegisterUser("Bob", b.ID); err != nil {
		t.Fatal(err)
	}
	if got, ok := r.ShopForUser("ALICE"); !ok || got != a.ID {
		t.Fatalf("alice should route to A, got %q %v", got, ok)
	}
	if _, ok := r.ShopForUser("ghost"); ok {
		t.Fatal("unknown user must not route")
	}
	if err := r.RegisterUser("", a.ID); err == nil {
		t.Fatal("empty username must fail")
	}
	if err := r.RegisterUser("Zed", "nope"); err == nil {
		t.Fatal("unknown shop must fail")
	}
	// Survives reload from disk.
	r2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := r2.ShopForUser("bob"); !ok || got != b.ID {
		t.Fatal("registry must persist")
	}
	if _, ok := r2.Find(a.ID); !ok {
		t.Fatal("Find must locate shop A")
	}
}

func TestPoolCachesAndMigrates(t *testing.T) {
	dir := t.TempDir()
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.CreateShop("Shop A", filepath.Join(dir, "a.db"), "2026-09-16T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	p := NewPool(r, "sqlite", "")
	db1, err := p.Open(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	db2, err := p.Open(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if db1 != db2 {
		t.Fatal("pool must return the cached handle")
	}
	// Migrated: settings table exists.
	var n int
	if err := db1.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&n); err != nil {
		t.Fatalf("shop DB must be migrated: %v", err)
	}
	if _, err := p.Open("sh-deadbeef"); err == nil {
		t.Fatal("unknown shop must fail")
	}
	p.CloseAll()
}
