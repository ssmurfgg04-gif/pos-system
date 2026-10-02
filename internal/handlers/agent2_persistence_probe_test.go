package handlers_test

// agent2_persistence_probe_test.go — T2 adversarial probe: DATA PERSISTENCE
// ACROSS UPDATES. Targets the client's #1 complaint (inventory vanishing
// after an update):
//
//   1. Update pipeline  — PreUpdateSnapshot coverage (all shop DBs + registry
//      + vault key), crash-mid-update safety, undo restores data, undo
//      snapshots first, restore retry semantics.
//   2. Migrations       — zero destructive DDL, idempotency, old-schema DBs
//      migrate without table rebuilds.
//   3. Tenant isolation — file-per-shop; one shop's rows never appear in
//      another shop's queries; PIN-routing cannot land in an empty shop.
//
// Tests named TestAgent2* assert the CORRECT invariant; a failure here is a
// found bug, not a flaky test. Expect two known failures until fixed:
//   - shop DBs inside <dataDir>/shops/ are restored to the wrong directory
//     by the undo flow (snapshot stores basename only)
//   - RestorePending deletes the manifest even when a file copy failed
//     (its own comment promises a retry)

import (
        "encoding/json"
        "os"
        "path/filepath"
        "strings"
        "testing"

        "github.com/gin-gonic/gin"

        "posapp/internal/database"
        "posapp/internal/handlers"
        "posapp/internal/printer"
        "posapp/internal/router"
        "posapp/internal/services"
        "posapp/internal/settings"
        "posapp/internal/tenants"
        "posapp/internal/ws"
)

// ---- harness -----------------------------------------------------------

type agent2Stack struct {
        dir      string // data dir (pos.db, shops.json, secret.key, shops/)
        engine   *gin.Engine
        h        *handlers.H
        db       *database.DB // default shop handle (dir/pos.db)
        reg      *tenants.Registry
        pool     *services.ShopPool
        defShop  string
        shopB    string // app-created shop (file: dir/shops/<id>.db)
        shopC    string // app-created shop (file: dir/shops/<id>.db)
        adminTok string
}

func agent2DefPath(s *agent2Stack) string { return filepath.Join(s.dir, "pos.db") }

// newAgent2Stack mirrors main.go's desktop boot: pos.db adopted as default
// shop, two more shops created through the app (shops/<id>.db files), the
// vault key file next to the DB, and a rotated admin session.
func newAgent2Stack(t *testing.T) *agent2Stack {
        t.Helper()
        gin.SetMode(gin.TestMode)
        s := &agent2Stack{dir: t.TempDir()}

        settings.UseKeyFile(filepath.Join(s.dir, "secret.key")) // creates vault file (main.go order)
        db, err := database.Open("sqlite", agent2DefPath(s), "")
        if err != nil {
                t.Fatalf("db: %v", err)
        }
        if err := db.Migrate(); err != nil {
                t.Fatalf("migrate: %v", err)
        }
        if err := db.Seed(true); err != nil {
                t.Fatalf("seed: %v", err)
        }
        s.db = db

        reg, err := tenants.Load(s.dir)
        if err != nil {
                t.Fatalf("tenants: %v", err)
        }
        s.reg = reg
        def, err := reg.CreateShop("Main Street", agent2DefPath(s), "2026-01-01T00:00:00Z")
        if err != nil {
                t.Fatalf("adopt default shop: %v", err)
        }
        s.defShop = def.ID
        for _, u := range []string{"admin", "cashier", "designer"} {
                if err := reg.RegisterUser(u, def.ID); err != nil {
                        t.Fatalf("register %s: %v", u, err)
                }
        }
        // App-created shops (signup/join path): dbFile "" → shops/<id>.db.
        b, err := reg.CreateShop("Branch B", "", "")
        if err != nil {
                t.Fatalf("shop B: %v", err)
        }
        c, err := reg.CreateShop("Branch C", "", "")
        if err != nil {
                t.Fatalf("shop C: %v", err)
        }
        s.shopB, s.shopC = b.ID, c.ID

        masterSecret := reg.EnsureJWTSecret("")
        hub := ws.NewHub(func() []byte { return []byte(masterSecret) })
        go hub.Run()
        dbPool := tenants.NewPool(reg, "sqlite", "")
        dbPool.Inject(def.ID, db)
        s.pool = services.NewShopPool(dbPool, hub, nil)
        svc, err := s.pool.Service(def.ID)
        if err != nil {
                t.Fatalf("default service: %v", err)
        }
        pw := printer.NewWorker(db, svc.Settings())
        s.pool.SetPrinter(pw)
        h := handlers.New(db, svc.Settings(), svc, hub, pw)
        h.Tenants = reg
        h.Shops = s.pool
        h.DefaultShop = def.ID
        h.MasterSecret = []byte(masterSecret)
        h.Version = "v1.2.3"
        s.h = h
        t.Cleanup(func() { s.pool.CloseAll() })
        s.engine = router.New(h, nil)

        tok := login(t, s.engine, "admin", "admin123")
        rotateFresh(t, s.engine, tok)
        s.adminTok = login(t, s.engine, "admin", rotatedPassword)
        return s
}

