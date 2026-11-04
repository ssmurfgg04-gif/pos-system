package handlers

import (
	"fmt"
	"io"
	"time"

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

// SendWhatsappNow (orders.view) — the receipt screen's Share-via-WhatsApp
// button, SERVER leg: queue one branded-IMAGE send to the customer
// through the gateway. The refusal is a 409 (a routing decision, not a
// server error) so the UI falls back to the device share paths: Web
// Share sheet, or download + wa.me chat. Cashiers may re-send — "I
// didn't get it" is the button's reason to exist; every attempt lands in
// the Settings queue list and the audit log.
func (h *H) SendWhatsappNow(c *gin.Context) {
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	order, err := h.svc(c).GetOrderScoped(h.principal(c), id)
	if err != nil {
		h.fail(c, 404, "order not found")
		return
	}
	phone, fallback, err := h.svc(c).ResendWhatsappReceipt(order)
	if err != nil {
		if fallback {
			h.failWith(c, 409, gin.H{"error": err.Error(), "fallback": true})
		} else {
			h.fail(c, 409, err.Error())
		}
		return
	}
	p := h.principal(c)
	h.svc(c).Audit(p.ID, p.Username, "WHATSAPP_MANUAL_SEND", "order", fmt.Sprint(order.ID), phone)
	h.ok(c, gin.H{"queued": true, "phone": phone})
}

// VerifyReceipt (settings.manage) — the owner's authenticity check: upload
// a receipt image a customer sent "as proof" and the server reads its
// invisible watermark. Verifies only images THIS shop actually issued,
// unmodified (edited amounts, AI-generated fakes and foreign shops fail).
func (h *H) VerifyReceipt(c *gin.Context) {
	fh, err := c.FormFile("file")
	if err != nil {
		h.fail(c, 400, "upload the receipt image as multipart field 'file'")
		return
	}
	f, err := fh.Open()
	if err != nil {
		h.fail(c, 400, "cannot read upload")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 12<<20))
	if err != nil {
		h.fail(c, 400, "cannot read upload")
		return
	}
	wm, ok, reason := h.svc(c).VerifyReceiptImage(data)
	if !ok {
		h.fail(c, 422, reason)
		return
	}
	out := gin.H{
		"valid":      true,
		"layer":      wm.Layer,
		"orderId":    wm.OrderID,
		"totalCents": wm.TotalCents,
		"issuedAt":   time.Unix(wm.IssuedUnix, 0).Format(time.RFC3339),
	}
	if o, err := h.svc(c).GetOrder(wm.OrderID); err == nil {
		out["number"] = o.Number
		out["orderStatus"] = o.Status
		// The check that matters: does the watermarked total still match
		// the order as it exists NOW? A mismatch means doctored evidence.
		out["totalMatchesOrder"] = o.TotalCents == wm.TotalCents
		// What the owner should DO with the verdict, stated plainly:
		// a VOIDED receipt is proof of nothing; the watermarked total is
		// authoritative — a picture showing any other amount was edited.
		out["usable"] = o.Status == "PAID"
		if o.Status != "PAID" {
			out["note"] = "This receipt was issued against a " + o.Status + " order — it is not proof of payment."
		} else if o.TotalCents != wm.TotalCents {
			out["note"] = "The watermarked total no longer matches the order — doctored evidence."
		} else {
			out["note"] = "Trusted total: " + fmt.Sprint(o.TotalCents) + " cents. If the picture shows any other amount, it was edited after issue."
		}
	}
	p := h.principal(c)
	num, _ := out["number"].(string)
	h.svc(c).Audit(p.ID, p.Username, "WHATSAPP_VERIFY", "order", fmt.Sprint(wm.OrderID), num)
	h.ok(c, out)
}
