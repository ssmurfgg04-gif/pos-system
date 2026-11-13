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

// DisposeProduct (v1.7.0) writes stock off the shelf with a structured
// reason — the friend's SmartPOS disposal module (expired / spoilt / lost /
// damaged / other) with the loss value computable at report time from the
// product's cost. The deduction is guarded (never below zero) and lands in
// the ledger like every other stock move.
func (s *Service) DisposeProduct(productID, qty int64, reason, note string, p *auth.Principal) (map[string]any, error) {
	if qty <= 0 {
		return nil, fmt.Errorf("%w: disposal quantity must be positive", ErrInvalidInput)
	}
	switch reason {
	case "expired", "spoilt", "lost", "damaged", "other":
	default:
		return nil, fmt.Errorf("%w: invalid disposal reason %q (expired|spoilt|lost|damaged|other)", ErrInvalidInput, reason)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var track int
	if err := tx.QueryRow(s.db.Rebind(`SELECT COALESCE(track_stock,1) FROM products WHERE id = ?`), productID).Scan(&track); err != nil {
		return nil, fmt.Errorf("product %d: %w", productID, err)
	}
	if track == 1 {
		gres, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty = stock_qty - ?, updated_at = ? WHERE id = ? AND stock_qty >= ?`),
			qty, nowStamp(), productID, qty)
		if err != nil {
			return nil, err
		}
		if n, _ := gres.RowsAffected(); n != 1 {
			return nil, fmt.Errorf("%w: not enough stock to dispose (requested %d)", ErrInsufficientStock, qty)
		}
		if err := recordStockMoveTx(s, tx, productID, -qty, models.MoveDisposal, reason, truncStr(note, 200), p.ID, p.Username); err != nil {
			return nil, err
		}
	} else {
		// Untracked product: nothing to deduct, still record the event so
		// the report shows the loss claim.
		if err := recordStockMoveTx(s, tx, productID, 0, models.MoveDisposal, reason, truncStr("untracked: "+note, 200), p.ID, p.Username); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.EmitProduct(productID, true)
	s.Audit(p.ID, p.Username, "STOCK_DISPOSAL", "product", fmt.Sprintf("%d", productID), fmt.Sprintf("%d x %s: %s", qty, reason, note))
	var stock int64
	if err := s.db.QueryRow(s.db.Rebind(`SELECT stock_qty FROM products WHERE id = ?`), productID).Scan(&stock); err != nil {
		return nil, err
	}
	return map[string]any{"id": productID, "stockQty": stock, "disposed": qty, "reason": reason}, nil
}
