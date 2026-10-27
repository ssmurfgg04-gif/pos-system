package services

// reports_regression_test.go — the daily report must never 500 on a day
// that HAD sales. Shipped v1.4.4–v1.4.5 with a live scan bug: SQLite's
// AVG() is float, COALESCE(AVG(...),0) therefore returned float64 the
// moment one paid order existed, and scanning it into AvgOrderCents
// int64 failed the whole query ("converting driver.Value type float64").
// Quiet days worked (AVG is NULL → COALESCE yields integer 0), which is
// exactly why the fleet did not report it until a busy day.

import (
	"testing"

	"posapp/internal/printer"
)

func TestDailySummarySurvivesSalesDay(t *testing.T) {
	s := openV146Till(t)

	// One product to sell.
	s.db.Exec(`INSERT INTO categories (name) VALUES ('r')`)
	s.db.Exec(`INSERT INTO products (name, category_id, price_cents, is_active, created_at, updated_at)
                SELECT 'R1', id, 15000, 1, datetime('now'), datetime('now') FROM categories WHERE name='r'`)

	// Two paid orders today, different totals (avg would be 22500.0 float).
	// created_at must match the app's own stamp shape ("2006-01-02T15:04:05.000Z")
	// — dayBounds compares STRINGS, so datetime('now')'s space separator
	// would silently fall outside the window.
	for i, total := range []int64{15000, 30000} {
		now := nowStamp()
		s.db.Exec(`INSERT INTO orders (number, status, subtotal_cents, tax_cents, total_cents,
                        cashier_id, customer_name, created_at, paid_at, client_uuid)
                        VALUES (?, 'PAID', ?, 0, ?, 1, 'cash1', ?, ?, ?)`,
			"ORD-R-000"+string(rune('1'+i)), total, total, now, now, "r-uuid-"+string(rune('1'+i)))
		var oid int64
		s.db.QueryRow(`SELECT id FROM orders WHERE client_uuid = ?`, "r-uuid-"+string(rune('1'+i))).Scan(&oid)
		s.db.Exec(`INSERT INTO order_items (order_id, product_id, name, qty, unit_price_cents, line_total_cents)
                        VALUES (?, 1, 'R1', 1, ?, ?)`, oid, total, total)
		s.db.Exec(`INSERT INTO payments (order_id, method, amount_cents, status, created_at)
                        VALUES (?, 'cash', ?, 'COMPLETED', ?)`, oid, total, now)
	}

	// The regression: this used to error with the float→int64 scan failure.
	sum, err := s.GetDailySummary("")
	if err != nil {
		t.Fatalf("daily summary with sales: %v", err)
	}
	if sum.SalesCents != 45000 || sum.OrdersPaid != 2 {
		t.Fatalf("sales/orders wrong: %d / %d", sum.SalesCents, sum.OrdersPaid)
	}
	if sum.AvgOrderCents != 22500 {
		t.Fatalf("avg order: got %d want 22500", sum.AvgOrderCents)
	}
	if sum.CashCents != 45000 || sum.MpesaCents != 0 {
		t.Fatalf("tender split wrong: cash=%d mpesa=%d", sum.CashCents, sum.MpesaCents)
	}

	// And an empty day must stay healthy (the pre-fix happy path).
	if _, err := s.GetDailySummary("2001-01-01"); err != nil {
		t.Fatalf("daily summary empty day: %v", err)
	}
	_ = printer.NewWorker(s.db, s.settings) // keep the import meaningful if helpers change
}
