package services

import (
	"database/sql"
	"fmt"
	"math"
	"strings"

	"posapp/internal/auth"
	"posapp/internal/models"
)

// ListSuppliers returns active-first matches for name/phone search.
func (s *Service) ListSuppliers(search string) ([]models.Supplier, error) {
	q := "%" + strings.TrimSpace(search) + "%"
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT id, name, COALESCE(phone,''), COALESCE(email,''), COALESCE(address,''),
                        COALESCE(notes,''), is_active, created_at, updated_at
                FROM suppliers
                WHERE name LIKE ? OR phone LIKE ?
                ORDER BY is_active DESC, name ASC
                LIMIT 200`), q, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Supplier
	for rows.Next() {
		var c models.Supplier
		var active int
		if err := rows.Scan(&c.ID, &c.Name, &c.Phone, &c.Email, &c.Address,
			&c.Notes, &active, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Active = active == 1
		out = append(out, c)
	}
	if out == nil {
		out = []models.Supplier{}
	}
	return out, rows.Err()
}

// GetSupplier loads one supplier.
func (s *Service) GetSupplier(id int64) (*models.Supplier, error) {
	var c models.Supplier
	var active int
	err := s.db.QueryRow(s.db.Rebind(`
                SELECT id, name, COALESCE(phone,''), COALESCE(email,''), COALESCE(address,''),
                        COALESCE(notes,''), is_active, created_at, updated_at
                FROM suppliers WHERE id = ?`), id).
		Scan(&c.ID, &c.Name, &c.Phone, &c.Email, &c.Address,
			&c.Notes, &active, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, ErrNotFound
	}
	c.Active = active == 1
	return &c, nil
}

// CreateSupplier registers a supplier.
func (s *Service) CreateSupplier(name, phone, email, address, notes string, p *auth.Principal) (*models.Supplier, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("supplier name required")
	}
	now := nowStamp()
	res, err := s.db.Exec(s.db.Rebind(`
                INSERT INTO suppliers (name, phone, email, address, notes, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, ?)`),
		name, strings.TrimSpace(phone), strings.TrimSpace(email),
		strings.TrimSpace(address), strings.TrimSpace(notes), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	s.Audit(p.ID, p.Username, "SUPPLIER_CREATED", "supplier", fmt.Sprint(id), name)
	return s.GetSupplier(id)
}

// UpdateSupplier edits a supplier.
func (s *Service) UpdateSupplier(id int64, name, phone, email, address, notes string, active bool, p *auth.Principal) (*models.Supplier, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("supplier name required")
	}
	activeInt := 0
	if active {
		activeInt = 1
	}
	res, err := s.db.Exec(s.db.Rebind(`
                UPDATE suppliers SET name = ?, phone = ?, email = ?, address = ?,
                        notes = ?, is_active = ?, updated_at = ? WHERE id = ?`),
		name, strings.TrimSpace(phone), strings.TrimSpace(email),
		strings.TrimSpace(address), strings.TrimSpace(notes),
		activeInt, nowStamp(), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrNotFound
	}
	s.Audit(p.ID, p.Username, "SUPPLIER_UPDATED", "supplier", fmt.Sprint(id), name)
	return s.GetSupplier(id)
}

// POItemInput is one line on a new purchase order (prices re-read
// server-side). ExpiryDate/BatchNo become the goods-in lot for
// batch-tracked products on receive (v1.8.0).
type POItemInput struct {
	ProductID  int64
	QtyMilli   int64
	CostCents  int64
	ExpiryDate string
	BatchNo    string
}

// CreatePO drafts a PENDING purchase order (no stock movement until receive).
func (s *Service) CreatePO(supplierID int64, items []POItemInput, note string, p *auth.Principal) (*models.PurchaseOrder, error) {
	sup, err := s.GetSupplier(supplierID)
	if err != nil {
		return nil, err
	}
	if !sup.Active {
		return nil, fmt.Errorf("supplier is inactive")
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("purchase order needs at least one line")
	}
	if len(items) > models.MaxOrderLines {
		return nil, fmt.Errorf("too many lines (max %d)", models.MaxOrderLines)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	number, err := s.nextDocNumber(tx, "PO", 0)
	if err != nil {
		return nil, err
	}
	now := nowStamp()
	res, err := tx.Exec(s.db.Rebind(`
                INSERT INTO purchase_orders (number, supplier_id, status, subtotal_cents, note, created_by, created_at)
                VALUES (?, ?, 'PENDING', 0, ?, ?, ?)`),
		number, supplierID, strings.TrimSpace(note), p.ID, now)
	if err != nil {
		return nil, err
	}
	poID, _ := res.LastInsertId()
	var subtotal int64
	for _, it := range items {
		if it.QtyMilli <= 0 {
			return nil, fmt.Errorf("quantity must be positive")
		}
		if it.QtyMilli > models.MaxOrderQtyMilli {
			return nil, fmt.Errorf("quantity exceeds maximum (%d milli)", models.MaxOrderQtyMilli)
		}
		if it.CostCents < 0 || it.CostCents > models.MaxPriceCents {
			return nil, fmt.Errorf("cost out of range")
		}
		if !validExpiryDate(it.ExpiryDate) {
			return nil, fmt.Errorf("expiry date must be YYYY-MM-DD (or empty)")
		}
		var name, sku string
		var active int
		err := tx.QueryRow(s.db.Rebind(`
                        SELECT name, COALESCE(sku,''), COALESCE(is_active,1) FROM products WHERE id = ?`),
			it.ProductID).Scan(&name, &sku, &active)
		if err != nil {
			return nil, fmt.Errorf("product %d: %w", it.ProductID, ErrNotFound)
		}
		if active != 1 {
			return nil, fmt.Errorf("product %d is inactive", it.ProductID)
		}
		if it.CostCents > 0 && it.QtyMilli > math.MaxInt64/it.CostCents {
			return nil, fmt.Errorf("line total overflow")
		}
		line := milliLineTotal(it.CostCents, it.QtyMilli)
		subtotal += line
		if subtotal < 0 {
			return nil, fmt.Errorf("order total overflow")
		}
		if _, err := tx.Exec(s.db.Rebind(`
                        INSERT INTO purchase_order_items (po_id, product_id, name, sku, qty_milli, qty, cost_cents, line_total_cents, expiry_date, batch_no)
                        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			poID, it.ProductID, name, sku, it.QtyMilli, it.QtyMilli/1000, it.CostCents, line, it.ExpiryDate, truncStr(strings.TrimSpace(it.BatchNo), 80)); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(s.db.Rebind(`UPDATE purchase_orders SET subtotal_cents = ? WHERE id = ?`), subtotal, poID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.Audit(p.ID, p.Username, "PO_CREATED", "purchase_order", number, fmt.Sprintf("total %d", subtotal))
	return s.GetPO(poID)
}

