package handlers

import (
        "database/sql"

        "github.com/gin-gonic/gin"

        "posapp/internal/auth"
        "posapp/internal/models"
)

type userBody struct {
        Username string `json:"username" binding:"required"`
        FullName string `json:"fullName"`
        Password string `json:"password"`
        PIN      string `json:"pin"`
        RoleID   int64  `json:"roleId" binding:"required"`
        Active   *bool  `json:"active"`
}

// ListUsers (users.manage).
func (h *H) ListUsers(c *gin.Context) {
        rows, err := h.DB.Query(h.DB.Rebind(`
                SELECT u.id, u.username, COALESCE(u.full_name,''), u.role_id, COALESCE(r.name,''),
                        u.is_active, CASE WHEN u.pin_hash != '' THEN 1 ELSE 0 END, u.created_at
                FROM users u JOIN roles r ON r.id = u.role_id ORDER BY u.id`))
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        defer rows.Close()
        out := []models.User{}
        for rows.Next() {
                var u models.User
                var active, pinSet int
                if err := rows.Scan(&u.ID, &u.Username, &u.FullName, &u.RoleID, &u.RoleName, &active, &pinSet, &u.CreatedAt); err != nil {
                        break
                }
                u.Active = active == 1
                u.PINSet = pinSet == 1
                out = append(out, u)
        }
        h.ok(c, out)
}

// CreateUser (users.manage).
func (h *H) CreateUser(c *gin.Context) {
        p := h.principal(c)
        var body userBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        if body.Password == "" {
                h.fail(c, 400, "password required")
                return
        }
        var roleExists int
        if err := h.DB.QueryRow(`SELECT COUNT(*) FROM roles WHERE id = ?`, body.RoleID).Scan(&roleExists); err != nil || roleExists == 0 {
                h.fail(c, 400, "role not found")
                return
        }
        pwHash, err := auth.HashPassword(body.Password)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        pinHash := ""
        if body.PIN != "" {
                if len(body.PIN) != 4 {
                        h.fail(c, 400, "PIN must be exactly 4 digits")
                        return
                }
                pinHash, err = auth.HashPassword(body.PIN)
                if err != nil {
                        h.fail(c, 500, err.Error())
                        return
                }
        }
        active := 1
        if body.Active != nil && !*body.Active {
                active = 0
        }
        res, err := h.DB.Exec(h.DB.Rebind(`
                INSERT INTO users (username, full_name, password_hash, pin_hash, role_id, is_active)
                VALUES (?, ?, ?, ?, ?, ?)`), body.Username, body.FullName, pwHash, pinHash, body.RoleID, active)
        if err != nil {
                h.fail(c, 409, "username already exists")
                return
        }
        id, _ := res.LastInsertId()
        h.Svc.Audit(p.ID, p.Username, "USER_CREATED", "user", body.Username, "")
        h.created(c, gin.H{"id": id})
}

// UpdateUser (users.manage).
func (h *H) UpdateUser(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var body userBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        var active int
        if body.Active != nil && !*body.Active {
                active = 0
        } else {
                active = 1
        }
        res, err := h.DB.Exec(h.DB.Rebind(`
                UPDATE users SET full_name = ?, role_id = ?, is_active = ?, updated_at = CURRENT_TIMESTAMP
                WHERE id = ?`), body.FullName, body.RoleID, active, id)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        if n, _ := res.RowsAffected(); n != 1 {
                h.fail(c, 404, "user not found")
                return
        }
        h.Svc.Audit(p.ID, p.Username, "USER_UPDATED", "user", itoa64(id), body.Username)
        h.ok(c, gin.H{"updated": true})
}

type passwordBody struct {
        Password string `json:"password" binding:"required,min=6"`
}

// SetPassword (users.manage).
func (h *H) SetPassword(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var body passwordBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "password must be at least 6 characters")
                return
        }
        hash, err := auth.HashPassword(body.Password)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        res, err := h.DB.Exec(h.DB.Rebind(`UPDATE users SET password_hash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`), hash, id)
        if err != nil || n(res) != 1 {
                h.fail(c, 404, "user not found")
                return
        }
        h.Svc.Audit(p.ID, p.Username, "USER_PASSWORD_RESET", "user", itoa64(id), "")
        h.ok(c, gin.H{"updated": true})
}

type pinBody2 struct {
        PIN string `json:"pin" binding:"required,len=4"`
}

// SetPIN (users.manage).
func (h *H) SetPIN(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var body pinBody2
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "PIN must be exactly 4 digits")
                return
        }
        hash, err := auth.HashPassword(body.PIN)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        res, err := h.DB.Exec(h.DB.Rebind(
                `UPDATE users SET pin_hash = ?, failed_pin_attempts = 0, pin_locked_until = '', updated_at = CURRENT_TIMESTAMP WHERE id = ?`), hash, id)
        if err != nil || n(res) != 1 {
                h.fail(c, 404, "user not found")
                return
        }
        h.Svc.Audit(p.ID, p.Username, "USER_PIN_RESET", "user", itoa64(id), "")
        h.ok(c, gin.H{"updated": true})
}

// DeactivateUser soft-deletes (orders keep their cashier reference).
func (h *H) DeactivateUser(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        if id == p.ID {
                h.fail(c, 409, "you cannot deactivate yourself")
                return
        }
        res, err := h.DB.Exec(h.DB.Rebind(`UPDATE users SET is_active = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?`), id)
        if err != nil || n(res) != 1 {
                h.fail(c, 404, "user not found")
                return
        }
        h.Svc.Audit(p.ID, p.Username, "USER_DEACTIVATED", "user", itoa64(id), "")
        h.ok(c, gin.H{"deactivated": true})
}

func n(res sql.Result) int64 {
        v, _ := res.RowsAffected()
        return v
}

func itoa64(i int64) string {
        return itoa(int(i))
}
