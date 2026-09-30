package handlers_test

// recovery_probe_test.go — the late-money recovery paths (v1.1.10).
//
// Field report that motivated these: a KES 10 STK sale confirmed by M-Pesa
// 21 minutes after the push. The 15-minute sweep timeout had already
// failed the leg, so the money landed with nothing left watching and the
// order sat Pending for good. Three guards now close that hole:
//
//   1. SweepFailedPaystack — once a minute, re-verify recently-failed
//      Paystack references on still-PENDING orders; money that landed
//      completes its sale exactly-once.
//   2. RecheckPayment (POST /orders/:id/recheck) — the cashier's
//      "Check payment status" button: verifies pending + recent failed
//      refs, completes on success, returns a human message otherwise.
//   3. Both refuse to double-count: an order already PAID (e.g. via
//      manual receipt entry) is never re-completed by a late STK verify —
//      the manual code and the late charge are the SAME money.

import (
        "encoding/json"
        "net/http"
        "net/http/httptest"
        "strings"
        "testing"
        "time"

        "posapp/internal/models"
        "posapp/internal/paystack"
        "posapp/internal/services"
)

// fakePaystack stands in for api.paystack.co: /transaction/verify/:ref
// answers with the status configured per reference.
type fakePaystack struct {
        srv    *httptest.Server
        status map[string]string // ref → success | failed | abandoned | pending
        calls  map[string]int
}

func newFakePaystack(t *testing.T) *fakePaystack {
        t.Helper()
        f := &fakePaystack{status: map[string]string{}, calls: map[string]int{}}
        mux := http.NewServeMux()
        mux.HandleFunc("/transaction/verify/", func(w http.ResponseWriter, r *http.Request) {
                ref := strings.TrimPrefix(r.URL.Path, "/transaction/verify/")
                f.calls[ref]++
                st, ok := f.status[ref]
                if !ok {
                        st = "abandoned"
                }
                amount := 100000 // KES 1000.00 in subunits — matches the seeded checkout
                if st == "success" {
                        amount = 100000
                }
                w.Header().Set("Content-Type", "application/json")
                _ = json.NewEncoder(w).Encode(map[string]any{
                        "status":  true,
                        "message": "Verification successful",
                        "data": map[string]any{
                                "reference":        ref,
                                "status":           st,
                                "amount":           amount,
                                "currency":         "KES",
                                "channel":          "mobile_money",
                                "gateway_response": "Successful",
                        },
                })
        })
        f.srv = httptest.NewServer(mux)
        paystack.SetAPIBase(f.srv.URL)
        t.Cleanup(func() {
                paystack.SetAPIBase("https://api.paystack.co")
                f.srv.Close()
        })
        return f
}

func recoveryOrder(t *testing.T, r *agent1Rig, ref string) *models.Order {
        t.Helper()
        o := agent1Checkout(t, r, models.CheckoutRequest{PaymentMethod: models.MethodPaystack})
        agent1AttachRef(t, r, o.ID, ref)
        o, _ = r.Svc.GetOrder(o.ID)
        return o
}

func recoveryOrderStatus(t *testing.T, r *agent1Rig, orderID int64) (string, string) {
        t.Helper()
        o, err := r.Svc.GetOrder(orderID)
        if err != nil {
                t.Fatalf("get order: %v", err)
        }
        pay := o.Payments[len(o.Payments)-1]
        return o.Status, pay.Status
}

// The field bug, replayed: sweep times the leg out (15 min, final verify
// pending), the customer pays anyway, the deep sweep recovers it.
func TestSweepFailedPaystackRecoversLatePayment(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        f := newFakePaystack(t)
        o := recoveryOrder(t, r, "LP-ORD-REC1-ab12")

        // Age the leg past the 15-minute sweep timeout (the customer is still
        // digging out their phone), then let the sweep's final verify give up.
        backdate(t, r, o.ID, "LP-ORD-REC1-ab12", 21*time.Minute)
        f.status["LP-ORD-REC1-ab12"] = "pending"
        r.Svc.SweepPaystack(t.Context())
        if st, paySt := recoveryOrderStatus(t, r, o.ID); st != models.OrderPending || paySt != models.PaymentFailed {
                t.Fatalf("pre: expected timed-out leg on pending order, got order=%s pay=%s", st, paySt)
        }

        // Minute 21: the customer's PIN lands. The deep sweep must complete.
        f.status["LP-ORD-REC1-ab12"] = "success"
        r.Svc.SweepFailedPaystack(t.Context())
        if st, paySt := recoveryOrderStatus(t, r, o.ID); st != models.OrderPaid || paySt != models.PaymentCompleted {
                t.Fatalf("recovery failed: order=%s pay=%s (want PAID/COMPLETED)", st, paySt)
        }
}

// backdate rewinds a payment leg's created_at — stands in for waiting out
// the real 15-minute timeout the way the field sale did.
func backdate(t *testing.T, r *agent1Rig, orderID int64, ref string, age time.Duration) {
        t.Helper()
        st := time.Now().UTC().Add(-age).Format(time.RFC3339)
        if _, err := r.DB.Exec(`UPDATE payments SET created_at = ? WHERE order_id = ? AND checkout_request_id = ?`, st, orderID, ref); err != nil {
                t.Fatalf("backdate: %v", err)
        }
}

