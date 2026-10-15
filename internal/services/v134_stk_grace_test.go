package services

// v134_stk_grace_test.go — the STK cancellation-timing regression tests.
//
// Field report (v1.3.3): the cashier screen cancelled the M-Pesa prompt
// before the customer could enter their PIN, because the sweeper flipped
// the leg to FAILED the moment a verify said abandoned/failed. The fix is a
// 10-second grace for STK legs: stay pending, re-verify, and if the money
// lands inside the window the sale completes. Popup legs fail immediately,
// unchanged, and the late-payment recovery paths are untouched.

import (
        "bytes"
        "context"
        "encoding/base64"
        "encoding/json"
        "image"
        "image/color"
        "image/png"
        "net/http"
        "net/http/httptest"
        "sync/atomic"
        "testing"
        "time"

        "posapp/internal/auth"
        "posapp/internal/printer"
)

// stkVerifyFake spins a fake cloud whose paystack-verify function answers
// with the current status (flippable mid-test). The service uses the cloud
// proxy path (no local key), mirroring real cloud-connected tills.
type fakeVerify struct {
        status atomic.Value // string
}

func (f *fakeVerify) set(s string) { f.status.Store(s) }
func (f *fakeVerify) get() string  { s, _ := f.status.Load().(string); return s }

func stkVerifyFake(t *testing.T, initial string, amount int64) (*httptest.Server, *fakeVerify) {
        t.Helper()
        fv := &fakeVerify{}
        fv.status.Store(initial)
        mux := http.NewServeMux()
        var self string
        mux.HandleFunc("/rest/v1/sync_bootstrap", func(w http.ResponseWriter, r *http.Request) {
                w.Header().Set("Content-Type", "application/json")
                json.NewEncoder(w).Encode([]map[string]any{{
                        "project_url": self, "team_code": "TEAM-A", "auto_approve": true,
                }})
        })
        mux.HandleFunc("/functions/v1/paystack-verify", func(w http.ResponseWriter, r *http.Request) {
                w.Header().Set("Content-Type", "application/json")
                json.NewEncoder(w).Encode(map[string]any{
                        "ok": true, "status": true, "message": "Verification successful",
                        "data": map[string]any{"status": fv.get(), "amount": amount, "channel": "mobile_money"},
                })
        })
        srv := httptest.NewServer(mux)
        self = srv.URL
        t.Cleanup(srv.Close)
        return srv, fv
}

// seedSTKLegService prepares a cloud-linked test service with a printer
// worker (completePayment enqueues receipts).
func seedSTKLegService(t *testing.T) *Service {
        t.Helper()
        s := newSyncTestService(t)
        s.printer = printer.NewWorker(s.db, s.settings)
        return s
}

