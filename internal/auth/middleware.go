package auth

import (
        "encoding/json"
        "fmt"
        "sort"
        "time"

        "github.com/gin-gonic/gin"

        "posapp/internal/database"
        "posapp/internal/models"
)

// Principal is the authenticated caller, reloaded from the DB on every
// request so role/permission edits apply immediately (no stale JWT perms).
type Principal struct {
        ID          int64
        Username    string
        FullName    string
        RoleID      int64
        RoleName    string
        Active      bool
        Permissions map[string]bool
}

func (p *Principal) Can(perm string) bool { return p.Permissions[perm] }

// LoadPrincipal fetches user + role permissions in two sequential queries
// (never nested — see the single-connection discipline).
func LoadPrincipal(db *database.DB, userID int64) (*Principal, error) {
        p := &Principal{Permissions: map[string]bool{}}
        err := db.QueryRow(db.Rebind(`
                SELECT u.id, u.username, COALESCE(u.full_name,''), u.role_id, COALESCE(r.name,''), u.is_active
                FROM users u JOIN roles r ON r.id = u.role_id
                WHERE u.id = ?`), userID).
                Scan(&p.ID, &p.Username, &p.FullName, &p.RoleID, &p.RoleName, &p.Active)
        if err != nil {
                return nil, err
        }
        if !p.Active {
                return nil, fmt.Errorf("user deactivated")
        }
        var permsJSON string
        if err := db.QueryRow(`SELECT permissions FROM roles WHERE id = ?`, p.RoleID).Scan(&permsJSON); err != nil {
                return nil, err
        }
        var perms []string
        if err := json.Unmarshal([]byte(permsJSON), &perms); err == nil {
                for _, k := range perms {
                        p.Permissions[k] = true
                }
        }
        return p, nil
}

func (p *Principal) User() models.User {
        keys := make([]string, 0, len(p.Permissions))
        for k := range p.Permissions {
                keys = append(keys, k)
        }
        sort.Strings(keys)
        return models.User{
                ID: p.ID, Username: p.Username, FullName: p.FullName,
                RoleID: p.RoleID, RoleName: p.RoleName, Permissions: keys, Active: p.Active,
        }
}

// ---- PIN lockout (escalating: 5th failure locks 30s, doubling to 300s) ----

const (
        MaxFreePINFailures = 5
        MinLockSeconds     = 30
        MaxLockSeconds     = 300
)

// RegisterPINFailure increments the counter and (re)locks when threshold hit.
// Returns how many seconds the account is locked for (0 = not locked).
func RegisterPINFailure(db *database.DB, userID int64) int {
        var fails int
        if err := db.QueryRow(`SELECT failed_pin_attempts FROM users WHERE id = ?`, userID).Scan(&fails); err != nil {
                return 0
        }
        fails++
        lock := 0
        if fails >= MaxFreePINFailures {
                lock = MinLockSeconds << (fails - MaxFreePINFailures)
                if lock > MaxLockSeconds {
                        lock = MaxLockSeconds
                }
        }
        var until string
        if lock > 0 {
                until = time.Now().UTC().Add(time.Duration(lock) * time.Second).Format(time.RFC3339)
        }
        db.Exec(db.Rebind(`UPDATE users SET failed_pin_attempts = ?, pin_locked_until = ? WHERE id = ?`), fails, until, userID)
        return lock
}

// ResetPINFailures clears the counter on success.
func ResetPINFailures(db *database.DB, userID int64) {
        db.Exec(db.Rebind(`UPDATE users SET failed_pin_attempts = 0, pin_locked_until = '' WHERE id = ?`), userID)
}

// PINLockRemaining returns seconds remaining in a lock (0 = unlocked).
func PINLockRemaining(db *database.DB, userID int64) int {
        var until string
        if err := db.QueryRow(`SELECT COALESCE(pin_locked_until,'') FROM users WHERE id = ?`, userID).Scan(&until); err != nil || until == "" {
                return 0
        }
        t, err := time.Parse(time.RFC3339, until)
        if err != nil {
                return 0
        }
        if d := int(time.Until(t).Seconds()); d > 0 {
                return d
        }
        return 0
}

// ---- Gin context helpers ----

const ctxPrincipal = "principal"

func WithPrincipal(c *gin.Context, p *Principal) { c.Set(ctxPrincipal, p) }

func FromContext(c *gin.Context) *Principal {
        if v, ok := c.Get(ctxPrincipal); ok {
                if p, ok := v.(*Principal); ok {
                        return p
                }
        }
        return nil
}

// RequirePermission is the server-side RBAC gate. The frontend hides UI;
// this enforces.
func RequirePermission(perm string) gin.HandlerFunc {
        return func(c *gin.Context) {
                p := FromContext(c)
                if p == nil {
                        c.AbortWithStatusJSON(401, gin.H{"error": "Authentication required"})
                        return
                }
                if !p.Can(perm) {
                        c.AbortWithStatusJSON(403, gin.H{"error": "Missing permission: " + perm})
                        return
                }
                c.Next()
        }
}
