package mpesa

import (
        "bytes"
        "context"
        "encoding/json"
        "fmt"
        "net/http"
        "strings"
        "sync"
        "time"
)

// BuniCloud is the KCB Buni M-Pesa Express provider, fronted by the
// buni-stkpush / buni-callback Supabase Edge Functions.
//
// WHY A PROXY (and not direct KCB calls like Daraja): the desktop till is
// effectively public source — consumers can extract any embedded secret.
// Buni consumer key/secret therefore live ONLY in Edge Function secrets;
// the till authenticates with its device identity (device id + secret
// hash, validated server-side against sync_devices) and the function
// performs the actual KCB OAuth + STK push. Completion is CALLBACK-DRIVEN:
// Buni exposes no STK query API (confirmed against the official spec and
// live production testing), so QuerySTK is deliberately unsupported and
// the till's Buni sweeper claims verified callback events instead.
//
// Wire contract (verified against production, October 2026):
//   token: POST {tokenURL} grant_type=client_credentials, Basic auth,
//          expires_in 3600 (server-side only — tills never see it)
//   push:  POST {stkURL} headers operation=STKPush, routeCode, messageId
//          body: phoneNumber, amount (INTEGER STRING), invoiceNumber
//          (till-orderref, <=12 chars), sharedShortCode=true,
//          orgShortCode/orgPassKey "" (shared till), callbackUrl,
//          transactionDescription (<=13 chars)
//   push response: {"response":{"MerchantRequestID","ResponseCode",
//          "CustomerMessage","CheckoutRequestID",...},"header":{...}}
//   callback: Daraja-shaped {"Body":{"stkCallback":{...}}} — parsed by
//          the existing ParseCallback; result codes 0 success,
//          1032 cancelled, 1037 timeout, 2001 wrong PIN.
type BuniCloud struct {
        // EdgeFunctionBase is the Supabase project URL
        // (https://<ref>.supabase.co) hosting buni-stkpush.
        EdgeFunctionBase string
        // EdgeKey is the project's public anon key (apikey header). It is NOT
        // a secret — RLS plus device auth protect the function.
        EdgeKey string
        // DeviceID + SecretHash are this till's cloud identity.
        DeviceID   string
        SecretHash string

        client *http.Client

        mu          sync.Mutex
        edgeWarnKey string // remembers the last config we warned about
}

func NewBuniCloud(edgeBase, edgeKey, deviceID, secretHash string) *BuniCloud {
        return &BuniCloud{
                EdgeFunctionBase: strings.TrimRight(edgeBase, "/"),
                EdgeKey:          edgeKey,
                DeviceID:         deviceID,
                SecretHash:       secretHash,
                client:           &http.Client{Timeout: 30 * time.Second},
        }
}

func (b *BuniCloud) Name() string { return "buni" }

// BuniCheckoutPrefix marks payments whose STK checkout was issued through
// the Buni cloud route (stored on payments.checkout_request_id). The
// provider echoes the RAW id in callbacks; the till stores the prefixed
// form so the Daraja sweeper can never poll a Buni leg.
const BuniCheckoutPrefix = "BUNI-"

// BuniCheckoutPrefixFor returns the prefixed storage form of a callback's
// checkout id (HandleCallback resolves both the raw and prefixed forms).
func BuniCheckoutPrefixFor(checkoutRequestID string) string {
        return BuniCheckoutPrefix + checkoutRequestID
}

// ErrQueryUnsupported marks providers with no query API. The sweeper
// treats it as "never poll this provider" — completion is callback-driven.
type ErrQueryUnsupported struct{}

func (ErrQueryUnsupported) Error() string {
        return "buni has no STK query API; completion is callback-driven (buni sweeper claims verified events)"
}

// InitiateSTK pushes the prompt through buni-stkpush. The edge function
// performs the KCB OAuth + push and persists the push mapping so the
// callback can be routed back to this team of tills.
func (b *BuniCloud) InitiateSTK(ctx context.Context, req STKRequest) (STKResponse, error) {
        if b.EdgeFunctionBase == "" || b.EdgeKey == "" || b.DeviceID == "" || b.SecretHash == "" {
                return STKResponse{}, fmt.Errorf("buni cloud link is not configured")
        }
        payload := map[string]any{
                "p_device_id":   b.DeviceID,
                "p_secret_hash": b.SecretHash,
                "phone":         req.Phone,
                "amount":        req.AmountCents,
                "invoice_ref":   req.AccountReference, // customer-visible ref; function folds it into invoiceNumber
                "order_number":  req.Description,      // audit trail on the push mapping
                "client_uuid":   req.ClientUUID,
        }
        body, err := json.Marshal(payload)
        if err != nil {
                return STKResponse{}, err
        }
        url := b.EdgeFunctionBase + "/functions/v1/buni-stkpush"
        httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
        if err != nil {
                return STKResponse{}, err
        }
        httpReq.Header.Set("apikey", b.EdgeKey)
        httpReq.Header.Set("Authorization", "Bearer "+b.EdgeKey)
        httpReq.Header.Set("Content-Type", "application/json")
        resp, err := b.client.Do(httpReq)
        if err != nil {
                return STKResponse{}, fmt.Errorf("buni proxy: %w", err)
        }
        defer resp.Body.Close()
        var out struct {
                OK                bool   `json:"ok"`
                Error             string `json:"error"`
                MerchantRequestID string `json:"merchant_request_id"`
                CheckoutRequestID string `json:"checkout_request_id"`
                CustomerMessage   string `json:"customer_message"`
        }
        dec := json.NewDecoder(resp.Body)
        if err := dec.Decode(&out); err != nil {
                return STKResponse{}, fmt.Errorf("buni proxy: bad response (%d)", resp.StatusCode)
        }
        if resp.StatusCode != 200 || !out.OK {
                return STKResponse{}, fmt.Errorf("buni proxy: %s", out.Error)
        }
        if out.CheckoutRequestID == "" {
                return STKResponse{}, fmt.Errorf("buni proxy: no checkout id")
        }
        return STKResponse{
                MerchantRequestID: out.MerchantRequestID,
                CheckoutRequestID: out.CheckoutRequestID,
                CustomerMessage:   out.CustomerMessage,
        }, nil
}

// QuerySTK is unsupported by design — see ErrQueryUnsupported.
func (b *BuniCloud) QuerySTK(ctx context.Context, checkoutRequestID string) (STKQueryResult, error) {
        return STKQueryResult{}, ErrQueryUnsupported{}
}
