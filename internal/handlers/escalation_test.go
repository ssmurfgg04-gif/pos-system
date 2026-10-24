package handlers_test

// escalation_test.go — privilege-escalation + settings-secret regression
// tests:
//
//  1. A user with users.manage but WITHOUT settings.manage/roles.manage
//     cannot (a) edit a role to grant authority keys, (b) create a role
//     with authority keys, (c) assign the full-access Admin role to
//     anyone, or (d) update an existing user into an owner-level role.
//  2. The same limited manager CAN run ordinary staffing.
//  3. Editing the last owner-level role to strip its owner powers is
//     refused while it is the only owner source, and allowed once a
//     second owner exists.
//  4. Settings saves: an emptied field preserves a configured secret,
//     a masked echo keeps it, __CLEAR__ removes it, and GET never
//     returns the secret value.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// roleIDByName resolves a seeded/custom role id via the API.
func roleIDByName(t *testing.T, engine *gin.Engine, token, name string) int64 {
	t.Helper()
	w := do(t, engine, "GET", "/api/v1/roles", token, nil)
	if w.Code != 200 {
		t.Fatalf("list roles: %d %s", w.Code, w.Body.String())
	}
	list, _ := decode(t, w)["data"].([]any)
	for _, it := range list {
		m, _ := it.(map[string]any)
		if m["name"] == name {
			return int64(m["id"].(float64))
		}
	}
	t.Fatalf("role %q not found", name)
	return -1
}

// userIDByName resolves a user id via the API.
func userIDByName(t *testing.T, engine *gin.Engine, token, username string) int64 {
	t.Helper()
	w := do(t, engine, "GET", "/api/v1/users", token, nil)
	if w.Code != 200 {
		t.Fatalf("list users: %d %s", w.Code, w.Body.String())
	}
	list, _ := decode(t, w)["data"].([]any)
	for _, it := range list {
		m, _ := it.(map[string]any)
		if m["username"] == username {
			return int64(m["id"].(float64))
		}
	}
	t.Fatalf("user %q not found", username)
	return -1
}

// seedLimitedManager creates a users.manage-only role + a user in it and
// returns a logged-in token for that user (post-rotation steady state).
func seedLimitedManager(t *testing.T, engine *gin.Engine, admin string) string {
	t.Helper()
	r := do(t, engine, "POST", "/api/v1/roles", admin, map[string]any{
		"name":        "Staffing Manager",
		"description": "day-to-day staffing, no owner powers",
		"permissions": []string{"users.manage", "products.manage", "orders.view"},
	})
	if r.Code != http.StatusCreated && r.Code != http.StatusOK {
		t.Fatalf("create role: %d %s", r.Code, r.Body.String())
	}
	roleID := roleIDByName(t, engine, admin, "Staffing Manager")
	u := do(t, engine, "POST", "/api/v1/users", admin, map[string]any{
		"username": "staffmgr", "password": "staffmgr-pass-1", "roleId": roleID, "fullName": "Staffing Manager",
	})
	if u.Code != http.StatusCreated && u.Code != http.StatusOK {
		t.Fatalf("create user: %d %s", u.Code, u.Body.String())
	}
	token := login(t, engine, "staffmgr", "staffmgr-pass-1")
	uid := itoa64(dataMap(t, do(t, engine, "GET", "/api/v1/me", token, nil))["id"])
	pw := do(t, engine, "PUT", "/api/v1/users/"+uid+"/password", token, map[string]any{"password": "staffmgr-rotated-1"})
	if pw.Code != 200 {
		t.Fatalf("rotate staffmgr: %d %s", pw.Code, pw.Body.String())
	}
	return login(t, engine, "staffmgr", "staffmgr-rotated-1")
}

func TestLimitedManagerCannotGrantAuthorityKeys(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)
	mgr := seedLimitedManager(t, engine, admin)
	mgrRoleID := itoa64(roleIDByName(t, engine, admin, "Staffing Manager"))
	designerID := itoa64(userIDByName(t, engine, admin, "designer"))

	// (a) Edit own role to add settings.manage → 403.
	r := do(t, engine, "PUT", "/api/v1/roles/"+mgrRoleID, mgr, map[string]any{
		"description": "self-promoted",
		"permissions": []string{"users.manage", "settings.manage", "products.manage", "orders.view"},
	})
	if r.Code != http.StatusForbidden {
		t.Fatalf("role self-escalation: got %d want 403 (%s)", r.Code, r.Body.String())
	}

	// (b) Create a new role with roles.manage → 403.
	r = do(t, engine, "POST", "/api/v1/roles", mgr, map[string]any{
		"name":        "Sneaky Super",
		"permissions": []string{"roles.manage"},
	})
	if r.Code != http.StatusForbidden {
		t.Fatalf("role creation escalation: got %d want 403", r.Code)
	}

	// (c) Assign the full-access Admin role to a new user → 403.
	r = do(t, engine, "POST", "/api/v1/users", mgr, map[string]any{
		"username": "sneaky", "password": "sneaky-pass-1", "roleId": roleIDByName(t, engine, admin, "Admin"),
	})
	if r.Code != http.StatusForbidden {
		t.Fatalf("admin-role assignment escalation: got %d want 403", r.Code)
	}

	// (d) Update an existing staff user into the Admin role → 403.
	r = do(t, engine, "PUT", "/api/v1/users/"+designerID, mgr, map[string]any{
		"username": "designer", "fullName": "Designer", "roleId": roleIDByName(t, engine, admin, "Admin"), "active": true,
	})
	if r.Code != http.StatusForbidden {
		t.Fatalf("user role-change escalation: got %d want 403", r.Code)
	}

	// The same limited manager CAN run ordinary staffing.
	r = do(t, engine, "POST", "/api/v1/users", mgr, map[string]any{
		"username": "newcashier", "password": "newcashier-pass-1", "roleId": roleIDByName(t, engine, admin, "Cashier"), "fullName": "New Cashier",
	})
	if r.Code != http.StatusCreated && r.Code != http.StatusOK {
		t.Fatalf("ordinary staffing blocked: %d %s", r.Code, r.Body.String())
	}
}

