package handlers_test

// agent1_paystack_probe_test.go — Task T1 (payments / money-flow) probe.
//
// Goal: try to break the Paystack stack WITHOUT any network access. The
// Paystack client hardcodes https://api.paystack.co, so the API-calling
// paths (PaystackInit success leg, SweepPaystack verify leg, PaystackVerify
// past the DB lookup) are exercised here only up to their pre-network
// guards. Everything after a signature check IS testable offline, and that
// is where the money lives:
//
//   - HandlePaystackWebhook end-to-end (signature → parse → lookup →
//     void-guard → completePayment): completion, exactly-once replay,
//     unknown refs, non-success events, wrong-method rows, voided orders,
//     amount-mismatch flagging.
//   - The fake-money guard: a shop with no Paystack keys must never move
//     to PAID by itself (checkout stays PENDING, sweeper is a no-op).
//   - PaymentConfig configured/enabled flags reacting to settings-store
//     writes (the cloud-config vault path) and to PAYSTACK_SECRET_KEY.
//   - Split tender (cash + paystack leg) completion via webhook.
//
// If a test here fails after a refactor, read the comment above it: most
// encode deliberate money-safety invariants, and two encode CURRENT
// (known-bug) behavior — those are marked KNOWN GAP.

import (
        "context"
        "crypto/hmac"
        "crypto/sha512"
        "encoding/hex"
        "encoding/json"
        "net/http"
        "net/http/httptest"
        "path/filepath"
        "strings"
        "testing"

        "github.com/gin-gonic/gin"

        "posapp/internal/auth"
        "posapp/internal/database"
        "posapp/internal/handlers"
        "posapp/internal/models"
        "posapp/internal/printer"
        "posapp/internal/router"
        "posapp/internal/services"
        "posapp/internal/settings"
        "posapp/internal/tenants"
        "posapp/internal/ws"
)

// agent1TestSecret is a structurally-valid test secret (sk_ + >20 chars).
// It never leaves the process; the client is only used for HMAC checks.
const agent1TestSecret = "sk_test_agent1probe0000000001"

const agent1TestPublic = "pk_test_agent1probe0000000001"

// ---- rig helpers -------------------------------------------------------

type agent1Rig struct {
        Svc *services.Service
        DB  *database.DB
        ST  *settings.Store
}

// newAgent1Rig builds a bare service (no HTTP) on a fresh temp DB.
// paystackKey "" means no Paystack configured.
func newAgent1Rig(t *testing.T, paystackKey string) *agent1Rig {
        t.Helper()
        dir := t.TempDir()
        db, err := database.Open("sqlite", filepath.Join(dir, "probe.db"), "")
        if err != nil {
                t.Fatalf("db: %v", err)
        }
        t.Cleanup(func() { _ = db.Close() })
        if err := db.Migrate(); err != nil {
                t.Fatalf("migrate: %v", err)
        }
        if err := db.Seed(true); err != nil {
                t.Fatalf("seed: %v", err)
        }
        st, err := settings.New(db)
        if err != nil {
                t.Fatalf("settings: %v", err)
        }
        if paystackKey != "" {
                if err := st.Set("paystack_secret_key", paystackKey); err != nil {
                        t.Fatalf("set key: %v", err)
                }
        }
        pw := printer.NewWorker(db, st)
        svc := services.New(db, st, nil, pw)
        return &agent1Rig{Svc: svc, DB: db, ST: st}
}

func agent1Principal() *auth.Principal {
        return &auth.Principal{
                ID:       1,
                Username: "agent1",
                Permissions: map[string]bool{
                        "pos.sell": true, "pos.void": true, "payments.manual": true,
                        "payments.override_price": true, "payments.apply_discount": true,
                        "loyalty.redeem": true,
                },
        }
}

// agent1ProductID resolves the first seeded product by SKU.
func agent1ProductID(t *testing.T, r *agent1Rig) int64 {
        t.Helper()
        var id int64
        if err := r.DB.QueryRow(`SELECT id FROM products WHERE sku = 'TS-001'`).Scan(&id); err != nil {
                t.Fatalf("seed product: %v", err)
        }
        return id
}

func agent1Stock(t *testing.T, r *agent1Rig, productID int64) int {
        t.Helper()
        var n int
        if err := r.DB.QueryRow(`SELECT stock_qty FROM products WHERE id = ?`, productID).Scan(&n); err != nil {
                t.Fatalf("stock: %v", err)
        }
        return n
}

func agent1Checkout(t *testing.T, r *agent1Rig, req models.CheckoutRequest) *models.Order {
        t.Helper()
        if req.PaymentMethod == "" {
                req.PaymentMethod = models.MethodPaystack
        }
        if len(req.Items) == 0 {
                req.Items = []models.CheckoutItem{{ProductID: agent1ProductID(t, r), Qty: 2}}
        }
        o, err := r.Svc.Checkout(context.Background(), agent1Principal(), req)
        if err != nil {
                t.Fatalf("checkout: %v", err)
        }
        return o
}

