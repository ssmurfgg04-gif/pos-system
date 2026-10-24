package services

// v134_team_sync_test.go — regression tests for the team join flow and
// cross-till sync of settings/branding changes.
//
// Backs the v1.3.4 fixes: the join link carries team/role/token only (no
// username), a fresh till redeems it end-to-end, a saved store name reaches
// the team's event log and other tills, brand logo set/clear propagates,
// and Approve-on-revoked (the Unrevoke path) still reaches the cloud.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func joinLink(team, role, token string) string {
	return "https://awesomeposs.netlify.app/join#t=" + team + "&r=" + role + "&k=" + token
}

// TestTeamJoinLinkRedeemsOnFreshTill: owner mints an invite on till A; a
// fresh till B redeems the FULL frontend-style link; the worker lands in
// the team with a role, a PIN sign-in, and sync configured. The link never
// carries a username, and the token is single-use.
func TestTeamJoinLinkRedeemsOnFreshTill(t *testing.T) {
	fake := newFakeSupabase(t)
	oldURL := cloudBaseURL
	cloudBaseURL = fake.srv.URL
	t.Cleanup(func() { cloudBaseURL = oldURL })

	// Till A: the owner's till, registered + approved on the cloud.
	a := newSyncTestService(t)
	ca, ok := a.syncConfig()
	if !ok {
		t.Fatal("till A bootstrap failed")
	}
	if err := ca.heartbeat(a, "test"); err != nil {
		t.Fatalf("till A register: %v", err)
	}

	inv, err := a.TeamCreateInvite("Cashier", nil, "front counter till")
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	if inv.Token == "" || inv.TeamCode != "TEST-TEAM" {
		t.Fatalf("invite = %+v", inv)
	}
	link := joinLink(inv.TeamCode, inv.RoleName, inv.Token)
	// The join flow must never require or expose the inviter's username:
	// the link carries team, role and token ONLY.
	if strings.Contains(strings.ToLower(link), "user") {
		t.Fatalf("join link leaks a username: %s", link)
	}
	if !strings.Contains(link, "#t=") || !strings.Contains(link, "r=") || !strings.Contains(link, "k="+inv.Token) {
		t.Fatalf("join link shape wrong: %s", link)
	}

	// Till B: a fresh machine, no account, redeeming the pasted link.
	b := newSyncTestService(t)
	info, err := b.TeamJoinWithLink(link, "Brian Kimani", "4729")
	if err != nil {
		t.Fatalf("redeem link: %v", err)
	}
	if info.TeamCode != "TEST-TEAM" || info.StoreName != "Test Store" || info.RoleName != "Cashier" {
		t.Fatalf("join info = %+v", info)
	}
	if info.Username == "" {
		t.Fatal("join did not create a local username")
	}
	var pinSet string
	if err := b.db.QueryRow(b.db.Rebind(`SELECT COALESCE(pin_hash,'') FROM users WHERE username = ?`), info.Username).Scan(&pinSet); err != nil || pinSet == "" {
		t.Fatalf("worker PIN not set (err=%v)", err)
	}
	if b.settings.Get("onboarding_done") != "true" {
		t.Fatal("join must skip onboarding for team tills")
	}
	if b.settings.Get("sync_team_code") != "TEST-TEAM" || !b.settings.GetBool("sync_approved", false) {
		t.Fatal("joined till is not a synced, approved team member")
	}
	var rolePerms string
	if err := b.db.QueryRow(b.db.Rebind(`SELECT COALESCE(r.permissions,'[]') FROM users u JOIN roles r ON r.id = u.role_id WHERE u.username = ?`), info.Username).Scan(&rolePerms); err != nil {
		t.Fatalf("worker role: %v", err)
	}
	if rolePerms == "" || rolePerms == "[]" {
		t.Fatal("worker role carries no permissions")
	}

	// The token is single-use: a second till with the same link is refused.
	c := newSyncTestService(t)
	if _, err := c.TeamJoinWithLink(link, "Second Worker", "1111"); err == nil {
		t.Fatal("a used join link must be refused")
	}
}

// TestSettingsStoreNameSyncsBetweenTills proves the settings-save fix
// end-to-end: till A saves a store name (with runtime bookkeeping keys
// present, the exact shape that used to 400), the change enters the team's
// event log, and till B applies it. Exactly-once: a re-pull applies nothing.
func TestSettingsStoreNameSyncsBetweenTills(t *testing.T) {
	fake := newFakeSupabase(t)
	oldURL := cloudBaseURL
	cloudBaseURL = fake.srv.URL
	t.Cleanup(func() { cloudBaseURL = oldURL })

	a := newSyncTestService(t)
	ca, ok := a.syncConfig()
	if !ok {
		t.Fatal("till A bootstrap failed")
	}
	if err := ca.heartbeat(a, "A"); err != nil {
		t.Fatalf("till A register: %v", err)
	}
	b := newSyncTestService(t)
	cb, ok := b.syncConfig()
	if !ok {
		t.Fatal("till B bootstrap failed")
	}
	if err := cb.heartbeat(b, "B"); err != nil {
		t.Fatalf("till B register: %v", err)
	}

	// The Settings page round-trips the whole snapshot INCLUDING runtime
	// bookkeeping keys (sync_source, device_id, syncstate_*) — this is the
	// exact payload shape that failed with "unknown setting key" before the
	// internal-key fix. The service-level Update is what the handler calls
	// after validation, and the runtime keys must not block the real change.
	a.settings.Set("device_id", "dev-A")
	a.settings.Set("syncstate_last_event_TEST-TEAM", "41")
	changed, err := a.settings.Update(map[string]string{
		"store_name":  "Kimani Whole Sale",
		"sync_source": "cloud", // stale runtime echo from an older frontend
	})
	if err != nil {
		t.Fatalf("save settings: %v", err)
	}
	if a.settings.Get("store_name") != "Kimani Whole Sale" {
		t.Fatalf("store_name = %q", a.settings.Get("store_name"))
	}
	a.EmitConfig(changed)

	pushed, err := ca.push(a)
	if err != nil || pushed < 1 {
		t.Fatalf("push: %d %v", pushed, err)
	}
	applied, err := cb.pull(b)
	if err != nil || applied < 1 {
		t.Fatalf("pull: %d %v", applied, err)
	}
	if got := b.settings.Get("store_name"); got != "Kimani Whole Sale" {
		t.Fatalf("till B store_name = %q, want Kimani Whole Sale", got)
	}
	// Exactly-once: pulling again applies nothing new.
	if applied, err := cb.pull(b); err != nil || applied != 0 {
		t.Fatalf("second pull: %d %v, re-pull must be idempotent", applied, err)
	}
}

