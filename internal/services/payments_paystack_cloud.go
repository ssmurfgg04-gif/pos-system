package services

// payments_paystack_cloud.go — the cloud-proxy payment path (v1.3.2).
//
// The Paystack SECRET key is migrating to a Supabase Edge Function secret
// (Deno.env PAYSTACK_SECRET_KEY; dual-read fallback: the app_config vault
// row). Tills stop needing the secret at all: transaction init and verify
// run through the paystack-init / paystack-verify Edge Functions, which
// authenticate the till's DEVICE identity and hold the secret server-side.
//
// Dual-read migration window (never skip it):
//   - a till WITH a local key keeps using it (zero behavior change);
//   - a till WITHOUT one (or after the owner flips paystack_via_cloud)
//     routes through the Edge Function automatically;
//   - the app_config row is deleted only after one full verified payment
//     cycle through the proxy — until then both paths coexist.
//
// Webhooks: Paystack posts to the paystack-webhook Edge Function (HMAC
// verified server-side with Deno.env), which stores verified events; the
// sweeper claims them per pending reference (paystack_claim_events SQL
// RPC, same-team scoped, exactly-once per device) and funnels them through
// the SAME completion guard as the direct webhook.

import (
        "bytes"
        "encoding/json"
        "fmt"
        "log"
        "net/http"
        "strings"

        "posapp/internal/models"
        "posapp/internal/paystack"
)

// paystackViaCloud reports whether the owner flipped the cloud proxy switch
// (a public app_config row distributed by the vault).
func (s *Service) paystackViaCloud() bool {
        return s.settings.GetBool("paystack_via_cloud", false)
}

// paystackUsable: a charge can happen with a local key OR the cloud proxy.
func (s *Service) paystackUsable() bool {
        return s.GetPaystack() != nil || (s.paystackViaCloud() && s.cloudEdgeAvailable())
}

// cloudEdgeAvailable resolves the cloud link (rpc mode = Supabase project).
func (s *Service) cloudEdgeAvailable() bool {
        c, ok := s.syncConfig()
        return ok && c.mode == "rpc"
}

// paystackEdgeCall posts to one of the paystack-* Edge Functions with this
// till's device identity. Returns the function's JSON object.
func (s *Service) paystackEdgeCall(fn string, args map[string]any) (map[string]any, error) {
        c, ok := s.syncConfig()
        if !ok || c.mode != "rpc" {
                return nil, fmt.Errorf("cloud payment proxy needs the LedgerPOS cloud link")
        }
        body, err := json.Marshal(args)
        if err != nil {
                return nil, err
        }
        url := strings.TrimRight(c.base, "/") + "/functions/v1/" + fn
        req, err := http.NewRequest("POST", url, bytes.NewReader(body))
        if err != nil {
                return nil, err
        }
        req.Header.Set("apikey", c.key)
        req.Header.Set("Authorization", "Bearer "+c.key)
        req.Header.Set("Content-Type", "application/json")
        hc := &http.Client{Timeout: syncHTTPTimeout}
        resp, err := hc.Do(req)
        if err != nil {
                return nil, fmt.Errorf("cloud proxy %s: %w", fn, err)
        }
        defer resp.Body.Close()
        var out map[string]any
        if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
                return nil, fmt.Errorf("cloud proxy %s: bad response (%d)", fn, resp.StatusCode)
        }
        return out, nil
}