// agent2InsertProduct stocks one product directly in the given DB handle
// (the way inventory exists before any update runs).
func agent2InsertProduct(t *testing.T, db *database.DB, sku, name string, qty int64) int64 {
        t.Helper()
        var cat int64
        err := db.QueryRow(`SELECT id FROM categories ORDER BY id LIMIT 1`).Scan(&cat)
        if err != nil {
                if _, err := db.Exec(db.Rebind(`INSERT INTO categories (name, slug) VALUES (?, 'agent2-cat')`), "Agent2"); err != nil {
                        t.Fatalf("category: %v", err)
                }
                if err := db.QueryRow(`SELECT id FROM categories WHERE slug='agent2-cat'`).Scan(&cat); err != nil {
                        t.Fatalf("category id: %v", err)
                }
        }
        res, err := db.Exec(db.Rebind(`INSERT INTO products (sku, name, category_id, price_cents, cost_cents, stock_qty)
                VALUES (?, ?, ?, 1000, 400, ?)`), sku, name, cat, qty)
        if err != nil {
                t.Fatalf("product %s: %v", name, err)
        }
        id, _ := res.LastInsertId()
        return id
}

// agent2OpenDB opens a snapshot/DB file read-only-ish for verification.
func agent2OpenDB(t *testing.T, path string) *database.DB {
        t.Helper()
        db, err := database.Open("sqlite", path, "")
        if err != nil {
                t.Fatalf("open %s: %v", path, err)
        }
        return db
}

func agent2CountNamed(t *testing.T, path, name string) int64 {
        t.Helper()
        db := agent2OpenDB(t, path)
        defer db.Close()
        var n int64
        if err := db.QueryRow(db.Rebind(`SELECT COUNT(*) FROM products WHERE name = ?`), name).Scan(&n); err != nil {
                t.Fatalf("count %s in %s: %v", name, path, err)
        }
        return n
}

func agent2SchemaVersion(t *testing.T, path string) int64 {
        t.Helper()
        db := agent2OpenDB(t, path)
        defer db.Close()
        var v int64
        if err := db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&v); err != nil {
                t.Fatalf("schema version %s: %v", path, err)
        }
        return v
}

func agent2ShopDBPath(t *testing.T, s *agent2Stack, shopID string) string {
        t.Helper()
        shop, ok := s.reg.Find(shopID)
        if !ok {
                t.Fatalf("shop %s vanished from registry", shopID)
        }
        return shop.DBFile
}

// ---- 1. pre-update snapshot coverage -----------------------------------

// The pre-update archive must cover EVERY shop DB (default + app-created
// shops under shops/), the tenants registry, and the vault key — with the
// snapshot contents actually readable and complete.

// agent2SnapName mirrors PreUpdateSnapshot's path-shaped snap naming:
// shops/x.db → shops_x.db.snap (path-exact undo).
func agent2SnapName(dir, p string) string {
        rel, _ := filepath.Rel(dir, p)
        return strings.ReplaceAll(rel, string(filepath.Separator), "_") + ".snap"
}

func TestAgent2PreUpdateSnapshotCoversEverything(t *testing.T) {
        s := newAgent2Stack(t)
        agent2InsertProduct(t, s.db, "SNAP-1", "SnapCola", 10)
        dbB, err := s.pool.DB(s.shopB)
        if err != nil {
                t.Fatalf("shop B: %v", err)
        }
        agent2InsertProduct(t, dbB, "SNAP-2", "SnapChips", 5)
        dbC, err := s.pool.DB(s.shopC)
        if err != nil {
                t.Fatalf("shop C: %v", err)
        }
        agent2InsertProduct(t, dbC, "SNAP-3", "SnapTea", 7)
        keyRaw, err := os.ReadFile(filepath.Join(s.dir, "secret.key"))
        if err != nil {
                t.Fatalf("vault key: %v", err)
        }

        archive, err := s.h.PreUpdateSnapshot("v1.2.4")
        if err != nil {
                t.Fatalf("PreUpdateSnapshot: %v", err)
        }
        if !strings.HasPrefix(archive, filepath.Join(s.dir, "backups", "pre-update-")) {
                t.Fatalf("archive in wrong place: %s", archive)
        }

        // Every shop DB snapshotted, incl. shops/<id>.db files. Naming keeps
        // the path shape (shops/x.db → shops_x.db.snap) so undo is path-exact.
        for _, p := range []string{agent2DefPath(s), agent2ShopDBPath(t, s, s.shopB), agent2ShopDBPath(t, s, s.shopC)} {
                snapName := agent2SnapName(s.dir, p)
                if _, err := os.Stat(filepath.Join(archive, snapName)); err != nil {
                        t.Errorf("pre-update archive missing snapshot for %s (want %s): %v", p, snapName, err)
                }
        }
        // Registry + vault key.
        for _, name := range []string{"shops.json", "secret.key"} {
                if _, err := os.Stat(filepath.Join(archive, name)); err != nil {
                        t.Errorf("archive missing %s: %v", name, err)
                }
        }
        gotKey, err := os.ReadFile(filepath.Join(archive, "secret.key"))
        if err != nil || string(gotKey) != string(keyRaw) {
                t.Errorf("vault key not archived verbatim (err=%v)", err)
        }

        // Snapshots must be complete DBs: the inventory is inside them.
        if n := agent2CountNamed(t, filepath.Join(archive, "pos.db.snap"), "SnapCola"); n != 1 {
                t.Errorf("pos.db snapshot lost SnapCola (count=%d)", n)
        }
        if n := agent2CountNamed(t, filepath.Join(archive, agent2SnapName(s.dir, agent2ShopDBPath(t, s, s.shopB))), "SnapChips"); n != 1 {
                t.Errorf("shop B snapshot lost SnapChips (count=%d)", n)
        }
        if n := agent2CountNamed(t, filepath.Join(archive, agent2SnapName(s.dir, agent2ShopDBPath(t, s, s.shopC))), "SnapTea"); n != 1 {
                t.Errorf("shop C snapshot lost SnapTea (count=%d)", n)
        }

        // Snapshot must not disturb the live DBs (VACUUM INTO is read-only).
        agent2InsertProduct(t, s.db, "SNAP-4", "SnapAfter", 1)
        if n := agent2CountNamed(t, agent2DefPath(s), "SnapAfter"); n != 1 {
                t.Errorf("live DB unusable after snapshot")
        }
}

