package handlers

// etims.go — v1.10.0 KRA eTIMS settings endpoints (services/etims.go).

import (
	"github.com/gin-gonic/gin"

	"posapp/internal/services"
)

// EtimsStatus (settings.manage): connection, defaults and the send queue.
func (h *H) EtimsStatus(c *gin.Context) {
	h.ok(c, h.svc(c).EtimsStatusNow())
}

// EtimsConnect (settings.manage): register this store's OSCU device with KRA.
func (h *H) EtimsConnect(c *gin.Context) {
	p := h.principal(c)
	var in services.EtimsConnectInput
	if err := c.ShouldBindJSON(&in); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	dev, err := h.svc(c).EtimsConnect(in)
	if err != nil {
		h.fail(c, 422, err.Error())
		return
	}
	h.svc(c).Audit(p.ID, p.Username, "ETIMS_CONNECTED", "settings", "", in.Env+" "+in.Tin+"/"+in.BhfID)
	h.ok(c, dev)
}

// EtimsDisable (settings.manage): stop sending new sales.
func (h *H) EtimsDisable(c *gin.Context) {
	p := h.principal(c)
	h.svc(c).EtimsDisable()
	h.svc(c).Audit(p.ID, p.Username, "ETIMS_DISABLED", "settings", "", "")
	h.ok(c, h.svc(c).EtimsStatusNow())
}

// EtimsClasses (settings.manage | products.manage): search KRA item classes.
func (h *H) EtimsClasses(c *gin.Context) {
	list, err := h.svc(c).EtimsClasses(c.Query("q"))
	if err != nil {
		h.fail(c, 422, err.Error())
		return
	}
	h.ok(c, list)
}

type etimsDefaultsBody struct {
	DefaultClsCd string `json:"defaultClsCd"`
	DefaultTax   string `json:"defaultTax"`
}

// EtimsDefaults (settings.manage): default item class + tax type.
func (h *H) EtimsDefaults(c *gin.Context) {
	var b etimsDefaultsBody
	if err := c.ShouldBindJSON(&b); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	if err := h.svc(c).EtimsSetDefaults(b.DefaultClsCd, b.DefaultTax); err != nil {
		h.fail(c, 422, err.Error())
		return
	}
	h.ok(c, h.svc(c).EtimsStatusNow())
}

// EtimsRetry (settings.manage): send failed invoices again now.
func (h *H) EtimsRetry(c *gin.Context) {
	n := h.svc(c).EtimsRetryFailed()
	go h.svc(c).EtimsTick()
	h.ok(c, gin.H{"requeued": n})
}
