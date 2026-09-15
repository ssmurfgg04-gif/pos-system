package handlers

import (
        "github.com/gin-gonic/gin"

        "posapp/internal/models"
        "posapp/internal/settings"
        "posapp/internal/services"
)

// GetSettings (settings.manage) — secrets masked as "__SET__".
func (h *H) GetSettings(c *gin.Context) {
        h.ok(c, h.Settings.Snapshot())
}

type settingsBody struct {
        Values map[string]string `json:"values" binding:"required"`
}

// UpdateSettings (settings.manage). Masked values keep their secret. Every
// value runs through a key allowlist — no arbitrary writes. A masked echo
// ("__SET__") of a key the API cannot write (e.g. jwt_secret echoed by an
// older frontend build) is skipped silently instead of failing the whole
// save — saving the form must never error on read-only secrets.
func (h *H) UpdateSettings(c *gin.Context) {
        p := h.principal(c)
        var body settingsBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "values map required")
                return
        }
        allowed := settings.AllowedKeys()
        for k, v := range body.Values {
                if !allowed[k] {
                        if settings.IsMaskToken(v) {
                                continue // untouched masked echo of a read-only key
                        }
                        h.fail(c, 400, "unknown setting key: "+k)
                        return
                }
                if len(v) > 500 {
                        h.fail(c, 400, "value too long for key: "+k)
                        return
                }
        }
        changed, err := h.Settings.Update(body.Values)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.Svc.Audit(p.ID, p.Username, "SETTINGS_UPDATED", "settings", "", joinKeys(changed))
        h.Hub.BroadcastJSON(services.EventSettingsUpdate, h.Settings.Branding())
        h.ok(c, h.Settings.Snapshot())
}

func joinKeys(keys []string) string {
        out := ""
        for i, k := range keys {
                if i > 0 {
                        out += ", "
                }
                out += k
        }
        return out
}

// TestPrint (printer.test) — sends a test receipt to the configured target.
func (h *H) TestPrint(c *gin.Context) {
        p := h.principal(c)
        if err := h.Printer.TestPrint(); err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        h.Svc.Audit(p.ID, p.Username, "PRINTER_TESTED", "printer", h.Settings.Get("printer_target"), "")
        h.ok(c, gin.H{"printed": true})
}

// KickDrawer (printer.test) — pulses the cash drawer for a till test.
func (h *H) KickDrawer(c *gin.Context) {
        p := h.principal(c)
        if err := h.Printer.Kick(); err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        h.Svc.Audit(p.ID, p.Username, "DRAWER_KICKED", "printer", h.Settings.Get("printer_target"), "")
        h.ok(c, gin.H{"kicked": true})
}

// ListPrintJobs (printer.test) — queue diagnostics.
func (h *H) ListPrintJobs(c *gin.Context) {
        jobs := h.Printer.ListJobs(50)
        if jobs == nil {
                jobs = []models.PrintJob{}
        }
        h.ok(c, jobs)
}