// TestLogoSetAndClearSyncBetweenTills: a brand-logo upload on till A
// reaches till B as the file + flag; a clear on A clears B.
func TestLogoSetAndClearSyncBetweenTills(t *testing.T) {
	fake := newFakeSupabase(t)
	oldURL := cloudBaseURL
	cloudBaseURL = fake.srv.URL
	t.Cleanup(func() { cloudBaseURL = oldURL })

	dir := t.TempDir()
	t.Chdir(dir) // brandLogoFile is relative to the process cwd

	a := newSyncTestService(t)
	ca, ok := a.syncConfig()
	if !ok {
		t.Fatal("till A bootstrap failed")
	}
	if err := ca.heartbeat(a, "A"); err != nil {
		t.Fatalf("till A register: %v", err)
	}
	b := newSyncTestService(t)
	cb, ok := b.syncConfig()
	if !ok {
		t.Fatal("till B bootstrap failed")
	}
	if err := cb.heartbeat(b, "B"); err != nil {
		t.Fatalf("till B register: %v", err)
	}

	a.EmitBrandLogo(tinyPNG(t))
	if pushed, err := ca.push(a); err != nil || pushed < 1 {
		t.Fatalf("push logo: %d %v", pushed, err)
	}
	if applied, err := cb.pull(b); err != nil || applied < 1 {
		t.Fatalf("pull logo: %d %v", applied, err)
	}
	if b.settings.Get("brand_logo") != "1" {
		t.Fatal("till B did not set the brand_logo flag")
	}
	if _, err := os.Stat(filepath.Join(dir, brandLogoFile)); err != nil {
		t.Fatalf("logo file not materialised on till B: %v", err)
	}

	// Clear on A propagates as a clear on B.
	a.EmitBrandLogo("")
	if pushed, err := ca.push(a); err != nil || pushed < 1 {
		t.Fatalf("push clear: %d %v", pushed, err)
	}
	if applied, err := cb.pull(b); err != nil || applied < 1 {
		t.Fatalf("pull clear: %d %v", applied, err)
	}
	if b.settings.Get("brand_logo") != "" {
		t.Fatalf("till B brand_logo = %q after a clear, want empty", b.settings.Get("brand_logo"))
	}
}

// TestApproveUnrevokesRevokedDevice pins the Unrevoke path: the roster's
// approve call reaches the cloud for a revoked device (the SQL un-revokes
// + approves in one stroke — v1.3.0 semantics that must never regress).
func TestApproveUnrevokesRevokedDevice(t *testing.T) {
	fake := newFakeSupabase(t)
	oldURL := cloudBaseURL
	cloudBaseURL = fake.srv.URL
	t.Cleanup(func() { cloudBaseURL = oldURL })

	a := newSyncTestService(t)
	ca, ok := a.syncConfig()
	if !ok {
		t.Fatal("bootstrap failed")
	}
	if err := ca.heartbeat(a, "A"); err != nil {
		t.Fatalf("register: %v", err)
	}
	// The roster now includes revoked rows (so a mistaken revoke is visible).
	fake.mu.Lock()
	fake.roster = []map[string]any{
		{"device_id": "dev-a", "device_name": "Till A", "app_version": "1.3.4",
			"last_seen": "2026-10-15T09:00:00Z", "approved": true, "revoked": false, "this_device": true},
		{"device_id": "dev-revoked", "device_name": "DESKTOP-O8A5QMR", "app_version": "1.2.1",
			"last_seen": "2026-10-11T06:19:14Z", "approved": false, "revoked": true, "this_device": false},
	}
	fake.mu.Unlock()

	devs, err := ca.fetchDevicesRPC(a)
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	var sawRevoked bool
	for _, d := range devs {
		if d.DeviceID == "dev-revoked" && d.Revoked {
			sawRevoked = true
		}
	}
	if !sawRevoked {
		t.Fatal("roster must include the revoked device so the owner can un-revoke it")
	}

	if err := a.TeamApproveDevice("dev-revoked"); err != nil {
		t.Fatalf("approve (un-revoke): %v", err)
	}
	fake.mu.Lock()
	calls := append([]string(nil), fake.approveTgt...)
	fake.mu.Unlock()
	if len(calls) != 1 || calls[0] != "dev-revoked" {
		t.Fatalf("sync_approve_device calls = %v, want [dev-revoked]", calls)
	}
}