// Fail-closed contract: if ANY shop DB cannot be opened, the snapshot must
// fail (UpdateInstall then aborts the update) — never silently skip a shop
// and bless the update anyway.
func TestAgent2PreUpdateSnapshotFailsClosedOnUnopenableShop(t *testing.T) {
        s := newAgent2Stack(t)
        ghost, err := s.reg.CreateShop("Ghost", filepath.Join(s.dir, "ghostdir", "ghost.db"), "")
        if err != nil {
                t.Fatalf("ghost shop: %v", err)
        }
        // Make the shop's DB unopenable: its directory is gone (e.g. a hung
        // handle, AV lock, or disk hiccup on the real box).
        if err := os.RemoveAll(filepath.Join(s.dir, "ghostdir")); err != nil {
                t.Fatalf("remove ghostdir: %v", err)
        }

        archive, err := s.h.PreUpdateSnapshot("v1.2.4")
        if err == nil {
                if _, serr := os.Stat(filepath.Join(archive, "pos.db.snap")); serr == nil {
                        t.Errorf("BUG T2-3: snapshot succeeded while shop %s (ghost) was unopenable — "+
                                "the shop was silently excluded and the update would proceed unprotected "+
                                "(archive=%s)", ghost.ID, archive)
                }
        }
        // On failure no half archive may be presented as usable.
        _ = archive
}

// ---- 2. undo update: restore must bring ALL data back -------------------

// The end-to-end client scenario: update runs, inventory vanishes, operator
// hits "Undo update", machine reboots. Every shop's inventory must come
// back from the pre-update archive at the path the registry points to.
func TestAgent2UndoRestoresAllShopDatabases(t *testing.T) {
        s := newAgent2Stack(t)
        agent2InsertProduct(t, s.db, "UND-1", "UndoCola", 10)
        dbB, err := s.pool.DB(s.shopB)
        if err != nil {
                t.Fatalf("shop B: %v", err)
        }
        agent2InsertProduct(t, dbB, "UND-2", "UndoChips", 22)

        preSchema := agent2SchemaVersion(t, agent2DefPath(s))
        archive, err := s.h.PreUpdateSnapshot("v1.2.3")
        if err != nil {
                t.Fatalf("pre-update snapshot: %v", err)
        }

        // ---- the bad update: inventory vanishes everywhere + schema bumps.
        if _, err := s.db.Exec(`DELETE FROM products`); err != nil {
                t.Fatalf("simulate wipe: %v", err)
        }
        if _, err := dbB.Exec(`DELETE FROM products`); err != nil {
                t.Fatalf("simulate wipe B: %v", err)
        }
        for _, h := range []*database.DB{s.db, dbB} {
                if _, err := h.Exec(`INSERT INTO schema_migrations (version) VALUES (999)`); err != nil {
                        t.Fatalf("simulate schema bump: %v", err)
                }
        }
        if n := agent2CountNamed(t, agent2DefPath(s), "UndoCola"); n != 0 {
                t.Fatal("setup: wipe failed")
        }

        // ---- operator hits Undo update.
        w := do(t, s.engine, "POST", "/api/v1/system/update/undo", s.adminTok, nil)
        if w.Code != 200 {
                t.Fatalf("undo: %d %s", w.Code, w.Body.String())
        }
        if reason, _ := dataMap(t, w)["restoring"].(string); !strings.Contains(reason, filepath.Base(archive)) {
                t.Errorf("undo should target the newest pre-update archive %s, got %q", filepath.Base(archive), reason)
        }
        // Undo must snapshot the post-update state first (lossless both ways).
        entries, rerr := os.ReadDir(filepath.Join(s.dir, "backups"))
        if rerr != nil {
                t.Fatalf("backups dir: %v", rerr)
        }
        var beforeUndo bool
        for _, e := range entries {
                if strings.HasPrefix(e.Name(), "pre-update-v1.2.3-before-undo") {
                        beforeUndo = true
                }
        }
        if !beforeUndo {
                t.Errorf("undo did not snapshot post-update state first (no pre-update-v1.2.3-before-undo-* dir)")
        }

        // ---- reboot: RestorePending runs BEFORE any DB is opened.
        s.pool.CloseAll()
        _ = s.db.Close()
        if reason := handlers.RestorePending(s.dir); reason == "" {
                t.Fatalf("RestorePending did not run (no manifest)")
        }
        if _, err := os.Stat(filepath.Join(s.dir, "restore-pending.json")); !os.IsNotExist(err) {
                t.Errorf("manifest still present after restore")
        }

        // ---- data must be back, schema coherent for the old binary.
        if got := agent2SchemaVersion(t, agent2DefPath(s)); got != preSchema {
                t.Errorf("pos.db schema version after undo = %d, want %d (restore incoherent)", got, preSchema)
        }
        if n := agent2CountNamed(t, agent2DefPath(s), "UndoCola"); n != 1 {
                t.Errorf("DEFAULT shop inventory not restored by undo (UndoCola count=%d)", n)
        }

        // Shop B lives at shops/<id>.db — the registry path. It must be restored THERE.
        shopBPath := agent2ShopDBPath(t, s, s.shopB)
        if n := agent2CountNamed(t, shopBPath, "UndoChips"); n != 1 {
                t.Errorf("BUG T2-1: shop B inventory NOT restored by undo at its registry path %s "+
                        "(UndoChips count=%d, schema=%d) — update_rollback.go restores shops/<id>.db "+
                        "into the data-dir root instead of shops/", shopBPath, n, agent2SchemaVersion(t, shopBPath))
        }
        stray := filepath.Join(s.dir, filepath.Base(shopBPath))
        if _, err := os.Stat(stray); err == nil {
                t.Errorf("BUG T2-1 confirmed: shop snapshot was restored to the WRONG location %s "+
                        "(a stray DB next to pos.db) while the registry still points at %s", stray, shopBPath)
        }
}

