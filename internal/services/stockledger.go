package services

// stockledger.go — the append-only stock ledger (v1.6.3, migration v22).
// Every place stock_qty moves writes a row here: kind + signed delta +
// balance_after + who. products.stock_qty stays the fast projection; the
// ledger is the audit trail ("quantity is a projection" — the pattern
// OSPOS and the friend's SmartPOS both run on).

import (
	"database/sql"
	"fmt"

	"posapp/internal/auth"
	"posapp/internal/models"
)

// recordStockMoveTx appends one ledger row inside the caller's transaction.
// balance_after is read from the product row AFTER the caller's stock_qty
// update, so the ledger always reconciles with the projection.
func recordStockMoveTx(s *Service, tx *sql.Tx, productID, delta int64,
	kind, reference, note string, userID int64, username string) error {
	var bal int64
	if err := tx.QueryRow(s.db.Rebind(`SELECT stock_qty FROM products WHERE id = ?`), productID).Scan(&bal); err != nil {
		return fmt.Errorf("stock ledger balance read (product %d): %w", productID, err)
	}
	_, err := tx.Exec(s.db.Rebind(`INSERT INTO stock_movements
                (product_id, delta, balance_after, kind, reference, note, user_id, username, device_id, client_uuid)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		productID, delta, bal, kind, truncStr(reference, 80), truncStr(note, 200), userID, username, s.deviceID(), "")
	if err != nil {
		return fmt.Errorf("stock ledger insert (product %d, %s): %w", productID, kind, err)
	}
	return nil
}

// RecordStockMove is the transaction-less entry point for handlers that
// adjust stock outside an explicit tx (AdjustStock). It opens its own.
func (s *Service) RecordStockMove(productID, delta int64, kind, reference, note string, p *auth.Principal) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	userID, username := int64(0), ""
	if p != nil {
		userID, username = p.ID, p.Username
	}
	if err := recordStockMoveTx(s, tx, productID, delta, kind, reference, note, userID, username); err != nil {
		return err
	}
	return tx.Commit()
}

// ListProductMovements returns the newest-first ledger for one product.
func (s *Service) ListProductMovements(productID int64, limit int) ([]models.StockMovement, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT m.id, m.product_id, COALESCE(p.name,''), COALESCE(p.sku,''),
                        m.delta, m.balance_after, m.kind, COALESCE(m.reference,''), COALESCE(m.note,''),
                        m.user_id, COALESCE(m.username,''), COALESCE(m.device_id,''), m.created_at
                FROM stock_movements m LEFT JOIN products p ON p.id = m.product_id
                WHERE m.product_id = ?
                ORDER BY m.id DESC LIMIT ?`), productID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMovements(rows)
}

// ListStockMovements returns the newest-first ledger across the shop.
// Optional filter kind=SALE|COUNT|... and date=YYYY-MM-DD.
func (s *Service) ListStockMovements(kind, date string, limit int) ([]models.StockMovement, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `
                SELECT m.id, m.product_id, COALESCE(p.name,''), COALESCE(p.sku,''),
                        m.delta, m.balance_after, m.kind, COALESCE(m.reference,''), COALESCE(m.note,''),
                        m.user_id, COALESCE(m.username,''), COALESCE(m.device_id,''), m.created_at
                FROM stock_movements m LEFT JOIN products p ON p.id = m.product_id
                WHERE 1=1`
	args := []any{}
	if kind != "" {
		q += ` AND m.kind = ?`
		args = append(args, kind)
	}
	if date != "" {
		if !validDay(date) {
			return nil, fmt.Errorf("date must be YYYY-MM-DD")
		}
		from, toExcl := dayBounds(date)
		q += ` AND m.created_at >= ? AND m.created_at < ?`
		args = append(args, from, toExcl)
	}
	q += ` ORDER BY m.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(s.db.Rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMovements(rows)
}

func scanMovements(rows *sql.Rows) ([]models.StockMovement, error) {
	out := []models.StockMovement{}
	for rows.Next() {
		var m models.StockMovement
		if err := rows.Scan(&m.ID, &m.ProductID, &m.ProductName, &m.SKU,
			&m.Delta, &m.BalanceAfter, &m.Kind, &m.Reference, &m.Note,
			&m.UserID, &m.Username, &m.DeviceID, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
