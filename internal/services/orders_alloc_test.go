package services

// orders_alloc_test.go — regression for the "could not allocate order
// number" outage (all payment methods, all day). The day counter in
// order_sequences can lag the orders table: cross-device sync ingests
// other tills' numbers verbatim (suffix only when they collide at ingest
// time), restored snapshots resurrect old rows, and rolled-back txs
// discard counter bumps. The old allocator re-minted the SAME number on
// every retry (its bump rolled back with the failed order tx) and checkout
// died for the rest of the day. The allocator must heal forward past
// reality and make progress on every retry.

import (
        "path/filepath"
        "testing"
        "time"

        "posapp/internal/database"
        "posapp/internal/settings"
)

func newAllocTestService(t *testing.T) *Service {
        t.Helper()
        dir := t.TempDir()
        db, err := database.Open("sqlite", filepath.Join(dir, "test.db"), "")
        if err != nil {
                t.Fatalf("open db: %v", err)
        }
        t.Cleanup(func() { db.Close() })
        if err := db.Migrate(); err != nil {
                t.Fatalf("migrate: %v", err)
        }
        st, err := settings.New(db)
        if err != nil {
                t.Fatalf("settings: %v", err)
        }
        return &Service{db: db, settings: st, appVersion: "test"}
}

// seedCashier + seedIngestedOrders stand in for what cross-device sync
// does to a till: local user, plus another till's orders for today
// inserted verbatim (no suffix — they didn't collide at ingest time).
func seedCashier(t *testing.T, s *Service) {
        t.Helper()
        s.db.Exec(s.db.Rebind(`INSERT INTO roles (name) VALUES ('Cashier')`))
        var roleID int64
        s.db.QueryRow(`SELECT id FROM roles WHERE name='Cashier'`).Scan(&roleID)
        s.db.Exec(s.db.Rebind(`INSERT INTO users (username, role_id) VALUES ('cash1', ?)`), roleID)
}

func seedIngestedOrders(t *testing.T, s *Service, day string, from, to int) {
        t.Helper()
        var userID int64
        s.db.QueryRow(`SELECT id FROM users WHERE username='cash1'`).Scan(&userID)
        for i := from; i <= to; i++ {
                number := string(rune(0)) // replaced below; keep loop simple
                _ = number
                num := timeDayNumber(day, i)
                if _, err := s.db.Exec(s.db.Rebind(`
                        INSERT INTO orders (number, status, subtotal_cents, total_cents, cashier_id, client_uuid)
                        VALUES (?, 'PAID', 1000, 1000, ?, ?)`),
                        num, userID, "ingest-"+num); err != nil {
                        t.Fatalf("seed order %s: %v", num, err)
                }
        }
}

func timeDayNumber(day string, seq int) string {
        out := "ORD" + day
        s := itoa(seq)
        for len(s) < 4 {
                s = "0" + s
        }
        return out + s
}

func itoa(v int) string {
        if v == 0 {
                return "0"
        }
        var b []byte
        for v > 0 {
                b = append([]byte{byte('0' + v%10)}, b...)
                v /= 10
        }
        return string(b)
}

// The exact field outage: counter at zero, other till's orders 0001..0004
// already ingested for today. First mint must be 0005 — and a real order
// insert with it must succeed.
func TestOrderNumberHealsPastIngestedOrders(t *testing.T) {
        s := newAllocTestService(t)
        seedCashier(t, s)
        day := time.Now().Format("20060102")
        seedIngestedOrders(t, s, day, 1, 4)

        tx, err := s.db.Begin()
        if err != nil {
                t.Fatal(err)
        }
        num, err := s.nextDocNumber(tx, "ORD", 0)
        if err != nil {
                t.Fatal(err)
        }
        want := timeDayNumber(day, 5)
        if num != want {
                t.Fatalf("minted %s, want %s — allocator did not heal past ingested orders", num, want)
        }
        if _, err := tx.Exec(s.db.Rebind(`
                INSERT INTO orders (number, status, subtotal_cents, total_cents, cashier_id, client_uuid)
                VALUES (?, 'PENDING', 1000, 1000, (SELECT id FROM users WHERE username='cash1'), 'alloc-test-1')`), num); err != nil {
                t.Fatalf("insert with healed number: %v", err)
        }
        if err := tx.Commit(); err != nil {
                t.Fatal(err)
        }
}

