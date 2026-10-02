package services

// p0_sync_hardening_test.go — the P0 acceptance journey from the client's
// work order: an order created offline synchronizes EXACTLY ONCE after
// reconnection, survives an application restart, and appears correctly on
// another authorized device. Plus hardening guard tests for the quarantine
// and prune paths.

import (
        "context"
        "path/filepath"
        "testing"

        "posapp/internal/auth"
        "posapp/internal/database"
        "posapp/internal/models"
        "posapp/internal/printer"
        "posapp/internal/settings"
)

// p0Fixture is a till with a product, a staff user, and a cloud link.
type p0Fixture struct {
        s      *Service
        prodID int64
        userID int64
        roleID int64
}

func p0OpenTill(t *testing.T, dbPath string) *Service {
        t.Helper()
        db, err := database.Open("sqlite", dbPath, "")
        if err != nil {
                t.Fatalf("open: %v", err)
        }
        t.Cleanup(func() { db.Close() })
        if err := db.Migrate(); err != nil {
                t.Fatalf("migrate: %v", err)
        }
        st, err := settings.New(db)
        if err != nil {
                t.Fatalf("settings: %v", err)
        }
        db.Exec(`INSERT INTO categories (name, slug) VALUES ('C','c')`)
        db.Exec(`INSERT INTO products (sku, name, category_id, price_cents, stock_qty)
                SELECT 'SKU-P0','P0', id, 500, 10 FROM categories LIMIT 1`)
        db.Exec(`INSERT INTO roles (name) VALUES ('Cashier')`)
        db.Exec(`INSERT INTO users (username, role_id) SELECT 'cash1', id FROM roles LIMIT 1`)
        return &Service{db: db, settings: st, appVersion: "p0-test", printer: printer.NewWorker(db, st)}
}

