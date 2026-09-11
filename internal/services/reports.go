package services

import (
	"fmt"
	"time"

	"posapp/internal/models"
)

// DailySummary is the reports payload for one day.
type DailySummary struct {
	Date         string             `json:"date"`
	SalesCents   int64              `json:"salesCents"`
	OrdersPaid   int                `json:"ordersPaid"`
	OrdersOpen   int                `json:"ordersOpen"`
	OrdersVoided int                `json:"ordersVoided"`
	AvgOrderCents int64             `json:"avgOrderCents"`
	CashCents    int64              `json:"cashCents"`
	MpesaCents   int64              `json:"mpesaCents"`
	Discrepancies int               `json:"discrepancies"`
	TopProducts  []TopProduct       `json:"topProducts"`
	Series       []DayPoint         `json:"series"` // 7-day window ending on date
}

type TopProduct struct {
	ProductID    int64  `json:"productId"`
	Name         string `json:"name"`
	Qty          int    `json:"qty"`
	SalesCents   int64  `json:"salesCents"`
}

type DayPoint struct {
	Date       string `json:"date"`
	SalesCents int64  `json:"salesCents"`
	Orders     int    `json:"orders"`
}

func dayBounds(date string) (from, to string) {
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	return date + "T00:00:00", date + "T23:59:59"
}

// GetDailySummary aggregates one day (integer cents everywhere).
func (s *Service) GetDailySummary(date string) (*DailySummary, error) {
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	from, to := dayBounds(date)
	out := &DailySummary{Date: date}

	err := s.db.QueryRow(s.db.Rebind(`
		SELECT COALESCE(SUM(total_cents),0), COUNT(*),
			COALESCE(AVG(total_cents),0),
			COALESCE(SUM(CASE WHEN discrepancy != 0 THEN 1 ELSE 0 END),0)
		FROM orders WHERE status = 'PAID' AND created_at BETWEEN ? AND ?`), from, to).
		Scan(&out.SalesCents, &out.OrdersPaid, &out.AvgOrderCents, &out.Discrepancies)
	if err != nil {
		return nil, err
	}
	if out.OrdersPaid > 0 {
		out.AvgOrderCents = out.SalesCents / int64(out.OrdersPaid)
	}
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM orders WHERE status = 'PENDING' AND created_at BETWEEN ? AND ?`), from, to).Scan(&out.OrdersOpen)
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM orders WHERE status = 'VOIDED' AND created_at BETWEEN ? AND ?`), from, to).Scan(&out.OrdersVoided)
	_ = s.db.QueryRow(s.db.Rebind(`
		SELECT
			COALESCE(SUM(CASE WHEN method = 'cash' THEN amount_cents ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN method = 'mpesa' THEN amount_cents ELSE 0 END),0)
		FROM payments WHERE status = 'COMPLETED' AND completed_at BETWEEN ? AND ?`), from, to).
		Scan(&out.CashCents, &out.MpesaCents)

	// Top products of the day.
	rows, err := s.db.Query(s.db.Rebind(`
		SELECT oi.product_id, COALESCE(oi.name,''), SUM(oi.qty), SUM(oi.line_total_cents)
		FROM order_items oi JOIN orders o ON o.id = oi.order_id
		WHERE o.status = 'PAID' AND o.created_at BETWEEN ? AND ?
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
		_ = s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(SUM(total_cents),0), COUNT(*) FROM orders WHERE status = 'PAID' AND created_at BETWEEN ? AND ?`), f, t).Scan(&sales, &count)
		out.Series = append(out.Series, DayPoint{Date: day, SalesCents: sales, Orders: count})
	}
	return out, nil
}

func mustDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Now()
	}
	return t
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
