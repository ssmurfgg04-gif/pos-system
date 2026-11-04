// v160_receipt_watermark_test.go — v1.6.0 receipts: the invisible
// watermark must survive a round trip, catch tampering, reject foreign
// shops, and the EAT date windows behind the new Orders filters must
// include the last second of a day (the old T23:59:59 string bound
// silently dropped it).
package services

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"testing"
	"time"

	"posapp/internal/auth"
	"posapp/internal/models"
)

// adminP is a full-view principal (the owner reading order records).
func adminP(id int64) *auth.Principal {
	return &auth.Principal{ID: id, Username: "admin", Permissions: map[string]bool{
		"orders.view": true, "orders.view.all": true,
	}}
}

// staffP is a scoped cashier (no orders.view.all).
func staffP(id int64) *auth.Principal {
	return &auth.Principal{ID: id, Username: "cash", Permissions: map[string]bool{
		"orders.view": true,
	}}
}

func TestReceiptImageWatermarkRoundtrip(t *testing.T) {
	s := openV146Till(t)
	orderID, _ := waOrder(t, s)
	o, err := s.GetOrder(orderID)
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	shot, err := s.ReceiptPNG(o)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	wm, ok, reason := s.VerifyReceiptImage(shot)
	if !ok {
		t.Fatalf("fresh receipt failed verification: %s", reason)
	}
	if wm.OrderID != orderID {
		t.Fatalf("payload order id = %d, want %d", wm.OrderID, orderID)
	}
	if wm.TotalCents != o.TotalCents {
		t.Fatalf("payload total = %d, want %d", wm.TotalCents, o.TotalCents)
	}
	if wm.IssuedUnix <= 0 {
		t.Fatalf("issue time missing: %d", wm.IssuedUnix)
	}
	if _, err := png.Decode(bytes.NewReader(shot)); err != nil {
		t.Fatalf("output is not a decodable PNG: %v", err)
	}
}

