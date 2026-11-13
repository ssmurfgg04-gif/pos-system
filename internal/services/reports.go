package services

import (
	"fmt"
	"time"

	"posapp/internal/models"
)

// DailySummary is the reports payload for one day.
type DailySummary struct {
	Date          string       `json:"date"`
	SalesCents    int64        `json:"salesCents"`
	OrdersPaid    int          `json:"ordersPaid"`
	OrdersOpen    int          `json:"ordersOpen"`
	OrdersVoided  int          `json:"ordersVoided"`
	AvgOrderCents int64        `json:"avgOrderCents"`
	CashCents     int64        `json:"cashCents"`
	MpesaCents    int64        `json:"mpesaCents"`
	PaystackCents int64        `json:"paystackCents"` // card / mobile money via Paystack
	CreditCents   int64        `json:"creditCents"`   // prepaid store credit spent
	Discrepancies int          `json:"discrepancies"`
	TopProducts   []TopProduct `json:"topProducts"`
	Series        []DayPoint   `json:"series"`   // 7-day window ending on date
	PerStaff      []StaffSales `json:"perStaff"` // who sold what today
	ByTill        []TillSales  `json:"byTill"`   // sales grouped by originating till (v19)
	NewCustomers  int          `json:"newCustomers"`
	// v1.6.3 — the money side of the day (expenses), the returns that
	// came back, and what the shelf is worth right now.
	ExpensesCents   int64 `json:"expensesCents"`
	ReturnsCents    int64 `json:"returnsCents"`
	ReturnsCount    int   `json:"returnsCount"`
	NetSalesCents   int64 `json:"netSalesCents"` // sales - returns
	StockUnits      int64 `json:"stockUnits"`
	StockValueCents int64 `json:"stockValueCents"`
	LowStockCount   int   `json:"lowStockCount"`
}

// TillSales is one originating till's slice of a reporting window (v19:
// orders.device_id). DeviceID ” = pre-v19 rows.
type TillSales struct {
	DeviceID   string `json:"deviceId"`
	Orders     int    `json:"orders"`
	SalesCents int64  `json:"salesCents"`
}

// StaffSales is one staff member's day (P5: business reporting).
type StaffSales struct {
	UserID     int64  `json:"userId"`
	Name       string `json:"name"`
	Orders     int    `json:"orders"`
	SalesCents int64  `json:"salesCents"`
}

type TopProduct struct {
	ProductID  int64  `json:"productId"`
	Name       string `json:"name"`
	Qty        int    `json:"qty"`
	SalesCents int64  `json:"salesCents"`
}

type DayPoint struct {
	Date       string `json:"date"`
	SalesCents int64  `json:"salesCents"`
	Orders     int    `json:"orders"`
}

// eastAfrica is Kenya's fixed offset (UTC+3, no DST). Every report
// window and order-date filter buckets by EAST AFRICA calendar days: a
// sale at 00:30 in Nairobi belongs to today, not to "yesterday" the way
// a UTC bucket would place it (the shop runs on EAT; reports must too).
var eastAfrica = time.FixedZone("EAT", 3*60*60)

// eatToday is today's date in Nairobi.
func eatToday() string { return time.Now().In(eastAfrica).Format("2006-01-02") }

// eatStamp formats t as a UTC stamp in the exact shape orders.created_at
// uses ("2006-01-02T15:04:05.000Z"), so bounds compare lexicographically
// in SQL without parsing.
func eatStamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// dayBounds returns the UTC window [from, toExcl) covering one EAT
// calendar day. The upper bound is EXCLUSIVE: the old "T23:59:59" form
// lexicographically excluded orders in the final second of the day
// ("…59.123Z" > "…59") — none of them ever landed in any report.
func dayBounds(date string) (from, toExcl string) {
	if date == "" {
		date = eatToday()
	}
	d, err := time.ParseInLocation("2006-01-02", date, eastAfrica)
	if err != nil {
		d, _ = time.ParseInLocation("2006-01-02", eatToday(), eastAfrica)
	}
	return eatStamp(d), eatStamp(d.AddDate(0, 0, 1))
}