// agent1AttachRef points the pending paystack payment at a reference
// (stands in for the PaystackInit UPDATE, which needs the real API).
func agent1AttachRef(t *testing.T, r *agent1Rig, orderID int64, ref string) {
        t.Helper()
        res, err := r.DB.Exec(r.DB.Rebind(`UPDATE payments SET checkout_request_id = ?
                WHERE order_id = ? AND method = 'paystack' AND status = 'PENDING'`), ref, orderID)
        if err != nil {
                t.Fatalf("attach ref: %v", err)
        }
        if n, _ := res.RowsAffected(); n != 1 {
                t.Fatalf("attach ref: expected 1 pending paystack payment, got %d", n)
        }
}

func agent1AttachAnyRef(t *testing.T, r *agent1Rig, orderID int64, ref, method string) {
        t.Helper()
        res, err := r.DB.Exec(r.DB.Rebind(`UPDATE payments SET checkout_request_id = ?
                WHERE order_id = ? AND method = ? AND status = 'PENDING'`), ref, orderID, method)
        if err != nil {
                t.Fatalf("attach ref: %v", err)
        }
        if n, _ := res.RowsAffected(); n != 1 {
                t.Fatalf("attach ref (%s): expected 1 pending payment, got %d", method, n)
        }
}

type agent1PayRow struct {
        ID       int64
        Method   string
        Status   string
        Amount   int64
        Receipt  string
        Desc     string
        Checkout string
}

func agent1Payments(t *testing.T, r *agent1Rig, orderID int64) []agent1PayRow {
        t.Helper()
        rows, err := r.DB.Query(`SELECT id, method, status, amount_cents,
                COALESCE(mpesa_receipt,''), COALESCE(result_desc,''), COALESCE(checkout_request_id,'')
                FROM payments WHERE order_id = ? ORDER BY id`, orderID)
        if err != nil {
                t.Fatalf("payments: %v", err)
        }
        defer rows.Close()
        var out []agent1PayRow
        for rows.Next() {
                var p agent1PayRow
                if err := rows.Scan(&p.ID, &p.Method, &p.Status, &p.Amount, &p.Receipt, &p.Desc, &p.Checkout); err != nil {
                        t.Fatalf("scan: %v", err)
                }
                out = append(out, p)
        }
        if err := rows.Err(); err != nil {
                t.Fatalf("rows: %v", err)
        }
        return out
}

func agent1OrderStatus(t *testing.T, r *agent1Rig, orderID int64) (string, bool) {
        t.Helper()
        var status string
        var disc int
        if err := r.DB.QueryRow(`SELECT status, COALESCE(discrepancy,0) FROM orders WHERE id = ?`, orderID).Scan(&status, &disc); err != nil {
                t.Fatalf("order: %v", err)
        }
        return status, disc == 1
}

func agent1Sign(t *testing.T, secret string, body []byte) string {
        t.Helper()
        mac := hmac.New(sha512.New, []byte(secret))
        mac.Write(body)
        return hex.EncodeToString(mac.Sum(nil))
}

func agent1ChargeBody(event, ref, status string, amount int64) []byte {
        b, _ := json.Marshal(map[string]any{
                "event": event,
                "data": map[string]any{
                        "reference": ref, "status": status, "amount": amount,
                        "currency": "KES", "channel": "card",
                },
        })
        return b
}

// ---- service-level webhook tests ---------------------------------------

// INVARIANT: a signed charge.success for a known pending paystack
// reference is the ONLY thing that turns a paystack order PAID offline,
// and it must deduct stock exactly once and stamp the reference as the
// payment receipt.
func TestAgent1WebhookCompletesPendingPaystackOrder(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        pid := agent1ProductID(t, r)
        o := agent1Checkout(t, r, models.CheckoutRequest{})
        ref := "LP-AGENT1-COMPLETE-1"
        agent1AttachRef(t, r, o.ID, ref)

        if before := agent1Stock(t, r, pid); before != 40 {
                t.Fatalf("pre-completion stock must be untouched (fake-money guard): %d", before)
        }
        body := agent1ChargeBody("charge.success", ref, "success", o.TotalCents)
        if err := r.Svc.HandlePaystackWebhook(body, agent1Sign(t, agent1TestSecret, body)); err != nil {
                t.Fatalf("webhook: %v", err)
        }
        status, disc := agent1OrderStatus(t, r, o.ID)
        if status != models.OrderPaid || disc {
                t.Fatalf("order must be PAID without discrepancy, got %s disc=%v", status, disc)
        }
        if after := agent1Stock(t, r, pid); after != 38 {
                t.Fatalf("stock must deduct exactly 2, got %d", after)
        }
        pays := agent1Payments(t, r, o.ID)
        if len(pays) != 1 || pays[0].Status != models.PaymentCompleted ||
                pays[0].Receipt != ref || pays[0].Method != models.MethodPaystack {
                t.Fatalf("payment must complete with ref as receipt: %+v", pays)
        }
}

