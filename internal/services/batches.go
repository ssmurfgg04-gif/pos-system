package services

// batches.go — v1.8.0 batch/expiry tracking with FEFO allocation
// (First-Expired-First-Out), the pattern OSPOS item_batches and the
// friend's SmartPOS lot system both run on.
//
// Invariants this file is responsible for:
//
//  1. For every batch-tracked product,
//     SUM(product_batches.qty_remaining_milli) == products.stock_qty_milli.
//     Every helper here moves BOTH sides or neither (the caller's
//     transaction is the glue — any failure rolls the whole sale back).
//  2. Sales only ever consume ACTIVE lots, nearest expiry first; lots
//     without an expiry date sell after dated ones (oldest receipt first).
//  3. History is append-only: the stock_movements ledger records every
//     batch move; order_item_batches freezes which lot each sale line drew
//     from, so a recall can name the affected customers.

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"posapp/internal/auth"
	"posapp/internal/models"
)

// milliLineTotal prices a line in cents: unit price × milli quantity,
// rounded half-up to the whole cent. This is the SERVER side of the
// pricing contract; frontend/src/lib/qty.ts implements the identical
// formula (Math.round(price × milli / 1000)) and the golden tests pin
// both to the same fixtures.
func milliLineTotal(unitPriceCents, qtyMilli int64) int64 {
	return (unitPriceCents*qtyMilli + 500) / 1000
}

