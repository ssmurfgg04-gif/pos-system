package handlers_test

// users_password_guard_test.go — regression lock for the default-password
// rotation hole (backend scan 3-c, finding 1): SetPassword is the ONE
// password entry point that never ran auth.IsDefaultPassword, so the
// seeded admin/admin123 account could "rotate" straight back to admin123
// and clear must_rotate — a publicly-known password holding full admin.

import (
	"testing"

	"github.com/gin-gonic/gin"
)

func setPasswordCode(t *testing.T, engine *gin.Engine, token string, id int64, password string) int {
	t.Helper()
	w := do(t, engine, "PUT", "/api/v1/users/"+itoa64(id)+"/password", token,
		map[string]any{"password": password})
	return w.Code
}

func TestSetPasswordRejectsDefaultPassword(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)

	// Resolve the seeded admin's own id.
	me := do(t, engine, "GET", "/api/v1/me", admin, nil)
	if me.Code != 200 {
		t.Fatalf("me: %d", me.Code)
	}
	aid := int64(dataMap(t, me)["id"].(float64))

	// Rotating BACK into the public default list must be rejected —
	// otherwise must_rotate clears and admin123 holds full admin again.
	if c := setPasswordCode(t, engine, admin, aid, "admin123"); c != 400 {
		t.Fatalf("rotating to default password must 400, got %d", c)
	}
	if c := setPasswordCode(t, engine, admin, aid, "cashier123"); c != 400 {
		t.Fatalf("rotating to a seeded default (another user's) must 400, got %d", c)
	}

	// Control: a genuinely fresh password still rotates fine.
	if c := setPasswordCode(t, engine, admin, aid, "genuinely-fresh-9"); c != 200 {
		t.Fatalf("fresh password rotate must 200, got %d", c)
	}
	// The rotation cleared must_rotate: log back in with the new password.
	if tok := login(t, engine, "admin", "genuinely-fresh-9"); tok == "" {
		t.Fatal("re-login with new password failed")
	}
}