// INVARIANT: webhook redelivery (Paystack retries on timeout, operators
// re-send, sweeper races the webhook) must be exactly-once: no double
// stock deduction, no duplicate completion side effects, and no error.
func TestAgent1WebhookReplayIsExactlyOnce(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        pid := agent1ProductID(t, r)
        o := agent1Checkout(t, r, models.CheckoutRequest{})
        ref := "LP-AGENT1-REPLAY-1"
        agent1AttachRef(t, r, o.ID, ref)
        body := agent1ChargeBody("charge.success", ref, "success", o.TotalCents)
        sig := agent1Sign(t, agent1TestSecret, body)
        for i := 0; i < 3; i++ {
                if err := r.Svc.HandlePaystackWebhook(body, sig); err != nil {
                        t.Fatalf("replay %d: %v", i, err)
                }
        }
        if after := agent1Stock(t, r, pid); after != 38 {
                t.Fatalf("stock must deduct once across replays, got %d", after)
        }
        pays := agent1Payments(t, r, o.ID)
        if len(pays) != 1 || pays[0].Status != models.PaymentCompleted {
                t.Fatalf("exactly one completed payment expected: %+v", pays)
        }
}

// INVARIANT: an unsigned / wrongly-signed webhook must fail closed and
// change nothing — this is the only thing standing between the public
// endpoint and free money.
func TestAgent1WebhookRejectsBadSignature(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        pid := agent1ProductID(t, r)
        o := agent1Checkout(t, r, models.CheckoutRequest{})
        ref := "LP-AGENT1-BADSIG-1"
        agent1AttachRef(t, r, o.ID, ref)
        body := agent1ChargeBody("charge.success", ref, "success", o.TotalCents)

        cases := map[string]string{
                "wrong secret": agent1Sign(t, "sk_test_attacker_key_000000000", body),
                "empty":        "",
                "garbage":      "zz-not-hex",
                "truncated":    agent1Sign(t, agent1TestSecret, body)[:40],
        }
        for name, sig := range cases {
                if err := r.Svc.HandlePaystackWebhook(body, sig); err == nil {
                        t.Fatalf("%s: must be rejected", name)
                }
        }
        if status, _ := agent1OrderStatus(t, r, o.ID); status != models.OrderPending {
                t.Fatalf("order must stay PENDING, got %s", status)
        }
        if n := agent1Stock(t, r, pid); n != 40 {
                t.Fatalf("stock must be untouched, got %d", n)
        }
}

// INVARIANT: only a definitive, signature-checked charge.success moves
// money. Anything else is acknowledged and ignored.
func TestAgent1WebhookIgnoresNonSuccessAndUnknownRefs(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        pid := agent1ProductID(t, r)
        o := agent1Checkout(t, r, models.CheckoutRequest{})
        ref := "LP-AGENT1-IGNORE-1"
        agent1AttachRef(t, r, o.ID, ref)

        cases := []struct {
                name string
                body []byte
        }{
                {"unknown reference", agent1ChargeBody("charge.success", "LP-AGENT1-DOES-NOT-EXIST", "success", 99000)},
                {"other event", agent1ChargeBody("transfer.success", ref, "success", o.TotalCents)},
                {"charge.failed", agent1ChargeBody("charge.success", ref, "failed", o.TotalCents)},
                {"charge.abandoned", agent1ChargeBody("charge.success", ref, "abandoned", o.TotalCents)},
                {"empty reference", agent1ChargeBody("charge.success", "", "success", o.TotalCents)},
        }
        for _, tc := range cases {
                if err := r.Svc.HandlePaystackWebhook(tc.body, agent1Sign(t, agent1TestSecret, tc.body)); err != nil {
                        t.Fatalf("%s must be acked with no error, got %v", tc.name, err)
                }
        }
        if status, _ := agent1OrderStatus(t, r, o.ID); status != models.OrderPending {
                t.Fatalf("order must stay PENDING, got %s", status)
        }
        if n := agent1Stock(t, r, pid); n != 40 {
                t.Fatalf("stock must be untouched, got %d", n)
        }
}

