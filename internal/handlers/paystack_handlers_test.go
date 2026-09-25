package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPaystackWebhookRejectsBadSignature(t *testing.T) {
	engine := newTestEngine(t)
	// Public route, guarded by the HMAC check. No secret configured →
	// the handler must fail closed (never 200), with or without a header.
	body := map[string]any{
		"event": "charge.success",
		"data":  map[string]any{"reference": "LP-x", "status": "success", "amount": 100},
	}
	w := do(t, engine, "POST", "/api/v1/payments/paystack/webhook", "", body)
	if w.Code == http.StatusOK {
		t.Fatalf("webhook without configured secret or signature must not succeed: %d", w.Code)
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/v1/payments/paystack/webhook", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-paystack-signature", "deadbeef")
	w2 := httptest.NewRecorder()
	engine.ServeHTTP(w2, req)
	if w2.Code == http.StatusOK {
		t.Fatalf("forged signature must not succeed: %d", w2.Code)
	}
}

func TestPaystackInitWithoutKeys(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)
	// Enable paystack but leave keys blank → init must fail with a clear error.
	w := do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{
		"values": map[string]string{"paystack_enabled": "true"},
	})
	if w.Code != 200 {
		t.Fatalf("enable paystack: %d", w.Code)
	}
	// mpesa manual checkout yields a PENDING order without provider calls.
	w = do(t, engine, "POST", "/api/v1/orders/checkout", admin, map[string]any{
		"items":         []map[string]any{{"productId": 1, "qty": 1}},
		"paymentMethod": "mpesa",
		"paymentMode":   "manual",
		"clientUuid":    "psk-init-1",
	})
	if w.Code != 201 {
		t.Fatalf("pending order: %d %s", w.Code, w.Body.String())
	}
	order := dataMap(t, w)["id"].(float64)
	w = do(t, engine, "POST", "/api/v1/orders/"+itoa64(int64(order))+"/paystack/init", admin, map[string]any{"email": ""})
	if w.Code < 400 || w.Code >= 500 {
		t.Fatalf("init without keys should 4xx, got %d %s", w.Code, w.Body.String())
	}
}

func TestPaystackCheckoutRejectsInvalidMethod(t *testing.T) {
	engine, cashier, _, _ := newTestServer(t)
	w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
		"items":         []map[string]any{{"productId": 1, "qty": 1}},
		"paymentMethod": "bitcoin",
	})
	if w.Code != 400 {
		t.Fatalf("invalid method should 400, got %d", w.Code)
	}
}

func TestPaymentConfigMasksSecrets(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)
	w := do(t, engine, "GET", "/api/v1/payments/config", admin, nil)
	if w.Code != 200 {
		t.Fatalf("payment config: %d", w.Code)
	}
	if body := w.Body.String(); strings.Contains(body, "sk_live") || strings.Contains(body, "sk_test") {
		t.Fatalf("payment config leaked a secret-shaped key: %s", body)
	}
	// Settings snapshot masks the secret key.
	w = do(t, engine, "GET", "/api/v1/settings", admin, nil)
	if w.Code != 200 {
		t.Fatalf("settings: %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, `"paystack_secret_key":"sk_`) {
		t.Fatalf("paystack secret key leaked: %s", body)
	}
}

var _ = gin.TestMode
