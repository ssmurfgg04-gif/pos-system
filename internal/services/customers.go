package services

import (
	"database/sql"
	"fmt"
	"strings"

	"posapp/internal/auth"
	"posapp/internal/models"
)

// loyaltyPerCents is the legacy default earn rate (1 point per 100 KES of
// paid sales). The live rate is settings-driven (loyalty_earn_per_cents).
const loyaltyPerCents = 10000

// customerColumns is the shared SELECT list (email + notes joined CRM).
const customerColumns = `
        id, name, COALESCE(phone,''), COALESCE(email,''), COALESCE(notes,''), credit_limit_cents, loyalty_points,
        balance_cents, COALESCE(store_credit_cents,0), is_active, created_at, updated_at, COALESCE(price_tier,'retail')`

func scanCustomer(scan func(...any) error) (models.Customer, error) {
	var c models.Customer
	var active int
	err := scan(&c.ID, &c.Name, &c.Phone, &c.Email, &c.Notes, &c.CreditLimitCents, &c.LoyaltyPoints,
		&c.BalanceCents, &c.StoreCreditCents, &active, &c.CreatedAt, &c.UpdatedAt, &c.PriceTier)
	c.Active = active == 1
	return c, err
}

// GetCustomer loads one customer with its live balance.
func (s *Service) GetCustomer(id int64) (*models.Customer, error) {
	c, err := scanCustomer(func(dest ...any) error {
		return s.db.QueryRow(s.db.Rebind(`SELECT `+customerColumns+` FROM customers WHERE id = ?`), id).Scan(dest...)
	})
	if err != nil {
		return nil, ErrNotFound
	}
	return &c, nil
}

// ListCustomers returns active-first matches for name/phone/email search.
func (s *Service) ListCustomers(search string) ([]models.Customer, error) {
	// LOWER on both sides: user-facing search is case-insensitive on
	// SQLite AND on Postgres (bare LIKE is case-sensitive there).
	q := "%" + strings.ToLower(strings.TrimSpace(search)) + "%"
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT `+customerColumns+` FROM customers
                WHERE LOWER(name) LIKE ? OR LOWER(phone) LIKE ? OR LOWER(COALESCE(email,'')) LIKE ?
                ORDER BY is_active DESC, balance_cents DESC, name ASC
                LIMIT 200`), q, q, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Customer
	for rows.Next() {
		c, err := scanCustomer(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if out == nil {
		out = []models.Customer{}
	}
	return out, rows.Err()
}

// FindCustomerByPhone resolves the canonical CRM record for a phone number
// (normalizes 07…/7…/2547… shapes first). Used by checkout auto-capture so
// every sale by the same phone lands on one shared history.
func (s *Service) FindCustomerByPhone(phone string) *models.Customer {
	norm := normalizeKenyanPhone(phone)
	if norm == "" {
		return nil
	}
	var id int64
	if err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM customers WHERE phone = ? LIMIT 1`), norm).Scan(&id); err != nil {
		return nil
	}
	c, err := s.GetCustomer(id)
	if err != nil {
		return nil
	}
	return c
}