// INVARIANT: a webhook reference must belong to a paystack-method payment.
// A Daraja checkout id (or any other provider's token) pasted into a
// charge.success must never complete an M-Pesa payment.
func TestAgent1WebhookDoesNotCompleteNonPaystackPayment(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        pid := agent1ProductID(t, r)
        o := agent1Checkout(t, r, models.CheckoutRequest{PaymentMethod: models.MethodMpesa, PaymentMode: models.ModeManual})
        agent1AttachAnyRef(t, r, o.ID, "LP-AGENT1-MPESA-1", models.MethodMpesa)

        body := agent1ChargeBody("charge.success", "LP-AGENT1-MPESA-1", "success", o.TotalCents)
        if err := r.Svc.HandlePaystackWebhook(body, agent1Sign(t, agent1TestSecret, body)); err != nil {
                t.Fatalf("webhook: %v", err)
        }
        if status, _ := agent1OrderStatus(t, r, o.ID); status != models.OrderPending {
                t.Fatalf("mpesa order must stay PENDING, got %s", status)
        }
        if n := agent1Stock(t, r, pid); n != 40 {
                t.Fatalf("stock must be untouched, got %d", n)
        }
        pays := agent1Payments(t, r, o.ID)
        if len(pays) != 1 || pays[0].Status != models.PaymentPending {
                t.Fatalf("mpesa payment must stay PENDING: %+v", pays)
        }
}

// INVARIANT: money moved after a void is a refund problem, never a sale.
// The webhook must acknowledge (Paystack stops retrying), leave the order
// VOIDED, and not resurrect anything.
func TestAgent1WebhookRefusesVoidedOrder(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        pid := agent1ProductID(t, r)
        o := agent1Checkout(t, r, models.CheckoutRequest{})
        ref := "LP-AGENT1-VOID-1"
        agent1AttachRef(t, r, o.ID, ref)
        if _, err := r.Svc.Void(o.ID, "agent1 probe", agent1Principal()); err != nil {
                t.Fatalf("void: %v", err)
        }
        body := agent1ChargeBody("charge.success", ref, "success", o.TotalCents)
        if err := r.Svc.HandlePaystackWebhook(body, agent1Sign(t, agent1TestSecret, body)); err != nil {
                t.Fatalf("voided-order webhook must ack, got %v", err)
        }
        if status, _ := agent1OrderStatus(t, r, o.ID); status != models.OrderVoided {
                t.Fatalf("order must stay VOIDED, got %s", status)
        }
        if n := agent1Stock(t, r, pid); n != 40 {
                t.Fatalf("stock must be untouched, got %d", n)
        }
        for _, p := range agent1Payments(t, r, o.ID) {
                if p.Status == models.PaymentCompleted {
                        t.Fatalf("payment must not complete on a voided order: %+v", p)
                }
        }
}

// Documents the deliberate "flag, don't block" amount policy: a signed
// webhook whose amount differs from the payment row still completes the
// order, but the discrepancy flag must be set for staff review.
func TestAgent1WebhookAmountMismatchFlagsDiscrepancy(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        o := agent1Checkout(t, r, models.CheckoutRequest{})
        ref := "LP-AGENT1-MISMATCH-1"
        agent1AttachRef(t, r, o.ID, ref)
        body := agent1ChargeBody("charge.success", ref, "success", o.TotalCents-1)
        if err := r.Svc.HandlePaystackWebhook(body, agent1Sign(t, agent1TestSecret, body)); err != nil {
                t.Fatalf("webhook: %v", err)
        }
        status, disc := agent1OrderStatus(t, r, o.ID)
        if status != models.OrderPaid || !disc {
                t.Fatalf("paid with discrepancy expected, got %s disc=%v", status, disc)
        }
}

// Mixed tender: the cash leg is banked at checkout, the paystack leg is
// the only async money. The webhook must complete the sale with ONE stock
// deduction, and replays must not re-deduct.
func TestAgent1SplitCashPlusPaystackWebhook(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        pid := agent1ProductID(t, r)
        o := agent1Checkout(t, r, models.CheckoutRequest{
                PaymentMethod: models.MethodCash,
                SplitPayments: []models.SplitLeg{
                        {Method: models.MethodCash, AmountCents: 10000},
                        {Method: models.MethodPaystack, AmountCents: 100000},
                },
        })
        if o.Status != models.OrderPending {
                t.Fatalf("split with async leg must be PENDING, got %s", o.Status)
        }
        ref := "LP-AGENT1-SPLIT-1"
        agent1AttachRef(t, r, o.ID, ref)

        pays := agent1Payments(t, r, o.ID)
        if len(pays) != 2 || pays[0].Method != models.MethodCash || pays[0].Status != models.PaymentCompleted ||
                pays[1].Method != models.MethodPaystack || pays[1].Status != models.PaymentPending || pays[1].Amount != 100000 {
                t.Fatalf("split legs wrong: %+v", pays)
        }
        if n := agent1Stock(t, r, pid); n != 40 {
                t.Fatalf("stock must wait for the async leg, got %d", n)
        }

        body := agent1ChargeBody("charge.success", ref, "success", 100000)
        sig := agent1Sign(t, agent1TestSecret, body)
        for i := 0; i < 2; i++ {
                if err := r.Svc.HandlePaystackWebhook(body, sig); err != nil {
                        t.Fatalf("split webhook %d: %v", i, err)
                }
        }
        status, _ := agent1OrderStatus(t, r, o.ID)
        if status != models.OrderPaid {
                t.Fatalf("split order must be PAID, got %s", status)
        }
        if n := agent1Stock(t, r, pid); n != 38 {
                t.Fatalf("stock must deduct exactly once, got %d", n)
        }
        for _, p := range agent1Payments(t, r, o.ID) {
                if p.Status != models.PaymentCompleted {
                        t.Fatalf("both legs must be COMPLETED: %+v", p)
                }
        }
}