// Undo must pick the NEWEST pre-update archive and ignore before-undo dirs,
// and the restore must be verbatim (marker row planted in the archive).
func TestAgent2UndoPicksNewestArchiveAndRestoresVerbatim(t *testing.T) {
        s := newAgent2Stack(t)
        agent2InsertProduct(t, s.db, "LIVE-1", "LiveCola", 3)

        plant := func(stamp string) string {
                dir := filepath.Join(s.dir, "backups", "pre-update-v"+stamp)
                if err := os.MkdirAll(dir, 0o755); err != nil {
                        t.Fatalf("plant %s: %v", stamp, err)
                }
                pdb := agent2OpenDB(t, filepath.Join(s.dir, "plant-"+stamp+".db"))
                if err := pdb.Migrate(); err != nil {
                        t.Fatalf("plant migrate: %v", err)
                }
                if _, err := pdb.Exec(`DELETE FROM products`); err != nil {
                        t.Fatalf("plant wipe: %v", err)
                }
                agent2InsertProduct(t, pdb, "ARC-"+stamp, "Archive"+stamp, 9)
                pdb.Close()
                raw, err := os.ReadFile(filepath.Join(s.dir, "plant-"+stamp+".db"))
                if err != nil {
                        t.Fatalf("read plant: %v", err)
                }
                if err := os.WriteFile(filepath.Join(dir, "pos.db.snap"), raw, 0o644); err != nil {
                        t.Fatalf("write snap: %v", err)
                }
                return dir
        }
        plant("1.0.0-20260101-000001")
        newest := plant("2.0.0-20260102-000002")
        // Decoy: a before-undo archive that is NEWER still — must be excluded.
        decoy := filepath.Join(s.dir, "backups", "pre-update-v9.9.9-before-undo-20260103-000000")
        if err := os.MkdirAll(decoy, 0o755); err != nil {
                t.Fatalf("decoy: %v", err)
        }
        _ = os.WriteFile(filepath.Join(decoy, "pos.db.snap"), []byte("not a database"), 0o644)

        w := do(t, s.engine, "POST", "/api/v1/system/update/undo", s.adminTok, nil)
        if w.Code != 200 {
                t.Fatalf("undo: %d %s", w.Code, w.Body.String())
        }
        if reason, _ := dataMap(t, w)["restoring"].(string); !strings.Contains(reason, filepath.Base(newest)) {
                t.Fatalf("undo picked %q, want newest archive %s", reason, filepath.Base(newest))
        }

        s.pool.CloseAll()
        _ = s.db.Close()
        if reason := handlers.RestorePending(s.dir); reason == "" {
                t.Fatalf("restore did not run")
        }
        if n := agent2CountNamed(t, agent2DefPath(s), "Archive2.0.0-20260102-000002"); n != 1 {
                t.Errorf("restored pos.db is not the newest archive verbatim (marker count=%d)", n)
        }
        if n := agent2CountNamed(t, agent2DefPath(s), "Archive1.0.0-20260101-000001"); n != 0 {
                t.Errorf("restore used the OLD archive")
        }
}

// Undo with no archive at all must fail cleanly (404), never wipe anything.
func TestAgent2UndoWithoutArchiveFailsCleanly(t *testing.T) {
        s := newAgent2Stack(t)
        w := do(t, s.engine, "POST", "/api/v1/system/update/undo", s.adminTok, nil)
        if w.Code != 404 {
                t.Fatalf("undo without backups should 404, got %d %s", w.Code, w.Body.String())
        }
        var products int64
        if err := s.db.QueryRow(`SELECT COUNT(*) FROM products`).Scan(&products); err != nil || products == 0 {
                t.Errorf("live data disturbed by a failed undo (err=%v, products=%d)", err, products)
        }
}

// ---- 3. restore retry semantics -----------------------------------------