func TestLastOwnerRoleStripRefused(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)
	adminRoleID := itoa64(roleIDByName(t, engine, admin, "Admin"))

	// The seeded Admin role is the only owner-level role WITH active
	// members. Stripping settings.manage from it must be refused (409).
	r := do(t, engine, "PUT", "/api/v1/roles/"+adminRoleID, admin, map[string]any{
		"name":        "Admin",
		"description": "Full access",
		"permissions": []string{"products.manage", "orders.view", "users.manage"}, // drops settings.manage
	})
	if r.Code != http.StatusConflict {
		t.Fatalf("last-owner strip: got %d want 409 (%s)", r.Code, r.Body.String())
	}

	// Adding a second owner FIRST makes the strip legal.
	seed := do(t, engine, "POST", "/api/v1/roles", admin, map[string]any{
		"name":        "Co Owner",
		"permissions": []string{"settings.manage", "users.manage"},
	})
	if seed.Code != http.StatusCreated && seed.Code != http.StatusOK {
		t.Fatalf("seed co-owner role: %d %s", seed.Code, seed.Body.String())
	}
	coID := roleIDByName(t, engine, admin, "Co Owner")
	u := do(t, engine, "POST", "/api/v1/users", admin, map[string]any{
		"username": "coowner", "password": "coowner-pass-1", "roleId": coID, "fullName": "Co Owner",
	})
	if u.Code != http.StatusCreated && u.Code != http.StatusOK {
		t.Fatalf("seed co-owner user: %d %s", u.Code, u.Body.String())
	}
	r = do(t, engine, "PUT", "/api/v1/roles/"+adminRoleID, admin, map[string]any{
		"name":        "Admin",
		"description": "Full access",
		"permissions": []string{"products.manage", "orders.view", "users.manage"},
	})
	if r.Code != http.StatusOK {
		t.Fatalf("strip with second owner: got %d want 200 (%s)", r.Code, r.Body.String())
	}
}

func TestSettingsSecretsNeverEchoAndSurviveSaves(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)

	// Configure a secret.
	r := do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{
		"values": map[string]string{"paystack_secret_key": "sk_live_regression_test"},
	})
	if r.Code != 200 {
		t.Fatalf("save secret: %d %s", r.Code, r.Body.String())
	}

	// GET must return the mask, never the value.
	g := do(t, engine, "GET", "/api/v1/settings", admin, nil)
	if g.Code != 200 {
		t.Fatalf("get settings: %d", g.Code)
	}
	if strings.Contains(g.Body.String(), "sk_live_regression_test") {
		t.Fatal("GET response leaked the secret value")
	}
	if !strings.Contains(g.Body.String(), "__SET__") {
		t.Fatal("GET response should mask the configured secret with __SET__")
	}

	// A save with the field emptied preserves the secret.
	r = do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{
		"values": map[string]string{"paystack_secret_key": "", "store_name": "Secret Survive Shop"},
	})
	if r.Code != 200 {
		t.Fatalf("save with empty secret: %d %s", r.Code, r.Body.String())
	}

	// A masked echo also preserves it.
	r = do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{
		"values": map[string]string{"paystack_secret_key": "__SET__"},
	})
	if r.Code != 200 {
		t.Fatalf("save with mask: %d %s", r.Code, r.Body.String())
	}

	// Server-side proof the secret survived: the payment status endpoint
	// reports "configured" (it reads the real stored value, not the mask).
	pc := do(t, engine, "GET", "/api/v1/payments/config", admin, nil)
	if pc.Code != 200 {
		t.Fatalf("payments config: %d %s", pc.Code, pc.Body.String())
	}
	if !strings.Contains(pc.Body.String(), `"configured":true`) {
		t.Fatalf("secret did not survive the saves; payments config: %s", pc.Body.String())
	}

	// Explicit removal works and is reflected as unconfigured.
	r = do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{
		"values": map[string]string{"paystack_secret_key": "__CLEAR__"},
	})
	if r.Code != 200 {
		t.Fatalf("explicit clear: %d %s", r.Code, r.Body.String())
	}
	g = do(t, engine, "GET", "/api/v1/settings", admin, nil)
	if strings.Contains(g.Body.String(), `"__SET__"`) && strings.Contains(g.Body.String(), "paystack_secret_key") {
		// __SET__ may appear for OTHER configured secrets; re-check precisely.
	}
	pc = do(t, engine, "GET", "/api/v1/payments/config", admin, nil)
	if strings.Contains(pc.Body.String(), `"configured":true`) {
		t.Fatalf("explicit clear did not remove the secret; payments config: %s", pc.Body.String())
	}
}
