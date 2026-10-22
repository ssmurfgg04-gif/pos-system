package mpesa

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBuniCloudInitiateMapsEdgeResponse — the till POSTs device identity +
// push payload to buni-stkpush and maps the answer onto STKResponse.
// Verifies the exact fields the edge function contract requires.
func TestBuniCloudInitiateMapsEdgeResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/functions/v1/buni-stkpush", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("apikey") == "" || r.Header.Get("Authorization") == "" {
			t.Errorf("edge call missing apikey/Authorization headers")
		}
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		if in["p_device_id"] != "DEV-1" || in["p_secret_hash"] != "HASH-1" {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "device not recognized"})
			return
		}
		if in["phone"] != "254711000111" || in["amount"] != float64(11600) {
			t.Errorf("push payload = %v", in)
		}
		if in["invoice_ref"] != "ORD2" || in["client_uuid"] != "uuid-9" {
			t.Errorf("push payload refs = %v", in)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"ok":                 true,
			"merchant_request_id": "9e99-4e1f",
			"checkout_request_id": "ws_CO_TEST1",
			"customer_message":   "Success. Request accepted for processing",
			"invoice_number":     "7853959-ORD2",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	b := NewBuniCloud(srv.URL, "anon-key", "DEV-1", "HASH-1")
	resp, err := b.InitiateSTK(context.Background(), STKRequest{
		OrderID: 1, PaymentID: 2, Phone: "254711000111", AmountCents: 11600,
		AccountReference: "ORD2", Description: "Payment ORD2", ClientUUID: "uuid-9",
	})
	if err != nil {
		t.Fatalf("InitiateSTK: %v", err)
	}
	if resp.CheckoutRequestID != "ws_CO_TEST1" || resp.MerchantRequestID != "9e99-4e1f" {
		t.Fatalf("response mapping = %+v", resp)
	}
	if resp.CustomerMessage == "" {
		t.Fatal("customer message lost")
	}
}

// TestBuniCloudInitiateErrorsSurface — every failure mode is loud: no
// config, device rejection, and edge errors propagate to the caller (the
// till fails the leg and the cashier re-pushes; never a silent pending).
func TestBuniCloudInitiateErrorsSurface(t *testing.T) {
	missing := NewBuniCloud("", "", "", "")
	if _, err := missing.InitiateSTK(context.Background(), STKRequest{}); err == nil {
		t.Fatal("unconfigured provider must error")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/functions/v1/buni-stkpush", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "kcb 400: Invalid PhoneNumber"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	b := NewBuniCloud(srv.URL, "anon", "DEV", "HASH")
	_, err := b.InitiateSTK(context.Background(), STKRequest{Phone: "254711000111", AmountCents: 100})
	if err == nil || !strings.Contains(err.Error(), "Invalid PhoneNumber") {
		t.Fatalf("edge error must surface, got %v", err)
	}
}

// TestBuniQueryUnsupported — Buni has no STK query API (verified against
// the official spec + live production). QuerySTK must say so explicitly so
// sweepers never poll it expecting Daraja semantics.
func TestBuniQueryUnsupported(t *testing.T) {
	b := NewBuniCloud("http://unused.test", "anon", "DEV", "HASH")
	_, err := b.QuerySTK(context.Background(), "ws_CO_X")
	if err == nil {
		t.Fatal("QuerySTK must be unsupported")
	}
	var target ErrQueryUnsupported
	if !errors.As(err, &target) {
		t.Fatalf("error %v is not ErrQueryUnsupported", err)
	}
	if b.Name() != "buni" {
		t.Fatalf("Name = %q", b.Name())
	}
}

// TestBuniPrefixHelpers — the discriminator helpers.
func TestBuniPrefixHelpers(t *testing.T) {
	if got := BuniCheckoutPrefixFor("ws_CO_1"); got != "BUNI-ws_CO_1" {
		t.Fatalf("prefixed = %q", got)
	}
}
