package paystack

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// withFakeAPI points the client at a fake Paystack server for the duration
// of the test. The handler records the last request for payload assertions.
func withFakeAPI(t *testing.T, status int, respond func(w http.ResponseWriter)) (lastBody map[string]any) {
	t.Helper()
	lastBody = map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		_ = json.Unmarshal(raw, &lastBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if respond != nil {
			respond(w)
		}
	}))
	t.Cleanup(srv.Close)
	prev := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = prev })
	return lastBody
}

// TestInitializeCarriesServerOwnedAmountAndPhone pins the wire contract the
// till depends on: the charge is initialized SERVER-side with the full
// amount in subunits and the customer phone captured in the POS. The popup
// later resumes this transaction via its access code — the amount never
// comes from the client, which is what fixes "Transaction amount not set".
func TestInitializeCarriesServerOwnedAmountAndPhone(t *testing.T) {
	body := withFakeAPI(t, http.StatusOK, func(w http.ResponseWriter) {
		w.Write([]byte(`{"status":true,"message":"Authorization URL created","data":{"authorization_url":"https://checkout.paystack.com/abc","access_code":"ACCESS_1","reference":"LP-1"}}`))
	})
	c := NewClient("sk_test_xxxxxxxxxxxxxxxxxxxx")
	res, err := c.Initialize(InitializeRequest{
		Email:     "customer+ord@ledgerpos.app",
		Phone:     "254712345678",
		Amount:    60000, // KES 600.00 in pesewas
		Currency:  "KES",
		Reference: "LP-ORD100-ab12",
	})
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if res.AccessCode != "ACCESS_1" || res.AuthorizationURL == "" || res.Reference != "LP-1" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if got := body["amount"]; got != float64(60000) {
		t.Fatalf("amount must be sent in subunits, got %v", got)
	}
	if got := body["phone"]; got != "254712345678" {
		t.Fatalf("customer phone must ride the transaction, got %v", got)
	}
	if got := body["currency"]; got != "KES" {
		t.Fatalf("currency must be sent, got %v", got)
	}
}

// TestInitializeOmitsEmptyPhone: email-only checkouts (no phone captured)
// must not send an empty phone field at all.
func TestInitializeOmitsEmptyPhone(t *testing.T) {
	body := withFakeAPI(t, http.StatusOK, func(w http.ResponseWriter) {
		w.Write([]byte(`{"status":true,"data":{"authorization_url":"https://x","access_code":"A2","reference":"R2"}}`))
	})
	c := NewClient("sk_test_xxxxxxxxxxxxxxxxxxxx")
	if _, err := c.Initialize(InitializeRequest{Email: "e@x.app", Amount: 100, Currency: "KES", Reference: "R2"}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if _, present := body["phone"]; present {
		t.Fatalf("empty phone must be omitted, got %v", body["phone"])
	}
}

// TestInitializeRejectsBadKey: a structurally invalid secret fails closed
// before any HTTP call.
func TestInitializeRejectsBadKey(t *testing.T) {
	withFakeAPI(t, http.StatusOK, func(w http.ResponseWriter) {
		t.Fatal("no request should be made with an invalid key")
	})
	c := NewClient("nope")
	if _, err := c.Initialize(InitializeRequest{Email: "e@x.app", Amount: 100}); err == nil {
		t.Fatal("expected error for invalid key")
	}
}