// RestorePending's own comment promises: a file that fails to copy keeps
// the manifest "for a retry on next boot". Verify that promise — a deleted
// manifest on partial failure silently strands that database forever.
func TestAgent2RestorePendingRetriesFailedCopy(t *testing.T) {
        dir := t.TempDir()
        goodSrc := filepath.Join(dir, "snap-good.db")
        if err := os.WriteFile(goodSrc, []byte("GOOD-SNAPSHOT-BYTES"), 0o644); err != nil {
                t.Fatal(err)
        }
        dstGood := filepath.Join(dir, "restored.db")
        dstBad := filepath.Join(dir, "stranded.db")
        badSrc := filepath.Join(dir, "snap-missing.db") // does not exist

        manifest := map[string]any{
                "when":   "2026-01-01T00:00:00Z",
                "reason": "agent2 probe: partial failure",
                "files": map[string]string{
                        dstGood: goodSrc,
                        dstBad:  badSrc,
                },
        }
        raw, _ := json.Marshal(manifest)
        manifestPath := filepath.Join(dir, "restore-pending.json")
        if err := os.WriteFile(manifestPath, raw, 0o600); err != nil {
                t.Fatal(err)
        }

        reason := handlers.RestorePending(dir)
        if reason == "" {
                t.Fatalf("restore did not run")
        }
        if raw, err := os.ReadFile(dstGood); err != nil || string(raw) != "GOOD-SNAPSHOT-BYTES" {
                t.Errorf("good file not restored (err=%v)", err)
        }
        // The invariant: manifest SURVIVES so the failed copy retries next boot.
        if _, err := os.Stat(manifestPath); err != nil {
                t.Errorf("BUG T2-2: manifest DELETED although dstBad failed to restore — "+
                        "RestorePending's retry-on-next-boot promise is broken; %s stays stranded forever", dstBad)
        }
        // copyFileExclusive must be atomic: no half-written dstBad, no tmp litter.
        if raw, err := os.ReadFile(dstBad); err == nil && len(raw) > 0 && strings.Contains(string(raw), "restore-tmp") {
                t.Errorf("half-copied database left behind")
        }
        if _, err := os.Stat(dstBad + ".restore-tmp"); !os.IsNotExist(err) {
                t.Errorf("restore-tmp litter left behind")
        }
}

// ---- 4. migrations -------------------------------------------------------

// Migrate is idempotent: running it twice changes nothing and loses nothing.
func TestAgent2MigrationsIdempotent(t *testing.T) {
        dir := t.TempDir()
        db := agent2OpenDB(t, filepath.Join(dir, "twice.db"))
        defer db.Close()
        if err := db.Migrate(); err != nil {
                t.Fatalf("migrate 1: %v", err)
        }
        agent2InsertProduct(t, db, "IDEM-1", "IdemCola", 4)
        var seq1 int64
        if err := db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='products'`).Scan(&seq1); err != nil {
                t.Fatalf("seq: %v", err)
        }
        if err := db.Migrate(); err != nil {
                t.Fatalf("BUG: second Migrate must be a no-op, got %v", err)
        }
        var seq2 int64
        _ = db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='products'`).Scan(&seq2)
        if seq1 != seq2 {
                t.Errorf("second Migrate disturbed sqlite_sequence (%d → %d)", seq1, seq2)
        }
        if n := agent2CountNamed(t, filepath.Join(dir, "twice.db"), "IdemCola"); n != 1 {
                t.Errorf("second Migrate lost data")
        }
}