// KNOWN GAP (T1): re-running checkout overwrites checkout_request_id, so
// a payment completed on the OLD popup (or its "open checkout page" link)
// arrives as an unknown reference and is silently dropped — real money
// captured, order left PENDING, nothing but a log line. This test encodes
// current behavior; flipping it requires a reconciliation decision.
func TestAgent1WebhookForSupersededReferenceCurrentlyDropped(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        pid := agent1ProductID(t, r)
        o := agent1Checkout(t, r, models.CheckoutRequest{})
        agent1AttachRef(t, r, o.ID, "LP-AGENT1-OLDREF")
        // Simulate re-init minting a fresh reference over the same row.
        if _, err := r.DB.Exec(r.DB.Rebind(`UPDATE payments SET checkout_request_id = 'LP-AGENT1-NEWREF' WHERE order_id = ?`), o.ID); err != nil {
                t.Fatalf("re-init: %v", err)
        }
        body := agent1ChargeBody("charge.success", "LP-AGENT1-OLDREF", "success", o.TotalCents)
        if err := r.Svc.HandlePaystackWebhook(body, agent1Sign(t, agent1TestSecret, body)); err != nil {
                t.Fatalf("current behavior: unknown ref acks, got %v", err)
        }
        if status, _ := agent1OrderStatus(t, r, o.ID); status != models.OrderPending {
                t.Fatalf("current behavior: order stays PENDING, got %s", status)
        }
        if n := agent1Stock(t, r, pid); n != 40 {
                t.Fatalf("stock untouched: %d", n)
        }
}

// ---- fake-money guard / config flags -----------------------------------

// INVARIANT: with no Paystack secret anywhere, a paystack checkout must
// stay PENDING forever on its own: no provider, no sweeper, no completion,
// and the till reports unconfigured + manual M-Pesa route.
func TestAgent1PaystackWithoutKeysNeverCompletes(t *testing.T) {
        t.Setenv("PAYSTACK_SECRET_KEY", "")
        r := newAgent1Rig(t, "")
        pid := agent1ProductID(t, r)
        o := agent1Checkout(t, r, models.CheckoutRequest{})

        r.Svc.SweepPaystack(context.Background()) // must be a safe no-op
        if status, _ := agent1OrderStatus(t, r, o.ID); status != models.OrderPending {
                t.Fatalf("order must stay PENDING without keys, got %s", status)
        }
        if n := agent1Stock(t, r, pid); n != 40 {
                t.Fatalf("stock must be untouched, got %d", n)
        }
        cfg := r.Svc.PaymentConfig()
        if cfg.Paystack.Configured || cfg.Paystack.Enabled {
                t.Fatalf("unconfigured shop must report configured=false enabled=false: %+v", cfg.Paystack)
        }
        if route := r.Svc.MpesaRoute(); route != "manual" {
                t.Fatalf("route without providers must be manual, got %q", route)
        }
}

