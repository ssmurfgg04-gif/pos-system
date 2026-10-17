package services

// v135_pinreset_lan_test.go — regression tests for the v1.3.5 PIN recovery
// fixes: Approve is ONE tap (the owner-capability challenge is gone from
// the approve path), the cloud returns the requesting device id, and the
// owner till hands the code straight to the locked till over the shop WiFi
// so the worker only types their new PIN. The manual read-aloud fallback
// and the single-use cloud burn are pinned too.

import (
        "context"
        "testing"
        "time"

        "posapp/internal/landelivery"
        "posapp/internal/mdns"
)

func withFakeCloud(t *testing.T) *fakeSupabase {
        t.Helper()
        fake := newFakeSupabase(t)
        oldURL := cloudBaseURL
        cloudBaseURL = fake.srv.URL
        t.Cleanup(func() { cloudBaseURL = oldURL })
        return fake
}

// TestApproveNeedsNoOwnerCapabilityChallenge: the exact complaint from the
// shop — tap Approve on the admin till and the code must appear. The mint
// must succeed with an EMPTY capability cache (the old flow interrupted
// with a password dialog that could never succeed on real deployments).
func TestApproveNeedsNoOwnerCapabilityChallenge(t *testing.T) {
        withFakeCloud(t)
        oldCap := MarshalOwnerCapabilityState()
        if oldCap != nil {
                t.Fatalf("capability cache should start empty, got %v", oldCap)
        }
        owner := newSyncTestService(t)
        locked := newSyncTestService(t)
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

        req, err := locked.RequestPinReset("Salma PC", "")
        if err != nil {
                t.Fatalf("file reset request: %v", err)
        }

        out, err := owner.ApprovePinReset(req.RequestID, "")
        if err != nil {
                t.Fatalf("approve without capability: %v", err)
        }
        if len(out.Code) != 8 {
                t.Fatalf("code = %q", out.Code)
        }
        if out.DeviceID == "" || out.DeviceID == ca.device {
                t.Fatalf("approve must return the REQUESTING device id, got %q", out.DeviceID)
        }
        if MarshalOwnerCapabilityState() != nil {
                t.Fatal("approve must not mint or require an owner capability")
        }
}

// seedLocalUser inserts an active account on a test till so the redeem
// step can find the requester by name (mirrors a real till's users table).
func seedLocalUser(t *testing.T, s *Service, username string) {
        t.Helper()
        if _, err := s.db.Exec(`INSERT INTO roles (name, permissions) VALUES ('Cashier', '[]')`); err != nil {
                t.Fatalf("seed role: %v", err)
        }
        if _, err := s.db.Exec(s.db.Rebind(`INSERT INTO users (username, full_name, pin_hash, role_id, is_active) VALUES (?, ?, '', 1, 1)`), username, username); err != nil {
                t.Fatalf("seed user: %v", err)
        }
}

