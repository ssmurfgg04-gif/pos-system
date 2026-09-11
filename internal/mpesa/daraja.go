package mpesa

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Daraja is the direct Safaricom provider (sandbox or production).
// Docs: POST /mpesa/stkpush/v1/processrequest, /mpesa/stkpushquery/v1/query,
// OAuth /oauth/v1/generate?grant_type=client_credentials.
type Daraja struct {
	BaseURL        string // https://sandbox.safaricom.co.ke | https://api.safaricom.co.ke
	Shortcode      string
	Passkey        string
	ConsumerKey    string
	ConsumerSecret string
	CallbackURL    string

	client     *http.Client
	mu         sync.Mutex
	token      string
	tokenExpiry time.Time
}

func NewDaraja(env, shortcode, passkey, key, secret, callback string) *Daraja {
	base := "https://sandbox.safaricom.co.ke"
	if env == "production" {
		base = "https://api.safaricom.co.ke"
	}
	return &Daraja{
		BaseURL:        base,
		Shortcode:      shortcode,
		Passkey:        passkey,
		ConsumerKey:    key,
		ConsumerSecret: secret,
		CallbackURL:    callback,
		client:         &http.Client{Timeout: 20 * time.Second},
	}
}

func (d *Daraja) Name() string { return "daraja" }

func (d *Daraja) do(ctx context.Context, method, url string, body any, headers map[string]string) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return out, resp.StatusCode, err
}

// token caches the OAuth token until ~60s before expiry (mpesa-pos's
// re-auth-per-request was wasteful).
func (d *Daraja) getToken(ctx context.Context) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.token != "" && time.Now().Before(d.tokenExpiry) {
		return d.token, nil
	}
	url := d.BaseURL + "/oauth/v1/generate?grant_type=client_credentials"
	body, status, err := d.do(ctx, "GET", url, nil, map[string]string{
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(d.ConsumerKey+":"+d.ConsumerSecret)),
	})
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("oauth %d: %s", status, string(body))
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   string `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil || tr.AccessToken == "" {
		return "", errors.New("oauth: no access_token")
	}
	var secs int
	fmt.Sscanf(tr.ExpiresIn, "%d", &secs)
	if secs <= 0 {
		secs = 3599
	}
	d.token = tr.AccessToken
	d.tokenExpiry = time.Now().Add(time.Duration(secs-60) * time.Second)
	return d.token, nil
}

// timestamp is Daraja's YYYYMMDDHHMMSS format.
func (d *Daraja) timestamp() string {
	return time.Now().Format("20060102150405")
}

func (d *Daraja) password(ts string) string {
	return base64.StdEncoding.EncodeToString([]byte(d.Shortcode + d.Passkey + ts))
}

func (d *Daraja) InitiateSTK(ctx context.Context, req STKRequest) (STKResponse, error) {
	tok, err := d.getToken(ctx)
	if err != nil {
		return STKResponse{}, err
	}
	ts := d.timestamp()
	payload := map[string]any{
		"BusinessShortCode": d.Shortcode,
		"Password":           d.password(ts),
		"Timestamp":          ts,
		"TransactionType":    "CustomerPayBillOnline",
		"Amount":             req.AmountCents / 100,
		"PartyA":             req.Phone,
		"PartyB":             d.Shortcode,
		"PhoneNumber":        req.Phone,
		"CallBackURL":        d.CallbackURL,
		"AccountReference":   req.AccountReference,
		"TransactionDesc":    req.Description,
	}
	body, status, err := d.do(ctx, "POST", d.BaseURL+"/mpesa/stkpush/v1/processrequest", payload,
		map[string]string{"Authorization": "Bearer " + tok})
	if err != nil {
		return STKResponse{}, err
	}
	var r struct {
		MerchantRequestID string `json:"MerchantRequestID"`
		CheckoutRequestID string `json:"CheckoutRequestID"`
		CustomerMessage   string `json:"CustomerMessage"`
		ResponseCode       string `json:"ResponseCode"`
		ResponseDescription string `json:"ResponseDescription"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return STKResponse{}, fmt.Errorf("stkpush parse: %w (%s)", err, string(body))
	}
	if status != 200 || r.ResponseCode != "0" {
		return STKResponse{}, fmt.Errorf("stkpush %d: %s", status, r.ResponseDescription)
	}
	return STKResponse{
		MerchantRequestID: r.MerchantRequestID,
		CheckoutRequestID: r.CheckoutRequestID,
		CustomerMessage:   r.CustomerMessage,
	}, nil
}