// The cloud-config path lands paystack_secret_key in the settings store
// (applyCloudConfig → settings.Set). The configured/enabled flags and the
// M-Pesa route must react immediately, including cache invalidation when
// the key is removed, and an explicit paystack_enabled=false must win.
func TestAgent1PaymentConfigFollowsSettingsKeys(t *testing.T) {
        t.Setenv("PAYSTACK_SECRET_KEY", "")
        r := newAgent1Rig(t, "")

        cfg := r.Svc.PaymentConfig()
        if cfg.Paystack.Configured || cfg.Paystack.Enabled {
                t.Fatalf("pre-config: flags must be false: %+v", cfg.Paystack)
        }
        if err := r.ST.Set("paystack_secret_key", agent1TestSecret); err != nil {
                t.Fatalf("cloud-config write: %v", err)
        }
        cfg = r.Svc.PaymentConfig()
        if !cfg.Paystack.Configured || !cfg.Paystack.Enabled {
                t.Fatalf("after key write: configured+enabled expected: %+v", cfg.Paystack)
        }
        if route := r.Svc.MpesaRoute(); route != "paystack" {
                t.Fatalf("configured Paystack must own the M-Pesa route, got %q", route)
        }
        // Explicit admin kill-switch beats a valid key.
        if err := r.ST.Set("paystack_enabled", "false"); err != nil {
                t.Fatalf("disable: %v", err)
        }
        cfg = r.Svc.PaymentConfig()
        if !cfg.Paystack.Configured || cfg.Paystack.Enabled {
                t.Fatalf("disabled: configured=true enabled=false expected: %+v", cfg.Paystack)
        }
        if err := r.ST.Set("paystack_enabled", "true"); err != nil {
                t.Fatalf("re-enable: %v", err)
        }
        // Key removal invalidates the cached client (configured drops again).
        if err := r.ST.Set("paystack_secret_key", ""); err != nil {
                t.Fatalf("clear key: %v", err)
        }
        cfg = r.Svc.PaymentConfig()
        if cfg.Paystack.Configured || cfg.Paystack.Enabled {
                t.Fatalf("cleared key must invalidate the client: %+v", cfg.Paystack)
        }
}

// Env-provided keys (deployment path) must also light the till up — and
// FromEnv must say so, so the UI renders "(server keys)" instead of
// pretending the key came from the cloud vault.
func TestAgent1PaymentConfigEnvOverrideAndFromEnvGap(t *testing.T) {
        t.Setenv("PAYSTACK_SECRET_KEY", agent1TestSecret)
        r := newAgent1Rig(t, "")
        cfg := r.Svc.PaymentConfig()
        if !cfg.Paystack.Configured || !cfg.Paystack.Enabled {
                t.Fatalf("env key must configure the till: %+v", cfg.Paystack)
        }
        if cfg.Paystack.PublicKey != "" {
                t.Fatalf("no public key configured — must stay empty, got %q", cfg.Paystack.PublicKey)
        }
        if !cfg.Paystack.FromEnv {
                t.Fatal("FromEnv must be true when the secret comes from the process environment")
        }
}

// ---- pre-network guards on init / verify / sweeper ----------------------

// Init on a non-PENDING order must fail closed before any API call.
func TestAgent1InitOnPaidOrVoidedOrderFailsClosed(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        o := agent1Checkout(t, r, models.CheckoutRequest{})
        agent1AttachRef(t, r, o.ID, "LP-AGENT1-INITGUARD-1")
        body := agent1ChargeBody("charge.success", "LP-AGENT1-INITGUARD-1", "success", o.TotalCents)
        if err := r.Svc.HandlePaystackWebhook(body, agent1Sign(t, agent1TestSecret, body)); err != nil {
                t.Fatalf("webhook: %v", err)
        }
        if _, _, err := r.Svc.PaystackInit(o.ID, "", agent1Principal()); err == nil {
                t.Fatal("init on PAID order must fail")
        } else if !strings.Contains(err.Error(), "already paid") {
                t.Fatalf("wrong error: %v", err)
        }

        o2 := agent1Checkout(t, r, models.CheckoutRequest{})
        if _, err := r.Svc.Void(o2.ID, "agent1", agent1Principal()); err != nil {
                t.Fatalf("void: %v", err)
        }
        if _, _, err := r.Svc.PaystackInit(o2.ID, "", agent1Principal()); err == nil {
                t.Fatal("init on VOIDED order must fail")
        }
}

// Verify must reject junk references (and unconfigured shops) before it
// would ever reach the network; a well-formed but unknown reference is a
// clean not-found.
func TestAgent1PaystackVerifyRejectsBadReferences(t *testing.T) {
        t.Setenv("PAYSTACK_SECRET_KEY", "")
        rNoKey := newAgent1Rig(t, "")
        o := agent1Checkout(t, rNoKey, models.CheckoutRequest{})
        if _, err := rNoKey.Svc.PaystackVerify(o.ID, "LP-X", agent1Principal()); err == nil {
                t.Fatal("verify without keys must fail closed")
        }

        r := newAgent1Rig(t, agent1TestSecret)
        o2 := agent1Checkout(t, r, models.CheckoutRequest{})
        if _, err := r.Svc.PaystackVerify(o2.ID, "", agent1Principal()); err == nil {
                t.Fatal("empty reference must fail")
        }
        if _, err := r.Svc.PaystackVerify(o2.ID, strings.Repeat("x", 121), agent1Principal()); err == nil {
                t.Fatal("over-long reference must fail")
        }
        if _, err := r.Svc.PaystackVerify(o2.ID, "LP-AGENT1-UNKNOWN-REF", agent1Principal()); err == nil {
                t.Fatal("unknown reference must fail (DB lookup precedes any API call)")
        }
        if status, _ := agent1OrderStatus(t, r, o2.ID); status != models.OrderPending {
                t.Fatalf("order must stay PENDING, got %s", status)
        }
}