// validExpiryDate accepts ” (no expiry tracked) or a real YYYY-MM-DD day.
func validExpiryDate(s string) bool {
	if s == "" {
		return true
	}
	if len(s) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// batchLot is one candidate in the FEFO queue.
type batchLot struct {
	id        int64
	remaining int64
}

// fefoQueueTx lists the product's sellable lots in FEFO order: dated lots
// by expiry then receipt, undated lots last by receipt.
func fefoQueueTx(s *Service, tx *sql.Tx, productID int64) ([]batchLot, error) {
	rows, err := tx.Query(s.db.Rebind(`
                SELECT id, qty_remaining_milli FROM product_batches
                WHERE product_id = ? AND status = 'ACTIVE' AND qty_remaining_milli > 0
                ORDER BY (expiry_date = '') ASC, expiry_date ASC, id ASC`), productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var lots []batchLot
	for rows.Next() {
		var l batchLot
		if err := rows.Scan(&l.id, &l.remaining); err != nil {
			return nil, err
		}
		lots = append(lots, l)
	}
	return lots, rows.Err()
}

// deductBatchTx takes takeMilli out of one lot, exhausting it at zero.
// Guarded re-check (WHERE remaining >= take) — concurrent tills can never
// oversell a lot; the loser's whole transaction rolls back.
func deductBatchTx(s *Service, tx *sql.Tx, batchID, takeMilli int64) error {
	res, err := tx.Exec(s.db.Rebind(`
                UPDATE product_batches SET
                        qty_remaining_milli = qty_remaining_milli - ?,
                        status = CASE WHEN qty_remaining_milli - ? <= 0 THEN 'EXHAUSTED' ELSE status END,
                        updated_at = ?
                WHERE id = ? AND qty_remaining_milli >= ?`),
		takeMilli, takeMilli, NowStamp(), batchID, takeMilli)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: batch %d can no longer cover %d milli (concurrent sale?)", ErrInsufficientStock, batchID, takeMilli)
	}
	return nil
}

// allocateSaleBatchesTx splits a sale line's quantity across the product's
// lots FEFO-style and freezes the split onto the order line. The caller
// has ALREADY updated products.stock_qty_milli (guarded) and written the
// ledger row — this only moves batch rows, so the invariant is kept by
// construction inside one transaction.
func allocateSaleBatchesTx(s *Service, tx *sql.Tx, orderItemID, productID, needMilli int64) error {
	if needMilli <= 0 {
		return nil
	}
	lots, err := fefoQueueTx(s, tx, productID)
	if err != nil {
		return err
	}
	for _, lot := range lots {
		if needMilli <= 0 {
			break
		}
		take := lot.remaining
		if take > needMilli {
			take = needMilli
		}
		if err := deductBatchTx(s, tx, lot.id, take); err != nil {
			return err
		}
		if _, err := tx.Exec(s.db.Rebind(`
                        INSERT INTO order_item_batches (order_item_id, product_id, batch_id, qty_milli)
                        VALUES (?, ?, ?, ?)`), orderItemID, productID, lot.id, take); err != nil {
			return err
		}
		needMilli -= take
	}
	if needMilli > 0 {
		return fmt.Errorf("%w: batch-tracked product %d is short by %d milli across all lots", ErrInsufficientStock, productID, needMilli)
	}
	return nil
}

// restockSaleBatchesTx puts a voided PAID sale's batch allocations back
// onto their original lots (the audit trail knows exactly which ones).
// Recalled lots stay recalled — restock onto a RECALLED lot would put
// unsellable goods back into the sellable queue; those millimetres of
// stock land on the OPENING batch instead (next helper).
func restockSaleBatchesTx(s *Service, tx *sql.Tx, orderID int64) error {
	rows, err := tx.Query(s.db.Rebind(`
                SELECT a.batch_id, SUM(a.qty_milli) FROM order_item_batches a
                JOIN order_items oi ON oi.id = a.order_item_id
                WHERE oi.order_id = ?
                GROUP BY a.batch_id`), orderID)
	if err != nil {
		return err
	}
	type put struct {
		batchID int64
		qty     int64
	}
	var puts []put
	for rows.Next() {
		var p put
		if err := rows.Scan(&p.batchID, &p.qty); err != nil {
			rows.Close()
			return err
		}
		puts = append(puts, p)
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}
	for _, p := range puts {
		if _, err := tx.Exec(s.db.Rebind(`
                        UPDATE product_batches SET
                                qty_remaining_milli = qty_remaining_milli + ?,
                                status = CASE WHEN status = 'RECALLED' THEN status ELSE 'ACTIVE' END,
                                updated_at = ?
                        WHERE id = ?`), p.qty, NowStamp(), p.batchID); err != nil {
			return err
		}
	}
	return nil
}

// ensureOpeningBatchTx returns (creating if needed) the OPENING batch that
// catches non-sale stock movements for a batch-tracked product: returns
// restocks, stock-take corrections, manual adjustments. One product owns
// at most one OPENING lot; its cost tracks the product's current cost.
func ensureOpeningBatchTx(s *Service, tx *sql.Tx, productID int64) (int64, error) {
	var id int64
	err := tx.QueryRow(s.db.Rebind(`
                SELECT id FROM product_batches WHERE product_id = ? AND batch_no = 'OPENING'
                ORDER BY id LIMIT 1`), productID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := tx.Exec(s.db.Rebind(`
                INSERT INTO product_batches (product_id, batch_no, expiry_date, qty_received_milli, qty_remaining_milli, cost_cents, reference, status, updated_at)
                SELECT id, 'OPENING', '', 0, 0, COALESCE(cost_cents,0), 'auto (returns/adjustments catch-all)', 'ACTIVE', ?
                FROM products WHERE id = ?`), NowStamp(), productID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// adjustBatchStockTx moves a non-sale delta for a batch-tracked product:
// positive additions land on the OPENING lot; negative removals are FEFO'd
// across ACTIVE lots. The caller owns the projection update + ledger row.
func adjustBatchStockTx(s *Service, tx *sql.Tx, productID, deltaMilli int64) error {
	if deltaMilli == 0 {
		return nil
	}
	if deltaMilli > 0 {
		id, err := ensureOpeningBatchTx(s, tx, productID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(s.db.Rebind(`
                        UPDATE product_batches SET
                                qty_received_milli = qty_received_milli + ?,
                                qty_remaining_milli = qty_remaining_milli + ?,
                                status = CASE WHEN status = 'RECALLED' THEN status ELSE 'ACTIVE' END,
                                updated_at = ?
                        WHERE id = ?`), deltaMilli, deltaMilli, NowStamp(), id); err != nil {
			return err
		}
		return nil
	}
	need := -deltaMilli
	lots, err := fefoQueueTx(s, tx, productID)
	if err != nil {
		return err
	}
	for _, lot := range lots {
		if need <= 0 {
			break
		}
		take := lot.remaining
		if take > need {
			take = need
		}
		if err := deductBatchTx(s, tx, lot.id, take); err != nil {
			return err
		}
		need -= take
	}
	if need > 0 {
		return fmt.Errorf("%w: batch-tracked product %d short by %d milli across all lots", ErrInsufficientStock, productID, need)
	}
	return nil
}

// ListProductBatches returns a product's lots, sellable/active first.
func (s *Service) ListProductBatches(productID int64) ([]models.ProductBatch, error) {
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT b.id, b.product_id, COALESCE(p.name,''), COALESCE(p.sku,''),
                        COALESCE(b.batch_no,''), COALESCE(b.expiry_date,''),
                        b.qty_received_milli, b.qty_remaining_milli, b.cost_cents,
                        b.supplier_id, COALESCE(sp.name,''), COALESCE(b.reference,''),
                        COALESCE(b.status,''), b.created_at, b.updated_at
                FROM product_batches b
                LEFT JOIN products p ON p.id = b.product_id
                LEFT JOIN suppliers sp ON sp.id = b.supplier_id
                WHERE b.product_id = ?
                ORDER BY CASE b.status WHEN 'ACTIVE' THEN 0 WHEN 'RECALLED' THEN 1 ELSE 2 END,
                        (b.expiry_date = '') ASC, b.expiry_date ASC, b.id ASC`), productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.ProductBatch{}
	for rows.Next() {
		var b models.ProductBatch
		if err := rows.Scan(&b.ID, &b.ProductID, &b.ProductName, &b.SKU,
			&b.BatchNo, &b.ExpiryDate, &b.QtyReceivedMilli, &b.QtyRemainingMilli,
			&b.CostCents, &b.SupplierID, &b.SupplierName, &b.Reference,
			&b.Status, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CreateBatchGoodsIn records goods received straight onto a lot without a
// purchase order (the common case at a small duka: the milk delivery
// arrives, the owner types the expiry off the crate). Stock projection,
// batch row and ledger move together in one transaction.
func (s *Service) CreateBatchGoodsIn(productID int64, batchNo, expiryDate string, qtyMilli, costCents, supplierID int64, note string, p *auth.Principal) (*models.ProductBatch, error) {
	if qtyMilli <= 0 || qtyMilli > models.MaxOrderQtyMilli {
		return nil, fmt.Errorf("%w: quantity out of range", ErrInvalidInput)
	}
	if qtyMilli%models.MilliPerUnit != 0 {
		return nil, fmt.Errorf("%w: goods-in quantity must be whole units", ErrInvalidInput)
	}
	if costCents < 0 || costCents > models.MaxPriceCents {
		return nil, fmt.Errorf("%w: cost out of range", ErrInvalidInput)
	}
	if !validExpiryDate(expiryDate) {
		return nil, fmt.Errorf("%w: expiry date must be YYYY-MM-DD (or empty)", ErrInvalidInput)
	}
	batchNo = strings.TrimSpace(batchNo)
	if len(batchNo) > 80 {
		return nil, fmt.Errorf("%w: batch number too long (max 80)", ErrInvalidInput)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var track int
	if err := tx.QueryRow(s.db.Rebind(`SELECT COALESCE(track_batches,0) FROM products WHERE id = ?`), productID).Scan(&track); err != nil {
		return nil, fmt.Errorf("product %d: %w", productID, ErrNotFound)
	}
	if track != 1 {
		return nil, fmt.Errorf("%w: product does not track batches", ErrInvalidInput)
	}
	if costCents == 0 {
		tx.QueryRow(s.db.Rebind(`SELECT COALESCE(cost_cents,0) FROM products WHERE id = ?`), productID).Scan(&costCents)
	}
	if _, err := tx.Exec(s.db.Rebind(`
                UPDATE products SET stock_qty_milli = stock_qty_milli + ?, stock_qty = (stock_qty_milli + ?) / 1000, updated_at = ? WHERE id = ?`),
		qtyMilli, qtyMilli, NowStamp(), productID); err != nil {
		return nil, err
	}
	ref := truncStr(strings.TrimSpace(note), 80)
	res, err := tx.Exec(s.db.Rebind(`
                INSERT INTO product_batches (product_id, batch_no, expiry_date, qty_received_milli, qty_remaining_milli, cost_cents, supplier_id, reference, status, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'ACTIVE', ?)`),
		productID, batchNo, expiryDate, qtyMilli, qtyMilli, costCents, supplierID, ref, NowStamp())
	if err != nil {
		return nil, err
	}
	batchID, _ := res.LastInsertId()
	if err := recordStockMoveTx(s, tx, productID, qtyMilli, models.MoveBatchIn,
		truncStr("batch "+batchNo, 80), truncStr(note, 200), p.ID, p.Username); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.EmitProduct(productID, true)
	s.Audit(p.ID, p.Username, "BATCH_GOODS_IN", "product", fmt.Sprintf("%d", productID),
		fmt.Sprintf("batch %q expiry %s qty %d", batchNo, expiryDate, qtyMilli))
	b, err := s.GetBatch(batchID)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// GetBatch loads one lot with product/supplier names.
func (s *Service) GetBatch(batchID int64) (*models.ProductBatch, error) {
	var b models.ProductBatch
	err := s.db.QueryRow(s.db.Rebind(`
                SELECT b.id, b.product_id, COALESCE(p.name,''), COALESCE(p.sku,''),
                        COALESCE(b.batch_no,''), COALESCE(b.expiry_date,''),
                        b.qty_received_milli, b.qty_remaining_milli, b.cost_cents,
                        b.supplier_id, COALESCE(sp.name,''), COALESCE(b.reference,''),
                        COALESCE(b.status,''), b.created_at, b.updated_at
                FROM product_batches b
                LEFT JOIN products p ON p.id = b.product_id
                LEFT JOIN suppliers sp ON sp.id = b.supplier_id
                WHERE b.id = ?`), batchID).Scan(
		&b.ID, &b.ProductID, &b.ProductName, &b.SKU,
		&b.BatchNo, &b.ExpiryDate, &b.QtyReceivedMilli, &b.QtyRemainingMilli,
		&b.CostCents, &b.SupplierID, &b.SupplierName, &b.Reference,
		&b.Status, &b.CreatedAt, &b.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// RecallBatch pulls a lot off the shelf: it stops selling, its remaining
// quantity leaves the projection (the goods physically leave for the
// supplier / bin), and the ledger records the write-off. The disposal
// report's loss value picks it up from there.
func (s *Service) RecallBatch(batchID int64, reason string, p *auth.Principal) (*models.ProductBatch, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var productID, remaining int64
	var status string
	err = tx.QueryRow(s.db.Rebind(`
                SELECT product_id, qty_remaining_milli, COALESCE(status,'') FROM product_batches WHERE id = ?`),
		batchID).Scan(&productID, &remaining, &status)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if status == models.BatchRecalled {
		return nil, fmt.Errorf("%w: batch already recalled", ErrInvalidState)
	}
	if remaining > 0 {
		if _, err := tx.Exec(s.db.Rebind(`
                        UPDATE products SET stock_qty_milli = stock_qty_milli - ?, stock_qty = (stock_qty_milli - ?) / 1000, updated_at = ?
                        WHERE id = ? AND stock_qty_milli >= ?`),
			remaining, remaining, NowStamp(), productID, remaining); err != nil {
			return nil, err
		}
		if err := recordStockMoveTx(s, tx, productID, -remaining, models.MoveBatchRecall,
			truncStr("batch recall", 80), truncStr(reason, 200), p.ID, p.Username); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(s.db.Rebind(`
                UPDATE product_batches SET status = 'RECALLED', qty_remaining_milli = 0, updated_at = ?
                WHERE id = ?`), NowStamp(), batchID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.EmitProduct(productID, true)
	s.Audit(p.ID, p.Username, "BATCH_RECALLED", "product", fmt.Sprintf("%d", productID),
		truncStr(fmt.Sprintf("batch %d: %s", batchID, reason), 200))
	return s.GetBatch(batchID)
}

// ListExpiryBatches is the expiry report: every lot holding stock whose
// expiry falls inside the window, plus everything already expired. days<0
// disables the window (all dated lots).
func (s *Service) ListExpiryBatches(days int) (expired, soon []models.ExpiryBatchRow, valueCents int64, err error) {
	q := `
                SELECT b.id, b.product_id, COALESCE(p.name,''), COALESCE(p.sku,''),
                        COALESCE(b.batch_no,''), b.expiry_date,
                        b.qty_remaining_milli, b.cost_cents, COALESCE(b.status,'')
                FROM product_batches b
                LEFT JOIN products p ON p.id = b.product_id
                WHERE b.expiry_date != '' AND b.qty_remaining_milli > 0 AND b.status != 'RECALLED'`
	args := []any{}
	if days >= 0 {
		horizon := time.Now().UTC().AddDate(0, 0, days).Format("2006-01-02")
		q += ` AND b.expiry_date <= ?`
		args = append(args, horizon)
	}
	q += ` ORDER BY b.expiry_date ASC, b.id ASC`
	rows, err := s.db.Query(s.db.Rebind(q), args...)
	if err != nil {
		return nil, nil, 0, err
	}
	defer rows.Close()
	today := time.Now().UTC().Format("2006-01-02")
	expired = []models.ExpiryBatchRow{}
	soon = []models.ExpiryBatchRow{}
	for rows.Next() {
		var r models.ExpiryBatchRow
		if err := rows.Scan(&r.BatchID, &r.ProductID, &r.Name, &r.SKU,
			&r.BatchNo, &r.ExpiryDate, &r.QtyRemainingMilli, &r.CostCents, &r.Status); err != nil {
			return nil, nil, 0, err
		}
		r.ValueCents = milliLineTotal(r.CostCents, r.QtyRemainingMilli)
		valueCents += r.ValueCents
		if r.ExpiryDate < today {
			expired = append(expired, r)
		} else {
			soon = append(soon, r)
		}
	}
	return expired, soon, valueCents, rows.Err()
}

// EnableBatchesIfNeeded wraps existing shelf stock into an OPENING lot the
// first time a product starts tracking batches — otherwise the batch sum
// could never reconcile with the projection the till already trusts.
func (s *Service) EnableBatchesIfNeeded(productID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var track, hasBatches int
	var stock int64
	if err := tx.QueryRow(s.db.Rebind(`
                SELECT COALESCE(track_batches,0),
                        (SELECT COUNT(*) FROM product_batches WHERE product_id = p.id),
                        p.stock_qty_milli
                FROM products p WHERE p.id = ?`), productID).Scan(&track, &hasBatches, &stock); err != nil {
		return err
	}
	if track != 1 || hasBatches > 0 || stock <= 0 {
		return tx.Commit()
	}
	if _, err := tx.Exec(s.db.Rebind(`
                INSERT INTO product_batches (product_id, batch_no, expiry_date, qty_received_milli, qty_remaining_milli, cost_cents, reference, status, updated_at)
                SELECT id, 'OPENING', '', stock, stock, COALESCE(cost_cents,0), 'opening stock at batch tracking start', 'ACTIVE', ?
                FROM (SELECT id, cost_cents, stock_qty_milli AS stock FROM products WHERE id = ?)`),
		NowStamp(), productID); err != nil {
		return err
	}
	return tx.Commit()
}
