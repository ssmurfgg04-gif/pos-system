package handlers_test

// whatsapp_open_test.go — v1.6.2: the host-side WhatsApp launcher endpoint.
// Permission: any-of orders.notify/pos.sell (the cyber-café till complaint —
// roles created before orders.notify existed hold pos.sell). Payload: the
// deep link is built SERVER-side from digits + caption so the OS opener can
// only ever be pointed at whatsapp:// — and Kenyan numbers normalize exactly
// like the frontend's waDigits / the services' normalizeWhatsappPhone.
import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestWhatsAppOpenAnyOfPermissions(t *testing.T) {
	engine, _, cashier, designer := newTestServer(t)

	// The till cashier (pos.sell) may open WhatsApp on the host…
	w := doJSON(t, engine, http.MethodPost, "/api/v1/whatsapp-open", cashier,
		map[string]any{"phone": "0711 222 333", "text": "Thank you for shopping with Creative Divine!"})
	if w.Code != 200 {
		t.Fatalf("cashier whatsapp-open: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Data struct {
			Opened bool   `json:"opened"`
			URL    string `json:"url"`
			Remote bool   `json:"remote"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// …and the deep link is built SERVER-side: Kenyan digits + caption.
	// (opened may be either bool on a headless CI box — the OS opener is
	// best-effort by design; the URL is the contract.)
	// Spaces travel as %20: WhatsApp Desktop shows a "+" literally.
	want := "whatsapp://send?phone=254711222333&text=Thank%20you%20for%20shopping%20with%20Creative%20Divine%21"
	if out.Data.URL != want {
		t.Fatalf("deep link malformed:\n got %q\nwant %q", out.Data.URL, want)
	}

	// The test client is not on the server's own machine (httptest's
	// 192.0.2.1): a cashier PC on the shop network must open WhatsApp on
	// its own screen, so the host never launches it here.
	if out.Data.Opened || !out.Data.Remote {
		t.Fatalf("a remote PC must get opened=false remote=true, got %+v", out.Data)
	}

	// Designer holds neither orders.notify nor pos.sell → the OS opener is
	// not an open relay for them.
	w = doJSON(t, engine, http.MethodPost, "/api/v1/whatsapp-open", designer,
		map[string]any{"phone": "254711222333", "text": "hi"})
	if w.Code != 403 {
		t.Fatalf("designer whatsapp-open must 403, got %d", w.Code)
	}
}
