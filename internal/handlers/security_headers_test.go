package handlers_test

import (
	"net/http"
	"strings"
	"testing"
)

// secureHeaders (router.go) is the browser-facing defense layer: the CSP
// is the XSS backstop that makes the localStorage session token acceptable
// (no third-party script origin can run, so nothing injected can read
// storage). This pins the headers so a refactor cannot silently drop them.
func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	engine, _, _, _ := newTestServer(t)

	w := do(t, engine, "GET", "/api/v1/health", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("health: %d", w.Code)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("Content-Security-Policy missing")
	}
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"frame-ancestors 'self'",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q (got %q)", want, csp)
		}
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("X-Content-Type-Options must be nosniff")
	}
	if w.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Error("Referrer-Policy must be no-referrer")
	}
	// The M-Pesa callback (public, no auth) must carry the headers too —
	// it is exactly the endpoint an attacker poke would reach first.
	w2 := do(t, engine, "POST", "/api/v1/payments/mpesa/callback", "", map[string]any{"Body": map[string]any{}})
	if w2.Header().Get("Content-Security-Policy") == "" {
		t.Error("public callback missing CSP")
	}
}
