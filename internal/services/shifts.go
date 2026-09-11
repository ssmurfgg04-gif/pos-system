package services

import (
        "database/sql"
        "errors"
        "fmt"
        "time"

        "posapp/internal/auth"
        "posapp/internal/models"
)

var ErrShiftOpen = errors.New("a shift is already open for this user")

// OpenShift starts a cash-drawer session with an opening float.
func (s *Service) OpenShift(p *auth.Principal, openingFloatCents int64) (*models.Shift, error) {
        var id int64
        err := s.db.QueryRow(`SELECT id FROM shifts WHERE user_id = ? AND closed_at = '' ORDER BY id DESC LIMIT 1`, p.ID).Scan(&id)
        if err == nil {
                return nil, ErrShiftOpen
        }
        if err != sql.ErrNoRows {
                return nil, err
        }
        res, err := s.db.Exec(s.db.Rebind(`INSERT INTO shifts (user_id, opening_float_cents, opened_at) VALUES (?, ?, ?)`), p.ID, openingFloatCents, nowStamp())
        if err != nil {
                return nil, err
        }
        sid, _ := res.LastInsertId()
        s.Audit(p.ID, p.Username, "SHIFT_OPENED", "shift", fmt.Sprint(sid), fmt.Sprintf("float %d", openingFloatCents))
        shift, err := s.GetShift(sid)
        if err == nil {
                s.broadcast(EventShiftUpdate, shift)
        }
        return shift, err
}

// GetShift loads one shift.
func (s *Service) GetShift(id int64) (*models.Shift, error) {
        var sh models.Shift
        var openedRaw, closedRaw string
        err := s.db.QueryRow(s.db.Rebind(`
                SELECT sh.id, sh.user_id, COALESCE(u.full_name, u.username, ''), sh.opening_float_cents,
                        sh.expected_cents, sh.counted_cents, sh.variance_cents, sh.opened_at, COALESCE(sh.closed_at,'')
                FROM shifts sh JOIN users u ON u.id = sh.user_id WHERE sh.id = ?`), id).
                Scan(&sh.ID, &sh.UserID, &sh.UserName, &sh.OpeningFloatCents, &sh.ExpectedCents, &sh.CountedCents,
                        &sh.VarianceCents, &openedRaw, &closedRaw)
        if err != nil {
                return nil, ErrNotFound
        }
        sh.OpenedAt = normTime(openedRaw)
        sh.ClosedAt = normTime(closedRaw)
        return &sh, nil
}

// CurrentShift returns the caller's open shift (nil when none).
func (s *Service) CurrentShift(userID int64) (*models.Shift, error) {
        var id int64
        err := s.db.QueryRow(`SELECT id FROM shifts WHERE user_id = ? AND closed_at = '' ORDER BY id DESC LIMIT 1`, userID).Scan(&id)
        if err == sql.ErrNoRows {
                return nil, nil
        }
        if err != nil {
                return nil, err
        }
        return s.GetShift(id)
}

// CloseShift counts the drawer. Expected = opening float + cash payments
// completed during the shift; variance = counted − expected.
func (s *Service) CloseShift(p *auth.Principal, countedCents int64) (*models.Shift, error) {
        shift, err := s.CurrentShift(p.ID)
        if err != nil || shift == nil {
                return nil, errors.New("no open shift")
        }
        var expected int64
        // Expected = opening float + cash payments completed since the shift
        // opened. Compared against the RAW stored opened_at (millisecond
        // stamp) — display-normalized values would round to whole seconds.
        var openedRaw string
        if err := s.db.QueryRow(`SELECT opened_at FROM shifts WHERE id = ?`, shift.ID).Scan(&openedRaw); err != nil {
                return nil, err
        }
        err = s.db.QueryRow(s.db.Rebind(`
                SELECT COALESCE(SUM(p.amount_cents), 0)
                FROM payments p JOIN orders o ON o.id = p.order_id
                WHERE p.method = 'cash' AND p.status = 'COMPLETED' AND o.cashier_id = ? AND p.completed_at >= ?`),
                p.ID, openedRaw).Scan(&expected)
        if err != nil {
                expected = 0
        }
        expected += shift.OpeningFloatCents
        variance := countedCents - expected

        tx, err := s.db.Begin()
        if err != nil {
                return nil, err
        }
        defer tx.Rollback()
        res, err := tx.Exec(s.db.Rebind(`UPDATE shifts SET expected_cents = ?, counted_cents = ?, variance_cents = ?, closed_at = ?
                WHERE id = ? AND closed_at = ''`), expected, countedCents, variance, nowStamp(), shift.ID)
        if err != nil {
                return nil, err
        }
        if n, _ := res.RowsAffected(); n != 1 {
                return nil, errors.New("shift already closed")
        }
        if err := tx.Commit(); err != nil {
                return nil, err
        }
        s.Audit(p.ID, p.Username, "SHIFT_CLOSED", "shift", fmt.Sprint(shift.ID),
                fmt.Sprintf("counted %d, expected %d, variance %d", countedCents, expected, variance))
        out, err := s.GetShift(shift.ID)
        if err == nil {
                s.broadcast(EventShiftUpdate, out)
        }
        return out, err
}

// ListShifts returns recent shifts (optionally per user).
func (s *Service) ListShifts(userID int64, limit int) []models.Shift {
        if limit <= 0 || limit > 100 {
                limit = 30
        }
        q := `
                SELECT sh.id, sh.user_id, COALESCE(u.full_name, u.username, ''), sh.opening_float_cents,
                        sh.expected_cents, sh.counted_cents, sh.variance_cents, sh.opened_at, COALESCE(sh.closed_at,'')
                FROM shifts sh JOIN users u ON u.id = sh.user_id`
        var args []any
        if userID != 0 {
                q += ` WHERE sh.user_id = ?`
                args = append(args, userID)
        }
        q += ` ORDER BY sh.id DESC`
        rows, err := s.db.Query(s.db.Rebind(q+fmt.Sprintf(" LIMIT %d", limit)), args...)
        if err != nil {
                return nil
        }
        defer rows.Close()
        var out []models.Shift
        for rows.Next() {
                var sh models.Shift
                var openedRaw, closedRaw string
                if err := rows.Scan(&sh.ID, &sh.UserID, &sh.UserName, &sh.OpeningFloatCents, &sh.ExpectedCents,
                        &sh.CountedCents, &sh.VarianceCents, &openedRaw, &closedRaw); err != nil {
                        return out
                }
                sh.OpenedAt = normTime(openedRaw)
                sh.ClosedAt = normTime(closedRaw)
                out = append(out, sh)
        }
        return out
}

// normTime normalizes sqlite datetime() or RFC3339 strings to RFC3339.
func normTime(raw string) string {
        if raw == "" {
                return ""
        }
        for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00"} {
                if t, err := time.Parse(layout, raw); err == nil {
                        return t.UTC().Format(time.RFC3339)
                }
        }
        return raw
}
