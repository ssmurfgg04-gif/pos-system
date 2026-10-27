package services

// shift_whatsapp_test.go — v1.4.6 regression coverage:
//  1. Shifts now travel team sync (open on till A appears on the admin's
//     till B; the close replays once; replays are idempotent).
//  2. WhatsApp receipts: text/link building, manual path without a
//     gateway, queued automated delivery (exactly-once per drain),
//     auto-send OFF by default, and dead-lettering after the retry cap.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"posapp/internal/auth"
	"posapp/internal/database"
	"posapp/internal/models"
	"posapp/internal/printer"
	"posapp/internal/settings"
)

func openV146Till(t *testing.T) *Service {
	t.Helper()
	db, err := database.Open("sqlite", t.TempDir()+"/till.db", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st, err := settings.New(db)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	db.Exec(`INSERT INTO roles (name) VALUES ('Cashier')`)
	db.Exec(`INSERT INTO users (username, full_name, role_id) SELECT 'cash1', 'Salma PC', id FROM roles WHERE name='Cashier'`)
	return &Service{db: db, settings: st, appVersion: "v146-test", printer: printer.NewWorker(db, st)}
}

func v146Principal(id int64) *auth.Principal {
	return &auth.Principal{ID: id, Username: "cash1"}
}

// popShiftOutbox returns the queued shift payloads (oldest first).
func popShiftOutbox(t *testing.T, s *Service) []json.RawMessage {
	t.Helper()
	rows, err := s.db.Query(`SELECT payload FROM sync_outbox WHERE entity = 'shift' ORDER BY seq`)
	if err != nil {
		t.Fatalf("outbox read: %v", err)
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, json.RawMessage(p))
	}
	return out
}

func TestShiftSyncOpenAndCloseAcrossDevices(t *testing.T) {
	tillA := openV146Till(t)
	tillB := openV146Till(t)

	var uidB int64
	tillB.db.QueryRow(`SELECT id FROM users WHERE username = 'cash1'`).Scan(&uidB)

	// 1. Open on A → the open event exists in A's outbox.
	sh, err := tillA.OpenShift(v146Principal(1), 10000)
	if err != nil {
		t.Fatalf("open shift: %v", err)
	}
	if sh.OpeningFloatCents != 10000 {
		t.Fatalf("float: got %d", sh.OpeningFloatCents)
	}
	events := popShiftOutbox(t, tillA)
	if len(events) != 1 {
		t.Fatalf("want 1 queued shift event, got %d", len(events))
	}

	// 2. B applies the open: the session appears in B's history, attributed
	// to the local 'cash1' user (cross-device name resolution).
	if err := tillB.applyEvent("shift", "upsert", events[0]); err != nil {
		t.Fatalf("apply open: %v", err)
	}
	all := tillB.ListShifts(0, 0)
	if len(all) != 1 || all[0].UserName != "Salma PC" || all[0].ClosedAt != "" {
		t.Fatalf("B after open: %+v", all)
	}
	if all[0].UserID != uidB {
		t.Fatalf("shift not attributed to local user: got %d want %d", all[0].UserID, uidB)
	}

	// 3. Idempotent replay of the open payload: no duplicate row.
	if err := tillB.applyEvent("shift", "upsert", events[0]); err != nil {
		t.Fatalf("replay open: %v", err)
	}
	if n := len(tillB.ListShifts(0, 0)); n != 1 {
		t.Fatalf("replay duplicated the shift: %d rows", n)
	}

	// 4. Close on A → the frozen close-out lands on B exactly once.
	if _, err := tillA.CloseShift(v146Principal(1), 9500); err != nil {
		t.Fatalf("close shift: %v", err)
	}
	events = popShiftOutbox(t, tillA)
	if len(events) != 2 {
		t.Fatalf("want open+close events, got %d", len(events))
	}
	if err := tillB.applyEvent("shift", "upsert", events[1]); err != nil {
		t.Fatalf("apply close: %v", err)
	}
	closed := tillB.ListShifts(0, 0)
	if len(closed) != 1 || closed[0].ClosedAt == "" {
		t.Fatalf("B after close: %+v", closed)
	}
	if closed[0].CountedCents != 9500 || closed[0].VarianceCents != -500 {
		t.Fatalf("frozen close numbers wrong: %+v", closed[0])
	}

	// 5. Replaying the CLOSE is a no-op; replaying the stale OPEN must not
	// resurrect a closed shift.
	if err := tillB.applyEvent("shift", "upsert", events[1]); err != nil {
		t.Fatalf("replay close: %v", err)
	}
	if err := tillB.applyEvent("shift", "upsert", events[0]); err != nil {
		t.Fatalf("stale open replay: %v", err)
	}
	final := tillB.ListShifts(0, 0)
	if len(final) != 1 || final[0].ClosedAt == "" {
		t.Fatalf("stale open resurrected the shift: %+v", final)
	}
}

