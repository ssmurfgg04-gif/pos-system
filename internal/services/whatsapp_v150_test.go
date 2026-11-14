package services

// whatsapp_v150_test.go — v1.5.0 automatic-receipt coverage:
//   - ONE gateway (whapi.cloud), ONE trigger (paid sale), zero cashier
//     steps: a configured token queues exactly one job per paid order;
//     no token queues nothing; the sale never learns about any of it.
//   - The delivery is a branded receipt IMAGE (multipart to/caption/media)
//     with the English thank-you caption — never the old wa.me manual link.
//   - A job whose order is no longer paid is cancelled, not sent; FAILED
//     jobs are retryable by the admin unless the order is unpaid.
//   - The PNG renderer is real: decodable image, right width, sane height,
//     with and without an uploaded shop logo.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posapp/internal/models"
)

func waOrder(t *testing.T, s *Service) (orderID, customerID int64) {
	t.Helper()
	s.db.Exec(`INSERT INTO categories (name, slug) VALUES ('Svc','svc')`)
	s.db.Exec(`INSERT INTO products (sku, name, category_id, price_cents, stock_qty_milli)
                SELECT 'P-1','Printing', id, 1000, 5000 FROM categories WHERE slug='svc'`)
	s.db.Exec(`INSERT INTO customers (name, phone) VALUES ('Sal','0717344440')`)
	s.db.QueryRow(`SELECT id FROM customers WHERE phone='0717344440'`).Scan(&customerID)
	waOrderSeq++
	number := fmt.Sprintf("ORD-WA%03d", waOrderSeq)
	res, err := s.db.Exec(`INSERT INTO orders (number, status, subtotal_cents, tax_cents, total_cents, tax_percent,
                tax_included, cashier_id, customer_id, customer_name, client_uuid, created_at, paid_at)
                VALUES (?, 'PAID', 1000, 138, 1000, 16, 1, 1, ?, 'Sal', ?, '2026-10-27T08:00:00.000Z', '2026-10-27T08:05:00.000Z')`,
		number, customerID, "uuid-"+number)
	if err != nil {
		t.Fatalf("order fixture: %v", err)
	}
	orderID, _ = res.LastInsertId()
	s.db.Exec(`INSERT INTO order_items (order_id, product_id, name, sku, qty_milli, unit_price_cents, line_total_cents)
                VALUES (?, 1, 'Printing', 'P-1', 1, 1000, 1000)`, orderID)
	s.db.Exec(`INSERT INTO payments (order_id, method, mode, amount_cents, status, phone, mpesa_receipt, created_at, completed_at)
                VALUES (?, 'mpesa', '', 1000, 'COMPLETED', '254717344440', 'UJRQBBDEU3', '2026-10-27T08:00:00.000Z', '2026-10-27T08:05:00.000Z')`, orderID)
	return orderID, customerID
}

var waOrderSeq int

func waGatewayImage(t *testing.T, status int, calls *int, capture func(r *http.Request, to, caption string, media []byte)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.URL.Path != "/messages/image" {
			t.Errorf("gateway path = %q, want /messages/image", r.URL.Path)
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Errorf("multipart parse: %v", err)
			w.WriteHeader(400)
			return
		}
		var media []byte
		if f, _, err := r.FormFile("media"); err == nil {
			defer f.Close()
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(f)
			media = buf.Bytes()
		}
		if capture != nil {
			capture(r, r.FormValue("to"), r.FormValue("caption"), media)
		}
		w.WriteHeader(status)
		w.Write([]byte(`{"id":"ok"}`))
	}))
}