// TestP0OfflineOrderJourney — the client's exact acceptance scenario:
// offline sale → app restart → reconnection → exactly-once on device B.
func TestP0OfflineOrderJourney(t *testing.T) {
        fake := newAgent3Cloud(t)
        dir := t.TempDir()
        dbPath := filepath.Join(dir, "tillA.db")

        // 1. Till A boots with the cloud UNREACHABLE (offline).
        agent3Link(t, "http://127.0.0.1:1")
        tillA1 := p0OpenTill(t, dbPath)
        if _, ok := tillA1.syncConfig(); ok {
                t.Fatal("precondition: cloud should be unreachable")
        }
        // The offline sale still completes and lands in the durable outbox.
        var prodID, userID, roleID int64
        tillA1.db.QueryRow(`SELECT id FROM products WHERE sku='SKU-P0'`).Scan(&prodID)
        tillA1.db.QueryRow(`SELECT id FROM users WHERE username='cash1'`).Scan(&userID)
        tillA1.db.QueryRow(`SELECT id FROM roles WHERE name='Cashier'`).Scan(&roleID)
        if prodID == 0 || userID == 0 {
                t.Fatal("fixture missing product/user")
        }
        f := &p0Fixture{s: tillA1, prodID: prodID, userID: userID, roleID: roleID}
        order := p0Checkout(t, f, "p0-journey-1")
        if order.Status != models.OrderPaid {
                t.Fatalf("offline order status = %s, want PAID", order.Status)
        }
        if got := agent3Stock(tillA1, "SKU-P0"); got != 9 {
                t.Fatalf("stock after offline sale = %d, want 9", got)
        }

        // 2. Application restart: same database file, fresh process.
        tillA2 := p0OpenTill(t, dbPath)
        var pending int64
        tillA2.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE pushed_at=''`).Scan(&pending)
        if pending == 0 {
                t.Fatal("restart lost the offline sale's pending sync events")
        }
        // The order itself is intact after the restart.
        var n int
        tillA2.db.QueryRow(`SELECT COUNT(*) FROM orders WHERE client_uuid='p0-journey-1' AND status='PAID'`).Scan(&n)
        if n != 1 {
                t.Fatalf("order rows after restart = %d, want 1", n)
        }

        // 3. Reconnection: the cloud is reachable again. Till A registers
        //    and drains its outbox; the SAME restart-proof secret registers
        //    (device id + secret live in the DB, so identity survives too).
        agent3Link(t, fake.srv.URL)
        cA, ok := tillA2.syncConfig()
        if !ok {
                t.Fatal("cloud link did not resolve after reconnection")
        }
        if err := cA.heartbeat(tillA2, "p0-test"); err != nil {
                t.Fatalf("re-register after restart: %v", err)
        }
        tillA2.TeamSyncNow("p0-test")

        // 4. Another AUTHORIZED device pulls the order: exactly once.
        agent3Link(t, fake.srv.URL)
        tillB := p0OpenTill(t, filepath.Join(dir, "tillB.db"))
        var catB int64
        tillB.db.QueryRow(`SELECT id FROM categories LIMIT 1`).Scan(&catB)
        tillB.db.Exec(tillB.db.Rebind(`INSERT INTO products (sku, name, category_id, price_cents, stock_qty)
                VALUES ('SKU-P0','P0',?,500,10)`), catB)
        cB, ok := tillB.syncConfig()
        if !ok {
                t.Fatal("till B could not resolve the cloud")
        }
        if err := cB.heartbeat(tillB, "p0-test"); err != nil {
                t.Fatalf("till B register: %v", err)
        }
        if applied, err := cB.pull(tillB); err != nil || applied < 1 {
                t.Fatalf("till B first pull: applied=%d err=%v", applied, err)
        }
        tillB.db.QueryRow(`SELECT COUNT(*) FROM orders WHERE client_uuid='p0-journey-1' AND status='PAID'`).Scan(&n)
        if n != 1 {
                t.Fatalf("order on device B = %d, want exactly 1", n)
        }
        if got := agent3Stock(tillB, "SKU-P0"); got != 9 {
                t.Fatalf("stock on device B = %d, want 9 (deducted once)", got)
        }

        // 5. Repeated sync cycles (retries, reconnect flaps) duplicate nothing.
        tillA2.TeamSyncNow("p0-test")
        cB.pull(tillB)
        cB.pull(tillB)
        tillB.db.QueryRow(`SELECT COUNT(*) FROM orders WHERE client_uuid='p0-journey-1'`).Scan(&n)
        if n != 1 {
                t.Fatalf("order on device B after replays = %d, want exactly 1", n)
        }
        if got := agent3Stock(tillB, "SKU-P0"); got != 9 {
                t.Fatalf("stock on device B after replays = %d, want 9", got)
        }
}

func p0Checkout(t *testing.T, f *p0Fixture, uuid string) *models.Order {
        t.Helper()
        order, err := f.s.Checkout(context.Background(), &auth.Principal{
                ID: f.userID, Username: "cash1", RoleID: f.roleID, RoleName: "Cashier", Active: true,
        }, models.CheckoutRequest{
                Items:         []models.CheckoutItem{{ProductID: f.prodID, Qty: 1}},
                PaymentMethod: models.MethodCash,
                ClientUUID:    uuid,
        })
        if err != nil {
                t.Fatalf("checkout %s: %v", uuid, err)
        }
        return order
}

// TestP0DeadLetterRetryRecoversLedger — an out-of-order ledger event (its
// customer has not synced yet) is quarantined, then recovers on a later
// retry once the customer event lands. No money movement is ever dropped.
func TestP0DeadLetterRetryRecoversLedger(t *testing.T) {
        fake := newAgent3Cloud(t)
        agent3Link(t, fake.srv.URL)
        s1 := agent3NewService(t)
        s2 := agent3NewService(t)
        c1 := agent3Register(t, s1)
        c2 := agent3Register(t, s2)

        // Ledger event arrives BEFORE its customer event (out-of-order).
        s1.Emit("ledger", "upsert", map[string]any{
                "phone": "0700000009", "kind": models.LedgerCharge,
                "amountCents": 1200, "pointsDelta": 0, "note": "tab charge", "createdAt": nowStamp(),
        })
        c1.push(s1)
        if applied, err := c2.pull(s2); err != nil || applied != 0 {
                t.Fatalf("out-of-order ledger pull: applied=%d err=%v", applied, err)
        }
        var dl int
        s2.db.QueryRow(`SELECT COUNT(*) FROM sync_dead_letter WHERE direction='apply'`).Scan(&dl)
        if dl != 1 {
                t.Fatalf("ledger event not quarantined (rows=%d)", dl)
        }
        // The customer event lands on a later cycle.
        s1.Emit("customer", "upsert", map[string]any{
                "phone": "0700000009", "name": "Late Customer", "active": true, "updatedAt": nowStamp(),
        })
        c1.push(s1)
        if applied, err := c2.pull(s2); err != nil || applied != 1 {
                t.Fatalf("customer pull: applied=%d err=%v", applied, err)
        }
        // The auto-retry inside the sync cycle (or the manual retry button)
        // recovers the quarantined ledger row.
        if recovered := s2.retryFailedApplies(); recovered != 1 {
                t.Fatalf("recovered %d, want 1", recovered)
        }
        var balance int64
        s2.db.QueryRow(`SELECT balance_cents FROM customers WHERE phone='0700000009'`).Scan(&balance)
        if balance != 1200 {
                t.Fatalf("balance = %d, want 1200 (ledger recovered exactly once)", balance)
        }
        s2.db.QueryRow(`SELECT COUNT(*) FROM sync_dead_letter`).Scan(&dl)
        if dl != 0 {
                t.Fatalf("dead letter after recovery = %d, want 0", dl)
        }
        // Retrying again moves nothing (already recovered + marked applied).
        if recovered := s2.retryFailedApplies(); recovered != 0 {
                t.Fatalf("second retry recovered %d, want 0", recovered)
        }
        if balance2 := func() int64 {
                var b int64
                s2.db.QueryRow(`SELECT balance_cents FROM customers WHERE phone='0700000009'`).Scan(&b)
                return b
        }(); balance2 != 1200 {
                t.Fatalf("balance after double retry = %d, want 1200", balance2)
        }
}

// TestP0OutboxPrunedAndCursorBounded — pushed outbox rows and applied ids
// are pruned so the hardening bookkeeping never grows unbounded.
func TestP0OutboxPrunedAndCursorBounded(t *testing.T) {
        s := agent3NewService(t)
        s.db.Exec(s.db.Rebind(`INSERT INTO sync_outbox (entity, op, client_uuid, payload, pushed_at)
                VALUES ('product','upsert','prune-1','{}','2020-01-01T00:00:00.000Z')`))
        s.db.Exec(s.db.Rebind(`INSERT INTO sync_outbox (entity, op, client_uuid, payload, pushed_at)
                VALUES ('product','upsert','prune-2','{}',?)`), nowStamp())
        s.db.Exec(s.db.Rebind(`INSERT INTO sync_applied (event_id, applied_at) VALUES (7, '2020-01-01T00:00:00.000Z')`))
        s.db.Exec(s.db.Rebind(`INSERT INTO sync_applied (event_id, applied_at) VALUES (8, ?)`), nowStamp())

        s.pruneSyncTables()

        var n int
        s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE client_uuid='prune-1'`).Scan(&n)
        if n != 0 {
                t.Fatal("week-old pushed row not pruned")
        }
        s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE client_uuid='prune-2'`).Scan(&n)
        if n != 1 {
                t.Fatal("fresh pushed row must be kept")
        }
        s.db.QueryRow(`SELECT COUNT(*) FROM sync_applied WHERE event_id=7`).Scan(&n)
        if n != 0 {
                t.Fatal("month-old applied id not pruned")
        }
        s.db.QueryRow(`SELECT COUNT(*) FROM sync_applied WHERE event_id=8`).Scan(&n)
        if n != 1 {
                t.Fatal("fresh applied id must be kept")
        }
}
