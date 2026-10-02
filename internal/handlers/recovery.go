package handlers

// recovery.go — authentication lifecycle endpoints: first-run owner setup
// (replaces the old seeded demo accounts), the public forgot-password flow
// backed by single-use recovery codes, and self-service recovery-code
// issuance. Every failure path is generic (no username enumeration) and
// rate limited; every success is audited.

import (
        "github.com/gin-gonic/gin"

	"database/sql"

        "posapp/internal/auth"
        "posapp/internal/services"
)

type bootstrapBody struct {
        Username string `json:"username" binding:"required"`
        FullName string `json:"fullName"`
        Password string `json:"password" binding:"required"`
        PIN      string `json:"pin"`
}

// HasUsers (public) tells the login screen whether to render first-run
// owner setup or the normal login. Zero users can only mean a fresh
// database: demo accounts no longer ship (SEED_DEMO opts back in).
func (h *H) HasUsers(c *gin.Context) {
        db, err := h.shopDB(c)
        if err != nil {
                h.fail(c, 404, "unknown shop")
                return
        }
        var n int
        db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_active = 1`).Scan(&n)
        h.ok(c, gin.H{"hasUsers": n > 0})
}

// Bootstrap (public, first-run only) creates the FIRST owner account when
// the database has zero active users. It issues the owner's session and a
// single-use recovery code (shown once — this is the credential that
// unlocks forgot-password if the password is ever lost).
func (h *H) Bootstrap(c *gin.Context) {
        if !h.LoginRL.Allow("bootstrap") {
                h.fail(c, 429, "too many attempts — wait a minute")
                return
        }
        db, err := h.shopDB(c)
        if err != nil {
                h.fail(c, 404, "unknown shop")
                return
        }
        var n int
        db.QueryRow(`SELECT COUNT(*) FROM users WHERE is_active = 1`).Scan(&n)
        if n > 0 {
                h.fail(c, 409, "accounts already exist — sign in instead")
                return
        }
        var body bootstrapBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "username and password are required")
                return
        }
        if !auth.ValidUsername(body.Username) {
                h.fail(c, 400, "username must be 3-32 letters, digits, dot, underscore, or hyphen")
                return
        }
        if !auth.ValidPassword(body.Password) {
                h.fail(c, 400, "password must be 6-128 characters")
                return
        }
        if auth.IsDefaultPassword(body.Password) {
                h.fail(c, 400, "choose a stronger password — that one is public")
                return
        }
        pinHash := ""
        if body.PIN != "" {
                if !auth.ValidPIN(body.PIN) {
                        h.fail(c, 400, "PIN must be exactly 4 digits and not an obvious pattern")
                        return
                }
                ph, err := auth.HashPassword(body.PIN)
                if err != nil {
                        h.fail(c, 500, err.Error())
                        return
                }
                pinHash = ph
        }
        pwHash, err := auth.HashPassword(body.Password)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        fullName := body.FullName
        if fullName == "" {
                fullName = body.Username
        }
        // The first account is the owner: Admin role (every permission),
        // rotation already satisfied (they chose the secret just now).
        res, err := db.Exec(db.Rebind(`
                INSERT INTO users (username, full_name, password_hash, pin_hash, role_id, is_active, must_rotate, password_changed_at)
                SELECT ?, ?, ?, ?, (SELECT id FROM roles WHERE name = 'Admin'), 1, 0, ?
                WHERE NOT EXISTS (SELECT 1 FROM users WHERE LOWER(username) = LOWER(?))`),
                body.Username, fullName, pwHash, pinHash, rotationStamp(), body.Username)
        if err != nil || nRes(res) != 1 {
                h.fail(c, 409, "could not create the account (username taken?)")
                return
        }
        var userID int64
        db.QueryRow(`SELECT id FROM users WHERE LOWER(username) = LOWER(?)`, body.Username).Scan(&userID)
        svc, err := h.shopSvc(c)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        code, err := svc.IssueRecoveryCode(userID)
        if err != nil {
                h.fail(c, 500, "could not mint a recovery code: "+err.Error())
                return
        }
        if h.Tenants != nil {
                _ = h.Tenants.RegisterUser(body.Username, h.shopID(c))
        }
        svc.Audit(userID, body.Username, "OWNER_BOOTSTRAP", "user", body.Username, "first-run setup")
        p, err := auth.LoadPrincipal(db, userID)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        p.ShopID = h.shopID(c)
        token, err := auth.IssueToken(h.MasterSecret, p.ID, p.Username, p.ShopID)
        if err != nil {
                h.fail(c, 500, "token error")
                return
        }
        h.created(c, gin.H{"token": token, "user": p.User(), "recoveryCode": code})
}

type forgotBody struct {
        Username    string `json:"username" binding:"required"`
        RecoveryCode string `json:"recoveryCode" binding:"required"`
        NewPassword string `json:"newPassword" binding:"required"`
}

// ForgotPassword (public) completes the recovery flow: username + one-time
// recovery code + the replacement password. Failures are generic and rate
// limited; successes burn the code, clear rotation, and are audited.
func (h *H) ForgotPassword(c *gin.Context) {
        key := "forgot"
        if !h.LoginRL.Allow(key) {
                h.fail(c, 429, "too many attempts — wait a minute")
                return
        }
        var body forgotBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "username, recovery code, and new password are required")
                return
        }
        svc, err := h.shopSvc(c)
        if err != nil {
                h.fail(c, 404, "unknown shop")
                return
        }
        userID, err := svc.RedeemRecoveryCode(body.Username, body.RecoveryCode, body.NewPassword)
        if err != nil {
                auth.EqualizeLoginTiming(body.NewPassword)
                h.fail(c, 401, "invalid details")
                return
        }
        var username string
        svc.DB().QueryRow(`SELECT username FROM users WHERE id = ?`, userID).Scan(&username)
        svc.Audit(userID, username, "OWNER_RECOVERY_USED", "user", username, "password reset via recovery code")
        h.LoginRL.Forget(key)
        h.ok(c, gin.H{"reset": true})
}

// NewRecoveryCode (authenticated, self only) rotates the caller's recovery
// code and returns the plaintext exactly once. Used after setup, after a
// password change, or whenever the old code's paper copy went missing.
func (h *H) NewRecoveryCode(c *gin.Context) {
        p := h.principal(c)
        svc := h.svc(c)
        code, err := svc.IssueRecoveryCode(p.ID)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        svc.Audit(p.ID, p.Username, "RECOVERY_CODE_ISSUED", "user", p.Username, "")
        h.ok(c, gin.H{"recoveryCode": code})
}

// shopSvc resolves the default (or ?shop=) service for public endpoints.
func (h *H) shopSvc(c *gin.Context) (*services.Service, error) {
        if h.Shops == nil {
                return h.Svc, nil
        }
        return h.Shops.Service(h.publicShopID(c))
}

func nRes(res sql.Result) int64 {
        v, _ := res.RowsAffected()
        return v
}