func TestWhatsappCaptionThankYouAndDetails(t *testing.T) {
	s := openV146Till(t)
	s.settings.Update(map[string]string{"store_name": "Creative Divine Concepts"})
	orderID, _ := waOrder(t, s)
	o, err := s.GetOrder(orderID)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	cap := s.receiptCaption(o)
	// The order NUMBER comes from a package-wide sequence — other tests'
	// fixtures advance it, so assert on the number THIS order carries
	// (the caption contract is "carries the order's own number").
	for _, want := range []string{
		"Hi Sal!", "Creative Divine Concepts", "we're glad you chose us",
		"Receipt " + o.Number, "Total:", "Paid M-Pesa", "UJRQBBDEU3",
	} {
		if !strings.Contains(cap, want) {
			t.Fatalf("caption missing %q:\n%s", want, cap)
		}
	}
	// The pending marker the client removed stays gone everywhere.
	if strings.Contains(cap, "pending") {
		t.Fatalf("caption must not show a pending marker:\n%s", cap)
	}
}

func TestWhatsappAutoQueuedWithTokenNoneWithout(t *testing.T) {
	s := openV146Till(t)
	orderID, _ := waOrder(t, s)
	order, _ := s.GetOrder(orderID)

	// No token → zero jobs, sale unaffected, no error surfaces.
	s.QueueWhatsappReceipt(order)
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM whatsapp_jobs`).Scan(&count)
	if count != 0 {
		t.Fatalf("no token must queue nothing, got %d", count)
	}

	// Token alone (auto-send untouched, defaults ON) → exactly one job.
	s.settings.Update(map[string]string{"whatsapp_api_key": "tok-123"})
	s.QueueWhatsappReceipt(order)
	s.db.QueryRow(`SELECT COUNT(*) FROM whatsapp_jobs WHERE status='PENDING'`).Scan(&count)
	if count != 1 {
		t.Fatalf("token present must queue exactly one job, got %d", count)
	}

	// Explicit off switch stays available to the admin.
	s.settings.Update(map[string]string{"whatsapp_auto_send": "false"})
	s.QueueWhatsappReceipt(order)
	s.db.QueryRow(`SELECT COUNT(*) FROM whatsapp_jobs`).Scan(&count)
	if count != 1 {
		t.Fatalf("auto-send off must not queue more, got %d", count)
	}
}

func TestWhatsappDeliversBrandedImageWithCaption(t *testing.T) {
	calls := 0
	var gotTo, gotCaption string
	var gotMedia []byte
	var gotAuth, gotCT string
	gw := waGatewayImage(t, 200, &calls, func(r *http.Request, to, caption string, media []byte) {
		gotTo, gotCaption, gotMedia = to, caption, media
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
	})
	defer gw.Close()

	s := openV146Till(t)
	s.settings.Update(map[string]string{
		"store_name":       "Creative Divine Concepts",
		"whatsapp_api_key": "tok-123",
		"whatsapp_api_url": gw.URL,
		"brand_color":      "#0B7A3B",
		"receipt_footer":   "Creative Divine Concepts Ltd · Karibu tena!",
	})
	orderID, _ := waOrder(t, s)
	order, _ := s.GetOrder(orderID)
	s.QueueWhatsappReceipt(order)

	if sent := s.drainWhatsappJobs(time.Now()); sent != 1 {
		t.Fatalf("drain sent %d, want 1", sent)
	}
	if gotAuth != "Bearer tok-123" {
		t.Fatalf("auth %q", gotAuth)
	}
	if !strings.Contains(gotCT, "multipart/form-data") {
		t.Fatalf("content type %q", gotCT)
	}
	if gotTo != "254717344440" {
		t.Fatalf("to %q", gotTo)
	}
	if !strings.Contains(gotCaption, "we're glad you chose us") || !strings.Contains(gotCaption, "Receipt ORD-WA") {
		t.Fatalf("caption %q", gotCaption)
	}
	if len(gotMedia) < 200 || gotMedia[0] != 0x89 || gotMedia[1] != 'P' {
		t.Fatalf("media is not a real PNG (%d bytes)", len(gotMedia))
	}
	img, err := png.Decode(bytes.NewReader(gotMedia))
	if err != nil {
		t.Fatalf("gateway media does not decode: %v", err)
	}
	if img.Bounds().Dx() != receiptW || img.Bounds().Dy() < 300 {
		t.Fatalf("receipt image %v", img.Bounds())
	}
	status := ""
	s.db.QueryRow(`SELECT status FROM whatsapp_jobs`).Scan(&status)
	if status != "SENT" {
		t.Fatalf("job status %s", status)
	}
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

func TestWhatsappDeadLetterThenRetryable(t *testing.T) {
	calls := 0
	gw := waGatewayImage(t, 500, &calls, nil)
	defer gw.Close()

	s := openV146Till(t)
	s.settings.Update(map[string]string{
		"whatsapp_api_key": "tok-123",
		"whatsapp_api_url": gw.URL,
	})
	orderID, _ := waOrder(t, s)
	order, _ := s.GetOrder(orderID)
	s.QueueWhatsappReceipt(order)

	now := time.Now()
	for i := 0; i < whatsappMaxAttempts; i++ {
		s.drainWhatsappJobs(now.Add(time.Duration(i) * 2 * time.Minute))
	}
	status := ""
	attempts := 0
	lastError := ""
	s.db.QueryRow(`SELECT status, attempts, last_error FROM whatsapp_jobs`).Scan(&status, &attempts, &lastError)
	if status != "FAILED" || attempts != whatsappMaxAttempts || lastError == "" {
		t.Fatalf("dead-letter: status=%s attempts=%d err=%q", status, attempts, lastError)
	}
	if calls != whatsappMaxAttempts {
		t.Fatalf("gateway called %d times, want %d", calls, whatsappMaxAttempts)
	}

	// Admin retry re-queues the job (attempts reset, backoff restarts).
	var jobID int64
	s.db.QueryRow(`SELECT id FROM whatsapp_jobs`).Scan(&jobID)
	if err := s.RetryWhatsappJob(jobID); err != nil {
		t.Fatalf("retry: %v", err)
	}
	status, attempts = "", 0
	s.db.QueryRow(`SELECT status, attempts FROM whatsapp_jobs`).Scan(&status, &attempts)
	if status != "PENDING" || attempts != 0 {
		t.Fatalf("after retry: status=%s attempts=%d", status, attempts)
	}
	// Retrying a non-FAILED job is refused.
	status = "SENT"
	s.db.Exec(`UPDATE whatsapp_jobs SET status = 'SENT' WHERE id = ?`, jobID)
	if err := s.RetryWhatsappJob(jobID); err == nil {
		t.Fatal("retrying a SENT job must fail")
	}
}

func TestWhatsappCancelledWhenOrderUnpaid(t *testing.T) {
	calls := 0
	gw := waGatewayImage(t, 200, &calls, nil)
	defer gw.Close()

	s := openV146Till(t)
	s.settings.Update(map[string]string{
		"whatsapp_api_key": "tok-123",
		"whatsapp_api_url": gw.URL,
	})
	orderID, _ := waOrder(t, s)
	order, _ := s.GetOrder(orderID)
	s.QueueWhatsappReceipt(order)

	// The order gets voided/refunded before the worker reaches the job.
	s.db.Exec(`UPDATE orders SET status = 'REFUNDED' WHERE id = ?`, orderID)
	if sent := s.drainWhatsappJobs(time.Now()); sent != 0 {
		t.Fatalf("cancelled order must not send, sent %d", sent)
	}
	if calls != 0 {
		t.Fatalf("gateway must not be called for an unpaid order (%d calls)", calls)
	}
	status, lastError := "", ""
	s.db.QueryRow(`SELECT status, last_error FROM whatsapp_jobs`).Scan(&status, &lastError)
	if status != "FAILED" || !strings.Contains(lastError, "no longer paid") {
		t.Fatalf("job should be FAILED with a clear reason: %s %q", status, lastError)
	}
	// And the admin cannot resurrect it.
	var jobID int64
	s.db.QueryRow(`SELECT id FROM whatsapp_jobs`).Scan(&jobID)
	if err := s.RetryWhatsappJob(jobID); err == nil {
		t.Fatal("retrying a cancelled (unpaid-order) send must be refused")
	}
}

func TestWhatsappTestSendMessage(t *testing.T) {
	var gotTo, gotBody, gotAuth string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages/text" {
			t.Errorf("path %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var req map[string]string
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotTo, gotBody = req["to"], req["body"]
		w.WriteHeader(200)
		w.Write([]byte(`{"sent":true}`))
	}))
	defer gw.Close()

	s := openV146Till(t)
	s.settings.Update(map[string]string{
		"store_name":       "Creative Divine Concepts",
		"whatsapp_api_key": "tok-123",
		"whatsapp_api_url": gw.URL,
	})
	phone, err := s.WhatsappTestSend("0717 344-440")
	if err != nil {
		t.Fatalf("test send: %v", err)
	}
	if phone != "254717344440" || gotTo != "254717344440" {
		t.Fatalf("phone %q / gateway to %q", phone, gotTo)
	}
	if gotAuth != "Bearer tok-123" || !strings.Contains(gotBody, "connected") {
		t.Fatalf("auth %q body %q", gotAuth, gotBody)
	}
	if _, err := s.WhatsappTestSend("12345"); err == nil {
		t.Fatal("garbage phone must be refused")
	}
}

func TestWhatsappListJobsNewestFirst(t *testing.T) {
	s := openV146Till(t)
	s.settings.Update(map[string]string{"whatsapp_api_key": "tok-123"})
	for i := 1; i <= 3; i++ {
		orderID, _ := waOrder(t, s)
		s.db.Exec(`UPDATE orders SET client_uuid = ? WHERE id = ?`, "wa-list-"+string(rune('0'+i)), orderID)
		order, _ := s.GetOrder(orderID)
		s.QueueWhatsappReceipt(order)
	}
	jobs, err := s.ListWhatsappJobs(50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("want 3 jobs, got %d", len(jobs))
	}
	if jobs[0].ID <= jobs[2].ID {
		t.Fatalf("jobs must be newest first: %d then %d", jobs[0].ID, jobs[2].ID)
	}
	if jobs[0].Status != "PENDING" || jobs[0].Phone != "254717344440" {
		t.Fatalf("row shape: %+v", jobs[0])
	}
}

func TestReceiptPNGRendersWithAndWithoutLogo(t *testing.T) {
	// No logo: monogram fallback.
	s := openV146Till(t)
	s.settings.Update(map[string]string{
		"store_name":     "Creative Divine Concepts",
		"brand_color":    "#0047AB",
		"receipt_footer": "Thank you for your business.",
	})
	orderID, _ := waOrder(t, s)
	o, _ := s.GetOrder(orderID)
	png1, err := s.ReceiptPNG(o)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(png1))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if img.Bounds().Dx() != receiptW || img.Bounds().Dy() < 300 || img.Bounds().Dy() > 3600 {
		t.Fatalf("bounds %v", img.Bounds())
	}

	// With an uploaded logo file + flag: header carries the real logo.
	dir := t.TempDir()
	t.Chdir(dir)
	logo := image.NewRGBA(image.Rect(0, 0, 120, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 120; x++ {
			logo.Set(x, y, color.RGBA{R: 200, G: 30, B: 30, A: 255})
		}
	}
	var logoBuf bytes.Buffer
	if err := png.Encode(&logoBuf, logo); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, receiptLogoFile), logoBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	s.settings.Update(map[string]string{"brand_logo": "1"})
	png2, err := s.ReceiptPNG(o)
	if err != nil {
		t.Fatalf("render with logo: %v", err)
	}
	if _, err := png.Decode(bytes.NewReader(png2)); err != nil {
		t.Fatalf("decode with logo: %v", err)
	}
}

// TestWhatsappJobsListEmptyQueueNotNull locks the fresh-install contract:
// the admin panel maps over the jobs array, so an empty queue must be []
// (non-nil), never JSON null.
func TestWhatsappJobsListEmptyQueueNotNull(t *testing.T) {
	s := openV146Till(t)
	jobs, err := s.ListWhatsappJobs(50)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if jobs == nil {
		t.Fatal("empty queue returned a nil slice — JSON null breaks the Settings panel")
	}
	if len(jobs) != 0 {
		t.Fatalf("empty queue should list zero jobs, got %d", len(jobs))
	}
	b, _ := json.Marshal(jobs)
	if string(b) != "[]" {
		t.Fatalf("empty queue must marshal to [], got %s", b)
	}
}

// TestWhatsappPhoneValidationMapsTo422 locks the error-message contract:
// the handler's mapErr turns any message containing "phone" into a 422.
// The garbage-phone smoke check (Settings → WhatsApp → Send test) broke
// exactly here when the message said "mobile number" and fell to a 500.
func TestWhatsappPhoneValidationMapsTo422(t *testing.T) {
	for _, bad := range []string{"12345", "", "abcdefghij", "254"} {
		_, err := normalizeWhatsappPhone(bad)
		if err == nil {
			t.Fatalf("%q: expected rejection", bad)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "phone") {
			t.Fatalf("%q: error %q must mention \"phone\" (mapErr → 422 contract)", bad, err)
		}
	}
	for in, want := range map[string]string{
		"0711223344":    "254711223344",
		"+254711223344": "254711223344",
		"254711223344":  "254711223344",
		"711223344":     "254711223344",
		"0711 223 344":  "254711223344",
		"0711-223-344":  "254711223344",
	} {
		got, err := normalizeWhatsappPhone(in)
		if err != nil {
			t.Fatalf("%q: unexpected error %v", in, err)
		}
		if got != want {
			t.Fatalf("%q: normalized to %q, want %q", in, got, want)
		}
	}
}

// TestResendWhatsappReceiptPaths — the Share-via-WhatsApp button's server
// leg (v1.6.0). Three outcomes, each with the UI path it drives:
//
//	token + PAID + phone  → one PENDING job queued (customer gets the
//	                        branded IMAGE server-side), phone returned;
//	no token              → fallback=true (device share sheet / download);
//	pending order + token → hard refusal (never message payment "proof"
//	                        for money that has not arrived).
//
// A re-send is ALLOWED after an automatic send: "I didn't get it" is the
// button's reason to exist, so no dedupe.
func TestResendWhatsappReceiptPaths(t *testing.T) {
	s := openV146Till(t)
	orderID, _ := waOrder(t, s)
	o, err := s.GetOrder(orderID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}

	// 1. Gateway not connected → fallback to the device paths.
	phone, fallback, err := s.ResendWhatsappReceipt(o)
	if err == nil || !fallback || phone != "" {
		t.Fatalf("no token: want fallback refusal, got phone=%q fallback=%v err=%v", phone, fallback, err)
	}

	// 2. Token connected → queues exactly one PENDING image job.
	s.settings.Update(map[string]string{"whatsapp_api_key": "tok-resend"})
	phone, fallback, err = s.ResendWhatsappReceipt(o)
	if err != nil || fallback {
		t.Fatalf("connected: unexpected refusal phone=%q fallback=%v err=%v", phone, fallback, err)
	}
	if phone != "254717344440" {
		t.Fatalf("queued phone = %q, want the normalized customer number", phone)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM whatsapp_jobs WHERE order_id = ? AND status = 'PENDING'`, orderID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("pending jobs = %d (err %v), want exactly 1", n, err)
	}

	// 3. Deliberate re-send: "I didn't get it" — allowed, second job.
	if _, fallback, err := s.ResendWhatsappReceipt(o); err != nil || fallback {
		t.Fatalf("resend refused: fallback=%v err=%v", fallback, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM whatsapp_jobs WHERE order_id = ?`, orderID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("jobs after resend = %d (err %v), want 2 (resend is deliberate)", n, err)
	}

	// 4. A pending (unpaid) sale is refused outright — its "receipt" is
	// not proof of anything.
	o.Status = models.OrderPending
	if _, fallback, err := s.ResendWhatsappReceipt(o); err == nil || fallback {
		t.Fatalf("pending order: want hard refusal, got fallback=%v err=%v", fallback, err)
	}
}

// TestDrainSkipsDeadOrderWithoutStarvingQueue — criticism fix P1-5: a job
// whose order row is gone must FAIL (not panic-and-stay-PENDING at the
// head of the batch), and the NEXT job in the same tick must still send.
func TestDrainSkipsDeadOrderWithoutStarvingQueue(t *testing.T) {
	calls := 0
	gw := waGatewayImage(t, 200, &calls, func(*http.Request, string, string, []byte) {})
	defer gw.Close()

	s := openV146Till(t)
	s.settings.Update(map[string]string{"whatsapp_api_key": "tok", "whatsapp_api_url": gw.URL})

	// Job A: points at an order id that does not exist (max+10).
	var maxID int64
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(id),0) FROM orders`).Scan(&maxID)
	dead := maxID + 10
	_, err := s.db.Exec(s.db.Rebind(`
                INSERT INTO whatsapp_jobs (order_id, order_number, phone, body, status, created_at)
                VALUES (?, ?, ?, ?, 'PENDING', ?)`), dead, "ORD-DEAD", "254700000001", "caption", nowStamp())
	if err != nil {
		t.Fatalf("seed dead job: %v", err)
	}

	// Job B: a healthy paid order.
	orderID, _ := waOrder(t, s)
	o, _ := s.GetOrder(orderID)
	if err := s.queueWhatsappJob(o, "254700000002", "caption"); err != nil {
		t.Fatalf("seed live job: %v", err)
	}

	sent := s.drainWhatsappJobs(time.Now()) // must not panic
	if sent != 1 {
		t.Fatalf("sent %d, want 1 (the live job) — a dead job must not starve the batch", sent)
	}
	var st string
	if err := s.db.QueryRow(`SELECT status FROM whatsapp_jobs WHERE order_id = ?`, dead).Scan(&st); err != nil || st != "FAILED" {
		t.Fatalf("dead job status %q (err %v), want FAILED", st, err)
	}
	if calls != 1 {
		t.Fatalf("gateway calls = %d, want 1", calls)
	}
}