// paystackCloudInit creates the transaction server-side (no local secret).
func (s *Service) paystackCloudInit(email string, amount int64, currency, reference, callback string, metadata map[string]any) (*paystack.InitializeResult, error) {
        c, ok := s.syncConfig()
        if !ok {
                return nil, fmt.Errorf("cloud payment proxy unavailable")
        }
        out, err := s.paystackEdgeCall("paystack-init", map[string]any{
                "p_device_id":   c.device,
                "p_secret_hash": c.secretHash,
                "email":         email,
                "amount":        amount,
                "currency":      currency,
                "reference":     reference,
                "callback_url":  callback,
                "metadata":      metadata,
        })
        if err != nil {
                return nil, err
        }
        if v, _ := out["ok"].(bool); !v {
                msg, _ := out["error"].(string)
                return nil, fmt.Errorf("cloud proxy init: %s", msg)
        }
        authURL, _ := out["authorization_url"].(string)
        accessCode, _ := out["access_code"].(string)
        if authURL == "" || accessCode == "" {
                return nil, fmt.Errorf("cloud proxy init: incomplete response")
        }
        return &paystack.InitializeResult{
                AuthorizationURL: authURL,
                AccessCode:       accessCode,
        }, nil
}

// paystackVerifyRef verifies a reference with the local client when present,
// else through the cloud proxy. Both paths return the same VerifyResult.
func (s *Service) paystackVerifyRef(reference string) (*paystack.VerifyResult, error) {
        if client := s.GetPaystack(); client != nil {
                return client.Verify(reference)
        }
        c, ok := s.syncConfig()
        if !ok || c.mode != "rpc" {
                return nil, fmt.Errorf("cloud payment proxy unavailable")
        }
        out, err := s.paystackEdgeCall("paystack-verify", map[string]any{
                "p_device_id": c.device, "p_secret_hash": c.secretHash, "reference": reference,
        })
        if err != nil {
                return nil, err
        }
        if v, _ := out["ok"].(bool); !v {
                msg, _ := out["error"].(string)
                return nil, fmt.Errorf("cloud proxy verify: %s", msg)
        }
        data, _ := out["data"].(map[string]any)
        if data == nil {
                return nil, fmt.Errorf("cloud proxy verify: no data")
        }
        v := &paystack.VerifyResult{}
        v.Status, _ = data["status"].(string)
        v.Channel, _ = data["channel"].(string)
        if amt, ok := data["amount"].(float64); ok {
                v.Amount = int64(amt)
        }
        return v, nil
}

// small accessors retained for tests
func c_device(s *Service) string {
        c, ok := s.syncConfig()
        if !ok {
                return ""
        }
        return c.device
}
func c_secret(s *Service) string {
        c, ok := s.syncConfig()
        if !ok {
                return ""
        }
        return c.secretHash
}

// ---- webhook relay -------------------------------------------------------

type paystackClaimedEvent struct {
        ID        int64           `json:"id"`
        EventType string          `json:"eventType"`
        Reference string          `json:"reference"`
        Payload   json.RawMessage `json:"payload"`
        CreatedAt string          `json:"createdAt"`
}

// claimPaystackWebhookEvents pulls cloud-verified webhook events for THIS
// till's pending references (same-team scoped server-side; each event is
// delivered to each device exactly once).
func (s *Service) claimPaystackWebhookEvents(references []string) []paystackClaimedEvent {
        if len(references) == 0 {
                return nil
        }
        c, ok := s.syncConfig()
        if !ok || c.mode != "rpc" {
                return nil
        }
        var out struct {
                OK     bool                   `json:"ok"`
                Events []paystackClaimedEvent `json:"events"`
        }
        err := rpcCall(c.base, "paystack_claim_events", map[string]any{
                "p_device_id":   c.device,
                "p_secret_hash": c.secretHash,
                "p_references":  references,
        }, &out)
        if err != nil || !out.OK {
                return nil // relay is best-effort; the verify sweeper is the authority
        }
        return out.Events
}

// HandlePaystackEvent processes an ALREADY-VERIFIED webhook event (the Edge
// Function checked the HMAC-SHA512 signature server-side before storing).
// It funnels into the identical completion guard as the direct webhook.
func (s *Service) HandlePaystackEvent(raw []byte) error {
        ev, err := paystack.ParseWebhook(raw)
        if err != nil {
                return err
        }
        return s.completePaystackEvent(ev)
}

