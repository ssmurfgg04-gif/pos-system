package services

// stocktake.go — physical stock counts and gift cards.
//
// Stocktake: open a session (snapshot expected quantities), record what is
// physically on the shelf, close with an optional apply — the counted value
// becomes the system stock, the variance (units + value at cost) is
// reported, audited, and team-synced so every till converges on the truth.
//
// Gift cards: a product flagged is_gift_card mints one code per unit when
// the sale is PAID. Redeeming a code moves its value into a customer's
// prepaid store credit (one ledger entry), after which the balance spends
// partially like any store credit.

import (
        "database/sql"
        "fmt"
        "strings"

        "posapp/internal/auth"
        "posapp/internal/models"
)

// ---- Stocktake ----

// StartStockCount snapshots every stock-tracked product into a new session.
func (s *Service) StartStockCount(p *auth.Principal, note string) (*models.StockCount, error) {
        tx, err := s.db.Begin()
        if err != nil {
                return nil, err
        }
        defer tx.Rollback()

        number, err := s.nextDocNumber(tx, "CNT")
        if err != nil {
                tx.Rollback()
                return nil, err
        }
        now := nowStamp()
        res, err := tx.Exec(s.db.Rebind(`INSERT INTO stock_counts (number, status, note, counted_by, counted_by_name, opened_at)
                VALUES (?, 'OPEN', ?, ?, ?, ?)`), number, truncStr(note, 300), p.ID, p.Username, now)
        if err != nil {
                tx.Rollback()
                return nil, err
        }
        countID, _ := res.LastInsertId()

        // Snapshot every tracked product (gift cards never track stock).
        _, err = tx.Exec(s.db.Rebind(`INSERT INTO stock_count_lines
                (count_id, product_id, sku, name, expected_qty, system_qty, unit_cost_cents)
                SELECT ?, p.id, COALESCE(p.sku,''), p.name, p.stock_qty, p.stock_qty, COALESCE(p.cost_cents,0)
                FROM products p WHERE COALESCE(p.track_stock,1) = 1 AND COALESCE(p.is_active,1) = 1
                ORDER BY p.name`), countID)
        if err != nil {
                tx.Rollback()
                return nil, err
        }
        var total int64
        if err := tx.QueryRow(`SELECT COUNT(*) FROM stock_count_lines WHERE count_id = ?`, countID).Scan(&total); err != nil {
                tx.Rollback()
                return nil, err
        }
        if _, err := tx.Exec(`UPDATE stock_counts SET lines_total = ? WHERE id = ?`, total, countID); err != nil {
                tx.Rollback()
                return nil, err
        }
        if err := tx.Commit(); err != nil {
                return nil, err
        }
        s.Audit(p.ID, p.Username, "STOCKCOUNT_OPENED", "stocktake", number, fmt.Sprintf("%d lines", total))
        return s.GetStockCount(countID)
}

// SaveCountLine records (or clears) the counted quantity for one product.
func (s *Service) SaveCountLine(p *auth.Principal, countID, productID int64, countedQty *int) error {
        var status string
        var number string
        if err := s.db.QueryRow(`SELECT status, number FROM stock_counts WHERE id = ?`, countID).Scan(&status, &number); err != nil {
                return fmt.Errorf("count session: %w", err)
        }
        if status != "OPEN" {
                return fmt.Errorf("count session %s is closed", number)
        }
        if countedQty != nil && (*countedQty < 0 || *countedQty > 1_000_000) {
                return fmt.Errorf("counted quantity out of range")
        }
        if countedQty != nil {
                if _, err := s.db.Exec(s.db.Rebind(`UPDATE stock_count_lines SET counted_qty = ? WHERE count_id = ? AND product_id = ?`),
                        *countedQty, countID, productID); err != nil {
                        return err
                }
        } else {
                if _, err := s.db.Exec(s.db.Rebind(`UPDATE stock_count_lines SET counted_qty = NULL WHERE count_id = ? AND product_id = ?`),
                        countID, productID); err != nil {
                        return err
                }
        }
        // Keep the progress counter fresh (cheap COUNT on the indexed child).
        _, err := s.db.Exec(s.db.Rebind(`UPDATE stock_counts SET lines_counted =
                (SELECT COUNT(*) FROM stock_count_lines WHERE count_id = ? AND counted_qty IS NOT NULL) WHERE id = ?`),
                countID, countID)
        return err
}

