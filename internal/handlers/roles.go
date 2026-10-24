package handlers

import (
	"encoding/json"

	"github.com/gin-gonic/gin"

	"posapp/internal/models"
)

type roleBody struct {
	Name        string   `json:"name" binding:"required"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions" binding:"required"`
}

// ListRoles (roles.manage to see; users.manage screens need it too — gate
// with the looser of the two at the router).
func (h *H) ListRoles(c *gin.Context) {
	rows, err := h.db(c).Query(`
                SELECT r.id, r.name, COALESCE(r.description,''), r.is_system, r.permissions,
                        (SELECT COUNT(*) FROM users u WHERE u.role_id = r.id)
                FROM roles r ORDER BY r.id`)
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, isSystem, userCount int64
		var name, desc, permsJSON string
		if err := rows.Scan(&id, &name, &desc, &isSystem, &permsJSON, &userCount); err != nil {
			break
		}
		var perms []string
		json.Unmarshal([]byte(permsJSON), &perms)
		out = append(out, map[string]any{
			"id": id, "name": name, "description": desc,
			"permissions": perms, "system": isSystem == 1, "userCount": userCount,
		})
	}
	h.ok(c, out)
}

// PermissionCatalog (roles.manage) — the full catalog with groups.
func (h *H) Permissions(c *gin.Context) {
	order, groups := models.PermissionGroups()
	h.ok(c, gin.H{"catalog": models.PermissionCatalog, "groups": groups, "groupOrder": order})
}

// ---- privilege-escalation guards ----

// authorityKeys are the governance permissions: whoever holds them can
// reshape who holds power (users, roles, settings). GRANTING any of them
// — by editing a role's permission set or assigning a role to a user —
// requires the caller to already hold that same key. A roles.manage
// manager can run day-to-day staffing but cannot mint themselves (or a
// colleague) into the business owner; only the owner can grant owner.
var authorityKeys = []string{"users.manage", "settings.manage", "roles.manage"}

// guardGrantElevation refuses when permsJSON contains an authority key the
// caller does not hold. Returns true when the request may proceed.
func (h *H) guardGrantElevation(c *gin.Context, permsJSON string) bool {
	p := h.principal(c)
	var perms []string
	json.Unmarshal([]byte(permsJSON), &perms)
	for _, key := range authorityKeys {
		for _, perm := range perms {
			if perm == key && !p.Can(key) {
				h.fail(c, 403, "only the business owner (an account with "+key+") can grant "+key+", ask the owner to make this change")
				return false
			}
		}
	}
	return true
}

// guardLastOwnerStrip refuses a role-permission edit that would remove
// owner-level power from the LAST role that carries it with active users.
func (h *H) guardLastOwnerStrip(c *gin.Context, roleID int64, newPermsJSON string) bool {
	var oldPerms string
	if err := h.db(c).QueryRow(`SELECT COALESCE(permissions,'[]') FROM roles WHERE id = ?`, roleID).Scan(&oldPerms); err != nil {
		h.fail(c, 409, "cannot verify owner roles right now, the change was refused. Try again.")
		return false
	}
	if !permsIncludeBoth(oldPerms, "settings.manage", "users.manage") ||
		permsIncludeBoth(newPermsJSON, "settings.manage", "users.manage") {
		return true // not owner-level before, or still owner-level after: fine
	}
	// The role is losing owner-level power. Another ACTIVE owner-level user
	// on a DIFFERENT role must exist, or this is a lockout in the making.
	rows, err := h.db(c).Query(`
                SELECT COALESCE(r.permissions, '[]') FROM users u JOIN roles r ON r.id = u.role_id
                WHERE u.is_active = 1 AND u.role_id != ?`, roleID)
	if err != nil {
		h.fail(c, 409, "cannot verify owner accounts right now, the change was refused. Try again.")
		return false // fail closed
	}
	defer rows.Close()
	for rows.Next() {
		var permsJSON string
		if err := rows.Scan(&permsJSON); err == nil && permsIncludeBoth(permsJSON, "settings.manage", "users.manage") {
			return true // another owner exists outside this role
		}
	}
	h.fail(c, 409, "this is the last owner role with active members, promote another owner before removing its powers")
	return false
}

func validatePerms(perms []string) (string, bool) {
	seen := map[string]bool{}
	for _, p := range perms {
		if !models.ValidPermission(p) {
			return "", false
		}
		seen[p] = true
	}
	b, _ := json.Marshal(seenKeys(seen))
	return string(b), true
}

func seenKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// CreateRole (roles.manage) — dynamic RBAC: admins invent any role. The
// permission set may not contain authority keys the caller does not hold.
func (h *H) CreateRole(c *gin.Context) {
	p := h.principal(c)
	var body roleBody
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "name and permissions required")
		return
	}
	permsJSON, ok := validatePerms(body.Permissions)
	if !ok {
		h.fail(c, 400, "unknown permission key")
		return
	}
	if !h.guardGrantElevation(c, permsJSON) {
		return
	}
	res, err := h.db(c).Exec(h.db(c).Rebind(
		`INSERT INTO roles (name, description, is_system, permissions) VALUES (?, ?, 0, ?)`),
		body.Name, body.Description, permsJSON)
	if err != nil {
		h.fail(c, 409, "role name already exists")
		return
	}
	id, _ := res.LastInsertId()
	h.svc(c).Audit(p.ID, p.Username, "ROLE_CREATED", "role", body.Name, permsJSON)
	h.created(c, gin.H{"id": id})
}

// UpdateRole (roles.manage). System roles (Admin/Cashier/Designer) are
// editable — that's the point of dynamic RBAC — but keep the name stable
// so seeds stay coherent.
func (h *H) UpdateRole(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body roleBody
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "name and permissions required")
		return
	}
	permsJSON, valid := validatePerms(body.Permissions)
	if !valid {
		h.fail(c, 400, "unknown permission key")
		return
	}
	if !h.guardGrantElevation(c, permsJSON) {
		return
	}
	if !h.guardLastOwnerStrip(c, id, permsJSON) {
		return
	}
	res, err := h.db(c).Exec(h.db(c).Rebind(
		`UPDATE roles SET description = ?, permissions = ? WHERE id = ?`), body.Description, permsJSON, id)
	if err != nil || n(res) != 1 {
		h.fail(c, 404, "role not found")
		return
	}
	h.svc(c).Audit(p.ID, p.Username, "ROLE_UPDATED", "role", itoa64(id), permsJSON)
	h.ok(c, gin.H{"updated": true})
}

// DeleteRole (roles.manage) — only custom roles with no users assigned.
func (h *H) DeleteRole(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var isSystem, userCount int
	err := h.db(c).QueryRow(`SELECT is_system, (SELECT COUNT(*) FROM users u WHERE u.role_id = roles.id) FROM roles WHERE id = ?`, id).
		Scan(&isSystem, &userCount)
	if err != nil {
		h.fail(c, 404, "role not found")
		return
	}
	if isSystem == 1 {
		h.fail(c, 409, "system roles cannot be deleted (they can be edited)")
		return
	}
	if userCount > 0 {
		h.fail(c, 409, "role still has users assigned")
		return
	}
	if _, err := h.db(c).Exec(`DELETE FROM roles WHERE id = ?`, id); err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	h.svc(c).Audit(p.ID, p.Username, "ROLE_DELETED", "role", itoa64(id), "")
	h.ok(c, gin.H{"deleted": true})
}
