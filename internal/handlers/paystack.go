package handlers

// paystack.go — HTTP layer for Paystack card / mobile-money checkout.
// Handlers are thin: all business rules live in services, all money
// transitions go through the same completePayment guard as M-Pesa.

import (
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"posapp/internal/auth"
)

// paystackRL limiter: initializing checkout is one call per sale attempt;
// tight enough to stop reference-guessing, loose enough for busy rushes.
var paystackRL = auth.NewRateLimiter(time.Minute, 30)

func paystackAllowed(key string) bool { return paystackRL.Allow(key) }

// PaymentConfig (authed) — tender-button capabilities for the till.
func (h *H) PaymentConfig(c *gin.Context) {
	h.ok(c, h.svc(c).PaymentConfig())
}

// PaystackInit (pos.sell) opens (or re-opens) checkout for a pending
// order. Returns the PUBLIC key + access_code for the popup, plus the
// redirect URL fallback.
func (h *H) PaystackInit(c *gin.Context) {
	p := h.principal(c)
	if !paystackAllowed("init:" + p.Username) {
		h.fail(c, 429, "too many checkout attempts — wait a moment")
		return
	}
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	_ = c.ShouldBindJSON(&body) // email optional
	order, init, err := h.svc(c).PaystackInit(id, body.Email, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, gin.H{"order": order, "paystack": init})
}

// PaystackVerify (pos.sell) completes a payment from the popup callback.
// The reference is re-verified server-side — client callbacks are hints,
// never proof.
func (h *H) PaystackVerify(c *gin.Context) {
	p := h.principal(c)
	if !paystackAllowed("verify:" + p.Username) {
		h.fail(c, 429, "too many verification attempts — wait a moment")
		return
	}
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body struct {
		Reference string `json:"reference" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "reference required")
		return
	}
	order, err := h.svc(c).PaystackVerify(id, body.Reference, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, order)
}

// PaystackWebhook (PUBLIC) — server-to-server charge.success events.
// Reads the RAW body for the HMAC-SHA512 signature check, caps the body
// size, and always answers 200 unless Paystack should retry.
func (h *H) PaystackWebhook(c *gin.Context) {
	if h.CallbackRL.Allow("paystack-webhook:" + c.ClientIP()) == false {
		c.Status(http.StatusTooManyRequests)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unreadable body"})
		return
	}
	signature := c.GetHeader("x-paystack-signature")
	if err := h.svc(c).HandlePaystackWebhook(raw, signature); err != nil {
		// Invalid signatures: 401. Transient processing failures: 500 so
		// Paystack retries with backoff.
		if err.Error() == "invalid webhook signature" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "processing failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