// TestPaymentWordIsStatusHonest — criticism fix P0-2: the branded image's
// payment rows come from paymentWord; an unconfirmed leg must never say
// "Paid" now that pending sales share through the manual button.
func TestPaymentWordIsStatusHonest(t *testing.T) {
	cases := []struct {
		pm   models.Payment
		want string
	}{
		{models.Payment{Method: "mpesa", Status: "PENDING"}, "M-Pesa (pending)"},
		{models.Payment{Method: "mpesa", Status: "PROCESSING"}, "M-Pesa (pending)"},
		{models.Payment{Method: "mpesa", Status: "COMPLETED"}, "Paid M-Pesa"},
		{models.Payment{Method: "account", Status: "PENDING"}, "Tab charge (pending)"},
		{models.Payment{Method: "credit", Status: "PENDING"}, "Store credit (pending)"},
		{models.Payment{Method: "paystack", Status: "PENDING"}, "Card / mobile money (pending)"},
		{models.Payment{Method: "paystack", Status: "COMPLETED"}, "Paid card / mobile money"},
		{models.Payment{Method: "cash", Status: "PENDING"}, "Paid cash"},
	}
	for _, c := range cases {
		got := paymentWord(c.pm)
		if got != c.want {
			t.Fatalf("paymentWord(%s/%s) = %q, want %q", c.pm.Method, c.pm.Status, got, c.want)
		}
		if strings.Contains(strings.ToLower(got), "paid") && !strings.Contains(strings.ToLower(c.want), "paid") {
			t.Fatalf("%s/%s: %q claims Paid", c.pm.Method, c.pm.Status, got)
		}
	}
}