// A DB created by an OLD schema version (pre-v2 files, hand-built here from
// the v1 table set) must migrate to head with zero data loss and no table
// rebuilds: ids, timestamps, and AUTOINCREMENT sequence all survive.
func TestAgent2OldSchemaMigratesCleanlyNoRebuild(t *testing.T) {
        dir := t.TempDir()
        path := filepath.Join(dir, "old.db")
        db := agent2OpenDB(t, path)
        _, err := db.Exec(`
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '');
CREATE TABLE roles (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE,
        description TEXT NOT NULL DEFAULT '', is_system INTEGER NOT NULL DEFAULT 0,
        permissions TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL DEFAULT (datetime('now')));
CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE,
        full_name TEXT NOT NULL DEFAULT '', password_hash TEXT NOT NULL DEFAULT '',
        pin_hash TEXT NOT NULL DEFAULT '', role_id INTEGER NOT NULL REFERENCES roles(id),
        is_active INTEGER NOT NULL DEFAULT 1, failed_pin_attempts INTEGER NOT NULL DEFAULT 0,
        pin_locked_until TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT (datetime('now')),
        updated_at TEXT NOT NULL DEFAULT (datetime('now')));
CREATE TABLE categories (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE,
        slug TEXT NOT NULL UNIQUE, sort_order INTEGER NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL DEFAULT (datetime('now')));
CREATE TABLE products (id INTEGER PRIMARY KEY AUTOINCREMENT, sku TEXT NOT NULL UNIQUE,
        barcode TEXT NOT NULL DEFAULT '', name TEXT NOT NULL,
        category_id INTEGER NOT NULL REFERENCES categories(id),
        price_cents INTEGER NOT NULL DEFAULT 0, cost_cents INTEGER NOT NULL DEFAULT 0,
        stock_qty INTEGER NOT NULL DEFAULT 0, track_stock INTEGER NOT NULL DEFAULT 1,
        is_active INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL DEFAULT (datetime('now')),
        updated_at TEXT NOT NULL DEFAULT (datetime('now')));
CREATE TABLE orders (id INTEGER PRIMARY KEY AUTOINCREMENT, number TEXT NOT NULL UNIQUE,
        status TEXT NOT NULL DEFAULT 'PENDING', subtotal_cents INTEGER NOT NULL DEFAULT 0,
        tax_cents INTEGER NOT NULL DEFAULT 0, total_cents INTEGER NOT NULL DEFAULT 0,
        cashier_id INTEGER NOT NULL REFERENCES users(id), customer_name TEXT NOT NULL DEFAULT '',
        note TEXT NOT NULL DEFAULT '', client_uuid TEXT NOT NULL DEFAULT '',
        discrepancy INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL DEFAULT (datetime('now')),
        paid_at TEXT NOT NULL DEFAULT '', voided_at TEXT NOT NULL DEFAULT '', void_reason TEXT NOT NULL DEFAULT '');
CREATE TABLE order_items (id INTEGER PRIMARY KEY AUTOINCREMENT,
        order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
        product_id INTEGER NOT NULL REFERENCES products(id), name TEXT NOT NULL,
        sku TEXT NOT NULL DEFAULT '', qty INTEGER NOT NULL,
        unit_price_cents INTEGER NOT NULL, line_total_cents INTEGER NOT NULL);
CREATE TABLE payments (id INTEGER PRIMARY KEY AUTOINCREMENT,
        order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE, method TEXT NOT NULL,
        mode TEXT NOT NULL DEFAULT '', amount_cents INTEGER NOT NULL,
        status TEXT NOT NULL DEFAULT 'PENDING', phone TEXT NOT NULL DEFAULT '',
        mpesa_receipt TEXT NOT NULL DEFAULT '', checkout_request_id TEXT NOT NULL DEFAULT '',
        merchant_request_id TEXT NOT NULL DEFAULT '', result_desc TEXT NOT NULL DEFAULT '',
        discrepancy INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL DEFAULT (datetime('now')),
        completed_at TEXT NOT NULL DEFAULT '');
CREATE TABLE print_jobs (id INTEGER PRIMARY KEY AUTOINCREMENT,
        order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
        status TEXT NOT NULL DEFAULT 'queued', target TEXT NOT NULL DEFAULT '',
        attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now')), printed_at TEXT NOT NULL DEFAULT '');
CREATE TABLE shifts (id INTEGER PRIMARY KEY AUTOINCREMENT,
        user_id INTEGER NOT NULL REFERENCES users(id), opening_float_cents INTEGER NOT NULL DEFAULT 0,
        expected_cents INTEGER NOT NULL DEFAULT 0, counted_cents INTEGER NOT NULL DEFAULT 0,
        variance_cents INTEGER NOT NULL DEFAULT 0, opened_at TEXT NOT NULL DEFAULT (datetime('now')),
        closed_at TEXT NOT NULL DEFAULT '');
CREATE TABLE design_jobs (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL,
        product_name TEXT NOT NULL DEFAULT '', customer_name TEXT NOT NULL DEFAULT '',
        notes TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'queue',
        assignee_id INTEGER REFERENCES users(id), created_by TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now')), updated_at TEXT NOT NULL DEFAULT (datetime('now')));
CREATE TABLE audit_log (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL DEFAULT 0,
        username TEXT NOT NULL DEFAULT '', action TEXT NOT NULL, entity TEXT NOT NULL DEFAULT '',
        entity_id TEXT NOT NULL DEFAULT '', details TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now')));
INSERT INTO roles (name, is_system) VALUES ('Admin', 1);
INSERT INTO users (username, password_hash, role_id) VALUES ('oda', 'x', 1);
INSERT INTO categories (name, slug) VALUES ('Legacy', 'legacy');
INSERT INTO products (sku, name, category_id, stock_qty) VALUES ('OLD-1', 'OldStock', 1, 41);
INSERT INTO orders (number, status, cashier_id) VALUES ('ORD-100', 'PAID', 1);
INSERT INTO order_items (order_id, product_id, name, qty, unit_price_cents, line_total_cents) VALUES (1, 1, 'OldStock', 2, 1000, 2000);
`)
        if err != nil {
                t.Fatalf("build old db: %v", err)
        }

        if err := db.Migrate(); err != nil {
                t.Fatalf("BUG: old v1-era DB must migrate cleanly, got %v", err)
        }
        // Data survived — no rebuild.
        var name string
        var qty int64
        if err := db.QueryRow(`SELECT name, stock_qty FROM products WHERE sku='OLD-1'`).Scan(&name, &qty); err != nil || name != "OldStock" || qty != 41 {
                t.Fatalf("old inventory lost in migration (name=%q qty=%d err=%v)", name, qty, err)
        }
        var pid, oid int64
        if err := db.QueryRow(`SELECT id FROM products WHERE sku='OLD-1'`).Scan(&pid); err != nil || pid != 1 {
                t.Errorf("product id changed — table was rebuilt (id=%d err=%v)", pid, err)
        }
        if err := db.QueryRow(`SELECT id FROM orders WHERE number='ORD-100'`).Scan(&oid); err != nil || oid != 1 {
                t.Errorf("order id changed — table was rebuilt (id=%d err=%v)", oid, err)
        }
        var mustRotate int64
        if err := db.QueryRow(`SELECT must_rotate FROM users WHERE username='oda'`).Scan(&mustRotate); err != nil || mustRotate != 1 {
                t.Errorf("v4 backfill not applied to legacy user (mustRotate=%d err=%v)", mustRotate, err)
        }
        var customers int64
        if err := db.QueryRow(`SELECT COUNT(*) FROM customers`).Scan(&customers); err != nil {
                t.Errorf("v2 tables missing after migration: %v", err)
        }
        // Migration ran exactly once.
        if v := agent2SchemaVersion(t, path); v != 12 {
                t.Errorf("schema version after migrate = %d, want 12", v)
        }
        if err := db.Migrate(); err != nil {
                t.Errorf("re-migrate on old DB: %v", err)
        }
        db.Close()
}