func (d *Daraja) QuerySTK(ctx context.Context, checkoutRequestID string) (STKQueryResult, error) {
	tok, err := d.getToken(ctx)
	if err != nil {
		return STKQueryResult{}, err
	}
	ts := d.timestamp()
	payload := map[string]any{
		"BusinessShortCode": d.Shortcode,
		"Password":           d.password(ts),
		"Timestamp":          ts,
		"CheckoutRequestID":  checkoutRequestID,
	}
	body, status, err := d.do(ctx, "POST", d.BaseURL+"/mpesa/stkpushquery/v1/query", payload,
		map[string]string{"Authorization": "Bearer " + tok})
	if err != nil {
		return STKQueryResult{}, err
	}
	var r struct {
		ResponseCode      string `json:"ResponseCode"`
		ResultCode        any    `json:"ResultCode"`
		ResultDesc        string `json:"ResultDesc"`
		MpesaReceiptNumber string `json:"MpesaReceiptNumber"`
		Amount            any    `json:"Amount"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return STKQueryResult{}, fmt.Errorf("stkquery parse: %w (%s)", err, string(body))
	}
	if status != 200 || r.ResponseCode != "0" {
		return STKQueryResult{}, fmt.Errorf("stkquery %d: %s", status, r.ResultDesc)
	}
	rc := -1
	switch v := r.ResultCode.(type) {
	case float64:
		rc = int(v)
	case string:
		fmt.Sscanf(v, "%d", &rc)
	}
	var amt int64
	if v, ok := r.Amount.(float64); ok {
		amt = int64(v * 100)
	}
	return STKQueryResult{ResultCode: rc, ResultDesc: r.ResultDesc, MpesaReceiptNumber: r.MpesaReceiptNumber, AmountCents: amt}, nil
}

// CallbackResult is the parsed async webhook body (Daraja nests everything).
type CallbackResult struct {
	MerchantRequestID string
	CheckoutRequestID string
	ResultCode        int
	ResultDesc        string
	AmountCents       int64
	MpesaReceipt      string
	Phone             string
}

// ParseCallback decodes the real Daraja callback shape:
// {"Body":{"stkCallback":{"MerchantRequestID":...,"CheckoutRequestID":...,
//  "ResultCode":0,"ResultDesc":"...","CallbackMetadata":{"Item":[
//  {"Name":"Amount","Value":1},{"Name":"MpesaReceiptNumber","Value":"NLJ7RT61SV"},...]}}}}
func ParseCallback(body []byte) (CallbackResult, error) {
	var raw struct {
		Body struct {
			StkCallback struct {
				MerchantRequestID string `json:"MerchantRequestID"`
				CheckoutRequestID string `json:"CheckoutRequestID"`
				ResultCode        int    `json:"ResultCode"`
				ResultDesc        string `json:"ResultDesc"`
				CallbackMetadata  *struct {
					Item []struct {
						Name  string `json:"Name"`
						Value any    `json:"Value"`
					} `json:"Item"`
				} `json:"CallbackMetadata"`
			} `json:"stkCallback"`
		} `json:"Body"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return CallbackResult{}, err
	}
	cb := raw.Body.StkCallback
	if cb.CheckoutRequestID == "" {
		return CallbackResult{}, errors.New("callback: missing CheckoutRequestID")
	}
	res := CallbackResult{
		MerchantRequestID: cb.MerchantRequestID,
		CheckoutRequestID: cb.CheckoutRequestID,
		ResultCode:        cb.ResultCode,
		ResultDesc:        cb.ResultDesc,
	}
	if cb.CallbackMetadata != nil {
		for _, item := range cb.CallbackMetadata.Item {
			switch item.Name {
			case "Amount":
				if v, ok := item.Value.(float64); ok {
					res.AmountCents = int64(v * 100)
				}
			case "MpesaReceiptNumber":
				if v, ok := item.Value.(string); ok {
					res.MpesaReceipt = v
				}
			case "PhoneNumber":
				if v, ok := item.Value.(float64); ok {
					res.Phone = fmt.Sprintf("%.0f", v)
				}
			}
		}
	}
	return res, nil
}