// CompleteStockCount closes a session. With apply=true every counted line
// becomes the product's new system stock (absolute set, LWW-synced).
func (s *Service) CompleteStockCount(p *auth.Principal, countID int64, apply bool) (*models.StockCount, error) {
        tx, err := s.db.Begin()
        if err != nil {
                return nil, err
        }
        defer tx.Rollback()

        var status, number string
        if err := tx.QueryRow(`SELECT status, number FROM stock_counts WHERE id = ?`, countID).Scan(&status, &number); err != nil {
                tx.Rollback()
                return nil, fmt.Errorf("count session: %w", err)
        }
        if status != "OPEN" {
                tx.Rollback()
                return nil, fmt.Errorf("count session %s is already closed", number)
        }

        // Variance snapshot: system qty as of NOW (sales since opening count).
        rows, err := tx.Query(s.db.Rebind(`
                SELECT l.id, l.product_id, l.sku, l.counted_qty, l.unit_cost_cents,
                       COALESCE((SELECT stock_qty FROM products WHERE id = l.product_id), l.expected_qty)
                FROM stock_count_lines l WHERE l.count_id = ?`), countID)
        if err != nil {
                tx.Rollback()
                return nil, err
        }
        type line struct {
                id        int64
                productID int64
                sku       string
                counted   sql.NullInt64
                cost      int64
                system    int
        }
        var lines []line
        for rows.Next() {
                var l line
                if err := rows.Scan(&l.id, &l.productID, &l.sku, &l.counted, &l.cost, &l.system); err != nil {
                        rows.Close()
                        tx.Rollback()
                        return nil, err
                }
                lines = append(lines, l)
        }
        rows.Close()
        if err := rows.Err(); err != nil {
                tx.Rollback()
                return nil, err
        }

        var varianceUnits, varianceValue int64
        var adjusted []int64 // product ids whose stock was set (emit after commit)
        for _, l := range lines {
                if !l.counted.Valid {
                        continue
                }
                diff := l.counted.Int64 - int64(l.system)
                varianceUnits += diff
                varianceValue += diff * l.cost
                if _, err := tx.Exec(s.db.Rebind(`UPDATE stock_count_lines SET system_qty = ?, applied = ? WHERE id = ?`),
                        l.system, btoi(apply && diff != 0), l.id); err != nil {
                        tx.Rollback()
                        return nil, err
                }
                if apply && diff != 0 {
                        // Absolute set with a guard: never lower than zero.
                        if _, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty = MAX(?, 0) WHERE id = ?`),
                                l.counted.Int64, l.productID); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                        adjusted = append(adjusted, l.productID)
                }
        }
        closedAt := nowStamp()
        if _, err := tx.Exec(s.db.Rebind(`UPDATE stock_counts SET status = 'DONE', closed_at = ?, variance_units = ?, variance_value_cents = ?
                WHERE id = ?`), closedAt, varianceUnits, varianceValue, countID); err != nil {
                tx.Rollback()
                return nil, err
        }
        if err := tx.Commit(); err != nil {
                return nil, err
        }

        // Post-commit: audit + team sync (absolute stock, admin wins LWW).
        action := "STOCKCOUNT_CLOSED"
        detail := fmt.Sprintf("%s: variance %d units, value %d", number, varianceUnits, varianceValue)
        if apply {
                action = "STOCKCOUNT_APPLIED"
                detail += " (applied)"
        }
        s.Audit(p.ID, p.Username, action, "stocktake", number, detail)
        for _, pid := range adjusted {
                s.EmitProduct(pid, true) // stock_set: last admin write wins across tills
        }
        return s.GetStockCount(countID)
}

func (s *Service) GetStockCount(countID int64) (*models.StockCount, error) {
        var c models.StockCount
        var opened, closed string
        err := s.db.QueryRow(`SELECT id, number, status, note, counted_by, counted_by_name, opened_at, closed_at,
                lines_total, lines_counted, variance_units, variance_value_cents
                FROM stock_counts WHERE id = ?`, countID).
                Scan(&c.ID, &c.Number, &c.Status, &c.Note, &c.CountedBy, &c.CountedByName, &opened, &closed,
                        &c.LinesTotal, &c.LinesCounted, &c.VarianceUnits, &c.VarianceValueCents)
        if err != nil {
                return nil, err
        }
        c.OpenedAt, c.ClosedAt = opened, closed
        return &c, nil
}

func (s *Service) ListStockCounts(limit int) ([]models.StockCount, error) {
        if limit <= 0 || limit > 200 {
                limit = 50
        }
        rows, err := s.db.Query(s.db.Rebind(`SELECT id, number, status, note, counted_by, counted_by_name, opened_at, closed_at,
                lines_total, lines_counted, variance_units, variance_value_cents
                FROM stock_counts ORDER BY id DESC LIMIT ?`), limit)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        out := []models.StockCount{}
        for rows.Next() {
                var c models.StockCount
                var opened, closed string
                if err := rows.Scan(&c.ID, &c.Number, &c.Status, &c.Note, &c.CountedBy, &c.CountedByName, &opened, &closed,
                        &c.LinesTotal, &c.LinesCounted, &c.VarianceUnits, &c.VarianceValueCents); err != nil {
                        return nil, err
                }
                c.OpenedAt, c.ClosedAt = opened, closed
                out = append(out, c)
        }
        return out, rows.Err()
}

func (s *Service) GetStockCountLines(countID int64) ([]models.StockCountLine, error) {
        rows, err := s.db.Query(`SELECT id, count_id, product_id, sku, name, expected_qty, counted_qty, system_qty, unit_cost_cents, applied
                FROM stock_count_lines WHERE count_id = ? ORDER BY name`, countID)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        out := []models.StockCountLine{}
        for rows.Next() {
                var l models.StockCountLine
                var counted sql.NullInt64
                var applied int
                if err := rows.Scan(&l.ID, &l.CountID, &l.ProductID, &l.SKU, &l.Name, &l.ExpectedQty, &counted, &l.SystemQty, &l.UnitCostCents, &applied); err != nil {
                        return nil, err
                }
                if counted.Valid {
                        v := int(counted.Int64)
                        l.CountedQty = &v
                }
                l.Applied = applied == 1
                out = append(out, l)
        }
        return out, rows.Err()
}

// ---- Gift cards ----

// issueGiftCardsTx mints one code per gift-card unit sold, inside the
// caller's transaction. Gift-card products do not track stock, so minting
// is the only side effect beyond the payment itself.
func issueGiftCardsTx(tx *sql.Tx, rebind func(query string) string, orderID int64) error {
        rows, err := tx.Query(rebind(`
                SELECT oi.qty, oi.unit_price_cents FROM order_items oi
                JOIN products p ON p.id = oi.product_id
                WHERE oi.order_id = ? AND COALESCE(p.is_gift_card, 0) = 1`), orderID)
        if err != nil {
                return err
        }
        type mint struct {
                qty    int
                amount int64
        }
        var mints []mint
        for rows.Next() {
                var m mint
                if err := rows.Scan(&m.qty, &m.amount); err != nil {
                        rows.Close()
                        return err
                }
                mints = append(mints, m)
        }
        rows.Close()
        if err := rows.Err(); err != nil {
                return err
        }
        now := nowStamp()
        for _, m := range mints {
                for i := 0; i < m.qty; i++ {
                        // Codes mint uppercase; redemption matches case-insensitively
                        // (UPPER(code) = UPPER(?)) so hand-typed codes just work.
                        code := "GC-" + strings.ToUpper(randToken(4)) + "-" + strings.ToUpper(randToken(4))
                        if _, err := tx.Exec(rebind(`INSERT INTO gift_cards (code, order_id, initial_cents, remaining_cents, status, issued_at)
                                VALUES (?, ?, ?, ?, 'ACTIVE', ?)`), code, orderID, m.amount, m.amount, now); err != nil {
                                return err
                        }
                }
        }
        return nil
}

// RedeemGiftCard converts a gift card's remaining value into the customer's
// prepaid store credit (single ledger entry; the balance spends partially).
func (s *Service) RedeemGiftCard(p *auth.Principal, code string, customerID int64) (*models.Customer, error) {
        code = strings.ToUpper(strings.TrimSpace(code))
        if code == "" || customerID <= 0 {
                return nil, fmt.Errorf("code and customer are required")
        }
        tx, err := s.db.Begin()
        if err != nil {
                return nil, err
        }
        defer tx.Rollback()

        var cardID, remaining int64
        var status string
        err = tx.QueryRow(`SELECT id, remaining_cents, status FROM gift_cards WHERE UPPER(code) = ?`, code).
                Scan(&cardID, &remaining, &status)
        if err == sql.ErrNoRows {
                tx.Rollback()
                return nil, fmt.Errorf("gift card %s not found", code)
        }
        if err != nil {
                tx.Rollback()
                return nil, err
        }
        if status != "ACTIVE" || remaining <= 0 {
                tx.Rollback()
                return nil, fmt.Errorf("gift card %s is already used", code)
        }
        var name string
        var active int
        if err := tx.QueryRow(`SELECT name, COALESCE(is_active,1) FROM customers WHERE id = ?`, customerID).
                Scan(&name, &active); err != nil {
                tx.Rollback()
                return nil, fmt.Errorf("customer: %w", err)
        }
        if active != 1 {
                tx.Rollback()
                return nil, fmt.Errorf("customer is inactive")
        }
        if _, err := tx.Exec(s.db.Rebind(`UPDATE customers SET store_credit_cents = store_credit_cents + ? WHERE id = ?`),
                remaining, customerID); err != nil {
                tx.Rollback()
                return nil, err
        }
        if err := recordLedgerTx(tx, s.db.Rebind, customerID, 0, models.LedgerCreditTopup,
                remaining, 0, "gift card "+code, p.ID); err != nil {
                tx.Rollback()
                return nil, err
        }
        if _, err := tx.Exec(s.db.Rebind(`UPDATE gift_cards SET status = 'EMPTY', remaining_cents = 0, redeemed_at = ?, redeemed_by_customer = ? WHERE id = ?`),
                nowStamp(), customerID, cardID); err != nil {
                tx.Rollback()
                return nil, err
        }
        if err := tx.Commit(); err != nil {
                return nil, err
        }
        s.Audit(p.ID, p.Username, "GIFT_CARD_REDEEMED", "customer", fmt.Sprint(customerID),
                fmt.Sprintf("%s: %d to %s", code, remaining, name))
        s.EmitLedger(customerID, models.LedgerCreditTopup, remaining, 0, "gift card "+code)
        return s.GetCustomer(customerID)
}

// ListGiftCards returns recent codes, optionally for one order.
func (s *Service) ListGiftCards(orderID int64, limit int) ([]models.GiftCard, error) {
        if limit <= 0 || limit > 500 {
                limit = 100
        }
        q := `SELECT id, code, order_id, initial_cents, remaining_cents, status, issued_at, redeemed_at, redeemed_by_customer
                FROM gift_cards`
        var args []any
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
        out := []models.GiftCard{}
        for rows.Next() {
                var g models.GiftCard
                if err := rows.Scan(&g.ID, &g.Code, &g.OrderID, &g.InitialCents, &g.RemainingCents,
                        &g.Status, &g.IssuedAt, &g.RedeemedAt, &g.RedeemedByCustomer); err != nil {
                        return nil, err
                }
                out = append(out, g)
        }
        return out, rows.Err()
}

func trimAndUpper(v string) string {
        return strings.ToUpper(strings.TrimSpace(v))
}