// GetPO loads an order with its lines.
func (s *Service) GetPO(poID int64) (*models.PurchaseOrder, error) {
	var o models.PurchaseOrder
	var supplierName string
	err := s.db.QueryRow(s.db.Rebind(`
                SELECT o.id, o.number, o.supplier_id, COALESCE(s.name,''), o.status,
                        o.subtotal_cents, COALESCE(o.note,''), o.created_at, COALESCE(o.received_at,'')
                FROM purchase_orders o JOIN suppliers s ON s.id = o.supplier_id
                WHERE o.id = ?`), poID).
		Scan(&o.ID, &o.Number, &o.SupplierID, &supplierName, &o.Status,
			&o.SubtotalCents, &o.Note, &o.CreatedAt, &o.ReceivedAt)
	if err != nil {
		return nil, ErrNotFound
	}
	o.SupplierName = supplierName
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT id, po_id, product_id, name, sku, qty_milli, cost_cents, line_total_cents, COALESCE(expiry_date,''), COALESCE(batch_no,'')
                FROM purchase_order_items WHERE po_id = ? ORDER BY id`), poID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	o.Items = []models.POItem{}
	for rows.Next() {
		var it models.POItem
		if err := rows.Scan(&it.ID, &it.POID, &it.ProductID, &it.Name,
			&it.SKU, &it.QtyMilli, &it.CostCents, &it.LineTotalCents, &it.ExpiryDate, &it.BatchNo); err != nil {
			return nil, err
		}
		o.Items = append(o.Items, it)
	}
	return &o, rows.Err()
}

// ListPOs returns newest-first orders with their lines (one extra query).
func (s *Service) ListPOs() ([]models.PurchaseOrder, error) {
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT o.id, o.number, o.supplier_id, COALESCE(s.name,''), o.status,
                        o.subtotal_cents, COALESCE(o.note,''), o.created_at, COALESCE(o.received_at,'')
                FROM purchase_orders o JOIN suppliers s ON s.id = o.supplier_id
                ORDER BY o.id DESC LIMIT 200`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.PurchaseOrder{}
	byID := map[int64]*models.PurchaseOrder{}
	var ids []int64
	for rows.Next() {
		var o models.PurchaseOrder
		if err := rows.Scan(&o.ID, &o.Number, &o.SupplierID, &o.SupplierName,
			&o.Status, &o.SubtotalCents, &o.Note, &o.CreatedAt, &o.ReceivedAt); err != nil {
			return nil, err
		}
		o.Items = []models.POItem{}
		out = append(out, o)
		byID[o.ID] = &out[len(out)-1]
		ids = append(ids, o.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	holders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		holders[i] = "?"
		args[i] = id
	}
	irows, err := s.db.Query(s.db.Rebind(`
                SELECT id, po_id, product_id, name, sku, qty_milli, cost_cents, line_total_cents, COALESCE(expiry_date,''), COALESCE(batch_no,'')
                FROM purchase_order_items WHERE po_id IN (`+strings.Join(holders, ",")+`) ORDER BY id`), args...)
	if err != nil {
		return nil, err
	}
	defer irows.Close()
	for irows.Next() {
		var it models.POItem
		if err := irows.Scan(&it.ID, &it.POID, &it.ProductID, &it.Name,
			&it.SKU, &it.QtyMilli, &it.CostCents, &it.LineTotalCents, &it.ExpiryDate, &it.BatchNo); err != nil {
			return nil, err
		}
		if o, ok := byID[it.POID]; ok {
			o.Items = append(o.Items, it)
		}
	}
	return out, irows.Err()
}

// ListTakes returns newest-first stock takes with line counts (open one for lines).
func (s *Service) ListTakes() ([]models.StockTake, error) {
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT t.id, t.number, t.status, COALESCE(t.note,''), t.created_at, COALESCE(t.applied_at,''),
                        (SELECT COUNT(*) FROM stock_take_items WHERE take_id = t.id)
                FROM stock_takes t ORDER BY t.id DESC LIMIT 200`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.StockTake{}
	for rows.Next() {
		var t models.StockTake
		if err := rows.Scan(&t.ID, &t.Number, &t.Status, &t.Note, &t.CreatedAt, &t.AppliedAt, &t.ItemCount); err != nil {
			return nil, err
		}
		t.Items = []models.StockTakeItem{}
		out = append(out, t)
	}
	return out, rows.Err()
}

// avgCost returns the weighted-average unit cost in cents, rounded half-up.
// Quantities are milli-units (v1.8.0) — the ratio is identical either way.
// Zero stock (or zero receipt) falls back to the incoming unit cost.
func avgCost(oldStockMilli int64, oldCost int64, recvQtyMilli int64, unitCost int64) int64 {
	newStock := oldStockMilli + recvQtyMilli
	if newStock <= 0 || recvQtyMilli <= 0 {
		return unitCost
	}
	num := oldStockMilli*oldCost + recvQtyMilli*unitCost
	return (num + newStock/2) / newStock
}

// ReceivePO posts a PENDING PO: stock_qty += qty per tracked line and
// cost_cents becomes the weighted average. One tx, guarded status flip.
func (s *Service) ReceivePO(poID int64, p *auth.Principal) (*models.PurchaseOrder, error) {
	po, err := s.GetPO(poID)
	if err != nil {
		return nil, err
	}
	if po.Status != models.POPending {
		return nil, fmt.Errorf("%w: only pending orders receive", ErrInvalidState)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, it := range po.Items {
		var stock int64
		var track, batches int
		var cost int64
		if err := tx.QueryRow(s.db.Rebind(`
                        SELECT stock_qty_milli, cost_cents, COALESCE(track_stock,1), COALESCE(track_batches,0) FROM products WHERE id = ?`),
			it.ProductID).Scan(&stock, &cost, &track, &batches); err != nil {
			return nil, fmt.Errorf("product %d: %w", it.ProductID, ErrNotFound)
		}
		if track == 0 {
			continue
		}
		newStock := stock + it.QtyMilli
		newCost := avgCost(stock, cost, it.QtyMilli, it.CostCents)
		if _, err := tx.Exec(s.db.Rebind(`
                        UPDATE products SET stock_qty_milli = ?, stock_qty = ? / 1000, cost_cents = ? WHERE id = ?`),
			newStock, newStock, newCost, it.ProductID); err != nil {
			return nil, err
		}
		if err := recordStockMoveTx(s, tx, it.ProductID, it.QtyMilli, models.MovePOReceive, po.Number, "", p.ID, p.Username); err != nil {
			return nil, err
		}
		// v1.8.0: a received line for a batch-tracked product BECOMES a
		// lot — batch number and expiry ride on the PO line.
		if batches == 1 {
			batchNo := strings.TrimSpace(it.BatchNo)
			if batchNo == "" {
				batchNo = po.Number
			}
			if !validExpiryDate(it.ExpiryDate) {
				return nil, fmt.Errorf("%w: expiry date must be YYYY-MM-DD (or empty)", ErrInvalidInput)
			}
			if _, err := tx.Exec(s.db.Rebind(`
                        INSERT INTO product_batches (product_id, batch_no, expiry_date, qty_received_milli, qty_remaining_milli, cost_cents, supplier_id, reference, status, updated_at)
                        VALUES (?, ?, ?, ?, ?, ?, (SELECT supplier_id FROM purchase_orders WHERE id = ?), ?, 'ACTIVE', ?)`),
				it.ProductID, truncStr(batchNo, 80), it.ExpiryDate, it.QtyMilli, it.QtyMilli, it.CostCents, poID, po.Number, nowStamp()); err != nil {
				return nil, err
			}
		}
	}
	res, err := tx.Exec(s.db.Rebind(`
                UPDATE purchase_orders SET status = 'RECEIVED', received_at = ?
                WHERE id = ? AND status = 'PENDING'`), nowStamp(), poID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrInvalidState
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Cross-till propagation: every received line is a stock delta event,
	// so tills that share this team see the new stock without a manual
	// adjust (same event vocabulary as sales and stock takes). The
	// receive also moved the weighted-average COST — a product event per
	// line (stockSet=false: the delta above already carries quantity)
	// keeps every till's COGS/margin reports identical.
	for _, it := range po.Items {
		s.EmitStockDelta(it.SKU, it.QtyMilli)
		var pid int64
		s.db.QueryRow(s.db.Rebind(`SELECT id FROM products WHERE sku = ?`), it.SKU).Scan(&pid)
		if pid != 0 {
			s.EmitProduct(pid, false)
		}
	}
	s.Audit(p.ID, p.Username, "PO_RECEIVED", "purchase_order", po.Number, fmt.Sprintf("total %d", po.SubtotalCents))
	return s.GetPO(poID)
}

// CancelPO voids a PENDING order (nothing was ever deducted, so nothing restores).
func (s *Service) CancelPO(poID int64, reason string, p *auth.Principal) (*models.PurchaseOrder, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(s.db.Rebind(`
                UPDATE purchase_orders SET status = 'CANCELLED', note = ?
                WHERE id = ? AND status = 'PENDING'`), strings.TrimSpace(reason), poID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("%w: only pending orders cancel", ErrInvalidState)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.Audit(p.ID, p.Username, "PO_CANCELLED", "purchase_order", fmt.Sprint(poID), reason)
	return s.GetPO(poID)
}

// loadTakeItemsTx reads a take's lines inside the caller's tx.
func (s *Service) loadTakeItemsTx(tx *sql.Tx, takeID int64) ([]models.StockTakeItem, error) {
	rows, err := tx.Query(s.db.Rebind(`
                SELECT id, take_id, product_id, name, sku, expected_qty_milli, counted_qty_milli
                FROM stock_take_items WHERE take_id = ? ORDER BY id`), takeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.StockTakeItem{}
	for rows.Next() {
		var it models.StockTakeItem
		if err := rows.Scan(&it.ID, &it.TakeID, &it.ProductID, &it.Name,
			&it.SKU, &it.ExpectedQtyMilli, &it.CountedQtyMilli); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// CreateTake snapshots current stock as expected quantities. Empty productIDs
// means all active tracked products.
func (s *Service) CreateTake(productIDs []int64, note string, p *auth.Principal) (*models.StockTake, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	number, err := s.nextDocNumber(tx, "STK", 0)
	if err != nil {
		return nil, err
	}
	now := nowStamp()
	res, err := tx.Exec(s.db.Rebind(`
                INSERT INTO stock_takes (number, status, note, created_by, created_at)
                VALUES (?, 'OPEN', ?, ?, ?)`), number, strings.TrimSpace(note), p.ID, now)
	if err != nil {
		return nil, err
	}
	takeID, _ := res.LastInsertId()
	var rows *sql.Rows
	if len(productIDs) == 0 {
		rows, err = tx.Query(s.db.Rebind(`
                        SELECT id, name, COALESCE(sku,''), stock_qty_milli FROM products
                        WHERE is_active = 1 AND COALESCE(track_stock,1) = 1 ORDER BY id`))
	} else {
		// Per-id lookups keep the tx portable across dialects.
		for _, pid := range productIDs {
			var name, sku string
			var stock int64
			var active int
			if err := tx.QueryRow(s.db.Rebind(`
                                SELECT name, COALESCE(sku,''), stock_qty_milli, COALESCE(is_active,1)
                                FROM products WHERE id = ?`), pid).Scan(&name, &sku, &stock, &active); err != nil {
				return nil, fmt.Errorf("product %d: %w", pid, ErrNotFound)
			}
			if _, err := tx.Exec(s.db.Rebind(`
                                INSERT INTO stock_take_items (take_id, product_id, name, sku, expected_qty_milli, expected_qty, counted_qty_milli, counted_qty)
                                VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
				takeID, pid, name, sku, stock, stock/1000, stock, stock/1000); err != nil {
				return nil, err
			}
		}
	}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var pid int64
			var name, sku string
			var stock int64
			if err := rows.Scan(&pid, &name, &sku, &stock); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(s.db.Rebind(`
                                INSERT INTO stock_take_items (take_id, product_id, name, sku, expected_qty_milli, expected_qty, counted_qty_milli, counted_qty)
                                VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
				takeID, pid, name, sku, stock, stock/1000, stock, stock/1000); err != nil {
				return nil, err
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.Audit(p.ID, p.Username, "TAKE_CREATED", "stock_take", number, note)
	return s.GetTake(takeID)
}

// GetTake loads a take with its lines.
func (s *Service) GetTake(takeID int64) (*models.StockTake, error) {
	var t models.StockTake
	err := s.db.QueryRow(s.db.Rebind(`
                SELECT id, number, status, COALESCE(note,''), created_at, COALESCE(applied_at,'')
                FROM stock_takes WHERE id = ?`), takeID).
		Scan(&t.ID, &t.Number, &t.Status, &t.Note, &t.CreatedAt, &t.AppliedAt)
	if err != nil {
		return nil, ErrNotFound
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	items, err := s.loadTakeItemsTx(tx, takeID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	t.Items = items
	return &t, nil
}

// CountTake records counted quantities (milli-units) on an OPEN take.
func (s *Service) CountTake(takeID int64, counts map[int64]int64, p *auth.Principal) (*models.StockTake, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(s.db.Rebind(`SELECT status FROM stock_takes WHERE id = ?`), takeID).Scan(&status); err != nil {
		return nil, ErrNotFound
	}
	if status != models.TakeOpen {
		return nil, fmt.Errorf("%w: only open takes take counts", ErrInvalidState)
	}
	for pid, qty := range counts {
		if qty < 0 || qty > models.MaxStockQtyMilli {
			return nil, fmt.Errorf("count out of range (0-%d milli)", models.MaxStockQtyMilli)
		}
		res, err := tx.Exec(s.db.Rebind(`
                        UPDATE stock_take_items SET counted_qty_milli = ?, counted_qty = ? / 1000 WHERE take_id = ? AND product_id = ?`),
			qty, qty, takeID, pid)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil, fmt.Errorf("product %d is not on this take", pid)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.Audit(p.ID, p.Username, "TAKE_COUNTED", "stock_take", fmt.Sprint(takeID), fmt.Sprintf("%d lines", len(counts)))
	return s.GetTake(takeID)
}

// ApplyTake writes counted quantities into products.stock_qty in one tx.
func (s *Service) ApplyTake(takeID int64, p *auth.Principal) (*models.StockTake, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status, number string
	if err := tx.QueryRow(s.db.Rebind(`SELECT status, number FROM stock_takes WHERE id = ?`), takeID).Scan(&status, &number); err != nil {
		return nil, ErrNotFound
	}
	if status != models.TakeOpen {
		return nil, fmt.Errorf("%w: only open takes apply", ErrInvalidState)
	}
	items, err := s.loadTakeItemsTx(tx, takeID)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		var current int64
		var batches int
		if err := tx.QueryRow(s.db.Rebind(`SELECT stock_qty_milli, COALESCE(track_batches,0) FROM products WHERE id = ?`), it.ProductID).Scan(&current, &batches); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty_milli = ?, stock_qty = ? / 1000 WHERE id = ?`), it.CountedQtyMilli, it.CountedQtyMilli, it.ProductID); err != nil {
			return nil, err
		}
		if it.CountedQtyMilli != current {
			if err := recordStockMoveTx(s, tx, it.ProductID, it.CountedQtyMilli-current, models.MoveCount, number, "", p.ID, p.Username); err != nil {
				return nil, err
			}
			// v1.8.0: the correction also has to land inside the lots —
			// negative deltas FEFO off ACTIVE batches, positive ones
			// arrive on the OPENING batch.
			if batches == 1 {
				if err := adjustBatchStockTx(s, tx, it.ProductID, it.CountedQtyMilli-current); err != nil {
					return nil, err
				}
			}
		}
	}
	res, err := tx.Exec(s.db.Rebind(`
                UPDATE stock_takes SET status = 'APPLIED', applied_at = ? WHERE id = ? AND status = 'OPEN'`),
		nowStamp(), takeID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrInvalidState
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.Audit(p.ID, p.Username, "TAKE_APPLIED", "stock_take", number, fmt.Sprintf("%d lines", len(items)))
	// Counted quantities are absolute stock changes — they follow the
	// team like every other admin write (the legacy take silently forked
	// stock across tills before).
	for _, it := range items {
		s.EmitProduct(it.ProductID, true)
	}
	return s.GetTake(takeID)
}

// CancelTake voids an OPEN take (counts never applied, nothing restores).
func (s *Service) CancelTake(takeID int64, reason string, p *auth.Principal) (*models.StockTake, error) {
	res, err := s.db.Exec(s.db.Rebind(`
                UPDATE stock_takes SET status = 'CANCELLED', note = ? WHERE id = ? AND status = 'OPEN'`),
		strings.TrimSpace(reason), takeID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("%w: only open takes cancel", ErrInvalidState)
	}
	s.Audit(p.ID, p.Username, "TAKE_CANCELLED", "stock_take", fmt.Sprint(takeID), reason)
	return s.GetTake(takeID)
}
