package handlers

import (
        "net/http"
        "os"
        "strconv"
        "strings"
        "time"

        "github.com/gin-gonic/gin"

        "posapp/internal/auth"
        "posapp/internal/models"
)

type loginBody struct {
        Username string `json:"username" binding:"required"`
        Password string `json:"password" binding:"required"`
}

// Login authenticates with username+password. The registry routes the
// username to its shop first, so correct credentials always land in the
// correct shop — and unknown names 401 without revealing shops.
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
        shopID, ok := h.Tenants.ShopForUser(body.Username)
        if !ok {
                h.fail(c, 401, "invalid credentials")
                return
        }
        svc, err := h.Shops.Service(shopID)
        if err != nil {
                h.fail(c, 401, "invalid credentials")
                return
        }
        db := svc.DB()
        var id int64
        var pwHash string
        err = db.QueryRow(db.Rebind(
                `SELECT id, COALESCE(password_hash,'') FROM users WHERE LOWER(username) = LOWER(?)`), body.Username).
                Scan(&id, &pwHash)
        if err != nil || !auth.VerifyPassword(pwHash, body.Password) {
                h.fail(c, 401, "invalid credentials")
                return
        }
        p, err := auth.LoadPrincipal(db, id)
        if err != nil {
                h.fail(c, 403, "account inactive")
                return
        }
        p.ShopID = shopID
        h.LoginRL.Forget(key)
        token, err := auth.IssueToken(h.MasterSecret, p.ID, p.Username, shopID)
        if err != nil {
                h.fail(c, 500, "token error")
                return
        }
        svc.Audit(p.ID, p.Username, "LOGIN", "user", p.Username, "password")
        h.ok(c, gin.H{"token": token, "user": p.User()})
}

// Signup is public iff ALLOW_SIGNUP=true: provisions a shop (migrate +
// catalog + one admin holding chosen credentials) and returns its token.
// Usernames are unique per box so login routing stays unambiguous.
func (h *H) Signup(c *gin.Context) {
        if os.Getenv("ALLOW_SIGNUP") != "true" {
                h.fail(c, 403, "signup disabled")
                return
        }
        var body struct {
                Username string `json:"username" binding:"required"`
                Password string `json:"password" binding:"required,min=6"`
                ShopName string `json:"shopName" binding:"required"`
        }
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "username, 6+ character password, and shop name required")
                return
        }
        if !h.LoginRL.Allow("signup:" + c.ClientIP()) {
                h.fail(c, 429, "too many attempts — wait a minute")
                return
        }
        if _, taken := h.Tenants.ShopForUser(body.Username); taken {
                h.fail(c, 409, "username taken")
                return
        }
        shop, err := h.Tenants.CreateShop(strings.TrimSpace(body.ShopName), "",
                time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        db, err := h.Shops.DB(shop.ID)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        // Pool.Open migrates; SeedShop adds settings/roles/catalog + admin.
        if err := db.SeedShop(body.ShopName, body.Username, body.Password); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        if err := h.Tenants.RegisterUser(body.Username, shop.ID); err != nil {
                h.fail(c, 409, err.Error())
                return
        }
        var id int64
        if err := db.QueryRow(db.Rebind(`SELECT id FROM users WHERE LOWER(username) = LOWER(?)`),
                body.Username).Scan(&id); err != nil {
                h.fail(c, 500, "signup failed")
                return
        }
        p, err := auth.LoadPrincipal(db, id)
        if err != nil {
                h.fail(c, 500, "signup failed")
                return
        }
        p.ShopID = shop.ID
        token, err := auth.IssueToken(h.MasterSecret, p.ID, p.Username, shop.ID)
        if err != nil {
                h.fail(c, 500, "token error")
                return
        }
        h.LoginRL.Forget("signup:" + c.ClientIP())
        svc, _ := h.Shops.Service(shop.ID)
        svc.Audit(p.ID, p.Username, "SHOP_CREATED", "shop", shop.ID, shop.Name)
        h.created(c, gin.H{"token": token, "user": p.User(),
                "shop": gin.H{"id": shop.ID, "name": shop.Name}})
}

// PinUsers lists staff available for quick PIN switch (name + role only —
// safe to show on a shared terminal, and it's how shift handoffs work).
// Scoped by ?shop= (a terminal belongs to one shop); default shop otherwise.
func (h *H) PinUsers(c *gin.Context) {
        db, err := h.shopDB(c)
        if err != nil {
                h.fail(c, 404, "unknown shop")
                return
        }
        rows, err := db.Query(`
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
        shopID, ok := h.Shops.FindUserShop(body.UserID)
        if !ok {
                h.fail(c, 401, "invalid user")
                return
        }
        db, err := h.Shops.DB(shopID)
        if err != nil {
                h.fail(c, 401, "invalid user")
                return
        }
        if wait := auth.PINLockRemaining(db, body.UserID); wait > 0 {
                h.fail(c, 423, "PIN locked — try again in "+itoa(wait)+"s")
                return
        }
        var pinHash string
        err = db.QueryRow(db.Rebind(
                `SELECT COALESCE(pin_hash,'') FROM users WHERE id = ? AND is_active = 1`), body.UserID).Scan(&pinHash)
        if err != nil || pinHash == "" {
                h.fail(c, 401, "invalid user")
                return
        }
        if !auth.VerifyPassword(pinHash, body.PIN) {
                lock := auth.RegisterPINFailure(db, body.UserID)
                if lock > 0 {
                        h.fail(c, 423, "wrong PIN — locked for "+itoa(lock)+"s")
                        return
                }
                h.fail(c, 401, "wrong PIN")
                return
        }
        auth.ResetPINFailures(db, body.UserID)
        p, err := auth.LoadPrincipal(db, body.UserID)
        if err != nil {
                h.fail(c, 403, "account inactive")
                return
        }
        p.ShopID = shopID
        h.PinRL.Forget("pin")
        token, err := auth.IssueToken(h.MasterSecret, p.ID, p.Username, shopID)
        if err != nil {
                h.fail(c, 500, "token error")
                return
        }
        svc, _ := h.Shops.Service(shopID)
        svc.Audit(p.ID, p.Username, "LOGIN", "user", p.Username, "pin")
        h.ok(c, gin.H{"token": token, "user": p.User()})
}

func itoa(n int) string { return strconv.Itoa(n) }

// Me returns the current principal (fresh permissions every request).
func (h *H) Me(c *gin.Context) {
        h.ok(c, h.principal(c).User())
}

// Branding is public white-label display data for login/POS chrome.
func (h *H) Branding(c *gin.Context) {
        st, err := h.shopSettings(c)
        if err != nil {
                h.fail(c, 404, "unknown shop")
                return
        }
        h.ok(c, st.Branding())
}

var _ = http.StatusOK
