package services

// v136_pin_grant_test.go — regression tests for the v1.3.6 mom-simple PIN
// recovery: the owner types the new 4-digit PIN ONCE on their own till,
// the cloud stores only its hash bound to the requesting device, and the
// locked till collects it by itself. No 8-digit code, nothing to type on
// the locked till, no "confirm it's you" challenge anywhere.

import (
	"context"
	"strings"
	"testing"
	"time"

	"posapp/internal/auth"
	"posapp/internal/landelivery"
	"posapp/internal/mdns"
)

func registerPair(t *testing.T) (owner, locked *Service) {
	t.Helper()
	owner = newSyncTestService(t)
	locked = newSyncTestService(t)
	ca, ok := owner.syncConfig()
	if !ok {
		t.Fatal("owner bootstrap failed")
	}
	if err := ca.heartbeat(owner, "test"); err != nil {
		t.Fatalf("owner register: %v", err)
	}
	cl, ok := locked.syncConfig()
	if !ok {
		t.Fatal("locked bootstrap failed")
	}
	if err := cl.heartbeat(locked, "test"); err != nil {
		t.Fatalf("locked register: %v", err)
	}
	return owner, locked
}

// TestApproveWithNewPinSetsDirectGrant: tap Approve with a new PIN and NO
// code is minted — the grant is the whole delivery.
func TestApproveWithNewPinSetsDirectGrant(t *testing.T) {
	withFakeCloud(t)
	owner, locked := registerPair(t)
	seedLocalUser(t, locked, "Salma PC")

	req, err := locked.RequestPinReset("Salma PC", "")
	if err != nil {
		t.Fatalf("file reset request: %v", err)
	}
	out, err := owner.ApprovePinReset(req.RequestID, "8188")
	if err != nil {
		t.Fatalf("approve with new PIN: %v", err)
	}
	if !out.Granted {
		t.Fatal("approve with a PIN must return granted=true")
	}
	if out.Code != "" {
		t.Fatalf("a grant must not mint a code, got %q", out.Code)
	}
	if out.Username != "Salma PC" {
		t.Fatalf("grant username = %q", out.Username)
	}
}

// TestWeakPINGrantRefused: the cloud-side weak-PIN dam must hold (the
// owner mistyping 1234 must not become a worker's PIN).
func TestWeakPINGrantRefused(t *testing.T) {
	withFakeCloud(t)
	owner, locked := registerPair(t)
	seedLocalUser(t, locked, "Salma PC")

	req, err := locked.RequestPinReset("Salma PC", "")
	if err != nil {
		t.Fatalf("file reset request: %v", err)
	}
	if _, err := owner.ApprovePinReset(req.RequestID, "1234"); err == nil {
		t.Fatal("1234 was accepted as a PIN grant")
	}
	if _, err := owner.ApprovePinReset(req.RequestID, "81"); err == nil {
		t.Fatal("a 2-digit PIN was accepted")
	}
}