// The sweeper without a configured client must not touch pending rows.
func TestAgent1SweepWithoutClientIsNoop(t *testing.T) {
        t.Setenv("PAYSTACK_SECRET_KEY", "")
        r := newAgent1Rig(t, "")
        o := agent1Checkout(t, r, models.CheckoutRequest{})
        agent1AttachRef(t, r, o.ID, "LP-AGENT1-SWEEP-1")
        r.Svc.SweepPaystack(context.Background())
        pays := agent1Payments(t, r, o.ID)
        if len(pays) != 1 || pays[0].Status != models.PaymentPending {
                t.Fatalf("sweep without client must leave the payment alone: %+v", pays)
        }
}

// ---- full-stack HTTP probes ---------------------------------------------

// agent1Server mirrors the handlers_test wiring but also exposes the shop
// service so probes can stage reference rows (PaystackInit needs the real
// API, which is off-limits here).
type agent1Server struct {
        engine  *gin.Engine
        svc     *services.Service
        admin   string
        cashier string
}

func newAgent1Server(t *testing.T) *agent1Server {
        t.Helper()
        gin.SetMode(gin.TestMode)
        dir := t.TempDir()
        db, err := database.Open("sqlite", filepath.Join(dir, "test.db"), "")
        if err != nil {
                t.Fatalf("db: %v", err)
        }
        t.Cleanup(func() { _ = db.Close() })
        if err := db.Migrate(); err != nil {
                t.Fatalf("migrate: %v", err)
        }
        if err := db.Seed(true); err != nil {
                t.Fatalf("seed: %v", err)
        }
        st, err := settings.New(db)
        if err != nil {
                t.Fatalf("settings: %v", err)
        }
        hub := ws.NewHub(st.JWTSecret)
        go hub.Run()
        reg, err := tenants.Load(dir)
        if err != nil {
                t.Fatalf("tenants: %v", err)
        }
        dbPath := filepath.Join(dir, "test.db")
        shop, err := reg.CreateShop("Agent1 Shop", dbPath, "2026-09-16T00:00:00Z")
        if err != nil {
                t.Fatalf("shop: %v", err)
        }
        for _, u := range []string{"admin", "cashier"} {
                if err := reg.RegisterUser(u, shop.ID); err != nil {
                        t.Fatalf("register %s: %v", u, err)
                }
        }
        secret := reg.EnsureJWTSecret(st.Get("jwt_secret"))
        dbPool := tenants.NewPool(reg, "sqlite", "")
        dbPool.Inject(shop.ID, db)
        shopPool := services.NewShopPool(dbPool, hub, nil)
        svc, err := shopPool.Service(shop.ID)
        if err != nil {
                t.Fatalf("service: %v", err)
        }
        pw := printer.NewWorker(db, svc.Settings())
        shopPool.SetPrinter(pw)
        h := handlers.New(db, svc.Settings(), svc, hub, pw)
        h.Tenants = reg
        h.Shops = shopPool
        h.DefaultShop = shop.ID
        h.MasterSecret = []byte(secret)
        t.Cleanup(func() { shopPool.CloseAll() })
        engine := router.New(h, nil)

        admin := login(t, engine, "admin", "admin123")
        rotateFresh(t, engine, admin)
        admin = login(t, engine, "admin", rotatedPassword)
        cashier := login(t, engine, "cashier", "cashier123")
        rotateFresh(t, engine, cashier)
        cashier = login(t, engine, "cashier", rotatedPassword)
        return &agent1Server{engine: engine, svc: svc, admin: admin, cashier: cashier}
}

// Cloud-config equivalence: keys written through the settings API light
// up PaymentConfig without ever leaking the secret back out.
func TestAgent1ConfigViaSettingsAPINoSecretLeak(t *testing.T) {
        s := newAgent1Server(t)
        w := do(t, s.engine, "PUT", "/api/v1/settings", s.admin, map[string]any{
                "values": map[string]string{
                        "paystack_secret_key": agent1TestSecret,
                        "paystack_public_key": agent1TestPublic,
                },
        })
        if w.Code != 200 {
                t.Fatalf("settings write: %d %s", w.Code, w.Body.String())
        }
        w = do(t, s.engine, "GET", "/api/v1/payments/config", s.admin, nil)
        if w.Code != 200 {
                t.Fatalf("config: %d", w.Code)
        }
        if strings.Contains(w.Body.String(), agent1TestSecret) || strings.Contains(w.Body.String(), "sk_") {
                t.Fatalf("payment config leaked a secret-shaped value: %s", w.Body.String())
        }
        cfg := dataMap(t, w)["paystack"].(map[string]any)
        if cfg["configured"] != true || cfg["enabled"] != true {
                t.Fatalf("flags must flip on after the key lands: %v", cfg)
        }
        if cfg["publicKey"] != agent1TestPublic {
                t.Fatalf("public key must be echoed for the popup: %v", cfg["publicKey"])
        }
        // Snapshot masks the secret.
        w = do(t, s.engine, "GET", "/api/v1/settings", s.admin, nil)
        snap := dataMap(t, w)["paystack_secret_key"]
        if snap != settings.MaskToken {
                t.Fatalf("settings snapshot must mask the secret, got %v", snap)
        }
}