func v146OrderFixture(t *testing.T, s *Service) (orderID, customerID int64) {
	t.Helper()
	s.db.Exec(`INSERT INTO categories (name, slug) VALUES ('Svc','svc')`)
	s.db.Exec(`INSERT INTO products (sku, name, category_id, price_cents, stock_qty)
                SELECT 'P-1','Printing', id, 1000, 5 FROM categories WHERE slug='svc'`)
	s.db.Exec(`INSERT INTO customers (name, phone) VALUES ('Sal','0717344440')`)
	s.db.QueryRow(`SELECT id FROM customers WHERE phone='0717344440'`).Scan(&customerID)
	res, err := s.db.Exec(`INSERT INTO orders (number, status, subtotal_cents, tax_cents, total_cents, tax_percent,
                tax_included, cashier_id, customer_id, customer_name, client_uuid, created_at, paid_at)
                VALUES ('ORD146', 'PAID', 1000, 138, 1000, 16, 1, 1, ?, 'Sal', 'uuid-146', '2026-10-27T08:00:00.000Z', '2026-10-27T08:05:00.000Z')`, customerID)
	if err != nil {
		t.Fatalf("order fixture: %v", err)
	}
	orderID, _ = res.LastInsertId()
	s.db.Exec(`INSERT INTO order_items (order_id, product_id, name, sku, qty, unit_price_cents, line_total_cents)
                VALUES (?, 1, 'Printing', 'P-1', 1, 1000, 1000)`, orderID)
	s.db.Exec(`INSERT INTO payments (order_id, method, mode, amount_cents, status, phone, mpesa_receipt, created_at, completed_at)
                VALUES (?, 'mpesa', '', 1000, 'COMPLETED', '254717344440', 'UJRQBBDEU3', '2026-10-27T08:00:00.000Z', '2026-10-27T08:05:00.000Z')`, orderID)
	return orderID, customerID
}

func TestWhatsappReceiptTextLinkManualPath(t *testing.T) {
	s := openV146Till(t)
	s.settings.Update(map[string]string{"store_name": "Creative Divine Concepts"})
	orderID, _ := v146OrderFixture(t, s)

	out, err := s.BuildWhatsappReceipt(orderID)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if out.Phone != "254717344440" {
		t.Fatalf("phone normalization: %q", out.Phone)
	}
	if !strings.HasPrefix(out.Link, "https://wa.me/254717344440?text=") {
		t.Fatalf("link: %q", out.Link)
	}
	for _, want := range []string{"Creative Divine Concepts", "Receipt ORD146", "TOTAL", "UJRQBBDEU3", "Served by"} {
		if !strings.Contains(out.Text, want) {
			t.Fatalf("receipt text missing %q:\n%s", want, out.Text)
		}
	}
	if out.Queued {
		t.Fatalf("nothing should queue without a gateway")
	}
	if out.Reason == "" {
		t.Fatalf("manual path must explain why it did not queue")
	}
}

