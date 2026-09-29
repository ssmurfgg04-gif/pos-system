package services

// payments_wirephone_test.go — pins the E.164 wire format for Paystack's
// Kenya mobile-money APIs. Verified against the LIVE /charge endpoint
// (2026-09-29): Paystack rejects the bare 2547… form ("Invalid phone
// number format"); only "+2547…" reaches the customer's phone.

import "testing"

func TestPaystackWirePhoneKe(t *testing.T) {
	cases := []struct{ in, want string }{
		{"254745000111", "+254745000111"},
		{"254122345678", "+254122345678"},
		{"0745000111", "0745000111"},      // non-canonical input passes through — callers validate first
		{"", ""},                          // empty stays empty (popup path allows empty phone)
		{"+254745000111", "+254745000111"}, // already E.164 — no double prefix
		{"254", "254"},                     // not a phone — untouched
	}
	for _, c := range cases {
		if got := paystackWirePhoneKe(c.in); got != c.want {
			t.Errorf("paystackWirePhoneKe(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPaystackMpesaSTKWirePhone(t *testing.T) {
	// The full boundary: whatever the cashier typed, the canonical form is
	// 2547… and the wire form MUST be +2547….
	typed := []string{"0745000111", "254745000111", "+254745000111", "745000111"}
	for _, in := range typed {
		canonical := paystackPhoneKe(in)
		if !mpesaPhoneOK(canonical) {
			t.Fatalf("input %q did not validate: %q", in, canonical)
		}
		wire := paystackWirePhoneKe(canonical)
		if wire != "+254745000111" {
			t.Errorf("input %q → wire %q, want +254745000111", in, wire)
		}
	}
}
