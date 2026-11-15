// whatsapp_open.go — v1.6.2: launch WhatsApp on the TILL itself.
//
// The customer complaint: on the desktop app (a WebView2 window) neither
// window.open('wa.me') nor a custom-protocol navigation opens the installed
// WhatsApp — the WebView2 shell swallows both, so the share button looked
// dead on Windows machines. The Go host is a native process on the same
// machine, and it can hand whatsapp:// to the OS the same way openBrowser
// hands it a URL: WhatsApp Desktop registers the protocol, the Android/iOS
// apps answer it too. The endpoint builds the URL SERVER-SIDE from digits +
// text — the client never supplies a URL, so the OS opener cannot be pointed
// at anything but whatsapp://.
package handlers

import (
	"context"
	"log"
	"net"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type whatsAppOpenIn struct {
	Phone string `json:"phone"`
	Text  string `json:"text"`
}

// sanitizeWhatsAppDigits mirrors the frontend's waDigits and the services'
// normalizeWhatsappPhone: Kenyan numbers in any local shape become 2547…
// digits the wa.me/whatsapp:// schemes accept.
func sanitizeWhatsAppDigits(raw string) string {
	d := strings.Join(strings.FieldsFunc(raw, func(r rune) bool {
		return r < '0' || r > '9'
	}), "")
	if d == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(d, "0"):
		return "254" + d[1:]
	case strings.HasPrefix(d, "7"), strings.HasPrefix(d, "1"):
		return "254" + d
	default:
		return d
	}
}

// openWhatsAppApp hands the deep link to the OS. Windows uses
// ShellExecute (whatsapp_open_windows.go), which reports honestly when no
// app is registered for whatsapp:// — rundll32 said "ok" either way, so a
// PC without WhatsApp Desktop showed nothing and never fell back.
func openWhatsAppApp(link string, timeout time.Duration) bool {
	if runtime.GOOS == "windows" {
		return shellOpen(link)
	}
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, name, link).Run() == nil
}

// whatsAppQuery encodes like WhatsApp expects: spaces as %20 (url.Values
// writes "+", which WhatsApp Desktop showed literally in the caption).
func whatsAppQuery(q url.Values) string {
	return strings.ReplaceAll(q.Encode(), "+", "%20")
}

// fromThisMachine: the host leg only makes sense when the browser asking
// runs on the same computer as this server. A second cashier PC using the
// POS over the shop network must open WhatsApp on ITS screen, not on the
// main till's (v1.12 fix: the share "did nothing" on such PCs).
func fromThisMachine(c *gin.Context) bool {
	ip := net.ParseIP(c.RemoteIP())
	return ip != nil && ip.IsLoopback()
}

// WhatsAppOpen (orders.notify or pos.sell) opens the installed WhatsApp app
// with the customer's chat + thank-you caption preloaded. Best-effort:
// opened=false means the OS refused (WhatsApp not installed, headless
// server) and the UI falls back to its browser legs.
func (h *H) WhatsAppOpen(c *gin.Context) {
	var in whatsAppOpenIn
	if err := c.ShouldBindJSON(&in); err != nil {
		h.fail(c, 400, "expected {phone, text}")
		return
	}
	digits := sanitizeWhatsAppDigits(in.Phone)
	text := strings.TrimSpace(in.Text)
	if len(text) > 1000 {
		text = text[:1000]
	}
	q := url.Values{}
	if digits != "" {
		q.Set("phone", digits)
	}
	if text != "" {
		q.Set("text", text)
	}
	link := "whatsapp://send"
	if len(q) > 0 {
		link += "?" + whatsAppQuery(q)
	}
	if !fromThisMachine(c) {
		// the page opens the app on its own computer (deep link / wa.me)
		h.ok(c, gin.H{"opened": false, "url": link, "remote": true})
		return
	}
	opened := openWhatsAppApp(link, 4*time.Second)
	if p := h.principal(c); p != nil {
		log.Printf("whatsapp open: user %s till app %s (opened=%v)", p.Username, digits, opened)
	}
	resp := gin.H{"opened": opened, "url": link}
	if !opened {
		resp["hint"] = "WhatsApp did not open — install the WhatsApp app on this machine, or press Share again from a browser tab."
	}
	h.ok(c, resp)
}
