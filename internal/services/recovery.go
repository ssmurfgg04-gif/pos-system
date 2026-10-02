package services

// recovery.go — owner account recovery without a second person in the
// building: single-use recovery codes, hashed at rest, issued once and
// shown once. The code unlocks exactly one password reset on the public
// forgot-password endpoint (rate limited, audited). The CLI escape hatch
// (`ledgerpos reset-owner`) mints a fresh code for a locked-out owner with
// local machine access — it never reveals or bypasses the password itself.

import (
        "crypto/rand"
        "database/sql"
        "fmt"

        "posapp/internal/hash"
)

// newRecoveryCode mints a 16-hex-char code grouped 4-4-4-4.
func newRecoveryCode() (string, error) {
        buf := make([]byte, 8)
        if _, err := rand.Read(buf); err != nil {
                return "", err
        }
        raw := fmt.Sprintf("%x", buf) // 16 chars
        return raw[0:4] + "-" + raw[4:8] + "-" + raw[8:12] + "-" + raw[12:16], nil
}

// IssueRecoveryCode generates a fresh single-use code for the user,
// invalidating all previous codes. Returns the plaintext (shown once).
func (s *Service) IssueRecoveryCode(userID int64) (string, error) {
        code, err := newRecoveryCode()
        if err != nil {
                return "", err
        }
        h, err := hash.Password(code)
        if err != nil {
                return "", err
        }
        s.db.Exec(s.db.Rebind(`DELETE FROM recovery_codes WHERE user_id = ?`), userID)
        _, err = s.db.Exec(s.db.Rebind(`
                INSERT INTO recovery_codes (user_id, code_hash, created_at) VALUES (?, ?, ?)`),
                userID, h, nowStamp())
        if err != nil {
                return "", err
        }
        return code, nil
}

// HasRecoveryCode reports whether the user has any unused code.
func (s *Service) HasRecoveryCode(userID int64) bool {
        var one int
        s.db.QueryRow(s.db.Rebind(`SELECT 1 FROM recovery_codes WHERE user_id = ? AND used_at = '' LIMIT 1`), userID).Scan(&one)
        return one == 1
}

// RedeemRecoveryCode validates username+code and rotates the password.
// Every failure path returns the same generic error (no enumeration);
// timing is equalized by the caller. Returns the user id on success.
func (s *Service) RedeemRecoveryCode(username, code, newPassword string) (int64, error) {
        var userID int64
        var pwHash string
        var active int
        err := s.db.QueryRow(s.db.Rebind(`
                SELECT id, COALESCE(password_hash,''), is_active FROM users WHERE LOWER(username) = LOWER(?)`),
                username).Scan(&userID, &pwHash, &active)
        if err != nil {
                if err == sql.ErrNoRows {
                        return 0, fmt.Errorf("invalid details")
                }
                return 0, err
        }
        if active != 1 {
                return 0, fmt.Errorf("invalid details")
        }
        // One bcrypt burn per attempt (lockout-equivalent via the caller's
        // rate limiter), then the code check over the unused codes.
        rows, err := s.db.Query(s.db.Rebind(`
                SELECT id, code_hash FROM recovery_codes WHERE user_id = ? AND used_at = ''`), userID)
        if err != nil {
                return 0, err
        }
        type cand struct {
                id   int64
                hash string
        }
        var candidates []cand
        for rows.Next() {
                var c cand
                if err := rows.Scan(&c.id, &c.hash); err == nil {
                        candidates = append(candidates, c)
                }
        }
        rows.Close()
        matched := int64(0)
        for _, c := range candidates {
                if hash.Verify(c.hash, code) {
                        matched = c.id
                        break
                }
        }
        if matched == 0 {
                return 0, fmt.Errorf("invalid details")
        }
        if len(newPassword) < 6 || len(newPassword) > 128 {
                return 0, fmt.Errorf("invalid details")
        }
        h, err := hash.Password(newPassword)
        if err != nil {
                return 0, err
        }
        now := nowStamp()
        tx, err := s.db.Begin()
        if err != nil {
                return 0, err
        }
        defer tx.Rollback()
        // Burn ALL unused codes for the user (a reset invalidates the rest).
        if _, err := tx.Exec(`UPDATE recovery_codes SET used_at = ? WHERE user_id = ? AND used_at = ''`, now, userID); err != nil {
                return 0, err
        }
        if _, err := tx.Exec(s.db.Rebind(`
                UPDATE users SET password_hash = ?, must_rotate = 0, password_changed_at = ?, updated_at = CURRENT_TIMESTAMP
                WHERE id = ? AND is_active = 1`), h, now, userID); err != nil {
                return 0, err
        }
        if err := tx.Commit(); err != nil {
                return 0, err
        }
        return userID, nil
}
