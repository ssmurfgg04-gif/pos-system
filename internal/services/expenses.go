package services

// expenses.go — shop expense log (v1.6.3, migration v24). Petty cash,
// rent, power, airtime: money that leaves the till outside a sale. The
// daily/monthly reports net these against sales so the day-end number
// finally answers "where did the money go".

import (
	"fmt"
	"time"

	"posapp/internal/auth"
	"posapp/internal/models"
)

// ListExpenseTypes returns the active type catalog (seeded by v24).
func (s *Service) ListExpenseTypes() ([]models.ExpenseType, error) {
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT id, name, COALESCE(is_active,1) FROM expense_types ORDER BY name COLLATE NOCASE`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.ExpenseType{}
	for rows.Next() {
		var t models.ExpenseType
		var active int
		if err := rows.Scan(&t.ID, &t.Name, &active); err != nil {
			return nil, err
		}
		t.IsActive = active == 1
		out = append(out, t)
	}
	return out, rows.Err()
}

// CreateExpense records one expense (EXP- numbering, like orders/returns).
func (s *Service) CreateExpense(req models.CreateExpenseRequest, p *auth.Principal) (*models.Expense, error) {
	if p == nil {
		return nil, fmt.Errorf("%w: no principal", ErrInvalidInput)
	}
	if req.AmountCents <= 0 {
		return nil, fmt.Errorf("%w: amount must be positive", ErrInvalidInput)
	}
	if req.AmountCents > models.MaxPriceCents {
		return nil, fmt.Errorf("%w: amount exceeds maximum", ErrInvalidInput)
	}
	method := req.Method
	if method == "" {
		method = "CASH"
	}
	if method != "CASH" && method != "MPESA" && method != "BANK" {
		return nil, fmt.Errorf("%w: method must be CASH, MPESA or BANK", ErrInvalidInput)
	}
	spentOn := req.SpentOn
	if spentOn == "" {
		spentOn = time.Now().In(eastAfrica).Format("2006-01-02")
	} else if !validDay(spentOn) {
		return nil, fmt.Errorf("%w: spentOn must be YYYY-MM-DD", ErrInvalidInput)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var typeName string
	if err := tx.QueryRow(s.db.Rebind(`SELECT name FROM expense_types WHERE id = ?`), req.ExpenseTypeID).
		Scan(&typeName); err != nil {
		return nil, fmt.Errorf("%w: expense type not found", ErrNotFound)
	}
	number, err := s.nextDocNumber(tx, "EXP-", 0)
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(s.db.Rebind(`INSERT INTO expenses
                (number, expense_type_id, amount_cents, method, reference, note, spent_on, client_uuid, created_by, created_by_name, created_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		number, req.ExpenseTypeID, req.AmountCents, method, truncStr(req.Reference, 100),
		truncStr(req.Note, 300), spentOn, newClientUUID(), p.ID, p.Username, nowStamp())
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.Audit(p.ID, p.Username, "EXPENSE_RECORDED", "expense", number,
		fmt.Sprintf("%s: %d cents via %s", typeName, req.AmountCents, method))
	doc, err := s.GetExpense(id)
	if err != nil {
		return nil, err
	}
	s.EmitExpense(doc)
	return doc, nil
}

// GetExpense loads one expense row with its type name.
func (s *Service) GetExpense(id int64) (*models.Expense, error) {
	var e models.Expense
	err := s.db.QueryRow(s.db.Rebind(`
                SELECT x.id, x.number, x.expense_type_id, COALESCE(t.name,''), x.amount_cents, x.method,
                        COALESCE(x.reference,''), COALESCE(x.note,''), x.spent_on, x.created_by,
                        COALESCE(x.created_by_name,''), x.created_at
                FROM expenses x LEFT JOIN expense_types t ON t.id = x.expense_type_id
                WHERE x.id = ?`), id).
		Scan(&e.ID, &e.Number, &e.ExpenseTypeID, &e.TypeName, &e.AmountCents, &e.Method,
			&e.Reference, &e.Note, &e.SpentOn, &e.CreatedBy, &e.CreatedByName, &e.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("%w: expense not found", ErrNotFound)
	}
	return &e, nil
}

// ListExpenses newest-first with optional date filter.
func (s *Service) ListExpenses(date string, limit int) ([]models.Expense, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `
                SELECT x.id, x.number, x.expense_type_id, COALESCE(t.name,''), x.amount_cents, x.method,
                        COALESCE(x.reference,''), COALESCE(x.note,''), x.spent_on, x.created_by,
                        COALESCE(x.created_by_name,''), x.created_at
                FROM expenses x LEFT JOIN expense_types t ON t.id = x.expense_type_id`
	args := []any{}
	if date != "" {
		if !validDay(date) {
			return nil, fmt.Errorf("%w: date must be YYYY-MM-DD", ErrInvalidInput)
		}
		q += ` WHERE x.spent_on = ?`
		args = append(args, date)
	}
	q += ` ORDER BY x.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(s.db.Rebind(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Expense{}
	for rows.Next() {
		var e models.Expense
		if err := rows.Scan(&e.ID, &e.Number, &e.ExpenseTypeID, &e.TypeName, &e.AmountCents, &e.Method,
			&e.Reference, &e.Note, &e.SpentOn, &e.CreatedBy, &e.CreatedByName, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// validDay reports whether s is a strict YYYY-MM-DD calendar date.
func validDay(s string) bool {
	_, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	return err == nil
}

// ExpensesTotalForDay is the money-side figure the reports net off sales.
func (s *Service) ExpensesTotalForDay(date string) (int64, error) {
	var total int64
	err := s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(SUM(amount_cents),0) FROM expenses WHERE spent_on = ?`), date).Scan(&total)
	return total, err
}

// StockValuation is the "what is the shelf worth" figure for reports:
// units on hand × unit cost across tracked products.
func (s *Service) StockValuation() (units int64, valueCents int64, err error) {
	err = s.db.QueryRow(`
                SELECT COALESCE(SUM(stock_qty_milli),0), COALESCE(SUM(stock_qty_milli * cost_cents) / 1000, 0)
                FROM products WHERE track_stock = 1 AND is_active = 1`).Scan(&units, &valueCents)
	return units, valueCents, err
}
