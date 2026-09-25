package paystack

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"testing"
)

func TestCheckSignature(t *testing.T) {
	secret := "sk_test_derelict_2ab7cdef1234567890abcdef"
	body := []byte(`{"event":"charge.success","data":{"reference":"LP-ORD1-abc","amount":500000,"status":"success"}}`)

	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(body)
	valid := hex.EncodeToString(mac.Sum(nil))

	cases := []struct {
		name string
		sig  string
		want bool
	}{
		{"valid lowercase", valid, true},
		{"valid uppercase", upper(valid), true},
		{"valid padded", " " + valid + "\n", true},
		{"tampered body sig", signBytes([]byte("other body"), secret), false},
		{"wrong secret", signBytes(body, "sk_test_other_key_000000000000000"), false},
		{"empty sig", "", false},
		{"not hex", "zzzz-not-hex-at-all", false},
		{"truncated", valid[:40], false},
	}
	for _, tc := range cases {
		if got := CheckSignature(body, tc.sig, secret); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	// Empty secret never validates (misconfiguration fails closed).
	if CheckSignature(body, valid, "") {
		t.Error("empty secret must not validate")
	}
}

func upper(s string) string {
	out := []rune(s)
	for i, r := range out {
		if i%2 == 0 && r >= 'a' && r <= 'f' {
			out[i] = r - 32
		}
	}
	return string(out)
}

func signBytes(body []byte, secret string) string {
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestParseWebhook(t *testing.T) {
	ok := []byte(`{"event":"charge.success","data":{"reference":"LP-1","status":"success","amount":250000,"currency":"KES","channel":"card"}}`)
	ev, err := ParseWebhook(ok)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Event != "charge.success" || ev.Data.Reference != "LP-1" || ev.Data.Amount != 250000 || ev.Data.Channel != "card" {
		t.Errorf("unexpected decoded event: %+v", ev)
	}
	if _, err := ParseWebhook([]byte("not json")); err == nil {
		t.Error("garbage payload must error")
	}
	if _, err := ParseWebhook([]byte(`{"no_event":true}`)); err == nil {
		t.Error("missing event must error")
	}
}

func TestKeyShape(t *testing.T) {
	if !ValidKey("sk_live_" + hex20()) {
		t.Error("valid live secret rejected")
	}
	if ValidKey("pk_live_" + hex20()) {
		t.Error("public key must not pass as secret")
	}
	if ValidKey("sk_") || ValidKey("") {
		t.Error("short keys must not pass")
	}
	if !IsPublicKey("pk_live_" + hex20()) {
		t.Error("valid public key rejected")
	}
	if IsPublicKey("sk_live_" + hex20()) {
		t.Error("secret key must not pass as public")
	}
}

func hex20() string {
	b := make([]byte, 20)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return string(b)
}
