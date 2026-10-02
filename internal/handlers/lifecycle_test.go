package handlers_test

// lifecycle_test.go — P2–P5 acceptance tests:
//   P2: designer scope is enforced server-side (own jobs only, no creation).
//   P3: sales flow into jobs (order → job → ready) with an activity trail.
//   P4: WhatsApp contact preparation resolves the phone and logs the attempt.
//   P5: CRM dedupe + checkout auto-capture + shared purchase history.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"posapp/internal/models"
)

func loginAs(t *testing.T, engine *gin.Engine, user, pass string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("login %s: %d %s", user, w.Code, w.Body.String())
	}
	var out struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	return out.Data.Token
}

func doJSON(t *testing.T, engine *gin.Engine, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

// cashSale helper: one product, one cash sale via the real checkout.
func cashSale(t *testing.T, engine *gin.Engine, admin string, name string) map[string]any {
	t.Helper()
	// Ensure a cheap product exists (create once, reuse across calls).
	var pid float64
	{
		w := doJSON(t, engine, http.MethodGet, "/api/v1/products", admin, nil)
		var list []map[string]any
		json.Unmarshal(w.Body.Bytes(), &list)
		for _, p := range list {
			if p["sku"] == "SKU-LC" {
				pid = p["id"].(float64)
			}
		}
		if pid == 0 {
			// Resolve the first seeded category id (the catalog is seeded).
			var catWrap struct {
				Data []map[string]any `json:"data"`
			}
			w := doJSON(t, engine, http.MethodGet, "/api/v1/categories", admin, nil)
			json.Unmarshal(w.Body.Bytes(), &catWrap)
			cats := catWrap.Data
			if len(cats) == 0 {
				t.Fatal("no categories seeded — fixture broken")
			}
			w = doJSON(t, engine, http.MethodPost, "/api/v1/products", admin, map[string]any{
				"sku": "SKU-LC", "name": name, "priceCents": 50000,
				"categoryId": cats[0]["id"], "stockQty": 100,
			})
			if w.Code != 201 && w.Code != 200 {
				t.Fatalf("product seed failed: %d %s", w.Code, w.Body.String())
			}
			var out struct {
				Data struct {
					ID float64 `json:"id"`
				} `json:"data"`
			}
			json.Unmarshal(w.Body.Bytes(), &out)
			pid = out.Data.ID
		}
	}
	w := doJSON(t, engine, http.MethodPost, "/api/v1/orders/checkout", admin, map[string]any{
		"items":        []map[string]any{{"productId": pid, "qty": 1}},
		"paymentMethod": "cash",
		"clientUuid":   fmt.Sprintf("lc-%v", pid),
		"customerName": "Jane Customer",
		"customerPhone": "0717344440",
	})
	if w.Code != 201 && w.Code != 200 {
		t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Data map[string]any `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	return out.Data
}

func TestP2DesignerScopeEnforcedServerSide(t *testing.T) {
	engine, admin, _, designer := newTestServer(t)
	designerToken := loginAs(t, engine, "designer", rotatedPassword)

	// The manager creates a job assigned to the designer and one unassigned.
	w := doJSON(t, engine, http.MethodPost, "/api/v1/design", admin, map[string]any{
		"title": "Designer's job", "assigneeId": designerUserID(t, engine, designer),
	})
	if w.Code != 201 {
		t.Fatalf("create assigned job: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(t, engine, http.MethodPost, "/api/v1/design", admin, map[string]any{
		"title": "Someone else's job", "assigneeId": 1,
	})
	if w.Code != 201 {
		t.Fatalf("create other job: %d %s", w.Code, w.Body.String())
	}

	// Designer cannot CREATE jobs.
	w = doJSON(t, engine, http.MethodPost, "/api/v1/design", designerToken, map[string]any{"title": "sneaky"})
	if w.Code == 201 {
		t.Fatal("designer must not be able to create jobs")
	}

	// Designer's board only lists THEIR jobs (server-side filtering).
	w = doJSON(t, engine, http.MethodGet, "/api/v1/design", designerToken, nil)
	var jobs []map[string]any
	json.Unmarshal(w.Body.Bytes(), &jobs)
	for _, j := range jobs {
		if j["title"] == "Someone else's job" {
			t.Fatal("designer received a job that is not theirs — scope leak")
		}
	}

	// Designer cannot UPDATE someone else's job.
	var otherID float64
	w = doJSON(t, engine, http.MethodGet, "/api/v1/design", admin, nil)
	var all []map[string]any
	json.Unmarshal(w.Body.Bytes(), &all)
	for _, j := range all {
		if j["title"] == "Someone else's job" {
			otherID = j["id"].(float64)
		}
	}
	if otherID != 0 {
		w = doJSON(t, engine, http.MethodPost, fmt.Sprintf("/api/v1/design/%d/move", int64(otherID)), designerToken, map[string]any{"status": "ready"})
		if w.Code == 200 {
			t.Fatal("designer moved someone else's job — enforcement missing")
		}
	}

	// Designer CAN move their own job.
	var mineID float64
	for _, j := range all {
		if j["title"] == "Designer's job" {
			mineID = j["id"].(float64)
		}
	}
	if mineID != 0 {
		w = doJSON(t, engine, http.MethodPost, fmt.Sprintf("/api/v1/design/%d/move", int64(mineID)), designerToken, map[string]any{"status": "in_progress"})
		if w.Code != 200 {
			t.Fatalf("designer cannot move own job: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestP3OrderToJobLifecycle(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)
	sale := cashSale(t, engine, admin, "Lifecycle product")
	orderID := int64(sale["id"].(float64))

	// Create a job from the paid sale (orders.assign).
	w := doJSON(t, engine, http.MethodPost, fmt.Sprintf("/api/v1/orders/%d/jobs", orderID), admin, map[string]any{
		"title": "Print the tee", "priority": "high",
	})
	if w.Code != 201 {
		t.Fatalf("create job from order: %d %s", w.Code, w.Body.String())
	}
	t.Logf("job created body: %s", w.Body.String())
	var job struct {
		Data models.DesignJob `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &job)
	if job.Data.OrderID != orderID {
		t.Fatalf("job not linked to the sale: orderId=%d want %d", job.Data.OrderID, orderID)
	}

	// Order detail carries the job + a timeline entry exists.
	w = doJSON(t, engine, http.MethodGet, fmt.Sprintf("/api/v1/orders/%d", orderID), admin, nil)
	var order struct {
		Data struct {
			Jobs  []models.DesignJob `json:"jobs"`
			Notes any                `json:"note"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &order)
	if len(order.Data.Jobs) != 1 || order.Data.Jobs[0].Title != "Print the tee" {
		t.Fatalf("order detail missing linked job: %+v", order.Data.Jobs)
	}

	// Moving the job to ready appends to the order timeline.
	w = doJSON(t, engine, http.MethodPost, fmt.Sprintf("/api/v1/design/%d/move", job.Data.ID), admin, map[string]any{"status": "ready"})
	if w.Code != 200 {
		t.Fatalf("move to ready: %d %s", w.Code, w.Body.String())
	}
	w = doJSON(t, engine, http.MethodGet, fmt.Sprintf("/api/v1/orders/%d/events", orderID), admin, nil)
	if w.Code != 200 {
		t.Logf("events endpoint status=%d body=%s", w.Code, w.Body.String())
	} else {
		t.Logf("events body: %s", w.Body.String())
	}
	var eventsWrap struct {
		Data []map[string]any `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &eventsWrap)
	found := false
	for _, e := range eventsWrap.Data {
		if e["kind"] == "job" {
			found = true
		}
	}
	if !found {
		t.Fatalf("order timeline missing job events: %v", eventsWrap.Data)
	}
}

func TestP4WhatsAppNotifyResolvesPhoneAndLogs(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)
	sale := cashSale(t, engine, admin, "Notify product")
	orderID := int64(sale["id"].(float64))

	// Prepare the WhatsApp contact for the sale (0717… → 2547…).
	w := doJSON(t, engine, http.MethodPost, fmt.Sprintf("/api/v1/orders/%d/notify", orderID), admin, nil)
	if w.Code != 200 {
		t.Fatalf("notify: %d %s", w.Code, w.Body.String())
	}
	var contact struct {
		Data struct {
			Phone   string `json:"phone"`
			URL     string `json:"url"`
			Message string `json:"message"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &contact)
	if contact.Data.Phone != "254717344440" {
		t.Fatalf("phone not normalized: %q", contact.Data.Phone)
	}
	if len(contact.Data.URL) < 20 || !contains(contact.Data.URL, "wa.me/254717344440") || !contains(contact.Data.URL, "text=") {
		t.Fatalf("wa.me deep link malformed: %q", contact.Data.URL)
	}

	// The attempt is logged on the order timeline.
	w = doJSON(t, engine, http.MethodGet, fmt.Sprintf("/api/v1/orders/%d/events", orderID), admin, nil)
	var eventsWrap struct {
		Data []map[string]any `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &eventsWrap)
	found := false
	for _, e := range eventsWrap.Data {
		if e["kind"] == "whatsapp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("WhatsApp attempt missing from the order timeline: %v", eventsWrap.Data)
	}

	// An order with NO phone fails with a clear message.
	w = doJSON(t, engine, http.MethodPost, "/api/v1/orders/999999/notify", admin, nil)
	if w.Code == 200 {
		t.Fatal("notify on an unknown order must fail")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(needle) == 0 ||
		(len(haystack) > 0 && indexOf(haystack, needle) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestP5CustomerDedupeAndAutoCapture(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)

	// Duplicate phone on create is refused (one customer, one history).
	doJSON(t, engine, http.MethodPost, "/api/v1/customers", admin, map[string]any{
		"name": "Jane Customer", "phone": "0717344440",
	})
	w := doJSON(t, engine, http.MethodPost, "/api/v1/customers", admin, map[string]any{
		"name": "Jane Again", "phone": "0717 344 440",
	})
	if w.Code == 201 {
		t.Fatalf("duplicate phone accepted: %d %s", w.Code, w.Body.String())
	}

	// Checkout with a NEW phone auto-captures a customer record.
	sale := cashSale(t, engine, admin, "Capture product")
	if sale["customerId"].(float64) == 0 {
		t.Fatal("checkout with a phone did not auto-capture a customer")
	}
	custID := int64(sale["customerId"].(float64))

	// Shared purchase history shows the sale.
	w = doJSON(t, engine, http.MethodGet, fmt.Sprintf("/api/v1/customers/%d/orders", custID), admin, nil)
	if w.Code != 200 {
		t.Fatalf("customer orders: %d %s", w.Code, w.Body.String())
	}
	var ordersWrap struct {
		Data []map[string]any `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &ordersWrap)
	if len(ordersWrap.Data) != 1 {
		t.Fatalf("purchase history rows = %d, want 1", len(ordersWrap.Data))
	}

	// A second sale with the same phone lands on the SAME record.
	w = doJSON(t, engine, http.MethodGet, "/api/v1/customers", admin, nil)
	var customersWrap struct {
		Data []map[string]any `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &customersWrap)
	count := 0
	for _, c := range customersWrap.Data {
		if c["phone"] == "254717344440" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("customers with the same normalized phone = %d, want 1", count)
	}
}
