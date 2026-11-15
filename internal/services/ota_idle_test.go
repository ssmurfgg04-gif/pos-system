package services

import (
	"testing"
	"time"
)

// The auto-update gate must judge real app timestamps: an order from this
// morning (app format "…T…Z") is not "selling right now", and an open tab
// or an abandoned prompt never blocks updates forever.
func TestOTAIdleGateRealTimestamps(t *testing.T) {
	s := newSyncTestService(t)
	s.db.Exec(`INSERT INTO roles (name, permissions) VALUES ('Cashier', '[]')`)
	s.db.Exec(`INSERT INTO users (username, full_name, pin_hash, role_id, is_active) VALUES ('a','A','',1,1)`)
	now := time.Now()
	old := now.Add(-3 * time.Hour).UTC().Format(nowStampLayout)
	s.db.Exec(`INSERT INTO orders (number, status, cashier_id, created_at) VALUES ('o1','PAID',1,?)`, old)
	if s.hasUnfinishedWorkAt(now) {
		t.Fatal("a sale 3 hours ago blocks the update")
	}
	s.db.Exec(`INSERT INTO orders (number, status, cashier_id, created_at) VALUES ('o2','PENDING',1,?)`, old)
	s.db.Exec(`INSERT INTO payments (order_id, method, amount_cents, status, created_at) VALUES (2,'account',100,'PENDING',?)`, old)
	s.db.Exec(`INSERT INTO payments (order_id, method, amount_cents, status, created_at) VALUES (2,'mpesa',100,'PENDING',?)`, old)
	if s.hasUnfinishedWorkAt(now) {
		t.Fatal("an open tab or an abandoned prompt blocks the update")
	}
	fresh := now.Add(-2 * time.Minute).UTC().Format(nowStampLayout)
	s.db.Exec(`INSERT INTO payments (order_id, method, amount_cents, status, created_at) VALUES (2,'mpesa',100,'PENDING',?)`, fresh)
	if !s.hasUnfinishedWorkAt(now) {
		t.Fatal("an M-Pesa prompt in flight must block the update")
	}
	s.db.Exec(`DELETE FROM payments`)
	s.db.Exec(`INSERT INTO orders (number, status, cashier_id, created_at) VALUES ('o3','PAID',1,?)`, fresh)
	if !s.hasUnfinishedWorkAt(now) {
		t.Fatal("a sale 2 minutes ago must block the update")
	}
	if s.hasUnfinishedWorkAt(now.Add(20 * time.Minute)) {
		t.Fatal("20 minutes later the till is quiet")
	}
}
