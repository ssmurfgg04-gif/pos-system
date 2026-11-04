// v160_receipt_endpoints_test.go — v1.6.0 endpoints through the real
// router: the cashier-filter visibility rule, the receipt-image endpoint
// feeding the WhatsApp verify roundtrip, and the design-job create gate
// following its one true rule (orders.assign) at every layer.
package handlers_test

import (
	"bytes"
	"encoding/json"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestOrdersCashiersEndpoint — the Orders page cashier filter's options:
// full-order viewers get the roster, scoped staff get 403 (their list is
// their own sales; the picker would be noise — and an info leak).
func TestOrdersCashiersEndpoint(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	w := do(t, engine, "GET", "/api/v1/orders/cashiers", admin, nil)
	if w.Code != 200 {
		t.Fatalf("admin cashiers: %d %s", w.Code, w.Body.String())
	}
	var env struct {
		Data []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data) < 2 {
		t.Fatalf("expected the seeded staff roster, got %d entries", len(env.Data))
	}
	for _, cs := range env.Data {
		if cs.ID == 0 || cs.Name == "" {
			t.Fatalf("cashier entry missing id/name: %+v", cs)
		}
	}

	w = do(t, engine, "GET", "/api/v1/orders/cashiers", cashier, nil)
	if w.Code != 403 {
		t.Fatalf("cashier cashiers: %d, want 403 (%s)", w.Code, w.Body.String())
	}
}

// TestReceiptImageAndVerifyRoundtrip — the full owner loop: pull the
// branded image the Share button shares, hand it back to the verify
// endpoint like a customer "proof" would be, and get a genuine verdict.
func TestReceiptImageAndVerifyRoundtrip(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)
	orderID := checkoutCash(t, engine, admin)

	w := do(t, engine, "GET", "/api/v1/orders/"+strconv.FormatInt(orderID, 10)+"/receipt-image", admin, nil)
	if w.Code != 200 {
		t.Fatalf("receipt-image: %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content type %q, want image/png", ct)
	}
	if _, err := png.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
		t.Fatalf("endpoint returned a non-PNG body: %v", err)
	}

	// Verify the SAME bytes through the settings-manage endpoint.
	vw := verifyReceiptUpload(t, engine, admin, w.Body.Bytes())
	if vw.Code != 200 {
		t.Fatalf("verify: %d %s", vw.Code, vw.Body.String())
	}
	var venv struct {
		Data struct {
			Valid             bool   `json:"valid"`
			OrderID           int64  `json:"orderId"`
			Number            string `json:"number"`
			OrderStatus       string `json:"orderStatus"`
			TotalCents        int64  `json:"totalCents"`
			TotalMatchesOrder bool   `json:"totalMatchesOrder"`
			Usable            bool   `json:"usable"`
			Note              string `json:"note"`
		} `json:"data"`
	}
	if err := json.Unmarshal(vw.Body.Bytes(), &venv); err != nil {
		t.Fatalf("verify decode: %v", err)
	}
	if !venv.Data.Valid || venv.Data.OrderID != orderID || !venv.Data.TotalMatchesOrder {
		t.Fatalf("verify verdict wrong: %+v", venv.Data)
	}
	if venv.Data.Usable != true {
		t.Fatalf("a PAID order's receipt must be usable: %+v", venv.Data)
	}
	if venv.Data.OrderStatus != "PAID" || venv.Data.Number == "" {
		t.Fatalf("verify missed the order join: %+v", venv.Data)
	}

	// The verify endpoint is settings.manage — a cashier gets 403.
	vw = verifyReceiptUpload(t, engine, cashier, w.Body.Bytes())
	if vw.Code != 403 {
		t.Fatalf("cashier verify: %d, want 403", vw.Code)
	}
	// Garbage upload → readable 422, not a 500.
	vw = verifyReceiptUpload(t, engine, admin, []byte("not an image at all"))
	if vw.Code != 422 {
		t.Fatalf("garbage verify: %d, want 422 (%s)", vw.Code, vw.Body.String())
	}

	// A VOIDED order's genuine receipt verifies as issued but is NOT
	// usable as payment proof — the note says so in plain words.
	if w2 := do(t, engine, "POST", "/api/v1/orders/"+strconv.FormatInt(orderID, 10)+"/void", admin, map[string]any{"reason": "smoke verify"}); w2.Code != 200 {
		t.Fatalf("void: %d %s", w2.Code, w2.Body.String())
	}
	reissued := do(t, engine, "GET", "/api/v1/orders/"+strconv.FormatInt(orderID, 10)+"/receipt-image", admin, nil)
	if reissued.Code != 200 {
		t.Fatalf("voided receipt-image: %d", reissued.Code)
	}
	vw = verifyReceiptUpload(t, engine, admin, reissued.Body.Bytes())
	if vw.Code != 200 {
		t.Fatalf("voided verify: %d %s", vw.Code, vw.Body.String())
	}
	if err := json.Unmarshal(vw.Body.Bytes(), &venv); err != nil {
		t.Fatalf("voided verify decode: %v", err)
	}
	if venv.Data.Usable || !strings.Contains(venv.Data.Note, "not proof of payment") {
		t.Fatalf("voided receipt must be unusable with a plain note: %+v", venv.Data)
	}
}

