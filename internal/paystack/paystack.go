// Package paystack is a minimal client for the Paystack Transaction API
// (initialize + verify) plus webhook signature validation.
//
// SECURITY MODEL (mirrors Paystack's guidance):
//   - The SECRET key only ever lives on the backend (settings store or the
//     PAYSTACK_SECRET_KEY env var). It is never serialized into an API
//     response, never logged, and never synced to other devices.
//   - The frontend receives only the PUBLIC key, which it passes to
//     PaystackPop (inline.js) to open the checkout popup.
//   - Client-side "success" callbacks are hints only: an order is marked
//     PAID exclusively after a server-side verify (or a signature-checked
//     webhook) confirms the charge with Paystack.
//
// Amounts are integer subunits (pesewas for KES) throughout — the POS
// already stores cents, so no conversion happens at call sites.
package paystack

import (
        "bytes"
        "crypto/hmac"
        "crypto/sha512"
        "encoding/hex"
        "encoding/json"
        "errors"
        "fmt"
        "io"
        "net/http"
        "strings"
        "time"
)

const apiBase = "https://api.paystack.co"

// Client talks to the Paystack API with one secret key. Create it per
// request-batch via NewClient whenever the configured key changes (the
// service caches by key, like the Daraja provider).
type Client struct {
        secret string
        hc     *http.Client
}

func NewClient(secretKey string) *Client {
        return &Client{secret: secretKey, hc: &http.Client{Timeout: 30 * time.Second}}
}

// ValidKey performs a cheap structural check: live and test keys share the
// sk_ prefix. Real validity is proven by the first API call.
func ValidKey(k string) bool {
        return strings.HasPrefix(k, "sk_") && len(k) > 20
}

// IsPublicKey mirrors the check for pk_ keys (frontend-safe identifier).
func IsPublicKey(k string) bool {
        return strings.HasPrefix(k, "pk_") && len(k) > 20
}

// ---- Initialize ----

type InitializeRequest struct {
        Email       string         `json:"email"`
        Amount      int64          `json:"amount"` // subunits (pesewas)
        Currency    string         `json:"currency"`
        Reference   string         `json:"reference"`
        CallbackURL string         `json:"callback_url,omitempty"`
        Metadata    map[string]any `json:"metadata,omitempty"`
}

type InitializeResult struct {
        AuthorizationURL string `json:"authorizationUrl"`
        AccessCode       string `json:"accessCode"`
        Reference        string `json:"reference"`
}

type apiResponse struct {
        Status  bool            `json:"status"`
        Message string          `json:"message"`
        Data    json.RawMessage `json:"data"`
}

// Initialize opens a transaction: the response carries an access_code for
// the inline popup and an authorization_url for redirect flows.
func (c *Client) Initialize(req InitializeRequest) (*InitializeResult, error) {
        if !ValidKey(c.secret) {
                return nil, errors.New("paystack secret key is not configured")
        }
        body, _ := json.Marshal(req)
        var out struct {
                AuthorizationURL string `json:"authorization_url"`
                AccessCode       string `json:"access_code"`
                Reference        string `json:"reference"`
        }
        if err := c.do("POST", "/transaction/initialize", body, &out); err != nil {
                return nil, err
        }
        if out.AuthorizationURL == "" || out.AccessCode == "" {
                return nil, errors.New("paystack initialize returned an incomplete payload")
        }
        return &InitializeResult{
                AuthorizationURL: out.AuthorizationURL,
                AccessCode:       out.AccessCode,
                Reference:        out.Reference,
        }, nil
}

// ---- Verify ----

type VerifyResult struct {
        Reference       string `json:"reference"`
        Status          string `json:"status"` // success | failed | abandoned | ...
        Amount          int64  `json:"amount"` // subunits, as initialized
        Currency        string `json:"currency"`
        Channel         string `json:"channel"` // card | mobile_money | bank | ...
        PaidAt          string `json:"paid_at"`
        CustomerEmail   string `json:"customer_email"`
        GatewayResponse string `json:"gateway_response"`
}

// Verify asks Paystack what happened to a reference. Callers must treat
// anything but Status == "success" as not-paid.
func (c *Client) Verify(reference string) (*VerifyResult, error) {
        if !ValidKey(c.secret) {
                return nil, errors.New("paystack secret key is not configured")
        }
        reference = strings.TrimSpace(reference)
        if reference == "" || len(reference) > 120 {
                return nil, errors.New("invalid reference")
        }
        var out VerifyResult
        if err := c.do("GET", "/transaction/verify/"+reference, nil, &out); err != nil {
                return nil, err
        }
        return &out, nil
}

func (c *Client) do(method, path string, body []byte, out any) error {
        var rdr io.Reader
        if body != nil {
                rdr = bytes.NewReader(body)
        }
        req, err := http.NewRequest(method, apiBase+path, rdr)
        if err != nil {
                return err
        }
        req.Header.Set("Authorization", "Bearer "+c.secret)
        req.Header.Set("Content-Type", "application/json")
        resp, err := c.hc.Do(req)
        if err != nil {
                return fmt.Errorf("paystack %s: %w", path, err)
        }
        defer resp.Body.Close()
        raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
        if err != nil {
                return fmt.Errorf("paystack %s: read body: %w", path, err)
        }
        var env apiResponse
        if err := json.Unmarshal(raw, &env); err != nil {
                return fmt.Errorf("paystack %s: unexpected response (http %d)", path, resp.StatusCode)
        }
        if !env.Status {
                msg := env.Message
                if msg == "" {
                        msg = fmt.Sprintf("http %d", resp.StatusCode)
                }
                return fmt.Errorf("paystack %s: %s", path, msg)
        }
        if out != nil && len(env.Data) > 0 {
                if err := json.Unmarshal(env.Data, out); err != nil {
                        return fmt.Errorf("paystack %s: decode data: %w", path, err)
                }
        }
        return nil
}

// ---- Webhooks ----

// CheckWebhookSignature validates a webhook header against this client's
// secret key (keeps the key encapsulated).
func (c *Client) CheckWebhookSignature(rawBody []byte, signature string) bool {
        return CheckSignature(rawBody, signature, c.secret)
}

// CheckSignature validates the x-paystack-signature header: hex-encoded
// HMAC-SHA512 of the RAW request body keyed by the secret key. Constant
// time, raw bytes only (never re-serialize before hashing).
func CheckSignature(rawBody []byte, signature, secretKey string) bool {
        if signature == "" || secretKey == "" {
                return false
        }
        mac := hmac.New(sha512.New, []byte(secretKey))
        mac.Write(rawBody)
        decoded, err := hex.DecodeString(strings.ToLower(strings.TrimSpace(signature)))
        if err != nil {
                return false
        }
        return hmac.Equal(decoded, mac.Sum(nil))
}

// WebhookEvent is the decoded charge.success payload we care about.
type WebhookEvent struct {
        Event string      `json:"event"` // charge.success
        Data  WebhookData `json:"data"`
}

type WebhookData struct {
        Reference string `json:"reference"`
        Status    string `json:"status"` // success
        Amount    int64  `json:"amount"` // subunits
        Currency  string `json:"currency"`
        Channel   string `json:"channel"`
        PaidAt    string `json:"paid_at"`
}

// ParseWebhook decodes (already signature-verified) webhook bytes.
func ParseWebhook(raw []byte) (*WebhookEvent, error) {
        var ev WebhookEvent
        if err := json.Unmarshal(raw, &ev); err != nil {
                return nil, errors.New("invalid webhook payload")
        }
        if ev.Event == "" {
                return nil, errors.New("webhook event missing")
        }
        return &ev, nil
}