// Robustness edge: a DB built BEFORE migration tracking existed (all later
// columns already present, empty schema_migrations) must still migrate —
// SQLite ALTER TABLE ADD COLUMN has no IF NOT EXISTS, so each ADD must be
// guarded or tolerant.
func TestAgent2PreTrackingSchemaMigrates(t *testing.T) {
        dir := t.TempDir()
        path := filepath.Join(dir, "pretracking.db")
        db := agent2OpenDB(t, path)
        defer db.Close()
        _, err := db.Exec(`
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '');
CREATE TABLE roles (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE,
        description TEXT NOT NULL DEFAULT '', is_system INTEGER NOT NULL DEFAULT 0,
        permissions TEXT NOT NULL DEFAULT '[]', home_page TEXT NOT NULL DEFAULT '',
        dashboard_config TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL DEFAULT (datetime('now')));
CREATE TABLE users (id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT NOT NULL UNIQUE,
        full_name TEXT NOT NULL DEFAULT '', password_hash TEXT NOT NULL DEFAULT '',
        pin_hash TEXT NOT NULL DEFAULT '', role_id INTEGER NOT NULL REFERENCES roles(id),
        is_active INTEGER NOT NULL DEFAULT 1, must_rotate INTEGER NOT NULL DEFAULT 0,
        password_changed_at TEXT NOT NULL DEFAULT '', failed_pin_attempts INTEGER NOT NULL DEFAULT 0,
        pin_locked_until TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT (datetime('now')),
        updated_at TEXT NOT NULL DEFAULT (datetime('now')));
CREATE TABLE categories (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE,
        slug TEXT NOT NULL UNIQUE, sort_order INTEGER NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL DEFAULT (datetime('now')));
CREATE TABLE products (id INTEGER PRIMARY KEY AUTOINCREMENT, sku TEXT NOT NULL UNIQUE,
        barcode TEXT NOT NULL DEFAULT '', name TEXT NOT NULL,
        category_id INTEGER NOT NULL REFERENCES categories(id),
        price_cents INTEGER NOT NULL DEFAULT 0, cost_cents INTEGER NOT NULL DEFAULT 0,
        stock_qty INTEGER NOT NULL DEFAULT 0, track_stock INTEGER NOT NULL DEFAULT 1,
        is_active INTEGER NOT NULL DEFAULT 1, image_url TEXT NOT NULL DEFAULT '',
        is_gift_card INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL DEFAULT (datetime('now')),
        updated_at TEXT NOT NULL DEFAULT (datetime('now')));
CREATE TABLE orders (id INTEGER PRIMARY KEY AUTOINCREMENT, number TEXT NOT NULL UNIQUE,
        status TEXT NOT NULL DEFAULT 'PENDING', subtotal_cents INTEGER NOT NULL DEFAULT 0,
        tax_cents INTEGER NOT NULL DEFAULT 0, total_cents INTEGER NOT NULL DEFAULT 0,
        cashier_id INTEGER NOT NULL REFERENCES users(id), customer_name TEXT NOT NULL DEFAULT '',
        note TEXT NOT NULL DEFAULT '', client_uuid TEXT NOT NULL DEFAULT '',
        discrepancy INTEGER NOT NULL DEFAULT 0, customer_id INTEGER NOT NULL DEFAULT 0,
        tax_percent REAL NOT NULL DEFAULT 16, tax_included INTEGER NOT NULL DEFAULT 1,
        discount_cents INTEGER NOT NULL DEFAULT 0, points_redeemed INTEGER NOT NULL DEFAULT 0,
        discount_label TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT (datetime('now')),
        paid_at TEXT NOT NULL DEFAULT '', voided_at TEXT NOT NULL DEFAULT '', void_reason TEXT NOT NULL DEFAULT '');
CREATE TABLE order_items (id INTEGER PRIMARY KEY AUTOINCREMENT,
        order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
        product_id INTEGER NOT NULL REFERENCES products(id), name TEXT NOT NULL,
        sku TEXT NOT NULL DEFAULT '', qty INTEGER NOT NULL,
        unit_price_cents INTEGER NOT NULL, line_total_cents INTEGER NOT NULL);
CREATE TABLE payments (id INTEGER PRIMARY KEY AUTOINCREMENT,
        order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE, method TEXT NOT NULL,
        mode TEXT NOT NULL DEFAULT '', amount_cents INTEGER NOT NULL,
        status TEXT NOT NULL DEFAULT 'PENDING', phone TEXT NOT NULL DEFAULT '',
        mpesa_receipt TEXT NOT NULL DEFAULT '', checkout_request_id TEXT NOT NULL DEFAULT '',
        merchant_request_id TEXT NOT NULL DEFAULT '', result_desc TEXT NOT NULL DEFAULT '',
        discrepancy INTEGER NOT NULL DEFAULT 0, email TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now')), completed_at TEXT NOT NULL DEFAULT '');
CREATE TABLE print_jobs (id INTEGER PRIMARY KEY AUTOINCREMENT,
        order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
        status TEXT NOT NULL DEFAULT 'queued', target TEXT NOT NULL DEFAULT '',
        attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now')), printed_at TEXT NOT NULL DEFAULT '');
CREATE TABLE shifts (id INTEGER PRIMARY KEY AUTOINCREMENT,
        user_id INTEGER NOT NULL REFERENCES users(id), opening_float_cents INTEGER NOT NULL DEFAULT 0,
        expected_cents INTEGER NOT NULL DEFAULT 0, counted_cents INTEGER NOT NULL DEFAULT 0,
        variance_cents INTEGER NOT NULL DEFAULT 0, opened_at TEXT NOT NULL DEFAULT (datetime('now')),
        closed_at TEXT NOT NULL DEFAULT '');
CREATE TABLE design_jobs (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL,
        product_name TEXT NOT NULL DEFAULT '', customer_name TEXT NOT NULL DEFAULT '',
        notes TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'queue',
        assignee_id INTEGER REFERENCES users(id), created_by TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now')), updated_at TEXT NOT NULL DEFAULT (datetime('now')));
CREATE TABLE audit_log (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL DEFAULT 0,
        username TEXT NOT NULL DEFAULT '', action TEXT NOT NULL, entity TEXT NOT NULL DEFAULT '',
        entity_id TEXT NOT NULL DEFAULT '', details TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now')));
INSERT INTO roles (name, is_system) VALUES ('Admin', 1);
INSERT INTO users (username, password_hash, role_id) VALUES ('early', 'x', 1);
INSERT INTO categories (name, slug) VALUES ('Early', 'early');
INSERT INTO products (sku, name, category_id, stock_qty) VALUES ('EARLY-1', 'EarlyStock', 1, 7);
`)
        if err != nil {
                t.Fatalf("build pre-tracking db: %v", err)
        }
        if err := db.Migrate(); err != nil {
                t.Errorf("BUG T2-4: DB created by a pre-migration-tracking schema version fails to boot: %v "+
                        "(SQLite ALTER TABLE ADD COLUMN lacks IF NOT EXISTS in migrations v2/v4/v6/v7/v8/v10 — "+
                        "an existing column aborts Migrate and startApp dies with 'migrate: …')", err)
        }
        var n int64
        if err := db.QueryRow(`SELECT COUNT(*) FROM products WHERE sku='EARLY-1'`).Scan(&n); err != nil || n != 1 {
                t.Errorf("pre-tracking data lost (err=%v)", err)
        }
}