// TestLockedTillPollsAndAppliesGrant: the end-to-end mom path — owner taps
// Approve with 8188 on their till; the locked till polls, claims, and the
// local account's PIN becomes 8188 with the lockout cleared. The worker
// then just types 8188.
func TestLockedTillPollsAndAppliesGrant(t *testing.T) {
	withFakeCloud(t)
	owner, locked := registerPair(t)
	seedLocalUser(t, locked, "Salma PC")

	req, err := locked.RequestPinReset("Salma PC", "")
	if err != nil {
		t.Fatalf("file reset request: %v", err)
	}
	if _, err := owner.ApprovePinReset(req.RequestID, "8188"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	grant, ok := locked.PollPinGrant()
	if !ok {
		t.Fatal("the locked till does not see the grant")
	}
	if grant.RequestID != req.RequestID || grant.Username != "Salma PC" {
		t.Fatalf("grant = %+v", grant)
	}

	applied, err := locked.ClaimAndApplyPinGrant(grant.RequestID)
	if err != nil {
		t.Fatalf("claim+apply: %v", err)
	}
	if applied.Username != "Salma PC" {
		t.Fatalf("applied user = %q", applied.Username)
	}

	// The local account now carries the new PIN: sign-in must verify.
	var pinHash string
	if err := locked.db.QueryRow(locked.db.Rebind(
		`SELECT pin_hash FROM users WHERE LOWER(username) = LOWER('Salma PC')`)).Scan(&pinHash); err != nil {
		t.Fatalf("read pin_hash: %v", err)
	}
	if !auth.VerifyPassword(pinHash, "8188") {
		t.Fatal("the applied PIN hash does not verify against 8188")
	}
	var lockouts int
	if err := locked.db.QueryRow(`SELECT COUNT(*) FROM users WHERE pin_locked_until != '' OR failed_pin_attempts > 0`).Scan(&lockouts); err != nil {
		t.Fatalf("read lockout: %v", err)
	}
	if lockouts != 0 {
		t.Fatal("the grant did not clear the lockout")
	}
	if _, still := locked.PendingPinReset(); still {
		t.Fatal("pending reset survived the grant")
	}
}

// TestGrantClaimIsSingleUse: a second claim (or a second poll after a
// claim) must not re-serve the grant within the stale-claim window.
func TestGrantClaimIsSingleUse(t *testing.T) {
	withFakeCloud(t)
	owner, locked := registerPair(t)
	seedLocalUser(t, locked, "Salma PC")

	req, _ := locked.RequestPinReset("Salma PC", "")
	if _, err := owner.ApprovePinReset(req.RequestID, "8188"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	grant, ok := locked.PollPinGrant()
	if !ok {
		t.Fatal("grant not visible")
	}
	if _, err := locked.ClaimAndApplyPinGrant(grant.RequestID); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := locked.ClaimAndApplyPinGrant(grant.RequestID); err == nil {
		t.Fatal("the grant was claimed twice")
	}
	if _, still := locked.PollPinGrant(); still {
		t.Fatal("a freshly claimed grant is still being served")
	}
}

// TestGrantBoundToRequestingDevice: a grant minted for one till is
// worthless on another — the claim must be refused.
func TestGrantBoundToRequestingDevice(t *testing.T) {
	withFakeCloud(t)
	owner, locked := registerPair(t)
	stranger := newSyncTestService(t)
	sc, _ := stranger.syncConfig()
	if err := sc.heartbeat(stranger, "test"); err != nil {
		t.Fatalf("stranger register: %v", err)
	}
	seedLocalUser(t, locked, "Salma PC")

	req, _ := locked.RequestPinReset("Salma PC", "")
	if _, err := owner.ApprovePinReset(req.RequestID, "8188"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, ok := stranger.PollPinGrant(); ok {
		t.Fatal("another till can see a grant bound to a different device")
	}
	if _, err := stranger.ClaimAndApplyPinGrant(req.RequestID); err == nil {
		t.Fatal("another till claimed the grant")
	}
}

// TestReApproveOverwritesGrant: the owner taps Approve again (typo fix),
// the fresh PIN wins.
func TestReApproveOverwritesGrant(t *testing.T) {
	withFakeCloud(t)
	owner, locked := registerPair(t)
	seedLocalUser(t, locked, "Salma PC")

	req, _ := locked.RequestPinReset("Salma PC", "")
	if _, err := owner.ApprovePinReset(req.RequestID, "2468"); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	if _, err := owner.ApprovePinReset(req.RequestID, "8188"); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	grant, ok := locked.PollPinGrant()
	if !ok {
		t.Fatal("re-approved grant not visible")
	}
	if _, err := locked.ClaimAndApplyPinGrant(grant.RequestID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	var pinHash string
	locked.db.QueryRow(locked.db.Rebind(`SELECT pin_hash FROM users WHERE LOWER(username) = LOWER('Salma PC')`)).Scan(&pinHash)
	if !auth.VerifyPassword(pinHash, "8188") {
		t.Fatal("the re-approved PIN did not win")
	}
}

// TestLegacyCodePathStillWorks: approving WITHOUT a PIN still mints the
// 8-digit code for older tills (read-aloud fallback stays intact).
func TestLegacyCodePathStillWorks(t *testing.T) {
	withFakeCloud(t)
	owner, locked := registerPair(t)
	req, _ := locked.RequestPinReset("Salma PC", "")
	out, err := owner.ApprovePinReset(req.RequestID, "")
	if err != nil {
		t.Fatalf("legacy approve: %v", err)
	}
	if out.Granted || len(out.Code) != 8 {
		t.Fatalf("legacy approve must mint an 8-digit code, got granted=%v code=%q", out.Granted, out.Code)
	}
}

// TestHeartbeatPublishesLanAddr: the till's LAN delivery address rides the
// heartbeat, so sibling tills can find it even on WiFi that blocks
// multicast (the shop's segmented network).
func TestHeartbeatPublishesLanAddr(t *testing.T) {
	fake := withFakeCloud(t)
	s := newSyncTestService(t)
	s.SetLanAddr("192.168.1.44:8123")
	c, ok := s.syncConfig()
	if !ok {
		t.Fatal("bootstrap failed")
	}
	if err := c.heartbeat(s, "test"); err != nil {
		t.Fatalf("register: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.lanAddrs[c.device] != "192.168.1.44:8123" {
		t.Fatalf("lan_addr = %q, want 192.168.1.44:8123", fake.lanAddrs[c.device])
	}
}

// TestEmptyLanAddrKeepsPublishedAddress: a till that boots without its LAN
// listener must not wipe a previously published address from the roster.
// (Mirrored by the live SQL's COALESCE; here we pin the Go-side refusal.)
func TestEmptyLanAddrKeepsPublishedAddress(t *testing.T) {
	s := newSyncTestService(t)
	s.SetLanAddr("192.168.1.44:8123")
	s.SetLanAddr("") // ignored
	if s.lanAddr != "192.168.1.44:8123" {
		t.Fatalf("empty SetLanAddr wiped the address: %q", s.lanAddr)
	}
}

// TestApproveCarriesTargetLanAddr: the approve response surfaces the
// locked till's cloud-registered LAN address so the WiFi hand-off can go
// straight to it without multicast.
func TestApproveCarriesTargetLanAddr(t *testing.T) {
	withFakeCloud(t)
	owner, locked := registerPair(t)
	seedLocalUser(t, locked, "Salma PC")
	locked.SetLanAddr("192.168.1.55:8124")
	cl, _ := locked.syncConfig()
	if err := cl.heartbeat(locked, "test"); err != nil {
		t.Fatalf("re-register with lan addr: %v", err)
	}
	req, _ := locked.RequestPinReset("Salma PC", "")
	out, err := owner.ApprovePinReset(req.RequestID, "8188")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if out.LANAddr != "192.168.1.55:8124" {
		t.Fatalf("approve lanAddr = %q", out.LANAddr)
	}
	_ = context.Background()
	_ = time.Now
	_ = strings.TrimSpace
}

// TestHasUnfinishedWorkGatesTheOTAInstall: money in flight or selling in
// the last 15 minutes postpones the auto-update restart; a quiet till
// (even with a forgotten open shift) lets it through.
func TestHasUnfinishedWorkGatesTheOTAInstall(t *testing.T) {
	s := newSyncTestService(t)
	s.db.Exec(`INSERT INTO roles (name, permissions) VALUES ('Cashier', '[]')`)
	s.db.Exec(s.db.Rebind(`INSERT INTO users (username, full_name, pin_hash, role_id, is_active) VALUES ('a','A','',1,1)`))
	if s.HasUnfinishedWork() {
		t.Fatal("an empty till looks busy")
	}
	// Money in flight always blocks.
	s.db.Exec(s.db.Rebind(`INSERT INTO orders (number, status, cashier_id) VALUES ('o1','PAID',1)`))
	s.db.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, amount_cents, status) VALUES (1,'cash',100,'PENDING')`))
	if !s.HasUnfinishedWork() {
		t.Fatal("a pending payment must block the update")
	}
	// A forgotten overnight shift does NOT block (durable on disk),
	// once the last order activity is older than the 15-minute window.
	s.db.Exec(s.db.Rebind(`UPDATE payments SET status='COMPLETED' WHERE id=1`))
	s.db.Exec(s.db.Rebind(`UPDATE orders SET created_at = datetime('now', '-2 hours') WHERE number='o1'`))
	s.db.Exec(s.db.Rebind(`INSERT INTO shifts (user_id) VALUES (1)`))
	if s.HasUnfinishedWork() {
		t.Fatal("an open shift must not block the update forever")
	}
	// Selling in the last 15 minutes blocks (someone is mid-sale).
	s.db.Exec(s.db.Rebind(`UPDATE shifts SET closed_at='x' WHERE id=1`))
	s.db.Exec(s.db.Rebind(`INSERT INTO orders (number, status, cashier_id) VALUES ('o2','PENDING',1)`))
	if !s.HasUnfinishedWork() {
		t.Fatal("a fresh order must block the update")
	}
}

// TestGrantApplyMemoryBlocksReapplication: the stale-claim re-serve must
// never re-apply an old owner PIN over the worker's own rotation. Once a
// grant id is applied, the till refuses to apply it again (2nd-round
// review finding).
func TestGrantApplyMemoryBlocksReapplication(t *testing.T) {
	fake := withFakeCloud(t)
	owner, locked := registerPair(t)
	seedLocalUser(t, locked, "Salma PC")

	req, _ := locked.RequestPinReset("Salma PC", "")
	if _, err := owner.ApprovePinReset(req.RequestID, "8188"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	grant, ok := locked.PollPinGrant()
	if !ok {
		t.Fatal("grant not visible")
	}
	if _, err := locked.ClaimAndApplyPinGrant(grant.RequestID); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// Simulate the stale-claim re-serve: the cloud offers the same
	// grant again (claimed >30 min ago).
	fake.mu.Lock()
	row := fake.pinResets[req.RequestID]
	row["status"] = "claimed"
	row["claimed_at"] = time.Now().Add(-45 * time.Minute)
	fake.mu.Unlock()

	if _, err := locked.ClaimAndApplyPinGrant(grant.RequestID); err == nil {
		t.Fatal("an applied grant was applied again")
	}
	var pinHash string
	locked.db.QueryRow(locked.db.Rebind(`SELECT pin_hash FROM users WHERE LOWER(username) = LOWER('Salma PC')`)).Scan(&pinHash)
	if !auth.VerifyPassword(pinHash, "8188") {
		t.Fatal("the worker's PIN changed unexpectedly")
	}
}

// TestPushPinResetCodeRejectsNonIPRegistry: the cloud-registered LAN
// address is parsed strictly - no loopback, no @-trick hostnames.
func TestPushPinResetCodeRejectsNonIPRegistry(t *testing.T) {
	oldBrowse := mdns.Browse
	t.Cleanup(func() { mdns.Browse = oldBrowse })
	mdns.Browse = func(ctx context.Context, d time.Duration) []mdns.Peer { return nil }
	// None of these may produce a delivery attempt (all refused or
	// ignored), and none may panic.
	if landelivery.PushPinResetCode(context.Background(), "dev-x", 1, "12345678", "127.0.0.1:8123") {
		t.Fatal("loopback registry address was used")
	}
	if landelivery.PushPinResetCode(context.Background(), "dev-x", 1, "12345678", "user@evil.com:80") {
		t.Fatal("@-trick address was used")
	}
	if landelivery.PushPinResetCode(context.Background(), "dev-x", 1, "12345678", "not an addr") {
		t.Fatal("garbage address was used")
	}
}
