package handlers

import (
	"github.com/gin-gonic/gin"
)

// WhatsApp receipt queue — admin endpoints (Settings → WhatsApp). The
// automatic sends themselves ride payment completion (no endpoint, no
// cashier step); these manage the aftermath: the failed-sends list, a
// retry, and a one-time setup test message. settings.manage keeps all of
// it on the Admin side by design.

// ListWhatsappJobs (settings.manage) — recent delivery jobs, newest
// first. This is the "failed sends in one place" view.
func (h *H) ListWhatsappJobs(c *gin.Context) {
	limit := 0
	if v := c.Query("limit"); v != "" {
		n := 0
		for _, ch := range v {
			if ch < '0' || ch > '9' {
				n = 0
				break
			}
			n = n*10 + int(ch-'0')
		}
		limit = n
	}
	jobs, err := h.svc(c).ListWhatsappJobs(limit)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, gin.H{"jobs": jobs})
}

type retryWhatsappReq struct{}

// RetryWhatsappJob (settings.manage) — re-queue one FAILED send.
func (h *H) RetryWhatsappJob(c *gin.Context) {
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	if err := h.svc(c).RetryWhatsappJob(id); err != nil {
		h.mapErr(c, err)
		return
	}
	p := h.principal(c)
	h.svc(c).Audit(p.ID, p.Username, "WHATSAPP_RETRY", "whatsapp_job", "", "")
	h.ok(c, gin.H{"retried": true})
}

type testWhatsappReq struct {
	Phone string `json:"phone"`
}

// TestWhatsapp (settings.manage) — one-time setup check: send a text to
// the given number (usually the shop's own) through whapi.cloud.
func (h *H) TestWhatsapp(c *gin.Context) {
	var req testWhatsappReq
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, 400, "phone required")
		return
	}
	phone, err := h.svc(c).WhatsappTestSend(req.Phone)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	p := h.principal(c)
	h.svc(c).Audit(p.ID, p.Username, "WHATSAPP_TEST", "settings", "", phone)
	h.ok(c, gin.H{"sent": true, "phone": phone})
}