func verifyReceiptUpload(t *testing.T, engine *gin.Engine, token string, fileBytes []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "proof.png")
	if err != nil {
		t.Fatalf("form file: %v", err)
	}
	if _, err := fw.Write(fileBytes); err != nil {
		t.Fatalf("write file: %v", err)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/api/v1/whatsapp/verify-receipt", &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

// TestDesignJobCreateFollowsDelegationRule — POST /design is a
// DELEGATION action (orders.assign), matching the service and the UI's
// "+ Job" button. The seeded Designer holds design.manage but NOT
// orders.assign: the router must refuse the same way the service always
// did, instead of bouncing them into a service 403.
func TestDesignJobCreateFollowsDelegationRule(t *testing.T) {
	engine, admin, _, designer := newTestServer(t)

	w := do(t, engine, "POST", "/api/v1/design", designer, map[string]any{
		"title": "Wedding mugs", "productName": "Couple mugs",
	})
	if w.Code != 403 {
		t.Fatalf("designer create: %d, want 403 (designer has no orders.assign) (%s)", w.Code, w.Body.String())
	}

	w = do(t, engine, "POST", "/api/v1/design", admin, map[string]any{
		"title": "Wedding mugs", "productName": "Couple mugs",
	})
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("admin create: %d %s", w.Code, w.Body.String())
	}
}

// TestOrdersListHTTPFilters — the Orders page's new query UI rides
// from/to/cashierId over HTTP: a future window is empty (the old
// T23:59:59 bound class of bug), and a cashier filtering by someone
// else's id still sees only their own sales.
func TestOrdersListHTTPFilters(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)
	checkoutCash(t, engine, admin)

	w := do(t, engine, "GET", "/api/v1/orders?from=2030-01-01&to=2030-01-02", admin, nil)
	if w.Code != 200 {
		t.Fatalf("future window: %d %s", w.Code, w.Body.String())
	}
	var fenv struct {
		Data []map[string]any `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &fenv)
	if len(fenv.Data) != 0 {
		t.Fatalf("future window returned %d orders — date filter ignored", len(fenv.Data))
	}

	// Cashier probing cashierId=<admin's id> cannot widen their scope.
	cashierSale := checkoutCash(t, engine, cashier)
	w = do(t, engine, "GET", "/api/v1/orders?cashierId=1", cashier, nil)
	if w.Code != 200 {
		t.Fatalf("cashier scoped list: %d %s", w.Code, w.Body.String())
	}
	var cenv struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &cenv)
	for _, o := range cenv.Data {
		if o.ID != cashierSale {
			t.Fatalf("cashier saw order %d they do not own", o.ID)
		}
	}
}

// TestSendWhatsappNowEndpoint — the Share button's server leg through the
// router: with a token the cashier's POST queues the image (200); without
// one the 409 carries fallback:true so the UI shares from the device.
func TestSendWhatsappNowEndpoint(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)
	// The CASHIER owns the sale — sales are scoped (a cashier's list is
	// their own), so the send must run against their own order. The
	// customerPhone auto-captures the CRM record the send needs.
	w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, checkoutBody(map[string]any{
		"paymentMethod": "cash", "customerPhone": "0712345678", "customerName": "Test Send",
	}))
	if w.Code != 200 && w.Code != 201 {
		t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
	}
	orderID := orderIDFromBody(t, w.Body.Bytes())
	id := strconv.FormatInt(orderID, 10)

	// No token on a fresh till → 409 + fallback (device share paths).
	w = do(t, engine, "POST", "/api/v1/orders/"+id+"/whatsapp-send", cashier, map[string]any{})
	if w.Code != 409 {
		t.Fatalf("no-token send: %d %s, want 409", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"fallback":true`) {
		t.Fatalf("no-token refusal must carry fallback:true, got %s", w.Body.String())
	}

	// Cashier cannot reach other shops' orders (scoped view) — their own
	// sale is fine. Owner with a token gets a straight 200 + queued phone.
	wk := do(t, engine, "PUT", "/api/v1/settings", admin, map[string]any{
		"values": map[string]string{"whatsapp_api_key": "tok-e2e"},
	})
	if wk.Code != 200 {
		t.Fatalf("set token: %d %s", wk.Code, wk.Body.String())
	}
	w = do(t, engine, "POST", "/api/v1/orders/"+id+"/whatsapp-send", admin, map[string]any{})
	if w.Code != 200 {
		t.Fatalf("token send: %d %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			Queued bool   `json:"queued"`
			Phone  string `json:"phone"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.Data.Queued || !strings.HasPrefix(env.Data.Phone, "2547") {
		t.Fatalf("queued=%v phone=%q — want true + 2547…", env.Data.Queued, env.Data.Phone)
	}
}
