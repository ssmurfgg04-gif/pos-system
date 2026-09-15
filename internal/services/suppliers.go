package services

import (
        "database/sql"
        "fmt"
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

// POItemInput is one line on a new purchase order (prices re-read server-side).
type POItemInput struct {
        ProductID int64
        Qty       int
        CostCents int64
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
        tx, err := s.db.Begin()
        if err != nil {
                return nil, err
        }
        defer tx.Rollback()
        number, err := s.nextDocNumber(tx, "PO")
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
                if it.Qty <= 0 {
                        return nil, fmt.Errorf("quantity must be positive")
                }
                if it.CostCents < 0 {
                        return nil, fmt.Errorf("cost cannot be negative")
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
                line := int64(it.Qty) * it.CostCents
                subtotal += line
                if _, err := tx.Exec(s.db.Rebind(`
                        INSERT INTO purchase_order_items (po_id, product_id, name, sku, qty, cost_cents, line_total_cents)
                        VALUES (?, ?, ?, ?, ?, ?, ?)`),
                        poID, it.ProductID, name, sku, it.Qty, it.CostCents, line); err != nil {
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
                SELECT id, po_id, product_id, name, sku, qty, cost_cents, line_total_cents
                FROM purchase_order_items WHERE po_id = ? ORDER BY id`), poID)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        o.Items = []models.POItem{}
        for rows.Next() {
                var it models.POItem
                if err := rows.Scan(&it.ID, &it.POID, &it.ProductID, &it.Name,
                        &it.SKU, &it.Qty, &it.CostCents, &it.LineTotalCents); err != nil {
                        return nil, err
                }
                o.Items = append(o.Items, it)
        }
        return &o, rows.Err()
}

// ListPOs returns newest-first orders (all suppliers).
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
        for rows.Next() {
                var o models.PurchaseOrder
                if err := rows.Scan(&o.ID, &o.Number, &o.SupplierID, &o.SupplierName,
                        &o.Status, &o.SubtotalCents, &o.Note, &o.CreatedAt, &o.ReceivedAt); err != nil {
                        return nil, err
                }
                o.Items = []models.POItem{}
                out = append(out, o)
        }
        return out, rows.Err()
}

// ListTakes returns newest-first stock takes (without lines; fetch one for detail).
func (s *Service) ListTakes() ([]models.StockTake, error) {
        rows, err := s.db.Query(s.db.Rebind(`
                SELECT id, number, status, COALESCE(note,''), created_at, COALESCE(applied_at,'')
                FROM stock_takes ORDER BY id DESC LIMIT 200`))
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        out := []models.StockTake{}
        for rows.Next() {
                var t models.StockTake
                if err := rows.Scan(&t.ID, &t.Number, &t.Status, &t.Note, &t.CreatedAt, &t.AppliedAt); err != nil {
                        return nil, err
                }
                t.Items = []models.StockTakeItem{}
                out = append(out, t)
        }
        return out, rows.Err()
}

// avgCost returns the weighted-average unit cost in cents, rounded half-up.
// Zero stock (or zero receipt) falls back to the incoming unit cost.
func avgCost(oldStock int, oldCost int64, recvQty int, unitCost int64) int64 {
        newStock := int64(oldStock + recvQty)
        if newStock <= 0 || recvQty <= 0 {
                return unitCost
        }
        num := int64(oldStock)*oldCost + int64(recvQty)*unitCost
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
                var stock, track int
                var cost int64
                if err := tx.QueryRow(s.db.Rebind(`
                        SELECT stock_qty, cost_cents, COALESCE(track_stock,1) FROM products WHERE id = ?`),
                        it.ProductID).Scan(&stock, &cost, &track); err != nil {
                        return nil, fmt.Errorf("product %d: %w", it.ProductID, ErrNotFound)
                }
                if track == 0 {
                        continue
                }
                newStock := stock + it.Qty
                newCost := avgCost(stock, cost, it.Qty, it.CostCents)
                if _, err := tx.Exec(s.db.Rebind(`
                        UPDATE products SET stock_qty = ?, cost_cents = ? WHERE id = ?`),
                        newStock, newCost, it.ProductID); err != nil {
                        return nil, err
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
                SELECT id, take_id, product_id, name, sku, expected_qty, counted_qty
                FROM stock_take_items WHERE take_id = ? ORDER BY id`), takeID)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        out := []models.StockTakeItem{}
        for rows.Next() {
                var it models.StockTakeItem
                if err := rows.Scan(&it.ID, &it.TakeID, &it.ProductID, &it.Name,
                        &it.SKU, &it.ExpectedQty, &it.CountedQty); err != nil {
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
        number, err := s.nextDocNumber(tx, "STK")
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
                        SELECT id, name, COALESCE(sku,''), stock_qty FROM products
                        WHERE is_active = 1 AND COALESCE(track_stock,1) = 1 ORDER BY id`))
        } else {
                // Per-id lookups keep the tx portable across dialects.
                for _, pid := range productIDs {
                        var name, sku string
                        var stock, active int
                        if err := tx.QueryRow(s.db.Rebind(`
                                SELECT name, COALESCE(sku,''), stock_qty, COALESCE(is_active,1)
                                FROM products WHERE id = ?`), pid).Scan(&name, &sku, &stock, &active); err != nil {
                                return nil, fmt.Errorf("product %d: %w", pid, ErrNotFound)
                        }
                        if _, err := tx.Exec(s.db.Rebind(`
                                INSERT INTO stock_take_items (take_id, product_id, name, sku, expected_qty, counted_qty)
                                VALUES (?, ?, ?, ?, ?, ?)`),
                                takeID, pid, name, sku, stock, stock); err != nil {
                                return nil, err
                        }
                }
        }
        if rows != nil {
                defer rows.Close()
                for rows.Next() {
                        var pid int64
                        var name, sku string
                        var stock int
                        if err := rows.Scan(&pid, &name, &sku, &stock); err != nil {
                                return nil, err
                        }
                        if _, err := tx.Exec(s.db.Rebind(`
                                INSERT INTO stock_take_items (take_id, product_id, name, sku, expected_qty, counted_qty)
                                VALUES (?, ?, ?, ?, ?, ?)`),
                                takeID, pid, name, sku, stock, stock); err != nil {
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

// CountTake records counted quantities on an OPEN take.
func (s *Service) CountTake(takeID int64, counts map[int64]int, p *auth.Principal) (*models.StockTake, error) {
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
                if qty < 0 {
                        return nil, fmt.Errorf("count cannot be negative")
                }
                res, err := tx.Exec(s.db.Rebind(`
                        UPDATE stock_take_items SET counted_qty = ? WHERE take_id = ? AND product_id = ?`),
                        qty, takeID, pid)
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
                if _, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty = ? WHERE id = ?`), it.CountedQty, it.ProductID); err != nil {
                        return nil, err
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