// seedSTKLeg inserts a PENDING order + paystack payment row. mode is "stk"
// or "popup". Returns (orderID, paymentID, reference).
func seedSTKLeg(t *testing.T, s *Service, mode, reference string) (int64, int64) {
        t.Helper()
        cashier := seedUser(t, s)
        res, err := s.db.Exec(s.db.Rebind(`INSERT INTO orders (number, status, subtotal_cents, total_cents, cashier_id, client_uuid, created_at)
                VALUES (?, 'PENDING', 100000, 116000, ?, 'stk-uuid-1', ?)`), "ORD-STK-"+reference, cashier, nowStamp())
        if err != nil {
                t.Fatalf("seed order: %v", err)
        }
        orderID, _ := res.LastInsertId()
        res, err = s.db.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, phone, checkout_request_id, created_at)
                VALUES (?, 'paystack', ?, 116000, 'PENDING', '254711000111', ?, ?)`), orderID, mode, reference, nowStamp())
        if err != nil {
                t.Fatalf("seed payment: %v", err)
        }
        payID, _ := res.LastInsertId()
        return orderID, payID
}

func payStatus(t *testing.T, s *Service, payID int64) string {
        t.Helper()
        var st string
        if err := s.db.QueryRow(`SELECT status FROM payments WHERE id = ?`, payID).Scan(&st); err != nil {
                t.Fatalf("payment status: %v", err)
        }
        return st
}

func orderStatus(t *testing.T, s *Service, orderID int64) string {
        t.Helper()
        var st string
        if err := s.db.QueryRow(`SELECT status FROM orders WHERE id = ?`, orderID).Scan(&st); err != nil {
                t.Fatalf("order status: %v", err)
        }
        return st
}

func withCloudProxy(t *testing.T, srv *httptest.Server) {
        t.Helper()
        oldURL := cloudBaseURL
        cloudBaseURL = srv.URL
        t.Cleanup(func() { cloudBaseURL = oldURL })
}

// setFailAt backdates the grace timer (tests construct Service structs
// literally, so the map may be nil here).
func (s *Service) setFailAt(ref string, at time.Time) {
        s.stkFailMu.Lock()
        defer s.stkFailMu.Unlock()
        if s.stkFailAt == nil {
                s.stkFailAt = map[string]time.Time{}
        }
        s.stkFailAt[ref] = at
}

// TestSTKAbandonedStaysPendingThroughGrace: the customer is still mid-PIN
// when Paystack says "abandoned". The leg must stay PENDING across sweeps
// and complete when the money lands inside the grace window.
func TestSTKAbandonedStaysPendingThroughGrace(t *testing.T) {
        srv, fv := stkVerifyFake(t, "abandoned", 116000)
        s := seedSTKLegService(t)
        withCloudProxy(t, srv)
        s.settings.Set("paystack_via_cloud", "true")
        s.settings.Set("paystack_enabled", "true")
        orderID, payID := seedSTKLeg(t, s, "stk", "LP-STK-GRACE-1")

        // First sweep: verify says abandoned. The grace window opens; the leg
        // stays pending so the cashier screen keeps waiting.
        s.SweepPaystack(context.Background())
        if got := payStatus(t, s, payID); got != "PENDING" {
                t.Fatalf("after first abandoned verify: status = %s, want PENDING (grace must keep the leg alive)", got)
        }
        // Second sweep (5s later in production): still within grace.
        s.SweepPaystack(context.Background())
        if got := payStatus(t, s, payID); got != "PENDING" {
                t.Fatalf("after second abandoned verify: status = %s, want PENDING", got)
        }

        // The customer enters their PIN: verify flips to success. The sweep
        // must complete the sale, not just report paid.
        fv.set("success")
        s.SweepPaystack(context.Background())
        if got := payStatus(t, s, payID); got != "COMPLETED" {
                t.Fatalf("after success verify: payment = %s, want COMPLETED", got)
        }
        if got := orderStatus(t, s, orderID); got != "PAID" {
                t.Fatalf("after success verify: order = %s, want PAID", got)
        }
}

// TestSTKFailsOnlyAfterGraceExpires: after the 10-second window, an
// abandoned/failed verify finally fails the leg (the cashier can move on).
func TestSTKFailsOnlyAfterGraceExpires(t *testing.T) {
        srv, _ := stkVerifyFake(t, "abandoned", 116000)
        s := seedSTKLegService(t)
        withCloudProxy(t, srv)
        s.settings.Set("paystack_via_cloud", "true")
        s.settings.Set("paystack_enabled", "true")
        _, payID := seedSTKLeg(t, s, "stk", "LP-STK-GRACE-2")

        // First abandoned verify opens the window...
        s.SweepPaystack(context.Background())
        if got := payStatus(t, s, payID); got != "PENDING" {
                t.Fatalf("within grace: status = %s, want PENDING", got)
        }
        // ...simulate the window having passed...
        s.setFailAt("LP-STK-GRACE-2", time.Now().Add(-STKFailGrace-time.Second))
        // ...and the next verify fails the leg.
        s.SweepPaystack(context.Background())
        if got := payStatus(t, s, payID); got != "FAILED" {
                t.Fatalf("after grace: status = %s, want FAILED", got)
        }
}

// TestPopupAbandonedFailsImmediately: popup checkout legs keep the old
// behavior — no grace, the popup was abandoned.
func TestPopupAbandonedFailsImmediately(t *testing.T) {
        srv, _ := stkVerifyFake(t, "abandoned", 116000)
        s := seedSTKLegService(t)
        withCloudProxy(t, srv)
        s.settings.Set("paystack_via_cloud", "true")
        s.settings.Set("paystack_enabled", "true")
        _, payID := seedSTKLeg(t, s, "popup", "LP-STK-POPUP-1")

        s.SweepPaystack(context.Background())
        if got := payStatus(t, s, payID); got != "FAILED" {
                t.Fatalf("popup leg: status = %s, want FAILED immediately", got)
        }
}

// TestPaystackVerifySTKGraceViaAPI: the explicit verify path (the cashier
// tapping "Check payment status") respects the same grace for STK legs.
func TestPaystackVerifySTKGraceViaAPI(t *testing.T) {
        srv, _ := stkVerifyFake(t, "abandoned", 116000)
        s := seedSTKLegService(t)
        withCloudProxy(t, srv)
        s.settings.Set("paystack_via_cloud", "true")
        s.settings.Set("paystack_enabled", "true")
        orderID, payID := seedSTKLeg(t, s, "stk", "LP-STK-GRACE-3")
        p := &auth.Principal{ID: 1, Username: "admin", Active: true}

        if _, err := s.PaystackVerify(orderID, "LP-STK-GRACE-3", p); err == nil {
                t.Fatal("verify should report not-successful for an abandoned charge")
        }
        if got := payStatus(t, s, payID); got != "PENDING" {
                t.Fatalf("explicit verify within grace: status = %s, want PENDING", got)
        }
        s.setFailAt("LP-STK-GRACE-3", time.Now().Add(-STKFailGrace-time.Second))
        if _, err := s.PaystackVerify(orderID, "LP-STK-GRACE-3", p); err == nil {
                t.Fatal("verify should still report not-successful after grace")
        }
        if got := payStatus(t, s, payID); got != "FAILED" {
                t.Fatalf("explicit verify after grace: status = %s, want FAILED", got)
        }
}

// tinyPNG builds a valid 8x8 PNG data URL for the logo-sync tests.
func tinyPNG(t *testing.T) string {
        t.Helper()
        img := image.NewRGBA(image.Rect(0, 0, 8, 8))
        for x := 0; x < 8; x++ {
                for y := 0; y < 8; y++ {
                        img.Set(x, y, color.RGBA{R: 0x10, G: 0x47, B: 0xab, A: 0xff})
                }
        }
        var buf bytes.Buffer
        if err := png.Encode(&buf, img); err != nil {
                t.Fatalf("png encode: %v", err)
        }
        return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}
