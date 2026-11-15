package handlers

// promotions.go — bulk SMS / WhatsApp promotions (v1.12.0).

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"posapp/internal/services"
)

func promoID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	return id, err == nil && id > 0
}

// PromoAudience (marketing.send) — POST /api/v1/promotions/audience {segment, body}
func (h *H) PromoAudience(c *gin.Context) {
	var body struct {
		Segment services.PromoSegment `json:"segment"`
		Body    string                `json:"body"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	a, err := h.svc(c).PromoAudiencePreview(body.Segment, body.Body)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, a)
}

// ListPromos (marketing.send) — GET /api/v1/promotions
func (h *H) ListPromos(c *gin.Context) {
	out, err := h.svc(c).ListPromos()
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, out)
}

// CreatePromo (marketing.send) — POST /api/v1/promotions
func (h *H) CreatePromo(c *gin.Context) {
	var body services.CreatePromoRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	out, err := h.svc(c).CreatePromo(body, h.principal(c))
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.created(c, out)
}

// GetPromo (marketing.send) — GET /api/v1/promotions/:id (with recipients)
func (h *H) GetPromo(c *gin.Context) {
	id, ok := promoID(c)
	if !ok {
		h.fail(c, 400, "bad id")
		return
	}
	p, err := h.svc(c).GetPromo(id)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	rcp, _ := h.svc(c).PromoRecipients(id)
	h.ok(c, gin.H{"campaign": p, "recipients": rcp})
}

// PromoAction (marketing.send) — POST /api/v1/promotions/:id/:action (start|pause|resume|cancel)
func (h *H) PromoAction(c *gin.Context) {
	id, ok := promoID(c)
	if !ok {
		h.fail(c, 400, "bad id")
		return
	}
	out, err := h.svc(c).PromoAction(id, c.Param("action"), h.principal(c))
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, out)
}

// PromoTest (marketing.send) — POST /api/v1/promotions/test {channel, phone, body}
func (h *H) PromoTest(c *gin.Context) {
	var body struct {
		Channel string `json:"channel"`
		Phone   string `json:"phone"`
		Body    string `json:"body"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	if err := h.svc(c).PromoTestSend(body.Channel, body.Phone, body.Body); err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, gin.H{"sent": true})
}

// PromoOptOut (marketing.send or customers.manage) — POST /api/v1/promotions/opt-out {phone}
func (h *H) PromoOptOut(c *gin.Context) {
	var body struct {
		Phone string `json:"phone" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "phone required")
		return
	}
	n, err := h.svc(c).OptOutPhone(body.Phone, h.principal(c))
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, gin.H{"customers": n})
}

// SetMarketingConsent (customers.manage) — PUT /api/v1/customers/:id/marketing {optIn}
func (h *H) SetMarketingConsent(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		h.fail(c, 400, "bad id")
		return
	}
	var body struct {
		OptIn bool `json:"optIn"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	if err := h.svc(c).SetMarketingConsent(id, body.OptIn, h.principal(c)); err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, gin.H{"optIn": body.OptIn})
}