// Money double-counting guard: an order paid via manual receipt entry must
// never be re-completed by a late verify of the same physical payment.
func TestSweepFailedPaystackSkipsPaidOrders(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        f := newFakePaystack(t)
        p := agent1Principal()
        o := recoveryOrder(t, r, "LP-ORD-REC2-cd34")

        // The leg times out, then the cashier keys the M-Pesa code manually.
        f.status["LP-ORD-REC2-cd34"] = "pending"
        r.Svc.SweepPaystack(t.Context())
        f.status["LP-ORD-REC2-cd34"] = "success"
        if _, err := r.Svc.ManualConfirm(o.ID, "UIUQB84HQS", p); err != nil {
                t.Fatalf("manual confirm: %v", err)
        }
        if st, _ := recoveryOrderStatus(t, r, o.ID); st != models.OrderPaid {
                t.Fatalf("pre: order should be PAID via manual entry, got %s", st)
        }

        // The late verify must be ignored: same money, already recorded.
        r.Svc.SweepFailedPaystack(t.Context())
        var completed int
        if err := r.DB.QueryRow(`SELECT COUNT(*) FROM payments WHERE order_id = ? AND status = 'COMPLETED'`, o.ID).Scan(&completed); err != nil {
                t.Fatalf("count: %v", err)
        }
        if completed != 1 {
                t.Fatalf("double completion: %d completed payment legs on a paid order (want 1)", completed)
        }
}

// The cashier's button: verifies pending + recent failed refs, completes on
// success, and answers idempotently on a paid order.
func TestRecheckPaymentCompletesAndStaysIdempotent(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        f := newFakePaystack(t)
        p := agent1Principal()
        o := recoveryOrder(t, r, "LP-ORD-REC3-ef56")

        // No money yet — must return a message, leave the order untouched.
        f.status["LP-ORD-REC3-ef56"] = "pending"
        got, msg, err := r.Svc.RecheckPayment(o.ID, p)
        if err != nil {
                t.Fatalf("recheck: %v", err)
        }
        if got.Status != models.OrderPending || msg == "" {
                t.Fatalf("expected pending order + reason, got order=%s msg=%q", got.Status, msg)
        }

        // The customer pays — one tap completes the sale.
        f.status["LP-ORD-REC3-ef56"] = "success"
        got, _, err = r.Svc.RecheckPayment(o.ID, p)
        if err != nil {
                t.Fatalf("recheck 2: %v", err)
        }
        if got.Status != models.OrderPaid {
                t.Fatalf("recheck should complete a paid charge, got %s", got.Status)
        }

        // Second tap on a paid order: idempotent, no new completions.
        if _, _, err := r.Svc.RecheckPayment(o.ID, p); err != nil {
                t.Fatalf("recheck 3: %v", err)
        }
        var completed int
        if err := r.DB.QueryRow(`SELECT COUNT(*) FROM payments WHERE order_id = ? AND status = 'COMPLETED'`, o.ID).Scan(&completed); err != nil {
                t.Fatalf("count: %v", err)
        }
        if completed != 1 {
                t.Fatalf("idempotency broken: %d completed legs (want 1)", completed)
        }
}

// A superseded push the customer still answered is recovered by the recheck
// (the live ref is abandoned; the money is on the failed one).
func TestRecheckPaymentRecoversSupersededPush(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        f := newFakePaystack(t)
        p := agent1Principal()
        o := recoveryOrder(t, r, "LP-ORD-REC4-gh78")

        // Cashier retried: old leg failed 'superseded', a fresh pending leg is minted.
        if _, err := r.Svc.RetrySTKWithPhone(t.Context(), o.ID, "0712345678", p); err == nil {
                t.Log("retry pushed a fresh STK via the fake API")
        }
        // RetrySTKWithPhone re-tags legs through InitiateSTK; normalize legs:
        // the failed original keeps the reference we verify; force statuses the
        // way the field left them.
        f.status["LP-ORD-REC4-gh78"] = "success"
        if _, err := r.DB.Exec(`UPDATE payments SET status = 'FAILED', result_desc = 'superseded by new STK push'
                WHERE order_id = ? AND checkout_request_id = 'LP-ORD-REC4-gh78'`, o.ID); err != nil {
                t.Fatalf("fail old leg: %v", err)
        }

        got, _, err := r.Svc.RecheckPayment(o.ID, p)
        if err != nil {
                t.Fatalf("recheck superseded: %v", err)
        }
        if got.Status != models.OrderPaid {
                t.Fatalf("money on the superseded push must complete the sale, got %s", got.Status)
        }
}

// The fake provider must actually be consulted by the sweeper (guard
// against a silent no-op refactor) and "abandoned" must stay abandoned.
func TestSweepPaystackBaselineStillWorks(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        f := newFakePaystack(t)
        o := recoveryOrder(t, r, "LP-ORD-REC5-ij90")

        f.status["LP-ORD-REC5-ij90"] = "success"
        r.Svc.SweepPaystack(t.Context())
        if st, _ := recoveryOrderStatus(t, r, o.ID); st != models.OrderPaid {
                t.Fatalf("baseline sweep completion broke: order=%s", st)
        }

        // And abandoned stays un-completed (FAILED leg, PENDING order — the
        // cashier can re-open checkout or the customer can still pay).
        o2 := recoveryOrder(t, r, "LP-ORD-REC6-kl01")
        f.status["LP-ORD-REC6-kl01"] = "abandoned"
        r.Svc.SweepPaystack(t.Context())
        if st, _ := recoveryOrderStatus(t, r, o2.ID); st != models.OrderPending {
                t.Fatalf("abandoned must not complete the order: order=%s", st)
        }
}

var _ = services.ErrNotConfigured // keep import stable if assertions change