// TestApproveDeliversCodeOverLAN: the locked till runs its LAN listener,
// the owner till discovers it and the code arrives without anyone reading
// digits aloud. The locked till's PIN screen then shows it as a pre-fill.
func TestApproveDeliversCodeOverLAN(t *testing.T) {
        withFakeCloud(t)
        owner := newSyncTestService(t)
        locked := newSyncTestService(t)
        ca, _ := owner.syncConfig()
        if err := ca.heartbeat(owner, "test"); err != nil {
                t.Fatalf("owner register: %v", err)
        }
        cl, _ := locked.syncConfig()
        if err := cl.heartbeat(locked, "test"); err != nil {
                t.Fatalf("locked register: %v", err)
        }
        seedLocalUser(t, locked, "Salma PC")

        req, err := locked.RequestPinReset("Salma PC", "")
        if err != nil {
                t.Fatalf("file reset request: %v", err)
        }
        if locked.PendingPinResetRequestID() != req.RequestID {
                t.Fatalf("pending id = %d, want %d", locked.PendingPinResetRequestID(), req.RequestID)
        }

        // The locked till's real LAN listener.
        srv, err := landelivery.Start(context.Background(), 36000,
                func() landelivery.Identity {
                        devID, team, ok := locked.PublicSyncIdentity()
                        if !ok {
                                t.Error("locked till lost its sync identity")
                        }
                        return landelivery.Identity{DeviceID: devID, Team: team, DeviceName: locked.DeviceName(), AppVersion: "test"}
                },
                locked.PendingPinResetRequestID,
                locked.StorePushedPinResetCode,
        )
        if err != nil {
                t.Fatalf("locked till LAN listener: %v", err)
        }

        // The LAN scan sees it (plus a decoy first, order must not matter).
        oldBrowse := mdns.Browse
        t.Cleanup(func() { mdns.Browse = oldBrowse })
        mdns.Browse = func(ctx context.Context, d time.Duration) []mdns.Peer {
                return []mdns.Peer{{Addr: "127.0.0.1", Port: srv.Port}}
        }

        out, err := owner.ApprovePinReset(req.RequestID, "")
        if err != nil {
                t.Fatalf("approve: %v", err)
        }
        if !out.Delivered {
                t.Fatal("code was not delivered over the LAN")
        }
        pending, ok := locked.PendingPinReset()
        if !ok || pending.PushedCode != out.Code {
                t.Fatalf("locked till pushedCode=%q, want %q", pending.PushedCode, out.Code)
        }

        // Redeem with the delivered code burns it at the cloud and clears the
        // pending state (the pushed pre-fill included).
        red, err := locked.RedeemPinReset(req.RequestID, out.Code, "8188")
        if err != nil {
                t.Fatalf("redeem delivered code: %v", err)
        }
        if red.Username != "Salma PC" {
                t.Fatalf("redeemed user = %q", red.Username)
        }
        if _, still := locked.PendingPinReset(); still {
                t.Fatal("pending reset survived redemption")
        }

        // The cloud burned the code: redeeming again fails even with the same
        // digits typed.
        if _, err := locked.RequestPinReset("Salma PC", ""); err != nil {
                t.Fatalf("re-request: %v", err)
        }
        req2 := locked.PendingPinResetRequestID()
        if _, err := locked.RedeemPinReset(req2, out.Code, "8188"); err == nil {
                t.Fatal("a burned code redeemed twice")
        }
}

// TestApproveWithoutLANStillShowsCode: WiFi is a bonus, never a dependency.
// When no sibling answers, the approve still returns the code for the owner
// to read aloud.
func TestApproveWithoutLANStillShowsCode(t *testing.T) {
        withFakeCloud(t)
        owner := newSyncTestService(t)
        locked := newSyncTestService(t)
        ca, _ := owner.syncConfig()
        if err := ca.heartbeat(owner, "test"); err != nil {
                t.Fatalf("owner register: %v", err)
        }
        cl, _ := locked.syncConfig()
        if err := cl.heartbeat(locked, "test"); err != nil {
                t.Fatalf("locked register: %v", err)
        }

        req, err := locked.RequestPinReset("Salma PC", "")
        if err != nil {
                t.Fatalf("file reset request: %v", err)
        }

        oldBrowse := mdns.Browse
        t.Cleanup(func() { mdns.Browse = oldBrowse })
        mdns.Browse = func(ctx context.Context, d time.Duration) []mdns.Peer { return nil }

        out, err := owner.ApprovePinReset(req.RequestID, "")
        if err != nil {
                t.Fatalf("approve: %v", err)
        }
        if out.Delivered {
                t.Fatal("delivery reported success with no LAN peers")
        }
        if len(out.Code) != 8 {
                t.Fatalf("fallback code = %q", out.Code)
        }
}

// TestStorePushedCodeRejectsForeignRequests: the LAN listener can never
// plant a code without an active, matching request on the till.
func TestStorePushedCodeRejectsForeignRequests(t *testing.T) {
        withFakeCloud(t)
        locked := newSyncTestService(t)
        cl, _ := locked.syncConfig()
        if err := cl.heartbeat(locked, "test"); err != nil {
                t.Fatalf("locked register: %v", err)
        }

        if err := locked.StorePushedPinResetCode(99, "12345678"); err == nil {
                t.Fatal("stored a code with no pending request")
        }
        req, err := locked.RequestPinReset("Salma PC", "")
        if err != nil {
                t.Fatalf("file reset request: %v", err)
        }
        if err := locked.StorePushedPinResetCode(req.RequestID+1, "12345678"); err == nil {
                t.Fatal("stored a code for a foreign request id")
        }
        if err := locked.StorePushedPinResetCode(req.RequestID, "87654321"); err != nil {
                t.Fatalf("matching push refused: %v", err)
        }
        if pending, _ := locked.PendingPinReset(); pending.PushedCode != "87654321" {
                t.Fatalf("pushedCode = %q", pending.PushedCode)
        }
}