// CreateCustomer registers a customer. The phone is normalized (07… →
// 2547…), and an exact-phone duplicate is refused — one customer, one
// history. limitCents 0 = cash only (no tab).
func (s *Service) CreateCustomer(name, phone, email, notes string, limitCents int64, priceTier string, p *auth.Principal) (*models.Customer, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("customer name required")
	}
	if limitCents < 0 {
		return nil, fmt.Errorf("credit limit cannot be negative")
	}
	norm := normalizeKenyanPhone(phone)
	if norm != "" {
		var existing int64
		if err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM customers WHERE phone = ?`), norm).Scan(&existing); err == nil {
			return nil, fmt.Errorf("a customer with phone %s already exists (customer #%d), open their record instead", norm, existing)
		}
	}
	now := nowStamp()
	res, err := s.db.Exec(s.db.Rebind(`
                INSERT INTO customers (name, phone, email, notes, credit_limit_cents, price_tier, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		name, norm, strings.TrimSpace(email), truncStr(strings.TrimSpace(notes), 500), limitCents, tierOrRetail(priceTier), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	s.Audit(p.ID, p.Username, "CUSTOMER_CREATED", "customer", fmt.Sprint(id), name)
	s.EmitCustomer(id) // the identity follows the team (balances ride ledger events)
	return s.GetCustomer(id)
}

// UpdateCustomer edits name/phone/email/notes/limit/active state.
func (s *Service) UpdateCustomer(id int64, name, phone, email, notes string, limitCents int64, priceTier string, active bool, p *auth.Principal) (*models.Customer, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("customer name required")
	}
	if limitCents < 0 {
		return nil, fmt.Errorf("credit limit cannot be negative")
	}
	norm := normalizeKenyanPhone(phone)
	if norm != "" {
		var existing int64
		if err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM customers WHERE phone = ? AND id != ?`), norm, id).Scan(&existing); err == nil {
			return nil, fmt.Errorf("another customer already uses phone %s", norm)
		}
	}
	activeInt := 0
	if active {
		activeInt = 1
	}
	res, err := s.db.Exec(s.db.Rebind(`
                UPDATE customers SET name = ?, phone = ?, email = ?, notes = ?, credit_limit_cents = ?,
                        is_active = ?, price_tier = ?, updated_at = ? WHERE id = ?`),
		name, norm, strings.TrimSpace(email), truncStr(strings.TrimSpace(notes), 500), limitCents, activeInt, tierOrRetail(priceTier), nowStamp(), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrNotFound
	}
	s.Audit(p.ID, p.Username, "CUSTOMER_UPDATED", "customer", fmt.Sprint(id), name)
	s.EmitCustomer(id)
	return s.GetCustomer(id)
}

// GetCustomerOrders is the shared purchase history: every sale linked to
// the CRM record, newest first. This is what makes the customer book a
// CRM and not just a list of tab balances.
// GetCustomerOrders is the shared purchase history: every sale linked to
// the CRM record, newest first. v19: staff without orders.view.all see
// only the sales THEY rang up for this customer.
func (s *Service) GetCustomerOrders(customerID int64, p *auth.Principal) ([]models.OrderSummary, error) {
	if _, err := s.GetCustomer(customerID); err != nil {
		return nil, ErrNotFound
	}
	q := `
                SELECT id, number, status, total_cents, created_at, paid_at
                FROM orders WHERE customer_id = ?`
	var args []any
	args = append(args, customerID)
	if !canViewAllOrders(p) {
		q += ` AND cashier_id = ?`
		args = append(args, p.ID)
	}
	q += ` ORDER BY id DESC LIMIT 100`
	rows, err := s.db.Query(s.db.Rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.OrderSummary{}
	for rows.Next() {
		var o models.OrderSummary
		if err := rows.Scan(&o.ID, &o.Number, &o.Status, &o.TotalCents, &o.CreatedAt, &o.PaidAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// recordLedgerTx appends one ledger row and applies it to the balance,
// points, and store-credit columns. Callers own the transaction
// (checkout/settle/void/credit paths).
//
// Column semantics: balance_cents tracks what the customer OWES (charge +,
// payment −); store_credit_cents tracks what the shop owes the customer
// (topup +, redeem −); loyalty_points moves on kind = loyalty.
func recordLedgerTx(tx *sql.Tx, rebind func(string) string, customerID, orderID int64,
	kind string, amountCents, pointsDelta int64, note string, by int64) error {
	// v1.7.0: apply the money first, THEN write the ledger row with the
	// balance it produced (SmartPOS's customer_account_transactions
	// balance_after pattern). The snapshot is what makes a drifted
	// customers.balance_cents projection provable after the fact.
	var balAfter int64
	if kind == models.LedgerCreditTopup || kind == models.LedgerCreditRedeem || kind == models.LedgerRefund {
		if _, err := tx.Exec(rebind(`
                        UPDATE customers SET store_credit_cents = store_credit_cents + ?, updated_at = ? WHERE id = ?`),
			amountCents, nowStamp(), customerID); err != nil {
			return err
		}
		if err := tx.QueryRow(rebind(`SELECT COALESCE(balance_cents,0) FROM customers WHERE id = ?`), customerID).Scan(&balAfter); err != nil {
			return err
		}
	} else {
		if _, err := tx.Exec(rebind(`
                UPDATE customers SET balance_cents = balance_cents + ?,
                        loyalty_points = loyalty_points + ?, updated_at = ? WHERE id = ?`),
			amountCents, pointsDelta, nowStamp(), customerID); err != nil {
			return err
		}
		if err := tx.QueryRow(rebind(`SELECT COALESCE(balance_cents,0) FROM customers WHERE id = ?`), customerID).Scan(&balAfter); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(rebind(`
                INSERT INTO customer_ledger (customer_id, order_id, kind, amount_cents, points_delta, note, created_by, created_at, balance_after)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		customerID, orderID, kind, amountCents, pointsDelta, note, by, nowStamp(), balAfter); err != nil {
		return err
	}
	return nil
}

