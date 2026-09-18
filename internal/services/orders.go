package services

import (
        "context"
        "database/sql"
        "errors"
        "fmt"
        "math"
        "strings"
        "time"

        "posapp/internal/auth"
        "posapp/internal/models"
        "posapp/internal/mpesa"
)

var (
        ErrNotFound          = errors.New("not found")
        ErrInsufficientStock = errors.New("insufficient stock")
        ErrInvalidState      = errors.New("invalid state transition")
        ErrDuplicateReceipt  = errors.New("receipt code already recorded")
        ErrOrderAlreadyPaid  = errors.New("order already paid")
        ErrOverpayment       = errors.New("payment exceeds balance")
        ErrCreditLimit       = errors.New("tab would exceed customer credit limit")
)

// nowStamp is the fixed-width millisecond timestamp used for all
// app-written time columns. Fixed width keeps string comparisons
// chronologically exact (variable RFC3339Nano trimming would not).
func nowStamp() string {
        return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// checkoutLine is a validated cart line with server-side pricing.
type checkoutLine struct {
        productID      int64
        name, sku      string
        qty            int
        unitPriceCents int64
        trackStock     bool
}

// roundToShilling rounds cents to the nearest whole shilling (M-Pesa STK
// only accepts integer amounts).
func roundToShilling(cents int64) int64 {
        return int64(math.Round(float64(cents)/100.0) * 100)
}

// Checkout creates an order atomically. Cash orders complete immediately;
// M-Pesa orders start PENDING and (in auto/stk mode) receive an STK push
// AFTER commit — network calls never run inside the transaction.
func (s *Service) Checkout(ctx context.Context, p *auth.Principal, req models.CheckoutRequest) (*models.Order, error) {
        // Idempotent replay (offline sync double-submits are the norm).
        if req.ClientUUID != "" {
                if existing, err := s.GetOrderByClientUUID(req.ClientUUID); err == nil {
                        return existing, nil
                }
        }

        method := req.PaymentMethod
        mode := req.PaymentMode
        if method == models.MethodMpesa {
                switch mode {
                case "", models.ModeAuto, models.ModeSTK, models.ModeManual:
                        if mode == "" {
                                mode = s.settings.GetString("payment_mode", models.ModeAuto)
                        }
                default:
                        return nil, fmt.Errorf("invalid payment mode %q", mode)
                }
        } else if method != models.MethodCash && method != models.MethodAccount {
                return nil, fmt.Errorf("invalid payment method %q", method)
        }
        // Tab checkout needs a live customer up front (limit enforced at
        // insert time inside the transaction).
        var tabCustomer *models.Customer
        if method == models.MethodAccount {
                if req.CustomerID == 0 {
                        return nil, fmt.Errorf("tab checkout needs a customer")
                }
                var err error
                tabCustomer, err = s.GetCustomer(req.CustomerID)
                if err != nil {
                        return nil, err
                }
                if !tabCustomer.Active {
                        return nil, fmt.Errorf("customer is inactive")
                }
        }

        // Validate lines with server-side price re-read (never trust client
        // prices). Duplicate product ids merge quantities. Hard ceilings
        // keep price*qty below int64 range (overflow would flip totals
        // negative) — far above any real sale.
        if len(req.Items) == 0 {
                return nil, errors.New("empty cart")
        }
        if len(req.Items) > models.MaxOrderLines {
                return nil, fmt.Errorf("too many lines (max %d)", models.MaxOrderLines)
        }
        acc := map[int64]checkoutLine{}
        var orderIdx []int64
        for _, it := range req.Items {
                if it.Qty <= 0 {
                        return nil, errors.New("quantity must be positive")
                }
                if it.Qty > models.MaxOrderQty {
                        return nil, fmt.Errorf("quantity exceeds maximum (%d)", models.MaxOrderQty)
                }
                if prev, ok := acc[it.ProductID]; ok {
                        prev.qty += it.Qty
                        if prev.qty > models.MaxOrderQty {
                                return nil, fmt.Errorf("quantity exceeds maximum (%d)", models.MaxOrderQty)
                        }
                        acc[it.ProductID] = prev
                        continue
                }
                var l checkoutLine
                var track, active int
                err := s.db.QueryRow(s.db.Rebind(
                        `SELECT id, name, COALESCE(sku,''), price_cents, COALESCE(track_stock,1), COALESCE(is_active,1)
                         FROM products WHERE id = ?`), it.ProductID).
                        Scan(&l.productID, &l.name, &l.sku, &l.unitPriceCents, &track, &active)
                if err != nil {
                        return nil, fmt.Errorf("product %d: %w", it.ProductID, err)
                }
                if active != 1 {
                        return nil, fmt.Errorf("product %d is inactive", it.ProductID)
                }
                l.trackStock = track == 1
                // Price override is a permission-gated feature.
                if it.UnitPriceCents != 0 && it.UnitPriceCents != l.unitPriceCents {
                        if !p.Can("payments.override_price") {
                                return nil, fmt.Errorf("price override requires payments.override_price permission")
                        }
                        if it.UnitPriceCents < 0 || it.UnitPriceCents > models.MaxPriceCents {
                                return nil, errors.New("price override out of range")
                        }
                        l.unitPriceCents = it.UnitPriceCents
                }
                if l.unitPriceCents > models.MaxPriceCents {
                        return nil, fmt.Errorf("product %d price out of range", it.ProductID)
                }
                l.qty = it.Qty
                acc[it.ProductID] = l
                orderIdx = append(orderIdx, it.ProductID)
        }
        lines := make([]checkoutLine, 0, len(orderIdx))
        for _, pid := range orderIdx {
                lines = append(lines, acc[pid])
        }

        // Totals with configurable, VAT-inclusive-by-default math (integer cents).
        pct := s.settings.GetFloat("tax_percent", 16)
        if pct < 0 || pct > models.MaxTaxPercent {
                return nil, fmt.Errorf("tax_percent misconfigured (0-%d)", models.MaxTaxPercent)
        }
        included := s.settings.GetBool("tax_included", true)
        var subtotal int64
        for _, l := range lines {
                line := l.unitPriceCents * int64(l.qty)
                if l.qty > 0 && line/int64(l.qty) != l.unitPriceCents {
                        return nil, errors.New("line total overflow")
                }
                subtotal += line
                if subtotal < 0 {
                        return nil, errors.New("order total overflow")
                }
        }
        var tax, total int64
        if included {
                total = subtotal
                tax = int64(math.Round(float64(subtotal) * pct / (100 + pct)))
        } else {
                tax = int64(math.Round(float64(subtotal) * pct / 100))
                total = subtotal + tax
        }

        immediateCash := method == models.MethodCash
        isManual := method == models.MethodMpesa && mode == models.ModeManual

        var orderID int64
        var orderNumber string

        // Retry loop: order numbers are unique per day (ORD{YYYYMMDD}{seq}).
        for attempt := 0; attempt < 3; attempt++ {
                tx, err := s.db.Begin()
                if err != nil {
                        return nil, err
                }
                number, err := s.nextOrderNumber(tx)
                if err != nil {
                        tx.Rollback()
                        return nil, err
                }
                now := nowStamp()
                status := models.OrderPending
                paidAt := ""
                if immediateCash {
                        status = models.OrderPaid
                        paidAt = now
                }
                taxIncludedInt := 0
                if included {
                        taxIncludedInt = 1
                }
                res, err := tx.Exec(s.db.Rebind(`
                        INSERT INTO orders (number, status, subtotal_cents, tax_cents, total_cents, tax_percent, tax_included, cashier_id, customer_name, note, client_uuid, created_at, paid_at, customer_id)
                        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
                        number, status, subtotal, tax, total, pct, taxIncludedInt, p.ID, req.CustomerName, req.Note, req.ClientUUID, now, paidAt, req.CustomerID)
                if err != nil {
                        tx.Rollback()
                        if isUniqueViolation(err) {
                                // A concurrent request won the race. If it was
                                // our own client_uuid, return the winner instead
                                // of minting a duplicate (offline sync bursts
                                // collide here, not on the order number). Spin
                                // briefly: the winner may not have committed yet.
                                if req.ClientUUID != "" {
                                        for i := 0; i < 20; i++ {
                                                if existing, qerr := s.GetOrderByClientUUID(req.ClientUUID); qerr == nil {
                                                        return existing, nil
                                                }
                                                time.Sleep(25 * time.Millisecond)
                                        }
                                }
                                continue // concurrent number allocation — retry with next seq
                        }
                        return nil, err
                }
                id, _ := res.LastInsertId()

                for _, l := range lines {
                        if _, err := tx.Exec(s.db.Rebind(`
                                INSERT INTO order_items (order_id, product_id, name, sku, qty, unit_price_cents, line_total_cents)
                                VALUES (?, ?, ?, ?, ?, ?, ?)`),
                                id, l.productID, l.name, l.sku, l.qty, l.unitPriceCents, l.unitPriceCents*int64(l.qty)); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                }

                if immediateCash {
                        // Fail fast on stock BEFORE taking money.
                        for _, l := range lines {
                                if l.trackStock {
                                        var stock int
                                        if err := tx.QueryRow(`SELECT stock_qty FROM products WHERE id = ?`, l.productID).Scan(&stock); err != nil {
                                                tx.Rollback()
                                                return nil, err
                                        }
                                        if stock < l.qty {
                                                tx.Rollback()
                                                return nil, fmt.Errorf("%w: %s (have %d, need %d)", ErrInsufficientStock, l.name, stock, l.qty)
                                        }
                                }
                        }
                        if _, err := tx.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, created_at, completed_at)
                                VALUES (?, 'cash', '', ?, 'COMPLETED', ?, ?)`), id, total, now, now); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                        // Guarded stock deduction (WHERE guard = no double-deduct ever).
                        for _, l := range lines {
                                if l.trackStock {
                                        gres, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty = stock_qty - ? WHERE id = ? AND stock_qty >= ?`), l.qty, l.productID, l.qty)
                                        if err != nil {
                                                tx.Rollback()
                                                return nil, err
                                        }
                                        if n, _ := gres.RowsAffected(); n != 1 {
                                                tx.Rollback()
                                                return nil, fmt.Errorf("%w: %s", ErrInsufficientStock, l.name)
                                        }
                                }
                        }
                        if _, err := tx.Exec(`UPDATE orders SET status = 'PAID' WHERE id = ? AND status = 'PENDING'`, id); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                } else if method == models.MethodAccount {
                        // Tab: like M-Pesa, the order stays PENDING and stock
                        // deducts once at settle via completePayment — never
                        // here, or settling would double-deduct. Limit
                        // enforced here inside the tx (0 = cash only, no tab).
                        if tabCustomer.CreditLimitCents <= 0 {
                                tx.Rollback()
                                return nil, fmt.Errorf("customer has no credit — cash only")
                        }
                        if tabCustomer.BalanceCents+total > tabCustomer.CreditLimitCents {
                                tx.Rollback()
                                return nil, fmt.Errorf("%w (%s)", ErrCreditLimit, tabCustomer.Name)
                        }
                        // Fail fast on stock BEFORE creating the tab (read
                        // check only — the guarded deduction happens once,
                        // at settle time, like every other PENDING order).
                        for _, l := range lines {
                                if l.trackStock {
                                        var stock int
                                        if err := tx.QueryRow(`SELECT stock_qty FROM products WHERE id = ?`, l.productID).Scan(&stock); err != nil {
                                                tx.Rollback()
                                                return nil, err
                                        }
                                        if stock < l.qty {
                                                tx.Rollback()
                                                return nil, fmt.Errorf("%w: %s (have %d, need %d)", ErrInsufficientStock, l.name, stock, l.qty)
                                        }
                                }
                        }
                        if _, err := tx.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, created_at)
                                VALUES (?, 'account', '', ?, 'PENDING', ?)`), id, total, now); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                        if err := recordLedgerTx(tx, s.db.Rebind, tabCustomer.ID, id, models.LedgerCharge,
                                total, 0, "tab charge "+number, p.ID); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                } else {
                        // M-Pesa: payment PENDING. STK requests whole shillings; manual
                        // entries use the exact total.
                        amount := total
                        phone := ""
                        if !isManual {
                                amount = roundToShilling(total)
                                ph, err := mpesa.NormalizePhone(req.CustomerPhone)
                                if err != nil {
                                        tx.Rollback()
                                        return nil, fmt.Errorf("customer phone: %w", err)
                                }
                                phone = ph
                        }
                        if _, err := tx.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, phone, created_at)
                                VALUES (?, 'mpesa', ?, ?, 'PENDING', ?, ?)`), id, mode, amount, phone, now); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                }

                if err := tx.Commit(); err != nil {
                        return nil, err
                }
                orderID, orderNumber = id, number
                break
        }
        if orderID == 0 {
                return nil, errors.New("could not allocate order number")
        }

        // Post-commit side effects (network + broadcasts never inside the tx).
        if method == models.MethodAccount {
                // Tab charged, not paid: audit only. No receipt print and no
                // paid broadcast — those happen on settle when money lands.
                s.Audit(p.ID, p.Username, "TAB_CHARGED", "order", orderNumber, fmt.Sprintf("total %d", total))
        } else if immediateCash {
                s.Audit(p.ID, p.Username, "CHECKOUT_CASH", "order", orderNumber, fmt.Sprintf("total %d", total))
                if order, err := s.GetOrder(orderID); err == nil {
                        s.printer.Enqueue(order)
                        s.broadcast(EventOrderPaid, order)
                }
        } else if !isManual {
                if _, err := s.InitiateSTK(ctx, orderID, p); err != nil {
                        // Order stays PENDING — cashier sees failure, can retry or void.
                        return s.GetOrder(orderID)
                }
        } else {
                s.Audit(p.ID, p.Username, "CHECKOUT_MPESA_MANUAL", "order", orderNumber, fmt.Sprintf("awaiting receipt code, total %d", total))
        }
        return s.GetOrder(orderID)
}

// isUniqueViolation matches sqlite/pg unique constraint errors.
func isUniqueViolation(err error) bool {
        if err == nil {
                return false
        }
        msg := strings.ToLower(err.Error())
        return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "duplicate key")
}

// nextOrderNumber allocates ORD{YYYYMMDD}{####} inside the caller's tx.
func (s *Service) nextOrderNumber(tx *sql.Tx) (string, error) {
        return s.nextDocNumber(tx, "ORD")
}

// nextDocNumber allocates PREFIX{YYYYMMDD}{####} inside the caller's tx.
// Uses a mutex to serialize number generation, avoiding race conditions
// under concurrent load. The day sequence is shared across prefixes.
func (s *Service) nextDocNumber(tx *sql.Tx, prefix string) (string, error) {
        day := time.Now().Format("20060102")
        
        // Initialize sequence table if not exists (idempotent)
        if _, err := tx.Exec(`
                CREATE TABLE IF NOT EXISTS order_sequences (
                        day TEXT PRIMARY KEY,
                        seq INTEGER NOT NULL DEFAULT 0
                )
        `); err != nil {
                return "", err
        }
        
        // Use a mutex to serialize order number generation across goroutines
        s.orderSeqMu.Lock()
        defer s.orderSeqMu.Unlock()
        
        var seq int
        err := tx.QueryRow(`SELECT seq FROM order_sequences WHERE day = ?`, day).Scan(&seq)
        if err == sql.ErrNoRows {
                seq = 0
        } else if err != nil {
                return "", err
        }
        
        seq++
        if _, err := tx.Exec(`
                INSERT INTO order_sequences (day, seq) VALUES (?, ?)
                ON CONFLICT(day) DO UPDATE SET seq = excluded.seq
        `, day, seq); err != nil {
                return "", err
        }
        
        return fmt.Sprintf(prefix+"%s%04d", day, seq), nil
}

// InitiateSTK sends (or re-sends) the push for a pending order's pending
// payment. Runs OUTSIDE any transaction; failures mark the payment FAILED.
func (s *Service) InitiateSTK(ctx context.Context, orderID int64, p *auth.Principal) (*models.Order, error) {
        order, err := s.GetOrder(orderID)
        if err != nil {
                return nil, err
        }
        if order.Status != models.OrderPending {
                return nil, fmt.Errorf("%w: cannot push STK for %s order", ErrInvalidState, order.Status)
        }
        var pay *models.Payment
        for i := range order.Payments {
                if order.Payments[i].Status == models.PaymentPending {
                        pay = &order.Payments[i]
                }
        }
        if pay == nil {
                return nil, fmt.Errorf("%w: order has no pending payment", ErrInvalidState)
        }
        if pay.Phone == "" {
                return nil, errors.New("payment has no phone number; collect the customer's number and retry")
        }
        provider := s.GetProvider()
        resp, err := provider.InitiateSTK(ctx, mpesa.STKRequest{
                OrderID:          order.ID,
                PaymentID:        pay.ID,
                Phone:            pay.Phone,
                AmountCents:      pay.AmountCents,
                AccountReference: order.Number,
                Description:      "Payment " + order.Number,
        })
        if err != nil {
                s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = ? WHERE id = ? AND status = 'PENDING'`),
                        truncStr(err.Error(), 200), pay.ID)
                s.Audit(p.ID, p.Username, "STK_FAILED", "order", order.Number, err.Error())
                return s.GetOrder(orderID)
        }
        s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'PENDING', checkout_request_id = ?, merchant_request_id = ?, result_desc = ? WHERE id = ?`),
                resp.CheckoutRequestID, resp.MerchantRequestID, resp.CustomerMessage, pay.ID)
        s.Audit(p.ID, p.Username, "STK_INITIATED", "order", order.Number, resp.CheckoutRequestID)
        // Reload AFTER STK state changes so the response reflects reality
        // (the original build returned a stale snapshot here).
        return s.GetOrder(orderID)
}

// RetrySTKWithPhone lets the cashier fix a bad number / re-push after a
// failure: pending payment is marked FAILED, a fresh one is created.
func (s *Service) RetrySTKWithPhone(ctx context.Context, orderID int64, phone string, p *auth.Principal) (*models.Order, error) {
        norm, err := mpesa.NormalizePhone(phone)
        if err != nil {
                return nil, fmt.Errorf("customer phone: %w", err)
        }
        order, err := s.GetOrder(orderID)
        if err != nil {
                return nil, err
        }
        if order.Status != models.OrderPending {
                return nil, fmt.Errorf("%w: cannot push STK for %s order", ErrInvalidState, order.Status)
        }
        // Supersede existing pending payments for this order.
        for _, pm := range order.Payments {
                if pm.Status == models.PaymentPending {
                        s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = 'superseded by retry' WHERE id = ?`), pm.ID)
                }
        }
        res, err := s.db.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, phone, created_at)
                VALUES (?, 'mpesa', 'stk', ?, 'PENDING', ?, ?)`),
                orderID, roundToShilling(order.TotalCents), norm, nowStamp())
        if err != nil {
                return nil, err
        }
        _ = res
        return s.InitiateSTK(ctx, orderID, p)
}

// completePayment is the single guarded transition PENDING → PAID.
// Sources: STK query result, STK callback, manual receipt entry. Idempotent;
// amount mismatches are flagged, never silently accepted.
func (s *Service) completePayment(paymentID int64, receipt string, amountCents int64, resultDesc string) (*models.Order, error) {
        tx, err := s.db.Begin()
        if err != nil {
                return nil, err
        }
        defer tx.Rollback()

        var orderID int64
        var payStatus, payMethod string
        var payAmount int64
        err = tx.QueryRow(`SELECT order_id, status, method, amount_cents FROM payments WHERE id = ?`, paymentID).
                Scan(&orderID, &payStatus, &payMethod, &payAmount)
        if err != nil {
                return nil, fmt.Errorf("payment %d: %w", paymentID, err)
        }
        if payStatus == models.PaymentCompleted {
                // CRITICAL: release the tx's connection BEFORE any new query —
                // with a single pooled connection, querying while this tx is open
                // self-deadlocks (the bug class this codebase is hardened against).
                tx.Rollback()
                return s.GetOrder(orderID) // idempotent: already completed
        }

        // Receipt uniqueness (dedupe manual re-entry / double callbacks).
        if receipt != "" {
                var other int64
                err := tx.QueryRow(s.db.Rebind(`SELECT id FROM payments WHERE mpesa_receipt = ? AND id != ?`), receipt, paymentID).Scan(&other)
                if err == nil {
                        return nil, fmt.Errorf("%w: %s", ErrDuplicateReceipt, receipt)
                }
                if err != sql.ErrNoRows {
                        return nil, err
                }
        }

        // Amount mismatch → flag, don't block (the money moved; staff must see it).
        discrepancy := 0
        if amountCents > 0 && amountCents != payAmount {
                discrepancy = 1
        }

        // Guarded order transition — exactly one winner.
        res, err := tx.Exec(`UPDATE orders SET status = 'PAID', paid_at = ? WHERE id = ? AND status = 'PENDING'`,
                nowStamp(), orderID)
        if err != nil {
                return nil, err
        }
        won, _ := res.RowsAffected()
        if won == 1 {
                // Deduct stock exactly once (guarded).
                items, err := s.loadItemsTx(tx, orderID)
                if err != nil {
                        return nil, err
                }
                for _, it := range items {
                        var track int
                        if err := tx.QueryRow(`SELECT COALESCE(track_stock,1) FROM products WHERE id = ?`, it.ProductID).Scan(&track); err != nil {
                                return nil, err
                        }
                        if track != 1 {
                                continue
                        }
                        gres, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty = stock_qty - ? WHERE id = ? AND stock_qty >= ?`),
                                it.Qty, it.ProductID, it.Qty)
                        if err != nil {
                                return nil, err
                        }
                        if n, _ := gres.RowsAffected(); n != 1 {
                                // Money received; stock can't cover. Flag for staff, keep paid.
                                discrepancy = 1
                                tx.Exec(`UPDATE orders SET discrepancy = 1 WHERE id = ?`, orderID)
                        }
                }
        }

        if receipt != "" {
                _, err = tx.Exec(s.db.Rebind(`UPDATE payments SET status = 'COMPLETED', mpesa_receipt = ?, completed_at = ?, result_desc = ?, discrepancy = ? WHERE id = ? AND status != 'COMPLETED'`),
                        receipt, nowStamp(), truncStr(resultDesc, 200), discrepancy, paymentID)
        } else {
                _, err = tx.Exec(s.db.Rebind(`UPDATE payments SET status = 'COMPLETED', completed_at = ?, result_desc = ?, discrepancy = ? WHERE id = ? AND status != 'COMPLETED'`),
                        nowStamp(), truncStr(resultDesc, 200), discrepancy, paymentID)
        }
        if err != nil {
                return nil, err
        }
        if discrepancy == 1 {
                tx.Exec(`UPDATE orders SET discrepancy = 1 WHERE id = ?`, orderID)
        }
        if err := tx.Commit(); err != nil {
                return nil, err
        }

        order, err := s.GetOrder(orderID)
        if err != nil {
                return nil, err
        }
        s.printer.Enqueue(order)
        if payMethod == models.MethodCash {
                // Best-effort drawer kick after commit — never fails the sale.
                if err := s.printer.Kick(); err != nil {
                        s.Audit(0, payMethod, "DRAWER_KICK_FAILED", "order", order.Number, err.Error())
                }
        }
        s.broadcast(EventOrderPaid, order)
        s.Audit(0, payMethod, "PAYMENT_COMPLETED", "order", order.Number, fmt.Sprintf("payment %d, receipt %s, amount %d", paymentID, receipt, payAmount))
        return order, nil
}