// paintOver renders shot, paints a solid box over [topPx, botPx), and
// re-encodes as PNG — the photo-editor attack.
func paintOver(t *testing.T, shot []byte, topPx, botPx int) []byte {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(shot))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	b := img.Bounds()
	// Zero-based box: draw maps dst r.Min onto the src point, so a
	// box that carries its own offset as the src silently clips the
	// whole operation away (the paint must actually land).
	box := image.NewRGBA(image.Rect(0, 0, b.Dx(), botPx-topPx))
	draw.Draw(box, box.Bounds(), &image.Uniform{color.RGBA{R: 0x11, G: 0x11, B: 0x11, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(img.(draw.Image), image.Rect(0, topPx, b.Dx(), botPx), box, image.Point{}, draw.Over)
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	return out.Bytes()
}

// The two layers have deliberate, complementary survival zones: the
// block strip sits UNDER the footer (bottom), the LSB payload rides the
// FIRST rows (top). A forger who paints over the totals+strip destroys
// the block layer; verification must then fall back to the LSB layer —
// and the payload it reads still carries the TRUE order id and total
// (the HMAC key makes a fabricated payload impossible), so the owner
// cross-checking against the POS sees any painted fake for what it is.
func TestReceiptImageTamperBottomHalfFallsBackToLSB(t *testing.T) {
	s := openV146Till(t)
	orderID, _ := waOrder(t, s)
	o, _ := s.GetOrder(orderID)
	shot, err := s.ReceiptPNG(o)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	h := image.Rect(0, 0, 1, 1).Dy() // not used; decode for real height
	_ = h
	decoded, err := png.Decode(bytes.NewReader(shot))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	full := decoded.Bounds()
	forged := paintOver(t, shot, full.Dy()/2, full.Dy())
	wm, ok, _ := s.VerifyReceiptImage(forged)
	if !ok {
		t.Fatal("bottom-half edit killed BOTH layers — LSB should carry it")
	}
	if wm.Layer == "block" {
		t.Fatal("block strip verified over a painted-over strip — extractor is not reading the strip")
	}
	if wm.OrderID != orderID || wm.TotalCents != o.TotalCents {
		t.Fatalf("forged image payload drifted: id=%d total=%d", wm.OrderID, wm.TotalCents)
	}
}

// Mirror attack: paint over the TOP (header/shop name) — the LSB layer
// dies, the block strip must carry verification.
func TestReceiptImageTamperTopFallsBackToBlock(t *testing.T) {
	s := openV146Till(t)
	orderID, _ := waOrder(t, s)
	o, _ := s.GetOrder(orderID)
	shot, err := s.ReceiptPNG(o)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	decoded, _ := png.Decode(bytes.NewReader(shot))
	forged := paintOver(t, shot, 0, decoded.Bounds().Dy()/4)
	wm, ok, _ := s.VerifyReceiptImage(forged)
	if !ok {
		t.Fatal("top-quarter edit killed BOTH layers — block strip should carry it")
	}
	if wm.Layer == "lsb" {
		t.Fatal("LSB verified after its first rows were painted over — extractor reads the wrong region")
	}
	if wm.OrderID != orderID {
		t.Fatalf("payload order id = %d, want %d", wm.OrderID, orderID)
	}
}

// The headline property: WhatsApp re-encodes media as JPEG (~q70-80).
// A forwarded receipt must STILL verify — via the block strip.
func TestReceiptImageSurvivesJPEGReencode(t *testing.T) {
	s := openV146Till(t)
	orderID, _ := waOrder(t, s)
	o, _ := s.GetOrder(orderID)
	shot, err := s.ReceiptPNG(o)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	decoded, err := png.Decode(bytes.NewReader(shot))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var jbuf bytes.Buffer
	if err := jpeg.Encode(&jbuf, decoded, &jpeg.Options{Quality: 70}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	// Decode the JPEG back (as WhatsApp's viewer would) before verifying.
	roundtrip, err := jpeg.Decode(&jbuf)
	if err != nil {
		t.Fatalf("jpeg decode: %v", err)
	}
	var repng bytes.Buffer
	if err := png.Encode(&repng, roundtrip); err != nil {
		t.Fatalf("re-png: %v", err)
	}
	wm, ok, reason := s.VerifyReceiptImage(repng.Bytes())
	if !ok {
		t.Fatalf("JPEG re-encode (WhatsApp's pipeline) killed verification: %s", reason)
	}
	if wm.Layer != "block" {
		t.Fatalf("layer = %s, want block (LSB is dead after JPEG)", wm.Layer)
	}
	if wm.OrderID != orderID || wm.TotalCents != o.TotalCents {
		t.Fatalf("payload drifted after JPEG: id=%d total=%d", wm.OrderID, wm.TotalCents)
	}
}

func TestReceiptImageWatermarkRejectsForeignShop(t *testing.T) {
	sA := openV146Till(t)
	sB := openV146Till(t)
	orderID, _ := waOrder(t, sA)
	o, _ := sA.GetOrder(orderID)
	shot, err := sA.ReceiptPNG(o)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// Shop B holds a different watermark key (fresh settings store) —
	// another shop's receipt, or a fabricated one, must not verify here.
	if _, ok, _ := sB.VerifyReceiptImage(shot); ok {
		t.Fatal("shop B verified shop A's receipt — keys are not isolated")
	}
}

func TestDayBoundsEATCoversTheWholeNairobiDay(t *testing.T) {
	from, to := dayBounds("2026-10-27")
	// EAT is UTC+3: the day window starts at 21:00Z the previous day.
	if from != "2026-10-26T21:00:00.000Z" {
		t.Fatalf("from = %s, want 2026-10-26T21:00:00.000Z", from)
	}
	if to != "2026-10-27T21:00:00.000Z" {
		t.Fatalf("to = %s, want 2026-10-27T21:00:00.000Z", to)
	}
	// The exact sale the old "T23:59:59" bound dropped: 23:59:59 EAT.
	last, _ := time.ParseInLocation("2006-01-02 15:04:05", "2026-10-27 23:59:59", eastAfrica)
	if !(from <= eatStamp(last) && eatStamp(last) < to) {
		t.Fatalf("23:59:59 EAT sale (%s) fell outside [%s,%s)", eatStamp(last), from, to)
	}
	// And 00:00:00 the NEXT day must stay outside.
	next, _ := time.ParseInLocation("2006-01-02 15:04:05", "2026-10-28 00:00:00", eastAfrica)
	if eatStamp(next) < to {
		t.Fatalf("midnight next day leaked into the previous day")
	}
}

// TestListOrdersDateWindowIncludesLastSecond runs the ACTUAL ListOrders
// SQL with from=to=same day — this is the query class the interrupted
// session shipped with `AND < ?` (a syntax error that only surfaces at
// runtime), so these tests execute every rewritten report query too.
func TestListOrdersDateWindowIncludesLastSecond(t *testing.T) {
	s := openV146Till(t)
	// Two PAID orders: one at 23:59:59 EAT on the 27th (stamp 20:59:59Z),
	// one the next morning at 06:00 EAT (03:00Z).
	s.db.Exec(`INSERT INTO orders (number, status, subtotal_cents, tax_cents, total_cents,
                cashier_id, customer_name, created_at, client_uuid)
                VALUES ('ORD-B1', 'PAID', 100, 0, 100, 1, 'a', '2026-10-27T20:59:59.000Z', 'b1'),
                       ('ORD-B2', 'PAID', 200, 0, 200, 1, 'b', '2026-10-28T03:00:00.000Z', 'b2')`)
	rows, err := s.ListOrders(OrderFilter{From: "2026-10-27", To: "2026-10-27", Limit: 50}, adminP(1))
	if err != nil {
		t.Fatalf("ListOrders with date window: %v", err)
	}
	if len(rows) != 1 || rows[0].Number != "ORD-B1" {
		t.Fatalf("day window picked wrong orders: %v", numbers(rows))
	}
	// The morning order belongs to the 28th.
	rows, err = s.ListOrders(OrderFilter{From: "2026-10-28", To: "2026-10-28", Limit: 50}, adminP(1))
	if err != nil {
		t.Fatalf("ListOrders next day: %v", err)
	}
	if len(rows) != 1 || rows[0].Number != "ORD-B2" {
		t.Fatalf("next-day window picked wrong orders: %v", numbers(rows))
	}
}

// TestListOrdersCashierFilterCannotWidenStaffScope — a staff principal
// passing cashierId=<someone else> still sees only their own sales.
func TestListOrdersCashierFilterCannotWidenStaffScope(t *testing.T) {
	s := openV146Till(t)
	// The fixture ships one cashier (id 1); FKs demand the second
	// cashier exists before orders reference them.
	s.db.Exec(`INSERT INTO users (username, full_name, role_id) SELECT 'cash2', 'Chep Kilel', id FROM roles WHERE name='Cashier'`)
	s.db.Exec(`INSERT INTO orders (number, status, subtotal_cents, tax_cents, total_cents,
                cashier_id, customer_name, created_at, client_uuid)
                VALUES ('ORD-C1', 'PAID', 100, 0, 100, 1, 'a', '2026-10-27T08:00:00.000Z', 'c1'),
                       ('ORD-C2', 'PAID', 200, 0, 200, 2, 'b', '2026-10-27T09:00:00.000Z', 'c2')`)
	// Staff (id 2) asks for cashier 1's orders: must get only their own.
	rows, err := s.ListOrders(OrderFilter{Cashier: 1, Limit: 50}, staffP(2))
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if len(rows) != 1 || rows[0].Number != "ORD-C2" {
		t.Fatalf("staff scope widened via cashierId: %v", numbers(rows))
	}
	// The admin filtering by cashier 1 gets exactly cashier 1's order.
	rows, err = s.ListOrders(OrderFilter{Cashier: 1, Limit: 50}, adminP(3))
	if err != nil {
		t.Fatalf("ListOrders admin: %v", err)
	}
	if len(rows) != 1 || rows[0].Number != "ORD-C1" {
		t.Fatalf("admin cashier filter wrong: %v", numbers(rows))
	}
}

// TestQuoteSearchCaseInsensitive — user-facing search must match on
// Postgres too, where a bare LIKE is case-sensitive (SQLite hides this
// in dev). Mirrors the same fix in ListCustomers.
func TestQuoteSearchCaseInsensitive(t *testing.T) {
	s := openV146Till(t)
	// A customer to search for (the till fixture ships none).
	s.db.Exec(`INSERT INTO customers (name, phone) VALUES ('Amina Njeri', '0717344440')`)
	s.db.Exec(`INSERT INTO quotes (quote_no, customer_name, customer_phone, status, items_json,
                subtotal_cents, tax_cents, total_cents, valid_until, created_at, updated_at, client_uuid)
                VALUES ('QT-2026-0001', 'Amina Njeri', '0717344440', 'draft', '[]', 100, 16, 116,
                '2026-11-10', '2026-10-27T08:00:00.000Z', '2026-10-27T08:00:00.000Z', 'q1')`)
	rows, err := s.ListQuotes("", "amina", 100)
	if err != nil {
		t.Fatalf("ListQuotes lowercase: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("lowercase quote search found %d rows — case-sensitive LIKE", len(rows))
	}
	rows, err = s.ListQuotes("", "QT-2026", 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("uppercase quote-no search: %v rows=%d", err, len(rows))
	}
	// Sanity: ListCustomers gets the same treatment.
	cs, err := s.ListCustomers("AMINA")
	if err != nil || len(cs) != 1 {
		t.Fatalf("uppercase customer search: %v rows=%d", err, len(cs))
	}
}

// TestReportsQueriesExecute executes every report aggregate end-to-end —
// the net that would have caught the invalid `AND < ?` SQL before it
// shipped (string SQL only fails at runtime; go vet is blind to it).
func TestReportsQueriesExecute(t *testing.T) {
	s := openV146Till(t)
	now := nowStamp()
	s.db.Exec(`INSERT INTO orders (number, status, subtotal_cents, tax_cents, total_cents,
                cashier_id, customer_name, created_at, paid_at, client_uuid)
                VALUES ('ORD-R1', 'PAID', 1000, 138, 1000, 1, 'a', ?, ?, 'r1')`, now, now)
	s.db.Exec(`INSERT INTO payments (order_id, method, amount_cents, status, created_at)
                SELECT id, 'cash', 1000, 'COMPLETED', ? FROM orders WHERE number='ORD-R1'`, now)

	if _, err := s.GetDailySummary(""); err != nil {
		t.Fatalf("GetDailySummary: %v", err)
	}
	if _, err := s.GetMonthlySummary(eatToday()[:7]); err != nil {
		t.Fatalf("GetMonthlySummary: %v", err)
	}
	// Cashier-day stats ride TeamOverview (retail.go) which shares
	// dayBounds + the paid_at COALESCE window — it broke the same way.
	if _, err := s.TeamOverview(); err != nil {
		t.Fatalf("TeamOverview: %v", err)
	}
}

func numbers(rows []models.Order) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.Number)
	}
	return out
}
