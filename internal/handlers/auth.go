package handlers

import (
        "net/http"
        "strconv"
        "strings"

        "github.com/gin-gonic/gin"

        "posapp/internal/auth"
        "posapp/internal/models"
)

type loginBody struct {
        Username string `json:"username" binding:"required"`
        Password string `json:"password" binding:"required"`
}

// Login authenticates with username+password (manager/admin path).
func (h *H) Login(c *gin.Context) {
        var body loginBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "username and password required")
                return
        }
        key := "login:" + strings.ToLower(body.Username)
        if !h.LoginRL.Allow(key) {
                h.fail(c, 429, "too many attempts — wait a minute")
                return
        }
        var id int64
        var pwHash string
        err := h.DB.QueryRow(h.DB.Rebind(
                `SELECT id, COALESCE(password_hash,'') FROM users WHERE LOWER(username) = LOWER(?)`), body.Username).
                Scan(&id, &pwHash)
        if err != nil || !auth.VerifyPassword(pwHash, body.Password) {
                h.fail(c, 401, "invalid credentials")
                return
        }
        p, err := auth.LoadPrincipal(h.DB, id)
        if err != nil {
                h.fail(c, 403, "account inactive")
                return
        }
        h.LoginRL.Forget(key)
        token, err := auth.IssueToken(h.Settings.JWTSecret(), p.ID, p.Username)
        if err != nil {
                h.fail(c, 500, "token error")
                return
        }
        h.Svc.Audit(p.ID, p.Username, "LOGIN", "user", p.Username, "password")
        h.ok(c, gin.H{"token": token, "user": p.User()})
}

// PinUsers lists staff available for quick PIN switch (name + role only —
// safe to show on a shared terminal, and it's how shift handoffs work).
func (h *H) PinUsers(c *gin.Context) {
        rows, err := h.DB.Query(`
                SELECT u.id, COALESCE(u.full_name, u.username), COALESCE(r.name,'')
                FROM users u JOIN roles r ON r.id = u.role_id
                WHERE u.is_active = 1 AND u.pin_hash != ''
                ORDER BY u.id`)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        defer rows.Close()
        out := []models.PinUser{}
        for rows.Next() {
                var u models.PinUser
                if err := rows.Scan(&u.ID, &u.FullName, &u.RoleName); err != nil {
                        break
                }
                out = append(out, u)
        }
        h.ok(c, out)
}

type pinBody struct {
        UserID int64  `json:"userId" binding:"required"`
        PIN    string `json:"pin" binding:"required,len=4"`
}

// PinLogin is the fast shift-switch path (4-digit PIN, bcrypt-hashed at
// rest, escalating lockout, rate limited).
func (h *H) PinLogin(c *gin.Context) {
        var body pinBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "userId and 4-digit pin required")
                return
        }
        if !h.PinRL.Allow("pin") {
                h.fail(c, 429, "too many attempts — wait a minute")
                return
        }
        if wait := auth.PINLockRemaining(h.DB, body.UserID); wait > 0 {
                h.fail(c, 423, "PIN locked — try again in "+itoa(wait)+"s")
                return
        }
        var pinHash string
        err := h.DB.QueryRow(h.DB.Rebind(
                `SELECT COALESCE(pin_hash,'') FROM users WHERE id = ? AND is_active = 1`), body.UserID).Scan(&pinHash)
        if err != nil || pinHash == "" {
                h.fail(c, 401, "invalid user")
                return
        }
        if !auth.VerifyPassword(pinHash, body.PIN) {
                lock := auth.RegisterPINFailure(h.DB, body.UserID)
                if lock > 0 {
                        h.fail(c, 423, "wrong PIN — locked for "+itoa(lock)+"s")
                        return
                }
                h.fail(c, 401, "wrong PIN")
                return
        }
        auth.ResetPINFailures(h.DB, body.UserID)
        p, err := auth.LoadPrincipal(h.DB, body.UserID)
        if err != nil {
                h.fail(c, 403, "account inactive")
                return
        }
        h.PinRL.Forget("pin")
        token, err := auth.IssueToken(h.Settings.JWTSecret(), p.ID, p.Username)
        if err != nil {
                h.fail(c, 500, "token error")
                return
        }
        h.Svc.Audit(p.ID, p.Username, "LOGIN", "user", p.Username, "pin")
        h.ok(c, gin.H{"token": token, "user": p.User()})
}

func itoa(n int) string { return strconv.Itoa(n) }

// Me returns the current principal (fresh permissions every request).
func (h *H) Me(c *gin.Context) {
        h.ok(c, h.principal(c).User())
}

// Branding is public white-label display data for login/POS chrome.
func (h *H) Branding(c *gin.Context) {
        h.ok(c, h.Settings.Branding())
}

var _ = http.StatusOK