func (s *Service) loadItemsTx(tx *sql.Tx, orderID int64) ([]models.OrderItem, error) {
        rows, err := tx.Query(`SELECT product_id, qty FROM order_items WHERE order_id = ?`, orderID)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        var out []models.OrderItem
        for rows.Next() {
                var it models.OrderItem
                if err := rows.Scan(&it.ProductID, &it.Qty); err != nil {
                        return nil, err
                }
                out = append(out, it)
        }
        return out, rows.Err()
}

// ManualConfirm completes an order from a cashier-typed 10-character
// receipt code (customer paid at the till/paybill manually).
func (s *Service) ManualConfirm(orderID int64, rawCode string, p *auth.Principal) (*models.Order, error) {
        code := mpesa.CanonicalReceipt(rawCode)
        if err := mpesa.ValidateReceiptCode(code); err != nil {
                return nil, err
        }
        order, err := s.GetOrder(orderID)
        if err != nil {
                return nil, err
        }
        if order.Status != models.OrderPending {
                if order.Status == models.OrderPaid {
                        return nil, ErrOrderAlreadyPaid
                }
                return nil, fmt.Errorf("%w: cannot confirm payment on %s order", ErrInvalidState, order.Status)
        }
        // Prefer the order's pending payment (keeps the audit trail on one row);
        // otherwise create a manual payment row.
        var pay *models.Payment
        for i := range order.Payments {
                if order.Payments[i].Status == models.PaymentPending {
                        pay = &order.Payments[i]
                }
        }
        if pay == nil {
                res, err := s.db.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, created_at)
                        VALUES (?, 'mpesa', 'manual', ?, 'PENDING', ?)`), orderID, order.TotalCents, nowStamp())
                if err != nil {
                        return nil, err
                }
                id, _ := res.LastInsertId()
                pay = &models.Payment{ID: id, AmountCents: order.TotalCents}
        }
        result, err := s.completePayment(pay.ID, code, pay.AmountCents, "Manual receipt entry")
        if err != nil {
                return nil, err
        }
        s.Audit(p.ID, p.Username, "PAYMENT_MANUAL", "order", order.Number, "receipt "+code)
        return result, nil
}

// Void cancels an order; PAID orders restore stock, payments become VOIDED.
func (s *Service) Void(orderID int64, reason string, p *auth.Principal) (*models.Order, error) {
        tx, err := s.db.Begin()
        if err != nil {
                return nil, err
        }
        defer tx.Rollback()

        var status string
        if err := tx.QueryRow(`SELECT status FROM orders WHERE id = ?`, orderID).Scan(&status); err != nil {
                return nil, ErrNotFound
        }
        if status == models.OrderVoided {
                return nil, fmt.Errorf("%w: order already voided", ErrInvalidState)
        }
        res, err := tx.Exec(`UPDATE orders SET status = 'VOIDED', voided_at = ?, void_reason = ? WHERE id = ? AND status != 'VOIDED'`,
                nowStamp(), reason, orderID)
        if err != nil {
                return nil, err
        }
        if n, _ := res.RowsAffected(); n != 1 {
                return nil, ErrInvalidState
        }
        items, err := s.loadItemsTx(tx, orderID)
        if err != nil {
                return nil, err
        }
        if status == models.OrderPaid {
                for _, it := range items {
                        if _, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty = stock_qty + ? WHERE id = ?`), it.Qty, it.ProductID); err != nil {
                                return nil, err
                        }
                }
        }
        if _, err := tx.Exec(`UPDATE payments SET status = 'VOIDED', result_desc = ? WHERE order_id = ? AND status IN ('PENDING','COMPLETED')`,
                "voided: "+truncStr(reason, 180), orderID); err != nil {
                return nil, err
        }
        // Tab void: reverse the ledger charge so a cancelled sale leaves no
        // debt on the customer's balance. (Stock needs no restore: PENDING
        // tabs never deduct; settled tabs restore via the PAID path above.)
        var tabCustomer, tabTotal int64
        var tabCount int
        if err := tx.QueryRow(`SELECT customer_id, total_cents FROM orders WHERE id = ?`, orderID).
                Scan(&tabCustomer, &tabTotal); err != nil {
                return nil, err
        }
        if tabCustomer != 0 {
                if err := tx.QueryRow(`SELECT COUNT(*) FROM payments WHERE order_id = ? AND method = 'account'`,
                        orderID).Scan(&tabCount); err != nil {
                        return nil, err
                }
                if tabCount > 0 {
                        if err := recordLedgerTx(tx, s.db.Rebind, tabCustomer, orderID, models.LedgerAdjust,
                                -tabTotal, 0, "void reversal", p.ID); err != nil {
                                return nil, err
                        }
                }
        }
        if err := tx.Commit(); err != nil {
                return nil, err
        }
        s.Audit(p.ID, p.Username, "ORDER_VOIDED", "order", fmt.Sprint(orderID), reason)
        order, err := s.GetOrder(orderID)
        if err != nil {
                return nil, err
        }
        s.broadcast(EventOrderVoided, order)
        return order, nil
}

// HandleCallback processes a Daraja webhook (public endpoint).
func (s *Service) HandleCallback(cb mpesa.CallbackResult) (*models.Order, error) {
        var paymentID int64
        err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM payments WHERE checkout_request_id = ?`), cb.CheckoutRequestID).Scan(&paymentID)
        if err != nil {
                return nil, fmt.Errorf("callback for unknown checkout %s: %w", cb.CheckoutRequestID, err)
        }
        if cb.ResultCode != 0 {
                // Customer cancelled / failed — payment FAILED, order stays PENDING
                // so the cashier can retry STK, take a manual code, or void.
                s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = ? WHERE id = ? AND status = 'PENDING'`),
                        truncStr(cb.ResultDesc, 200), paymentID)
                return s.GetOrderByPayment(paymentID)
        }
        return s.completePayment(paymentID, cb.MpesaReceipt, cb.AmountCents, cb.ResultDesc)
}

func truncStr(str string, n int) string {
        if len(str) > n {
                return str[:n]
        }
        return str
}