// A rolled-back tx discards its counter bump: the retry (skip=1) must
// never mint the number the failed attempt took — the old deadlock.
func TestOrderNumberRetrySkipsRolledBackMint(t *testing.T) {
        s := newAllocTestService(t)
        day := time.Now().Format("20060102")

        tx, err := s.db.Begin()
        if err != nil {
                t.Fatal(err)
        }
        first, err := s.nextDocNumber(tx, "ORD", 0)
        if err != nil {
                t.Fatal(err)
        }
        if err := tx.Rollback(); err != nil {
                t.Fatal(err)
        }

        tx2, err := s.db.Begin()
        if err != nil {
                t.Fatal(err)
        }
        second, err := s.nextDocNumber(tx2, "ORD", 1)
        if err != nil {
                t.Fatal(err)
        }
        tx2.Commit()
        if second == first {
                t.Fatalf("retry re-minted %s after rollback — the old all-day deadlock", second)
        }
        if second != timeDayNumber(day, 2) {
                t.Fatalf("retry minted %s, want %s", second, timeDayNumber(day, 2))
        }
}

// Suffixed ingest collisions ("...0007-XF") must also lift the floor.
func TestOrderNumberHealSeesSuffixedIngest(t *testing.T) {
        s := newAllocTestService(t)
        seedCashier(t, s)
        day := time.Now().Format("20060102")
        seedIngestedOrders(t, s, day, 1, 2)
        var userID int64
        s.db.QueryRow(`SELECT id FROM users WHERE username='cash1'`).Scan(&userID)
        suffixed := timeDayNumber(day, 7) + "-XF"
        if _, err := s.db.Exec(s.db.Rebind(`
                INSERT INTO orders (number, status, subtotal_cents, total_cents, cashier_id, client_uuid)
                VALUES (?, 'PAID', 1000, 1000, ?, 'ingest-suffix')`), suffixed, userID); err != nil {
                t.Fatalf("seed suffixed order: %v", err)
        }

        tx, err := s.db.Begin()
        if err != nil {
                t.Fatal(err)
        }
        num, err := s.nextDocNumber(tx, "ORD", 0)
        if err != nil {
                t.Fatal(err)
        }
        tx.Rollback()
        if num != timeDayNumber(day, 8) {
                t.Fatalf("minted %s, want %s — heal missed the suffixed ingest", num, timeDayNumber(day, 8))
        }
}

// Healthy path: a counter that is AHEAD of reality keeps driving (no
// gaps introduced by healing), retries still progress.
func TestOrderNumberHealthyCounterUnchanged(t *testing.T) {
        s := newAllocTestService(t)
        day := time.Now().Format("20060102")

        // Counter claims 9 consumed (e.g. rolled-back txs), nothing exists.
        // The table is created lazily by the allocator — mirror its DDL.
        if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS order_sequences (
                day TEXT PRIMARY KEY, seq INTEGER NOT NULL DEFAULT 0)`); err != nil {
                t.Fatal(err)
        }
        if _, err := s.db.Exec(s.db.Rebind(
                `INSERT INTO order_sequences (day, seq) VALUES (?, 9)`), day); err != nil {
                t.Fatal(err)
        }
        tx, _ := s.db.Begin()
        num, err := s.nextDocNumber(tx, "ORD", 0)
        if err != nil {
                t.Fatal(err)
        }
        tx.Rollback()
        if num != timeDayNumber(day, 10) {
                t.Fatalf("minted %s, want %s — healthy counter drifted", num, timeDayNumber(day, 10))
        }

        // Non-ORD prefixes keep independent readability but share the day
        // counter — allocation must still work.
        tx2, _ := s.db.Begin()
        cnt, err := s.nextDocNumber(tx2, "CNT", 0)
        if err != nil {
                t.Fatal(err)
        }
        tx2.Rollback()
        // The ORD attempt above rolled back, so its bump is discarded — the
        // counter still says 9, this bump makes 10. No heal (no CNT orders).
        if cnt != "CNT"+day+"0010" {
                t.Fatalf("minted %s, want CNT%s0010", cnt, day)
        }
}
