package handlers

// business.go — v1.9 one-cloud-many-businesses endpoints (services/business.go).

import (
	"errors"

	"github.com/gin-gonic/gin"

	"posapp/internal/services"
)

type registerBusinessBody struct {
	Name string `json:"name"`
}

// TeamRegisterBusiness (settings.manage) — give this shop its own business
// in LedgerPOS Cloud. Called at the end of first-run onboarding, and from
// Settings → Team for a shop that set up offline.
func (h *H) TeamRegisterBusiness(c *gin.Context) {
	p := h.principal(c)
	var body registerBusinessBody
	_ = c.ShouldBindJSON(&body)
	team, store, err := h.svc(c).TeamRegisterBusiness(body.Name)
	if err != nil {
		h.fail(c, 422, err.Error())
		return
	}
	h.svc(c).Audit(p.ID, p.Username, "BUSINESS_REGISTERED", "settings", "", store)
	h.ok(c, gin.H{"teamCode": team, "storeName": store})
}

// PaymentProfileGet (settings.manage) — where this store's M-Pesa money goes.
func (h *H) PaymentProfileGet(c *gin.Context) {
	h.ok(c, h.svc(c).PaymentProfileGet())
}

// PaymentProfileSet (settings.manage) — the owner sets the till/paybill
// customers pay, and optionally requests M-Pesa prompts to their own
// short code (turned on only after the platform verifies it live).
func (h *H) PaymentProfileSet(c *gin.Context) {
	p := h.principal(c)
	var body services.PaymentProfileUpdate
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	out, err := h.svc(c).PaymentProfileSet(body)
	if err != nil {
		h.fail(c, 422, err.Error())
		return
	}
	h.svc(c).Audit(p.ID, p.Username, "PAYMENT_PROFILE_SAVED", "settings", "", out.STKMode)
	h.ok(c, out)
}

// joinRefusal maps the fresh-till guard to 409 (state conflict, not bad input).
func joinRefusal(err error) int {
	if errors.Is(err, services.ErrTillHasShopData) {
		return 409
	}
	return 422
}
