package main

// agent2_adoption_probe_test.go — T2 adversarial probe: shop adoption
// (ensureDefaultShop, the orphaned-pos.db recovery) and desktop data-dir
// stability across updates. These live in package main because that is
// where the boot-time recovery logic runs.
//
// Scenario matrix (client's #1 complaint: "inventory vanished after update"):
//   - registry points at a DELETED shop file → legacy pos.db must be
//     re-adopted as its own shop, users re-registered, inventory reachable
//   - fresh registry (no shops.json) → legacy pos.db adopted as default
//   - pos.db already referenced → no duplicate adoption
//   - pos.db empty + named shops exist → left alone
//   - multi-shop: orphan pos.db with data → recovered, named shops untouched

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posapp/internal/database"
	"posapp/internal/tenants"
)

// agent2LegacyDB opens pos.db at dir, migrates it (v1 schema) and — when
// populated — seeds demo users + catalog, i.e. what an old pre-tenancy
// install looks like on disk.
func agent2LegacyDB(t *testing.T, dir string, populate bool) (*database.DB, string) {
	t.Helper()
	path := filepath.Join(dir, "pos.db")
	db, err := database.Open("sqlite", path, "")
	if err != nil {
		t.Fatalf("open pos.db: %v", err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate pos.db: %v", err)
	}
	if populate {
		if err := db.Seed(true); err != nil {
			t.Fatalf("seed pos.db: %v", err)
		}
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

func agent2ProductCount(t *testing.T, db *database.DB) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM products`).Scan(&n); err != nil {
		t.Fatalf("count products: %v", err)
	}
	return n
}

// Registry points at a deleted file, but the legacy pos.db still holds the
// inventory: adoption must recover it as its own shop, re-register its
// users, and the inventory must be reachable through the pool.
func TestAgent2AdoptionRecoversOrphanWhenRegistryDangling(t *testing.T) {
	dir := t.TempDir()
	db, posPath := agent2LegacyDB(t, dir, true)
	before := agent2ProductCount(t, db)
	if before == 0 {
		t.Fatal("setup: legacy pos.db should hold demo inventory")
	}

	reg, err := tenants.Load(dir)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	// A registered shop whose DB file was deleted (the update that "ate" it).
	ghost, err := reg.CreateShop("Ghost", filepath.Join(dir, "shops", "ghost.db"), "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("ghost shop: %v", err)
	}

	got := ensureDefaultShop(reg, db, posPath, "My Shop")
	if got == "" || got == ghost.ID {
		t.Fatalf("orphan pos.db must be adopted as its own shop, got %q", got)
	}
	shop, ok := reg.Find(got)
	if !ok {
		t.Fatalf("adopted shop missing from registry")
	}
	if abs, _ := filepath.Abs(posPath); shop.DBFile != abs && shop.DBFile != posPath {
		t.Errorf("adopted shop must point at %s, got %s", posPath, shop.DBFile)
	}
	if !strings.Contains(shop.Name, "(recovered)") {
		t.Errorf("recovered shop name should mark the recovery, got %q", shop.Name)
	}
	// Users re-registered so their logins land where their work lives.
	if ids := reg.ShopsForUser("admin"); len(ids) == 0 || ids[0] != got {
		t.Errorf("legacy users not registered for the recovered shop (admin → %v, want [%s])", ids, got)
	}

	// Inventory reachable: pool opens the adopted file with all products.
	pool := tenants.NewPool(reg, "sqlite", "")
	defer pool.CloseAll()
	pool.Inject(got, db)
	reopened, err := pool.Open(got)
	if err != nil {
		t.Fatalf("open recovered shop: %v", err)
	}
	if n := agent2ProductCount(t, reopened); n != before {
		t.Errorf("recovered shop lost inventory: %d products, want %d", n, before)
	}
	// The dangling shop opens (recreated) EMPTY — no cross-contamination.
	if dangling, err := pool.Open(ghost.ID); err != nil {
		t.Errorf("dangling shop should reopen cleanly, got %v", err)
	} else if n := agent2ProductCount(t, dangling); n != 0 {
		t.Errorf("dangling shop must stay empty, has %d products", n)
	}
}

// Fresh box upgrade: no shops.json at all, legacy pos.db with data.
func TestAgent2AdoptionFreshRegistryAdoptsLegacyDB(t *testing.T) {
	dir := t.TempDir()
	db, posPath := agent2LegacyDB(t, dir, true)

	reg, err := tenants.Load(dir)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	got := ensureDefaultShop(reg, db, posPath, "My Shop")
	if got == "" {
		t.Fatal("legacy DB must be adopted when registry is empty")
	}
	shop, _ := reg.Find(got)
	if shop.Name != "My Shop" {
		t.Errorf("first adoption keeps the store name, got %q", shop.Name)
	}
	if ids := reg.ShopsForUser("admin"); len(ids) != 1 || ids[0] != got {
		t.Errorf("admin not mapped to adopted shop: %v", ids)
	}
	if _, err := os.Stat(filepath.Join(dir, "shops.json")); err != nil {
		t.Errorf("registry not persisted: %v", err)
	}
}

// Already referenced → adoption is a no-op (no duplicate shops).
func TestAgent2AdoptionNoopWhenAlreadyReferenced(t *testing.T) {
	dir := t.TempDir()
	db, posPath := agent2LegacyDB(t, dir, true)

	reg, err := tenants.Load(dir)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	existing, err := reg.CreateShop("Main", posPath, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	before := len(reg.ShopList)
	got := ensureDefaultShop(reg, db, posPath, "My Shop")
	if got != existing.ID {
		t.Errorf("expected existing shop id %s, got %s", existing.ID, got)
	}
	if len(reg.ShopList) != before {
		t.Errorf("duplicate adoption: registry grew from %d to %d shops", before, len(reg.ShopList))
	}
}

// Empty pos.db with named shops → left alone, no junk shop created.
func TestAgent2AdoptionSkipsEmptyLegacyDBWhenShopsExist(t *testing.T) {
	dir := t.TempDir()
	db, posPath := agent2LegacyDB(t, dir, false) // migrate only: 0 products, 0 users

	reg, err := tenants.Load(dir)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	a, err := reg.CreateShop("Alpha", "", "")
	if err != nil {
		t.Fatalf("shop A: %v", err)
	}
	b, err := reg.CreateShop("Beta", "", "")
	if err != nil {
		t.Fatalf("shop B: %v", err)
	}
	before := len(reg.ShopList)
	got := ensureDefaultShop(reg, db, posPath, "My Shop")
	if got != a.ID && got != b.ID {
		t.Errorf("empty pos.db must not be adopted; expected a named shop id, got %q", got)
	}
	if len(reg.ShopList) != before {
		t.Errorf("empty pos.db created a junk shop (registry %d → %d)", before, len(reg.ShopList))
	}
}

// Multi-shop box: orphan pos.db WITH data + two named shops → recovered as
// its own shop, named shops untouched.
func TestAgent2AdoptionMultiShopRecovery(t *testing.T) {
	dir := t.TempDir()
	db, posPath := agent2LegacyDB(t, dir, true)

	reg, err := tenants.Load(dir)
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	if _, err := reg.CreateShop("Alpha", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.CreateShop("Beta", "", ""); err != nil {
		t.Fatal(err)
	}
	before := len(reg.ShopList)
	got := ensureDefaultShop(reg, db, posPath, "My Shop")
	if len(reg.ShopList) != before+1 {
		t.Fatalf("orphan pos.db not recovered as its own shop (registry %d → %d)", before, len(reg.ShopList))
	}
	shop, _ := reg.Find(got)
	if !strings.Contains(shop.Name, "(recovered)") || !strings.Contains(shop.Name, "My Shop") {
		t.Errorf("recovered shop should be named %q, got %q", "My Shop (recovered)", shop.Name)
	}
	if shop.DBFile == reg.ShopList[0].DBFile || shop.DBFile == reg.ShopList[1].DBFile {
		t.Errorf("recovered shop must point at pos.db, not at another shop's file")
	}
}

// Desktop data dir: deterministic per OS user, independent of cwd and of
// transient env — an updated exe at the same location must resolve the SAME
// directory (and therefore the SAME pos.db).
func TestAgent2UserDataDirDeterministicAcrossUpdates(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", base)

	got1, err := userDataDir()
	if err != nil {
		t.Fatalf("userDataDir: %v", err)
	}
	want := filepath.Join(base, appName)
	if got1 != want {
		t.Fatalf("data dir = %s, want %s", got1, want)
	}

	// "Update": new process, noisy env, different cwd — same dir.
	t.Setenv("PORT", "9999")
	t.Setenv("DB_PATH", "/somewhere/else.db")
	got2, err := userDataDir()
	if err != nil {
		t.Fatalf("userDataDir after update: %v", err)
	}
	if got2 != want {
		t.Errorf("update resolved a different data dir: %s vs %s", got1, got2)
	}

	// Same path must always yield the same DB file (the whole point).
	dbPath := filepath.Join(got1, "pos.db")
	if !strings.HasSuffix(dbPath, filepath.Join(appName, "pos.db")) {
		t.Errorf("db path not anchored in the app data dir: %s", dbPath)
	}

	// Per-OS-user: a different user home → a different dir (isolation).
	t.Setenv("XDG_DATA_HOME", "")
	other := t.TempDir()
	t.Setenv("HOME", other)
	got3, err := userDataDir()
	if err != nil {
		t.Fatalf("userDataDir fallback: %v", err)
	}
	if got3 != filepath.Join(other, ".local", "share", appName) {
		t.Errorf("XDG-less fallback wrong: %s", got3)
	}
	if got3 == got1 {
		t.Errorf("different OS users must not share a data dir")
	}
}