// recordLedgerTxNoBalance records ONLY the ledger row — the caller has
// already moved the balance with a GUARDED UPDATE (e.g. the atomic credit
// limit check on tab charges) and must not be double-applied.
func recordLedgerTxNoBalance(tx *sql.Tx, rebind func(string) string, customerID, orderID int64,
	kind string, amountCents, pointsDelta int64, note string, by int64) error {
	// v1.7.0: the caller already moved the balance with a guarded update
	// (e.g. the atomic credit-limit check) — read the post-move balance
	// and freeze it into the row.
	var balAfter int64
	if err := tx.QueryRow(rebind(`SELECT COALESCE(balance_cents,0) FROM customers WHERE id = ?`), customerID).Scan(&balAfter); err != nil {
		return err
	}
	if _, err := tx.Exec(rebind(`
                INSERT INTO customer_ledger (customer_id, order_id, kind, amount_cents, points_delta, note, created_by, created_at, balance_after)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		customerID, orderID, kind, amountCents, pointsDelta, note, by, nowStamp(), balAfter); err != nil {
		return err
	}
	return nil
}

// CustomerLedger returns newest-first entries for one customer.
func (s *Service) CustomerLedger(customerID int64) ([]models.LedgerEntry, error) {
	if _, err := s.GetCustomer(customerID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT id, customer_id, order_id, kind, amount_cents, points_delta,
                        COALESCE(note,''), created_by, created_at, balance_after, COALESCE(reversal_of,0)
                FROM customer_ledger WHERE customer_id = ?
                ORDER BY id DESC LIMIT 200`), customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.LedgerEntry
	for rows.Next() {
		var e models.LedgerEntry
		var bal sql.NullInt64
		if err := rows.Scan(&e.ID, &e.CustomerID, &e.OrderID, &e.Kind,
			&e.AmountCents, &e.PointsDelta, &e.Note, &e.CreatedBy, &e.CreatedAt, &bal, &e.ReversalOf); err != nil {
			return nil, err
		}
		if bal.Valid {
			v := bal.Int64
			e.BalanceAfter = &v
		}
		out = append(out, e)
	}
	if out == nil {
		out = []models.LedgerEntry{}
	}
	return out, rows.Err()
}

// AddOpeningBalance (v1.7.0) records debt a customer carried in when the
// shop joined LedgerPOS — the SmartPOS "opening balance as a real
// document" lesson: debt that exists only as a bare number can never be
// receipted, matched, or disputed; here it is a ledger row like any other.
func (s *Service) AddOpeningBalance(customerID, amountCents int64, note string, p *auth.Principal) (*models.Customer, error) {
	if amountCents == 0 {
		return nil, fmt.Errorf("%w: opening balance must be non-zero", ErrInvalidInput)
	}
	c, err := s.GetCustomer(customerID)
	if err != nil {
		return nil, err
	}
	if !c.Active {
		return nil, fmt.Errorf("%w: customer is inactive", ErrInvalidState)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := recordLedgerTx(tx, s.db.Rebind, customerID, 0, models.LedgerOpening,
		amountCents, 0, truncStr(note, 200), p.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.EmitLedger(customerID, models.LedgerOpening, amountCents, 0, truncStr(note, 200))
	s.Audit(p.ID, p.Username, "CUSTOMER_OPENING_BALANCE", "customer", fmt.Sprintf("%d", customerID), fmt.Sprintf("%d", amountCents))
	return s.GetCustomer(customerID)
}

// ReverseLedgerEntry (v1.7.0) cancels one manual ledger entry with a
// mirrored row (reversal_of). Money rows are immutable — nothing is
// updated or deleted; the reversal is a new fact that points at the entry
// it cancels (SmartPOS soft-reversal quartet, reduced to one foreign key).
// Only entries NOT created by an order (order_id = 0: opening balances,
// manual adjustments, standalone payments) can be reversed here; order
// charges are corrected through the returns/void flows so stock and money
// stay consistent.
func (s *Service) ReverseLedgerEntry(customerID, entryID int64, reason string, p *auth.Principal) (*models.Customer, error) {
	if reason == "" {
		return nil, fmt.Errorf("%w: a reversal reason is required", ErrInvalidInput)
	}
	if _, err := s.GetCustomer(customerID); err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var kind string
	var amount, points, orderID, already int64
	err = tx.QueryRow(s.db.Rebind(`
                SELECT kind, amount_cents, points_delta, order_id,
                       (SELECT COALESCE(MAX(reversal_of),0) FROM customer_ledger cl2 WHERE cl2.reversal_of = customer_ledger.id)
                FROM customer_ledger WHERE id = ? AND customer_id = ?`), entryID, customerID).
		Scan(&kind, &amount, &points, &orderID, &already)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: ledger entry %d not found for customer %d", ErrNotFound, entryID, customerID)
	}
	if err != nil {
		return nil, err
	}
	if orderID != 0 {
		return nil, fmt.Errorf("%w: order-linked entries are corrected via returns or voids, not direct reversal", ErrInvalidState)
	}
	if already != 0 {
		return nil, fmt.Errorf("%w: entry %d is already reversed", ErrInvalidState, entryID)
	}
	if kind == models.LedgerCreditTopup || kind == models.LedgerCreditRedeem || kind == models.LedgerRefund {
		return nil, fmt.Errorf("%w: store-credit entries are corrected via refunds, not direct reversal", ErrInvalidState)
	}
	note := truncStr("reversal: "+reason, 200)
	if _, err := tx.Exec(s.db.Rebind(`
                INSERT INTO customer_ledger (customer_id, order_id, kind, amount_cents, points_delta, note, created_by, created_at, balance_after, reversal_of)
                SELECT customer_id, 0, kind, -amount_cents, -points_delta, ?, ?, ?, NULL, id
                FROM customer_ledger WHERE id = ?`), note, p.ID, nowStamp(), entryID); err != nil {
		return nil, err
	}
	// Apply the mirror to the balance + capture the resulting snapshot on
	// the reversal row (the original keeps the balance it created).
	if _, err := tx.Exec(s.db.Rebind(`
                UPDATE customers SET balance_cents = balance_cents - ?, loyalty_points = loyalty_points - ?, updated_at = ? WHERE id = ?`),
		amount, points, nowStamp(), customerID); err != nil {
		return nil, err
	}
	var balAfter int64
	if err := tx.QueryRow(s.db.Rebind(`SELECT COALESCE(balance_cents,0) FROM customers WHERE id = ?`), customerID).Scan(&balAfter); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(s.db.Rebind(`UPDATE customer_ledger SET balance_after = ? WHERE customer_id = ? AND reversal_of = ?`),
		balAfter, customerID, entryID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.EmitLedger(customerID, kind, -amount, -points, note)
	s.Audit(p.ID, p.Username, "LEDGER_REVERSED", "customer", fmt.Sprintf("%d", customerID), fmt.Sprintf("entry %d: %s", entryID, reason))
	return s.GetCustomer(customerID)
}

// RecordCustomerPayment takes cash against a tab outside any order
// (walk-in partial payments). Reduces balance, never below zero tracking —
// overpayments are rejected, not stored as credit.
func (s *Service) RecordCustomerPayment(customerID, amountCents int64, note string, p *auth.Principal) (*models.Customer, error) {
	if amountCents <= 0 {
		return nil, fmt.Errorf("payment amount must be positive")
	}
	c, err := s.GetCustomer(customerID)
	if err != nil {
		return nil, err
	}
	if !c.Active {
		return nil, fmt.Errorf("%w: customer is inactive", ErrInvalidState)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Balance move is GUARDED inside the tx: the pre-tx read (c.BalanceCents)
	// cannot be trusted — two concurrent payments both passing a stale check
	// could drive the balance negative. RowsAffected==0 means the payment
	// would overpay (or the row vanished).
	gres, err := tx.Exec(s.db.Rebind(`UPDATE customers SET balance_cents = balance_cents - ?, updated_at = ?
                WHERE id = ? AND balance_cents >= ?`), amountCents, nowStamp(), customerID, amountCents)
	if err != nil {
		return nil, err
	}
	if n, _ := gres.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("%w: %d against balance %d", ErrOverpayment, amountCents, c.BalanceCents)
	}
	if err := recordLedgerTxNoBalance(tx, s.db.Rebind, customerID, 0, models.LedgerPayment,
		-amountCents, 0, note, p.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.Audit(p.ID, p.Username, "CUSTOMER_PAYMENT", "customer", fmt.Sprint(customerID), fmt.Sprintf("%d", amountCents))
	s.EmitLedger(customerID, models.LedgerPayment, -amountCents, 0, note)
	return s.GetCustomer(customerID)
}

// RecordCustomerAdjustment is the escape hatch for corrections (bad debt
// write-off, data fixes). Signed amount, audited, no limits applied.
func (s *Service) RecordCustomerAdjustment(customerID, amountCents int64, note string, p *auth.Principal) (*models.Customer, error) {
	if strings.TrimSpace(note) == "" {
		return nil, fmt.Errorf("adjustment needs a note")
	}
	if _, err := s.GetCustomer(customerID); err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := recordLedgerTx(tx, s.db.Rebind, customerID, 0, models.LedgerAdjust,
		amountCents, 0, note, p.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.Audit(p.ID, p.Username, "CUSTOMER_ADJUST", "customer", fmt.Sprint(customerID), note)
	s.EmitLedger(customerID, models.LedgerAdjust, amountCents, 0, note)
	return s.GetCustomer(customerID)
}

// SettleTab completes a tab order's account payment and posts the ledger
// payment + loyalty. Cash settles immediately; mpesa settles via a manual
// receipt code (same path as ordinary manual confirm).
func (s *Service) SettleTab(orderID int64, method, receiptCode string, p *auth.Principal) (*models.Order, error) {
	order, err := s.GetOrder(orderID)
	if err != nil {
		return nil, err
	}
	if order.Status != models.OrderPending {
		return nil, fmt.Errorf("%w: only pending tab orders settle", ErrInvalidState)
	}
	var pay *models.Payment
	for i := range order.Payments {
		if order.Payments[i].Method == models.MethodAccount && order.Payments[i].Status == models.PaymentPending {
			pay = &order.Payments[i]
			break
		}
	}
	if pay == nil {
		return nil, fmt.Errorf("%w: order has no pending tab payment", ErrInvalidState)
	}
	customerID, err := s.tabCustomerID(orderID)
	if err != nil {
		return nil, err
	}
	var result *models.Order
	switch method {
	case models.MethodCash:
		result, err = s.completePayment(pay.ID, "", pay.AmountCents, "Tab settled in cash")
		if err != nil {
			return nil, err
		}
	case models.MethodMpesa:
		if strings.TrimSpace(receiptCode) == "" {
			return nil, fmt.Errorf("receipt code required for M-Pesa settle")
		}
		// Reuse the manual-confirm path (receipt validation + dedupe).
		if _, err := s.ManualConfirm(orderID, receiptCode, p); err != nil {
			return nil, err
		}
		result, err = s.GetOrder(orderID)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("settle needs cash or mpesa, got %q", method)
	}
	// Ledger payment posted after the guarded transition — IDEMPOTENTLY:
	// completePayment returns idempotent success when it loses the
	// PENDING→PAID race (a double-click or a second till settling the same
	// tab), and a second unconditional post would erase the tab debt twice.
	// The (order_id, kind, note) triple is unique per settle — a repeat
	// caller finds the row and skips. Serialized by BEGIN IMMEDIATE.
	tx, err := s.db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var alreadyPosted int
	if err := tx.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM customer_ledger
                WHERE order_id = ? AND kind = ? AND note = ?`), orderID, models.LedgerPayment, "tab settled "+result.Number).Scan(&alreadyPosted); err != nil {
		return result, err
	}
	if alreadyPosted == 0 {
		if err := recordLedgerTx(tx, s.db.Rebind, customerID, orderID, models.LedgerPayment,
			-result.TotalCents, 0, "tab settled "+result.Number, p.ID); err != nil {
			return result, err
		}
		if err := tx.Commit(); err != nil {
			return result, err
		}
		s.Audit(p.ID, p.Username, "TAB_SETTLED", "order", result.Number, method)
		s.EmitLedger(customerID, models.LedgerPayment, -result.TotalCents, 0, "tab settled "+result.Number)
	}
	return s.GetOrder(orderID)
}

// tabCustomerID resolves the customer that owns a tab order.
func (s *Service) tabCustomerID(orderID int64) (int64, error) {
	var id int64
	if err := s.db.QueryRow(s.db.Rebind(
		`SELECT customer_id FROM orders WHERE id = ?`), orderID).Scan(&id); err != nil {
		return 0, ErrNotFound
	}
	if id == 0 {
		return 0, fmt.Errorf("%w: order has no tab customer", ErrInvalidState)
	}
	return id, nil
}
