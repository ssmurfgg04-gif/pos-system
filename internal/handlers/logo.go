package handlers

import (
	"bytes"
	"io"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"

	"posapp/internal/services"
)

// Brand logo: a shop logo uploaded from Settings → Store, shown on the
// login screen and topbar instead of the initial letter. Stored as a plain
// file next to the database (data dir — the desktop process chdir's there),
// tracked by the brand_logo setting flag. PNG or JPEG only, 2 MB max;
// magic bytes are sniffed because extensions lie.
const (
	logoFileName = "brand-logo.png"
	logoMaxBytes = 2 << 20
)

var (
	pngMagic  = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	jpegMagic = []byte{0xFF, 0xD8, 0xFF}
)

// logoContentType sniffs the image format; empty means "not an image".
func logoContentType(data []byte) string {
	if bytes.HasPrefix(data, pngMagic) {
		return "image/png"
	}
	if bytes.HasPrefix(data, jpegMagic) {
		return "image/jpeg"
	}
	return ""
}

// UploadLogo (settings.manage) — multipart `logo` file. Replaces any
// existing logo atomically (write temp + rename).
func (h *H) UploadLogo(c *gin.Context) {
	p := h.principal(c)
	file, err := c.FormFile("logo")
	if err != nil {
		h.fail(c, 400, "logo file required")
		return
	}
	if file.Size > logoMaxBytes {
		h.fail(c, 400, "logo too large (2 MB max)")
		return
	}
	src, err := file.Open()
	if err != nil {
		h.fail(c, 400, "cannot read logo file")
		return
	}
	defer src.Close()
	data, err := io.ReadAll(io.LimitReader(src, logoMaxBytes+1))
	if err != nil {
		h.fail(c, 400, "cannot read logo file")
		return
	}
	if logoContentType(data) == "" {
		h.fail(c, 400, "logo must be a PNG or JPEG image")
		return
	}
	tmp, err := os.CreateTemp("", "brand-logo-*")
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		h.fail(c, 500, err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		h.fail(c, 500, err.Error())
		return
	}
	if err := os.Rename(tmpName, logoFileName); err != nil {
		os.Remove(tmpName)
		h.fail(c, 500, err.Error())
		return
	}
	if err := h.Settings.Set("brand_logo", "1"); err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	h.Svc.Audit(p.ID, p.Username, "BRAND_LOGO_UPDATED", "settings", "", "brand_logo")
	h.Hub.BroadcastJSON(services.EventSettingsUpdate, h.Settings.Branding())
	h.ok(c, gin.H{"brand_logo_url": "/api/v1/settings/logo"})
}

// DeleteLogo (settings.manage) — removes the file and clears the flag.
// Missing file is fine (already gone).
func (h *H) DeleteLogo(c *gin.Context) {
	p := h.principal(c)
	if err := os.Remove(logoFileName); err != nil && !os.IsNotExist(err) {
		h.fail(c, 500, err.Error())
		return
	}
	if err := h.Settings.Set("brand_logo", ""); err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	h.Svc.Audit(p.ID, p.Username, "BRAND_LOGO_REMOVED", "settings", "", "brand_logo")
	h.Hub.BroadcastJSON(services.EventSettingsUpdate, h.Settings.Branding())
	h.ok(c, gin.H{"brand_logo_url": ""})
}

// GetLogo — public (the login screen shows it pre-auth, like branding).
func (h *H) GetLogo(c *gin.Context) {
	if h.Settings.Get("brand_logo") != "1" {
		c.Status(http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(logoFileName)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	ct := logoContentType(data)
	if ct == "" {
		c.Status(http.StatusNotFound)
		return
	}
	c.Data(http.StatusOK, ct+"; charset=binary", data)
}