// GetDailySummary aggregates one day (integer cents everywhere).
func (s *Service) GetDailySummary(date string) (*DailySummary, error) {
	if date == "" {
		date = eatToday()
	}
	from, to := dayBounds(date)
	out := &DailySummary{Date: date}

	err := s.db.QueryRow(s.db.Rebind(`
                SELECT COALESCE(SUM(total_cents),0), COUNT(*),
                        CAST(COALESCE(AVG(total_cents),0) AS INTEGER),
                        COALESCE(SUM(CASE WHEN discrepancy != 0 THEN 1 ELSE 0 END),0)
                FROM orders WHERE status = 'PAID' AND created_at >= ? AND created_at < ?`), from, to).
		Scan(&out.SalesCents, &out.OrdersPaid, &out.AvgOrderCents, &out.Discrepancies)
	if err != nil {
		return nil, err
	}
	// Authoritative average is sum/count (integer math); the CAST above
	// only keeps the scan alive — SQLite's AVG() is float, and scanning a
	// float into an int64 field 500'd every daily report that HAD sales
	// (quiet days worked: AVG is NULL → COALESCE yields the integer 0).
	if out.OrdersPaid > 0 {
		out.AvgOrderCents = out.SalesCents / int64(out.OrdersPaid)
	}
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM orders WHERE status = 'PENDING' AND created_at >= ? AND created_at < ?`), from, to).Scan(&out.OrdersOpen)
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM orders WHERE status = 'VOIDED' AND created_at >= ? AND created_at < ?`), from, to).Scan(&out.OrdersVoided)
	_ = s.db.QueryRow(s.db.Rebind(`
                SELECT
                        COALESCE(SUM(CASE WHEN method = 'cash' THEN amount_cents ELSE 0 END),0),
                        COALESCE(SUM(CASE WHEN method = 'mpesa' THEN amount_cents ELSE 0 END),0),
                        COALESCE(SUM(CASE WHEN method = 'paystack' THEN amount_cents ELSE 0 END),0),
                        COALESCE(SUM(CASE WHEN method = 'credit' THEN amount_cents ELSE 0 END),0)
                FROM payments p JOIN orders o ON o.id = p.order_id WHERE p.status = 'COMPLETED' AND o.created_at >= ? AND o.created_at < ?`), from, to).
		Scan(&out.CashCents, &out.MpesaCents, &out.PaystackCents, &out.CreditCents)

	// Sales by originating till (v19 device attribution). Legacy rows
	// (pre-v19) group under '' — surfaced to admin as one bucket.
	if tillRows, err := s.db.Query(s.db.Rebind(`
                SELECT COALESCE(device_id,''), COUNT(*), SUM(total_cents)
                FROM orders WHERE status = 'PAID' AND created_at >= ? AND created_at < ?
                GROUP BY device_id ORDER BY SUM(total_cents) DESC`), from, to); err == nil {
		for tillRows.Next() {
			var t TillSales
			if tillRows.Scan(&t.DeviceID, &t.Orders, &t.SalesCents) == nil {
				out.ByTill = append(out.ByTill, t)
			}
		}
		tillRows.Close()
	}

	// Top products of the day.
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT oi.product_id, COALESCE(oi.name,''), SUM(oi.qty), SUM(oi.line_total_cents)
                FROM order_items oi JOIN orders o ON o.id = oi.order_id
                WHERE o.status = 'PAID' AND o.created_at >= ? AND o.created_at < ?
                GROUP BY oi.product_id, oi.name ORDER BY SUM(oi.line_total_cents) DESC LIMIT 10`), from, to)
	if err == nil {
		for rows.Next() {
			var tp TopProduct
			var qty any
			var sales any
			if err := rows.Scan(&tp.ProductID, &tp.Name, &qty, &sales); err == nil {
				tp.Qty = asInt(qty)
				tp.SalesCents = asInt64(sales)
				out.TopProducts = append(out.TopProducts, tp)
			}
		}
		rows.Close()
	}

	// 7-day series (oldest first).
	start := mustDate(date).AddDate(0, 0, -6)
	for i := 0; i < 7; i++ {
		day := start.AddDate(0, 0, i).Format("2006-01-02")
		f, t := dayBounds(day)
		var sales int64
		var count int
		_ = s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(SUM(total_cents),0), COUNT(*) FROM orders WHERE status = 'PAID' AND created_at >= ? AND created_at < ?`), f, t).Scan(&sales, &count)
		out.Series = append(out.Series, DayPoint{Date: day, SalesCents: sales, Orders: count})
	}

	// Per-staff sales (who sold what today) + new customers captured.
	rows, err = s.db.Query(s.db.Rebind(`
                SELECT u.id, COALESCE(u.full_name, u.username), COUNT(o.id), COALESCE(SUM(o.total_cents),0)
                FROM orders o JOIN users u ON u.id = o.cashier_id
                WHERE o.status = 'PAID' AND o.created_at >= ? AND o.created_at < ?
                GROUP BY u.id, COALESCE(u.full_name, u.username)
                ORDER BY SUM(o.total_cents) DESC LIMIT 50`), from, to)
	if err == nil {
		for rows.Next() {
			var st StaffSales
			if err := rows.Scan(&st.UserID, &st.Name, &st.Orders, &st.SalesCents); err == nil {
				out.PerStaff = append(out.PerStaff, st)
			}
		}
		rows.Close()
	}
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM customers WHERE created_at >= ? AND created_at < ?`), from, to).Scan(&out.NewCustomers)

	// v1.6.3 — expenses booked today, returns issued today, and the
	// point-in-time stock picture for the day-end board.
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(SUM(amount_cents),0) FROM expenses WHERE spent_on = ?`), date).Scan(&out.ExpensesCents)
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(SUM(total_cents),0), COUNT(*) FROM returns WHERE created_at >= ? AND created_at < ?`), from, to).
		Scan(&out.ReturnsCents, &out.ReturnsCount)
	out.NetSalesCents = out.SalesCents - out.ReturnsCents
	_ = s.db.QueryRow(`
		SELECT COALESCE(SUM(stock_qty),0), COALESCE(SUM(stock_qty * cost_cents),0)
		FROM products WHERE track_stock = 1 AND is_active = 1`).Scan(&out.StockUnits, &out.StockValueCents)
	_ = s.db.QueryRow(s.db.Rebind(`
		SELECT COUNT(*) FROM products
		WHERE track_stock = 1 AND is_active = 1
		  AND stock_qty <= (CASE WHEN COALESCE(low_stock_units,0) > 0 THEN low_stock_units ELSE ? END)`),
		s.settings.GetInt("low_stock_threshold", 5)).Scan(&out.LowStockCount)
	return out, nil
}

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Now()
	}
	return t
}

// MonthlySummary is the KRA monthly-returns payload: one calendar month of
// VAT-relevant figures for the accountant. All integer cents.
type MonthlySummary struct {
	Month         string       `json:"month"` // YYYY-MM
	GrossCents    int64        `json:"grossCents"`
	NettCents     int64        `json:"nettCents"` // gross minus VAT (taxable value)
	VatCents      int64        `json:"vatCents"`  // VAT collected (orders.tax_cents sum)
	OrdersPaid    int          `json:"ordersPaid"`
	OrdersVoided  int          `json:"ordersVoided"`
	AvgOrderCents int64        `json:"avgOrderCents"`
	CashCents     int64        `json:"cashCents"`
	MpesaCents    int64        `json:"mpesaCents"`
	PaystackCents int64        `json:"paystackCents"` // card / mobile money via Paystack
	CreditCents   int64        `json:"creditCents"`   // prepaid store credit spent
	Discrepancies int          `json:"discrepancies"`
	TaxPercent    float64      `json:"taxPercent"`
	TaxIncluded   bool         `json:"taxIncluded"`
	Series        []DayPoint   `json:"series"` // per-day sales in the month
	TopProducts   []TopProduct `json:"topProducts"`
	// v1.6.3 — expenses booked in the month + refunds given out.
	ExpensesCents int64 `json:"expensesCents"`
	ReturnsCents  int64 `json:"returnsCents"`
}

// monthBounds returns the UTC window [from, toExcl) covering one EAT
// calendar month (exclusive end: first day of the next month, EAT).
func monthBounds(month string) (from, toExcl string) {
	if month == "" {
		month = eatToday()[:7]
	}
	start, err := time.ParseInLocation("2006-01", month, eastAfrica)
	if err != nil {
		start, _ = time.ParseInLocation("2006-01", eatToday()[:7], eastAfrica)
	}
	return eatStamp(start), eatStamp(start.AddDate(0, 1, 0))
}

// GetMonthlySummary aggregates one calendar month for KRA VAT returns.
func (s *Service) GetMonthlySummary(month string) (*MonthlySummary, error) {
	if month == "" {
		month = eatToday()[:7]
	}
	if _, err := time.Parse("2006-01", month); err != nil {
		return nil, fmt.Errorf("month must look like YYYY-MM")
	}
	from, to := monthBounds(month)
	out := &MonthlySummary{
		Month:       month,
		TaxPercent:  s.settings.GetFloat("tax_percent", 16),
		TaxIncluded: s.settings.GetBool("tax_included", true),
	}
	// Historic VAT: average of stored per-order rates (not current setting) — so changing 16→14 mid-year doesn't rewrite May.
	var avgTax *float64
	if err := s.db.QueryRow(s.db.Rebind(`SELECT AVG(tax_percent) FROM orders WHERE status='PAID' AND created_at >= ? AND created_at < ?`), from, to).Scan(&avgTax); err == nil && avgTax != nil {
		out.TaxPercent = *avgTax
	}
	// If month has mixed inclusive/exclusive, keep the majority.
	var includedCount, totalCount int
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM orders WHERE status='PAID' AND COALESCE(tax_included,1)=1 AND created_at >= ? AND created_at < ?`), from, to).Scan(&includedCount)
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM orders WHERE status='PAID' AND created_at >= ? AND created_at < ?`), from, to).Scan(&totalCount)
	if totalCount > 0 {
		out.TaxIncluded = includedCount*2 >= totalCount
	}

	err := s.db.QueryRow(s.db.Rebind(`
                SELECT COALESCE(SUM(total_cents),0), COALESCE(SUM(tax_cents),0), COUNT(*),
                        COALESCE(SUM(CASE WHEN discrepancy != 0 THEN 1 ELSE 0 END),0)
                FROM orders WHERE status = 'PAID' AND created_at >= ? AND created_at < ?`), from, to).
		Scan(&out.GrossCents, &out.VatCents, &out.OrdersPaid, &out.Discrepancies)
	if err != nil {
		return nil, err
	}
	out.NettCents = out.GrossCents - out.VatCents
	if out.OrdersPaid > 0 {
		out.AvgOrderCents = out.GrossCents / int64(out.OrdersPaid)
	}
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM orders WHERE status = 'VOIDED' AND created_at >= ? AND created_at < ?`), from, to).Scan(&out.OrdersVoided)
	_ = s.db.QueryRow(s.db.Rebind(`
                SELECT
                        COALESCE(SUM(CASE WHEN method = 'cash' THEN amount_cents ELSE 0 END),0),
                        COALESCE(SUM(CASE WHEN method = 'mpesa' THEN amount_cents ELSE 0 END),0),
                        COALESCE(SUM(CASE WHEN method = 'paystack' THEN amount_cents ELSE 0 END),0),
                        COALESCE(SUM(CASE WHEN method = 'credit' THEN amount_cents ELSE 0 END),0)
                FROM payments p JOIN orders o ON o.id = p.order_id WHERE p.status = 'COMPLETED' AND o.created_at >= ? AND o.created_at < ?`), from, to).
		Scan(&out.CashCents, &out.MpesaCents, &out.PaystackCents, &out.CreditCents)

	// Per-day series for the month (fills zero days).
	start, _ := time.Parse("2006-01", month)
	end := start.AddDate(0, 1, 0).AddDate(0, 0, -1)
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		f, t := dayBounds(day)
		var sales int64
		var count int
		_ = s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(SUM(total_cents),0), COUNT(*) FROM orders WHERE status = 'PAID' AND created_at >= ? AND created_at < ?`), f, t).Scan(&sales, &count)
		out.Series = append(out.Series, DayPoint{Date: day, SalesCents: sales, Orders: count})
	}

	// Top products of the month.
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT oi.product_id, COALESCE(oi.name,''), SUM(oi.qty), SUM(oi.line_total_cents)
                FROM order_items oi JOIN orders o ON o.id = oi.order_id
                WHERE o.status = 'PAID' AND o.created_at >= ? AND o.created_at < ?
                GROUP BY oi.product_id, oi.name ORDER BY SUM(oi.line_total_cents) DESC LIMIT 10`), from, to)
	if err == nil {
		for rows.Next() {
			var tp TopProduct
			var qty any
			var sales any
			if err := rows.Scan(&tp.ProductID, &tp.Name, &qty, &sales); err == nil {
				tp.Qty = asInt(qty)
				tp.SalesCents = asInt64(sales)
				out.TopProducts = append(out.TopProducts, tp)
			}
		}
		rows.Close()
	}

	// v1.6.3 — money out (expenses) and money back (returns) in the month.
	monthStart, monthEnd := from, to
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(SUM(amount_cents),0) FROM expenses WHERE spent_on >= ? AND spent_on < ?`),
		eatDayOfMonth(monthStart), eatDayOfMonth(monthEnd)).Scan(&out.ExpensesCents)
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(SUM(total_cents),0) FROM returns WHERE created_at >= ? AND created_at < ?`),
		monthStart, monthEnd).Scan(&out.ReturnsCents)
	return out, nil
}

// eatDayOfMonth converts an EAT timestamp to its YYYY-MM-DD calendar day.
func eatDayOfMonth(stamp string) string {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return eatToday()
	}
	return t.In(eastAfrica).Format("2006-01-02")
}

func asInt(v any) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case int:
		return x
	}
	return 0
}

func asInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
}

// ListAudit returns recent audit entries.
func (s *Service) ListAudit(limit int) []models.AuditEntry {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(fmt.Sprintf(`
                SELECT id, user_id, COALESCE(username,''), action, COALESCE(entity,''), COALESCE(entity_id,''),
                        COALESCE(details,''), created_at FROM audit_log ORDER BY id DESC LIMIT %d`, limit))
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []models.AuditEntry
	for rows.Next() {
		var a models.AuditEntry
		if err := rows.Scan(&a.ID, &a.UserID, &a.Username, &a.Action, &a.Entity, &a.EntityID, &a.Details, &a.CreatedAt); err != nil {
			return out
		}
		a.CreatedAt = normTime(a.CreatedAt)
		out = append(out, a)
	}
	return out
}
