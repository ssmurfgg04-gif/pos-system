package services

import (
	"database/sql"
	"fmt"
	"strings"

	"posapp/internal/auth"
	"posapp/internal/models"
)

// Loyalty: 1 point per 100 KES of paid sales (totalCents / 10000).
const loyaltyPerCents = 10000

// GetCustomer loads one customer with its live balance.
func (s *Service) GetCustomer(id int64) (*models.Customer, error) {
	var c models.Customer
	var active int
	err := s.db.QueryRow(s.db.Rebind(`
                SELECT id, name, COALESCE(phone,''), credit_limit_cents, loyalty_points,
                        balance_cents, is_active, created_at, updated_at
                FROM customers WHERE id = ?`), id).
		Scan(&c.ID, &c.Name, &c.Phone, &c.CreditLimitCents, &c.LoyaltyPoints,
			&c.BalanceCents, &active, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, ErrNotFound
	}
	c.Active = active == 1
	return &c, nil
}

// ListCustomers returns active-first matches for name/phone search.
func (s *Service) ListCustomers(search string) ([]models.Customer, error) {
	q := "%" + strings.TrimSpace(search) + "%"
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT id, name, COALESCE(phone,''), credit_limit_cents, loyalty_points,
                        balance_cents, is_active, created_at, updated_at
                FROM customers
                WHERE name LIKE ? OR phone LIKE ?
                ORDER BY is_active DESC, balance_cents DESC, name ASC
                LIMIT 200`), q, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Customer
	for rows.Next() {
		var c models.Customer
		var active int
		if err := rows.Scan(&c.ID, &c.Name, &c.Phone, &c.CreditLimitCents,
			&c.LoyaltyPoints, &c.BalanceCents, &active, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Active = active == 1
		out = append(out, c)
	}
	if out == nil {
		out = []models.Customer{}
	}
	return out, rows.Err()
}

// CreateCustomer registers a tab customer. limitCents 0 = cash only (no tab).
func (s *Service) CreateCustomer(name, phone string, limitCents int64, p *auth.Principal) (*models.Customer, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("customer name required")
	}
	if limitCents < 0 {
		return nil, fmt.Errorf("credit limit cannot be negative")
	}
	now := nowStamp()
	res, err := s.db.Exec(s.db.Rebind(`
                INSERT INTO customers (name, phone, credit_limit_cents, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?)`),
		name, strings.TrimSpace(phone), limitCents, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	s.Audit(p.ID, p.Username, "CUSTOMER_CREATED", "customer", fmt.Sprint(id), name)
	return s.GetCustomer(id)
}

// UpdateCustomer edits name/phone/limit/active state.
func (s *Service) UpdateCustomer(id int64, name, phone string, limitCents int64, active bool, p *auth.Principal) (*models.Customer, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("customer name required")
	}
	if limitCents < 0 {
		return nil, fmt.Errorf("credit limit cannot be negative")
	}
	activeInt := 0
	if active {
		activeInt = 1
	}
	res, err := s.db.Exec(s.db.Rebind(`
                UPDATE customers SET name = ?, phone = ?, credit_limit_cents = ?,
                        is_active = ?, updated_at = ? WHERE id = ?`),
		name, strings.TrimSpace(phone), limitCents, activeInt, nowStamp(), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, ErrNotFound
	}
	s.Audit(p.ID, p.Username, "CUSTOMER_UPDATED", "customer", fmt.Sprint(id), name)
	return s.GetCustomer(id)
}

// recordLedgerTx appends one ledger row and applies it to the balance and
// points. Callers own the transaction (checkout/settle/void paths).
func recordLedgerTx(tx *sql.Tx, rebind func(string) string, customerID, orderID int64,
	kind string, amountCents, pointsDelta int64, note string, by int64) error {
	if _, err := tx.Exec(rebind(`
                INSERT INTO customer_ledger (customer_id, order_id, kind, amount_cents, points_delta, note, created_by, created_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		customerID, orderID, kind, amountCents, pointsDelta, note, by, nowStamp()); err != nil {
		return err
	}
	if _, err := tx.Exec(rebind(`
                UPDATE customers SET balance_cents = balance_cents + ?,
                        loyalty_points = loyalty_points + ?, updated_at = ? WHERE id = ?`),
		amountCents, pointsDelta, nowStamp(), customerID); err != nil {
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
                        COALESCE(note,''), created_by, created_at
                FROM customer_ledger WHERE customer_id = ?
                ORDER BY id DESC LIMIT 200`), customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.LedgerEntry
	for rows.Next() {
		var e models.LedgerEntry
		if err := rows.Scan(&e.ID, &e.CustomerID, &e.OrderID, &e.Kind,
			&e.AmountCents, &e.PointsDelta, &e.Note, &e.CreatedBy, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if out == nil {
		out = []models.LedgerEntry{}
	}
	return out, rows.Err()
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
		return nil, fmt.Errorf("customer is inactive")
	}
	if amountCents > c.BalanceCents {
		return nil, fmt.Errorf("%w: %d against balance %d", ErrOverpayment, amountCents, c.BalanceCents)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := recordLedgerTx(tx, s.db.Rebind, customerID, 0, models.LedgerPayment,
		-amountCents, 0, note, p.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.Audit(p.ID, p.Username, "CUSTOMER_PAYMENT", "customer", fmt.Sprint(customerID), fmt.Sprintf("%d", amountCents))
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
	// Ledger payment + loyalty, posted after the guarded transition.
	points := result.TotalCents / loyaltyPerCents
	tx, err := s.db.Begin()
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err := recordLedgerTx(tx, s.db.Rebind, customerID, orderID, models.LedgerPayment,
		-result.TotalCents, 0, "tab settled "+result.Number, p.ID); err != nil {
		return result, err
	}
	if points > 0 {
		if err := recordLedgerTx(tx, s.db.Rebind, customerID, orderID, models.LedgerLoyalty,
			0, points, "loyalty earned "+result.Number, p.ID); err != nil {
			return result, err
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	s.Audit(p.ID, p.Username, "TAB_SETTLED", "order", result.Number, method)
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