// End-to-end through the real router: settings-staged key → paystack
// checkout → signed public webhook → PAID; bad signature → 401; replay →
// 200 and idempotent; oversized body → 4xx before any money moves.
func TestAgent1WebhookEndToEndViaRouter(t *testing.T) {
        s := newAgent1Server(t)
        do(t, s.engine, "PUT", "/api/v1/settings", s.admin, map[string]any{
                "values": map[string]string{"paystack_secret_key": agent1TestSecret},
        })
        w := do(t, s.engine, "POST", "/api/v1/orders/checkout", s.cashier, map[string]any{
                "items":         []map[string]any{{"productId": 1, "qty": 2}},
                "paymentMethod": "paystack",
                "clientUuid":    "agent1-e2e-1",
        })
        if w.Code != 201 {
                t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
        }
        orderID := itoa64(dataMap(t, w)["id"])
        ref := "LP-AGENT1-E2E-1"
        if res, err := s.svc.DB().Exec(s.svc.DB().Rebind(
                `UPDATE payments SET checkout_request_id = ? WHERE order_id = ? AND method = 'paystack' AND status = 'PENDING'`),
                ref, dataMap(t, w)["id"]); err != nil {
                t.Fatalf("stage ref: %v", err)
        } else if n, _ := res.RowsAffected(); n != 1 {
                t.Fatalf("stage ref: %d rows", n)
        }

        body := agent1ChargeBody("charge.success", ref, "success", 110000)
        sig := agent1Sign(t, agent1TestSecret, body)

        post := func(b []byte, signature string) *httptest.ResponseRecorder {
                req := httptest.NewRequest("POST", "/api/v1/payments/paystack/webhook", strings.NewReader(string(b)))
                req.Header.Set("Content-Type", "application/json")
                if signature != "" {
                        req.Header.Set("x-paystack-signature", signature)
                }
                rec := httptest.NewRecorder()
                s.engine.ServeHTTP(rec, req)
                return rec
        }

        if rec := post(body, sig); rec.Code != http.StatusOK {
                t.Fatalf("signed webhook must 200, got %d %s", rec.Code, rec.Body.String())
        }
        w = do(t, s.engine, "GET", "/api/v1/orders/"+orderID, s.cashier, nil)
        if got := dataMap(t, w)["status"]; got != models.OrderPaid {
                t.Fatalf("order must be PAID via public webhook, got %v", got)
        }
        // Replay: Paystack retry semantics — 200, no double stock.
        if rec := post(body, sig); rec.Code != http.StatusOK {
                t.Fatalf("replay must 200, got %d", rec.Code)
        }
        if rec := post(body, "deadbeef"); rec.Code != http.StatusUnauthorized {
                t.Fatalf("bad signature must 401, got %d", rec.Code)
        }
        // Oversized body must be rejected before parsing (1MB cap).
        if rec := post(append(make([]byte, 1<<20+64), body...), sig); rec.Code < 400 || rec.Code >= 500 {
                t.Fatalf("oversized body must 4xx, got %d", rec.Code)
        }
        var stock int
        if err := s.svc.DB().QueryRow(`SELECT stock_qty FROM products WHERE id = 1`).Scan(&stock); err != nil {
                t.Fatalf("stock: %v", err)
        }
        if stock != 38 {
                t.Fatalf("stock must deduct exactly once, got %d", stock)
        }
}

// guard against accidental float money math sneaking into the completion
// path: totals are int64 cents end-to-end (this mirrors the backend rule;
// kept tiny since the compiler enforces int64 signatures anyway).
func TestAgent1AmountsAreIntegerCents(t *testing.T) {
        r := newAgent1Rig(t, agent1TestSecret)
        o := agent1Checkout(t, r, models.CheckoutRequest{})
        if o.TotalCents != 110000 { // 2 × TS-001 @ 550.00 KES, VAT-inclusive
                t.Fatalf("total must be exact minor units, got %d", o.TotalCents)
        }
        pays := agent1Payments(t, r, o.ID)
        if pays[0].Amount != o.TotalCents {
                t.Fatalf("paystack payment must equal payable cents: %+v vs %d", pays[0], o.TotalCents)
        }
}