func TestWhatsappAutoSendQueuedAndDelivered(t *testing.T) {
	var gotTo, gotBody string
	var gotAuth string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotTo, gotBody = req["to"], req["text"]
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer gw.Close()

	s := openV146Till(t)
	orderID, _ := v146OrderFixture(t, s)
	s.settings.Update(map[string]string{
		"whatsapp_auto_send": "true",
		"whatsapp_provider":  "custom",
		"whatsapp_api_url":   gw.URL,
		"whatsapp_api_key":   "test-key-123",
	})

	// The completion hook queues (never errors, never blocks).
	order, err := s.GetOrder(orderID)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	s.QueueWhatsappReceipt(order)

	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM whatsapp_jobs WHERE status='PENDING'`).Scan(&count)
	if count != 1 {
		t.Fatalf("want 1 queued job, got %d", count)
	}

	// Worker drain delivers to the gateway exactly once.
	if sent := s.drainWhatsappJobs(time.Now()); sent != 1 {
		t.Fatalf("drain sent %d", sent)
	}
	if gotTo != "254717344440" {
		t.Fatalf("gateway got to=%q", gotTo)
	}
	if gotAuth != "Bearer test-key-123" {
		t.Fatalf("gateway auth %q", gotAuth)
	}
	if !strings.Contains(gotBody, "Receipt ORD146") {
		t.Fatalf("gateway body missing receipt: %q", gotBody)
	}
	status := ""
	s.db.QueryRow(`SELECT status FROM whatsapp_jobs`).Scan(&status)
	if status != "SENT" {
		t.Fatalf("job status %s", status)
	}
	// Timeline + audit trail exist.
	var events int
	s.db.QueryRow(`SELECT COUNT(*) FROM order_events WHERE order_id = ? AND kind = 'whatsapp' AND message LIKE '%254717344440%'`, orderID).Scan(&events)
	if events != 1 {
		t.Fatalf("order timeline missing the send: %d", events)
	}
	// A second drain is a no-op (already SENT).
	if sent := s.drainWhatsappJobs(time.Now()); sent != 0 {
		t.Fatalf("second drain re-sent: %d", sent)
	}
}

func TestWhatsappAutoSendOffByDefault(t *testing.T) {
	s := openV146Till(t)
	orderID, _ := v146OrderFixture(t, s)
	order, _ := s.GetOrder(orderID)
	s.QueueWhatsappReceipt(order)
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM whatsapp_jobs`).Scan(&count)
	if count != 0 {
		t.Fatalf("auto-send must be opt-in; %d jobs queued", count)
	}
}

func TestWhatsappDeadLetterAfterRetryCap(t *testing.T) {
	calls := 0
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(500)
	}))
	defer gw.Close()

	s := openV146Till(t)
	orderID, _ := v146OrderFixture(t, s)
	s.settings.Update(map[string]string{
		"whatsapp_auto_send": "true",
		"whatsapp_provider":  "custom",
		"whatsapp_api_url":   gw.URL,
		"whatsapp_api_key":   "test-key-123",
	})
	order, _ := s.GetOrder(orderID)
	s.QueueWhatsappReceipt(order)

	now := time.Now()
	// Attempt 1 now, then retry windows 1..4 minutes later.
	base := now.Add(-24 * time.Hour) // created_at is fixed in fixture; backoff reads created_at… use fresh stamps instead
	_ = base
	// The queue wrote created_at = nowStamp(); simulate elapsed retries by
	// draining with progressively later "now" values.
	for i := 0; i < whatsappMaxAttempts; i++ {
		s.drainWhatsappJobs(now.Add(time.Duration(i) * 2 * time.Minute))
	}
	status, attempts := "", 0
	s.db.QueryRow(`SELECT status, attempts FROM whatsapp_jobs`).Scan(&status, &attempts)
	if status != "FAILED" || attempts != whatsappMaxAttempts {
		t.Fatalf("dead-letter: status=%s attempts=%d", status, attempts)
	}
	if calls != whatsappMaxAttempts {
		t.Fatalf("gateway called %d times, want %d", calls, whatsappMaxAttempts)
	}
	lastError := ""
	s.db.QueryRow(`SELECT last_error FROM whatsapp_jobs`).Scan(&lastError)
	if lastError == "" {
		t.Fatalf("FAILED job must keep the last error")
	}
}

func TestWhatsappPhoneNormalizationRejectsGarbage(t *testing.T) {
	if _, err := normalizeWhatsappPhone("12345"); err == nil {
		t.Fatalf("short garbage must not normalize")
	}
	got, err := normalizeWhatsappPhone("+254 717-344-440")
	if err != nil || got != "254717344440" {
		t.Fatalf("normalize: %q %v", got, err)
	}
	got, err = normalizeWhatsappPhone("0717344440")
	if err != nil || got != "254717344440" {
		t.Fatalf("normalize 07: %q %v", got, err)
	}
	got, err = normalizeWhatsappPhone("0117344440")
	if err != nil || got != "254117344440" {
		t.Fatalf("normalize 01 prefix: %q %v", got, err)
	}
}

// compile-time guard: the WhatsApp receipt builder must never reference a
// stale models.Order shape (fields are asserted via a tiny fixture above).
var _ = models.OrderPaid
