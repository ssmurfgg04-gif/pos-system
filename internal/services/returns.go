package services

// returns.go — customer returns and refunds (v1.6.3, migration v23).
// A return mirrors lines of a PAID order at the original sold price,
// restocks tracked products through the stock ledger, and refunds by
// cash or store credit. Partial returns are allowed; each line can only
// be returned once overall (sold - previously returned - this return >= 0).

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"posapp/internal/auth"
	"posapp/internal/models"
)

// ErrInvalidInput — malformed request payload (maps to HTTP 400).
var ErrInvalidInput = errors.New("invalid input")

// newClientUUID mints a return idempotency key (server-initiated docs
// don't arrive with one from an offline client).
func newClientUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("ret-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// soldReturnLine is one sold line as the return engine sees it.
type soldReturnLine struct {
	id         int64
	productID  int64
	name       string
	sku        string
	qtyMilli   int64
	price      int64
	trackStock bool
}

// CreateReturn posts a return against a paid order. One tx covers the
// return header + lines + restock + ledger so a crash can never leave a
// half-refunded sale.
func (s *Service) CreateReturn(req models.CreateReturnRequest, p *auth.Principal) (*models.ReturnDoc, error) {
	if req.OrderID <= 0 {
		return nil, fmt.Errorf("%w: orderId required", ErrInvalidInput)
	}
	total := int64(0)
	for _, l := range req.Lines {
		if l.QtyMilli <= 0 {
			return nil, fmt.Errorf("%w: return quantities must be positive", ErrInvalidInput)
		}
	}
	method := models.RefundCash
	if req.Method != "" {
		if req.Method != models.RefundCash && req.Method != models.RefundStoreCredit {
			return nil, fmt.Errorf("%w: method must be CASH or STORE_CREDIT", ErrInvalidInput)
		}
		method = req.Method
	}
	if method == models.RefundStoreCredit && req.CustomerID <= 0 {
		return nil, fmt.Errorf("%w: STORE_CREDIT refunds need a customer", ErrInvalidInput)
	}
	if p == nil {
		return nil, fmt.Errorf("%w: no principal", ErrInvalidInput)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var orderNumber, orderStatus string
	if err := tx.QueryRow(s.db.Rebind(`SELECT COALESCE(number,''), status FROM orders WHERE id = ?`), req.OrderID).
		Scan(&orderNumber, &orderStatus); err != nil {
		return nil, fmt.Errorf("%w: order not found", ErrNotFound)
	}
	if orderStatus != models.OrderPaid {
		return nil, fmt.Errorf("%w: only PAID orders can be returned (this one is %s)", ErrInvalidState, orderStatus)
	}

	// Load the sold lines once; every requested line must map to one of
	// them and respect the remaining-returnable quantity.
	items, err := loadOrderItemsFullTx(tx, s.db.Rebind, req.OrderID)
	if err != nil {
		return nil, err
	}
	byID := map[int64]*soldReturnLine{}
	for i := range items {
		byID[items[i].id] = &items[i]
	}

	// How much of each line has already been returned (across returns).
	alreadyReturned := map[int64]int{}
	rows, err := tx.Query(s.db.Rebind(`
                SELECT ri.order_item_id, SUM(ri.qty_milli)
                FROM return_items ri JOIN returns r ON r.id = ri.return_id
                WHERE r.order_id = ?
                GROUP BY ri.order_item_id`), req.OrderID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var itemID int64
		var qty int64
		if err := rows.Scan(&itemID, &qty); err != nil {
			rows.Close()
			return nil, err
		}
		alreadyReturned[itemID] = int(qty)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Validate and build the return lines.
	type retLine struct {
		orderItemID int64
		productID   int64
		name        string
		sku         string
		qtyMilli    int64
		price       int64
		lineTotal   int64
		trackStock  bool
	}
	lines := []retLine{}
	for _, l := range req.Lines {
		sold, ok := byID[l.OrderItemID]
		if !ok {
			return nil, fmt.Errorf("%w: order item %d is not part of %s", ErrInvalidInput, l.OrderItemID, orderNumber)
		}
		remaining := sold.qtyMilli - int64(alreadyReturned[l.OrderItemID])
		if l.QtyMilli > remaining {
			return nil, fmt.Errorf("%w: %s — only %d of %d milli returnable", ErrInvalidInput, sold.name, remaining, sold.qtyMilli)
		}
		lineTotal := milliLineTotal(sold.price, l.QtyMilli)
		lines = append(lines, retLine{
			orderItemID: sold.id, productID: sold.productID, name: sold.name, sku: sold.sku,
			qtyMilli: l.QtyMilli, price: sold.price, lineTotal: lineTotal, trackStock: sold.trackStock,
		})
		total += lineTotal
	}
	if total <= 0 {
		return nil, fmt.Errorf("%w: return total must be positive", ErrInvalidInput)
	}

	number, err := s.nextDocNumber(tx, "RET-", 0)
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(s.db.Rebind(`INSERT INTO returns
                (number, order_id, order_number, total_cents, method, reason, status, client_uuid, created_by, created_by_name, created_at)
                VALUES (?, ?, ?, ?, ?, ?, 'COMPLETED', ?, ?, ?, ?)`),
		number, req.OrderID, orderNumber, total, method, truncStr(req.Reason, 300), newClientUUID(), p.ID, p.Username, nowStamp())
	if err != nil {
		return nil, err
	}
	returnID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}

	// Restock + ledger rows.
	for _, l := range lines {
		if _, err := tx.Exec(s.db.Rebind(`INSERT INTO return_items
                        (return_id, order_item_id, product_id, name, sku, qty_milli, unit_price_cents, line_total_cents)
                        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
			returnID, l.orderItemID, l.productID, l.name, l.sku, l.qtyMilli, l.price, l.lineTotal); err != nil {
			return nil, err
		}
		if !l.trackStock {
			continue
		}
		if _, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty_milli = stock_qty_milli + ?, stock_qty = (stock_qty_milli + ?) / 1000 WHERE id = ?`), l.qtyMilli, l.qtyMilli, l.productID); err != nil {
			return nil, err
		}
		if err := recordStockMoveTx(s, tx, l.productID, l.qtyMilli, models.MoveReturnRestock, number, orderNumber, p.ID, p.Username); err != nil {
			return nil, err
		}
		// v1.8.0: batch-tracked products receive the goods onto their
		// OPENING lot (the original lot of a returned item is not
		// recorded — the shelf gets the goods back, the OPENING batch
		// keeps the batch sum reconciled with the projection).
		var batches int
		tx.QueryRow(s.db.Rebind(`SELECT COALESCE(track_batches,0) FROM products WHERE id = ?`), l.productID).Scan(&batches)
		if batches == 1 {
			if err := adjustBatchStockTx(s, tx, l.productID, l.qtyMilli); err != nil {
				return nil, err
			}
		}
	}

	// Store-credit refund: money the shop now owes the customer.
	if method == models.RefundStoreCredit {
		var active int
		if err := tx.QueryRow(s.db.Rebind(`SELECT COALESCE(is_active,1) FROM customers WHERE id = ?`), req.CustomerID).Scan(&active); err != nil {
			return nil, fmt.Errorf("%w: customer not found", ErrNotFound)
		}
		if active != 1 {
			return nil, fmt.Errorf("%w: customer is inactive", ErrInvalidInput)
		}
		if err := recordLedgerTx(tx, s.db.Rebind, req.CustomerID, req.OrderID, models.LedgerRefund,
			total, 0, "return "+number+" ("+orderNumber+")", p.ID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	s.Audit(p.ID, p.Username, "RETURN_CREATED", "return", number,
		fmt.Sprintf("%s: %d cents via %s", orderNumber, total, method))

	// Team sync: the return doc, refreshed stock, and the customer ledger.
	doc, err := s.GetReturn(returnID)
	if err != nil {
		return nil, err
	}
	s.EmitReturn(doc)
	for _, l := range lines {
		if l.trackStock {
			s.EmitProduct(l.productID, true)
		}
	}
	if method == models.RefundStoreCredit {
		s.EmitLedger(req.CustomerID, models.LedgerRefund, total, 0, "return "+number)
		s.EmitCustomer(req.CustomerID)
	}
	return doc, nil
}

// GetReturn loads one return with its lines.
func (s *Service) GetReturn(id int64) (*models.ReturnDoc, error) {
	var d models.ReturnDoc
	var createdBy int64
	err := s.db.QueryRow(s.db.Rebind(`
                SELECT id, number, order_id, COALESCE(order_number,''), total_cents, method,
                        COALESCE(reason,''), status, created_by, COALESCE(created_by_name,''), created_at
                FROM returns WHERE id = ?`), id).
		Scan(&d.ID, &d.Number, &d.OrderID, &d.OrderNumber, &d.TotalCents, &d.Method,
			&d.Reason, &d.Status, &createdBy, &d.CreatedByName, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: return not found", ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	d.CreatedBy = createdBy
	d.Items = []models.ReturnItem{}
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT id, return_id, order_item_id, product_id, COALESCE(name,''), COALESCE(sku,''),
                        qty_milli, unit_price_cents, line_total_cents
                FROM return_items WHERE return_id = ? ORDER BY id`), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it models.ReturnItem
		if err := rows.Scan(&it.ID, &it.ReturnID, &it.OrderItemID, &it.ProductID, &it.Name, &it.SKU,
			&it.QtyMilli, &it.UnitPriceCents, &it.LineTotalCents); err != nil {
			return nil, err
		}
		d.Items = append(d.Items, it)
	}
	return &d, rows.Err()
}

// ListReturns returns newest-first returns, optionally filtered by order.
func (s *Service) ListReturns(orderID int64, limit int) ([]models.ReturnDoc, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `
                SELECT id, number, order_id, COALESCE(order_number,''), total_cents, method,
                        COALESCE(reason,''), status, created_by, COALESCE(created_by_name,''), created_at
                FROM returns`
	args := []any{}
	if orderID > 0 {
		q += ` WHERE order_id = ?`
		args = append(args, orderID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(s.db.Rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.ReturnDoc{}
	for rows.Next() {
		var d models.ReturnDoc
		if err := rows.Scan(&d.ID, &d.Number, &d.OrderID, &d.OrderNumber, &d.TotalCents, &d.Method,
			&d.Reason, &d.Status, &d.CreatedBy, &d.CreatedByName, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.Items = []models.ReturnItem{}
		out = append(out, d)
	}
	return out, rows.Err()
}

// loadOrderItemsFullTx reads the sold lines with everything the return
// needs (id, product, snapshot fields, trackability).
func loadOrderItemsFullTx(tx *sql.Tx, rebind func(string) string, orderID int64) ([]soldReturnLine, error) {
	rows, err := tx.Query(rebind(`
                SELECT oi.id, oi.product_id, oi.name, COALESCE(oi.sku,''), oi.qty_milli, oi.unit_price_cents,
                        COALESCE(p.track_stock,1)
                FROM order_items oi LEFT JOIN products p ON p.id = oi.product_id
                WHERE oi.order_id = ? ORDER BY oi.id`), orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []soldReturnLine{}
	for rows.Next() {
		var l soldReturnLine
		var track int
		if err := rows.Scan(&l.id, &l.productID, &l.name, &l.sku, &l.qtyMilli, &l.price, &track); err != nil {
			return nil, err
		}
		l.trackStock = track == 1
		out = append(out, l)
	}
	return out, rows.Err()
}