// completePaystackEvent holds the shared completion logic (direct webhook
// and relayed events) — unknown references get reconciled or audited,
// voided orders are refused, completion is exactly-once.
func (s *Service) completePaystackEvent(ev *paystack.WebhookEvent) error {
        if ev.Event != "charge.success" || ev.Data.Status != "success" || ev.Data.Reference == "" {
                return nil // not a completion event — ignore
        }
        var paymentID int64
        err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM payments WHERE checkout_request_id = ? AND method = 'paystack'`),
                ev.Data.Reference).Scan(&paymentID)
        if err != nil {
                // Unknown reference: most often a popup superseded by a retry whose
                // old checkout the customer still completed. Money moved — reconcile
                // it to the order inside the reference (LP-<orderNumber>-<rand>).
                if orderNum, ok := strings.CutPrefix(ev.Data.Reference, "LP-"); ok {
                        if i := strings.LastIndex(orderNum, "-"); i > 0 {
                                orderNum = orderNum[:i]
                        }
                        var orderID int64
                        var orderStatus string
                        err := s.db.QueryRow(s.db.Rebind(`SELECT id, status FROM orders WHERE number = ?`), orderNum).
                                Scan(&orderID, &orderStatus)
                        if err == nil && orderStatus == models.OrderPending {
                                var legID int64
                                legErr := s.db.QueryRow(s.db.Rebind(`SELECT id FROM payments WHERE order_id = ? AND method = 'paystack' AND status = 'PENDING'`), orderID).Scan(&legID)
                                if legErr == nil {
                                        s.db.Exec(s.db.Rebind(`UPDATE payments SET checkout_request_id = ? WHERE id = ? AND status = 'PENDING'`), ev.Data.Reference, legID)
                                        if _, cerr := s.completePayment(legID, ev.Data.Reference, ev.Data.Amount, "Paystack "+ev.Data.Channel+" (webhook, recovered reference)"); cerr != nil {
                                                return cerr
                                        }
                                        log.Printf("[paystack] relay recovered superseded reference %s onto order %s", ev.Data.Reference, orderNum)
                                        s.settleSiblings(orderID, legID)
                                        return nil
                                }
                        }
                        s.Audit(0, "system", "PAYSTACK_UNKNOWN_REF", "order", orderNum,
                                "money arrived for reference "+ev.Data.Reference+" — refund or reconcile in the Paystack dashboard")
                }
                log.Printf("[paystack] relay for unknown reference %s", ev.Data.Reference)
                return nil
        }
        if err := s.paystackGuardVoid(paymentID); err != nil {
                return nil // the audit trail notes the refund requirement
        }
        if _, err := s.completePayment(paymentID, ev.Data.Reference, ev.Data.Amount, "Paystack "+ev.Data.Channel+" (webhook relay)"); err != nil {
                return err
        }
        log.Printf("[paystack] relay completed reference %s", ev.Data.Reference)
        return nil
}

// sweepPaystackRelay claims verified webhook events for this till's
// pending + recently-failed references. Called from the Sweeper tick.
func (s *Service) sweepPaystackRelay() {
        if !s.paystackUsable() || !s.cloudEdgeAvailable() {
                return
        }
        var refs []string
        rows, err := s.db.Query(`SELECT checkout_request_id FROM payments
                WHERE method = 'paystack' AND status IN ('PENDING','FAILED')
                  AND checkout_request_id LIKE 'LP-%'
                ORDER BY id DESC LIMIT 100`)
        if err != nil {
                return
        }
        for rows.Next() {
                var ref string
                if rows.Scan(&ref) == nil && ref != "" {
                        refs = append(refs, ref)
                }
        }
        rows.Close()
        for _, ev := range s.claimPaystackWebhookEvents(refs) {
                if err := s.HandlePaystackEvent(ev.Payload); err != nil {
                        log.Printf("[paystack] relay event %s apply: %v", ev.Reference, err)
                }
        }
}