// Static guard: the migration chain must contain zero destructive DDL —
// user data is never dropped, truncated, or bulk-deleted by an update.
func TestAgent2MigrationsContainNoDestructiveDDL(t *testing.T) {
        raw, err := os.ReadFile(filepath.Join("..", "database", "migrations.go"))
        if err != nil {
                t.Fatalf("read migrations source: %v", err)
        }
        src := strings.ToUpper(string(raw))
        for _, banned := range []string{"DROP TABLE", "DROP COLUMN", "DELETE FROM", "TRUNCATE"} {
                if strings.Contains(src, banned) {
                        t.Errorf("destructive DDL in migration chain: %q found in internal/database/migrations.go", banned)
                }
        }
}

// ---- 5. tenant isolation (file level) ------------------------------------

// Products of one shop must be invisible through another shop's handle —
// same pool, same process, different files. Also: the pool must refuse
// unknown shop ids, and FindUserShop must never route a PIN into an empty
// shop when a data-bearing shop shares the same user id (the original
// "inventory vanished" bug).
func TestAgent2TenantIsolationAndPinRouting(t *testing.T) {
        s := newAgent2Stack(t)
        agent2InsertProduct(t, s.db, "ISO-DEF", "IslandDefault", 1)
        dbB, err := s.pool.DB(s.shopB)
        if err != nil {
                t.Fatalf("shop B: %v", err)
        }
        agent2InsertProduct(t, dbB, "ISO-B", "IslandB", 2)
        dbC, err := s.pool.DB(s.shopC)
        if err != nil {
                t.Fatalf("shop C: %v", err)
        }

        // Distinct files on disk.
        paths := map[string]string{
                s.defShop: agent2DefPath(s),
                s.shopB:   agent2ShopDBPath(t, s, s.shopB),
                s.shopC:   agent2ShopDBPath(t, s, s.shopC),
        }
        seen := map[string]bool{}
        for id, p := range paths {
                if seen[p] {
                        t.Errorf("shops %s and another share DB file %s", id, p)
                }
                seen[p] = true
                if _, err := os.Stat(p); err != nil {
                        t.Errorf("shop %s DB file missing: %v", id, err)
                }
        }

        // No leakage through any handle: every shop must see zero rows for the
        // OTHER shops' products (its own name is skipped — it belongs there).
        ownOf := map[string]string{s.defShop: "IslandDefault", s.shopB: "IslandB", s.shopC: ""}
        for reader, db := range map[string]*database.DB{s.shopB: dbB, s.shopC: dbC, s.defShop: s.db} {
                for owner, foreign := range ownOf {
                        if owner == reader {
                                continue
                        }
                        var leak int64
                        if err := db.QueryRow(db.Rebind(`SELECT COUNT(*) FROM products WHERE name = ?`), foreign).Scan(&leak); err != nil {
                                t.Fatalf("shop %s: %v", reader, err)
                        }
                        if leak != 0 {
                                t.Errorf("TENANT LEAK: shop %s sees %q owned by shop %s", reader, foreign, owner)
                        }
                }
        }

        // Unknown shop id must error, never fall back to another shop's file.
        if _, err := s.pool.DB("sh-doesnotexist"); err == nil {
                t.Errorf("pool.Open(unknown) must fail, not serve a default file")
        }

        // PIN routing: user id 1 exists in the default shop (admin, with data)
        // AND in an empty shop — the router must pick the data-bearing shop.
        if _, err := dbB.Exec(`INSERT INTO roles (name) VALUES ('B-Only')`); err != nil {
                t.Fatalf("role B: %v", err)
        }
        if _, err := dbB.Exec(dbB.Rebind(`INSERT INTO users (id, username, password_hash, role_id) VALUES (1, 'ghost', 'x', ?)`),
                1); err != nil {
                t.Fatalf("colliding user: %v", err)
        }
        got, ok := s.pool.FindUserShop(1)
        if !ok {
                t.Fatalf("FindUserShop(1) found nothing")
        }
        if got != s.defShop {
                t.Errorf("FindUserShop routed user 1 to empty shop %s — must prefer the shop that holds data (%s)", got, s.defShop)
        }
}
