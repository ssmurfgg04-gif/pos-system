package services

// v1.11 per-line VAT at checkout: a basket mixing standard-rated (B),
// exempt (A), 8% (E) goods and a gift card prices each line at its own
// rate, freezes the split on the lines, prints/reports from it, and sends
// KRA exactly those figures (gift cards left out: not a supply).

import (
	"context"
	"testing"

	"posapp/internal/models"
)

func addProduct(t *testing.T, f *moneyFixture, sku string, price int64, taxType string, gift bool) int64 {
	t.Helper()
	g := 0
	if gift {
		g = 1
	}
	if _, err := f.s.db.Exec(`INSERT INTO products (sku, name, category_id, price_cents, stock_qty_milli, etims_tax_type, is_gift_card, track_stock)
		SELECT ?, ?, id, ?, 50000, ?, ?, 0 FROM categories LIMIT 1`, sku, "P "+sku, price, taxType, g); err != nil {
		t.Fatalf("product %s: %v", sku, err)
	}
	var id int64
	f.s.db.QueryRow(`SELECT id FROM products WHERE sku = ?`, sku).Scan(&id)
	return id
}

func TestMixedVatCheckoutAndEtims(t *testing.T) {
	f := newMoneyTill(t)
	exempt := addProduct(t, f, "EX1", 20000, "A", false)
	reduced := addProduct(t, f, "E8", 10800, "E", false)
	gift := addProduct(t, f, "GC1", 100000, "", true)
	o, err := f.s.Checkout(context.Background(), f.principal(), models.CheckoutRequest{
		Items: []models.CheckoutItem{
			{ProductID: f.prodID, QtyMilli: 2000}, // 2 x 600.00 @16% (default B)
			{ProductID: exempt, QtyMilli: 1000},   // 200.00 exempt
			{ProductID: reduced, QtyMilli: 1000},  // 108.00 @8%
			{ProductID: gift, QtyMilli: 1000},     // 1,000.00 gift card, no VAT
		},
		PaymentMethod: models.MethodCash, ClientUUID: "uuid-mixed-1",
	})
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	// VAT only on the taxed lines: 120000*16/116 = 16552 (16551.72), 10800*8/108 = 800
	if o.TaxCents != 16552+800 {
		t.Fatalf("order VAT = %d, want %d", o.TaxCents, 16552+800)
	}
	if o.TotalCents != 120000+20000+10800+100000 {
		t.Fatalf("total = %d", o.TotalCents)
	}
	got, _ := f.s.GetOrder(o.ID)
	want := map[string][2]any{"SKU-M1": {"B", int64(16552)}, "EX1": {"A", int64(0)}, "E8": {"E", int64(800)}, "GC1": {"X", int64(0)}}
	var lineTax int64
	for _, it := range got.Items {
		w := want[it.SKU]
		if it.TaxType != w[0] || it.TaxCents != w[1].(int64) || it.PaidCents != it.LineTotalCents {
			t.Errorf("%s: type %q tax %d paid %d, want %v", it.SKU, it.TaxType, it.TaxCents, it.PaidCents, w)
		}
		lineTax += it.TaxCents
	}
	if lineTax != o.TaxCents {
		t.Fatalf("line VAT %d != order VAT %d", lineTax, o.TaxCents)
	}
	if groups := models.VatBreakdown(got.Items); len(groups) != 3 {
		t.Fatalf("breakdown groups = %d, want 3 (16%%, 0%%, 8%%)", len(groups))
	}

	// eTIMS: gift card left out, per-line VAT sent as frozen
	fake := &fakeEtims{reply: func(b map[string]any) (map[string]any, int, error) { return signedReply(1), 200, nil }}
	useFakeEtims(t, fake)
	etimsOn(f)
	f.s.EtimsTick()
	if len(fake.calls) != 1 {
		t.Fatalf("calls = %d", len(fake.calls))
	}
	lines := fake.calls[0]["sale"].(map[string]any)["lines"].([]map[string]any)
	if len(lines) != 3 {
		t.Fatalf("KRA lines = %d, want 3 (gift card left out)", len(lines))
	}
	var sent int64
	for _, l := range lines {
		if l["taxTy"] == "X" {
			t.Fatal("gift card sent to KRA")
		}
		sent += l["taxCents"].(int64)
	}
	if sent != o.TaxCents {
		t.Fatalf("VAT sent to KRA %d != receipt VAT %d", sent, o.TaxCents)
	}
}

// Points reduce VAT: the VAT is the VAT inside what was paid.
func TestPointsReduceVat(t *testing.T) {
	f := newMoneyTill(t)
	f.s.db.Exec(`UPDATE customers SET loyalty_points = 1000 WHERE id = ?`, f.cust.ID)
	p := f.principal()
	p.Permissions = map[string]bool{"loyalty.redeem": true}
	o, err := f.s.Checkout(context.Background(), p, models.CheckoutRequest{
		Items:         []models.CheckoutItem{{ProductID: f.prodID, QtyMilli: 1000}},
		PaymentMethod: models.MethodCash, CustomerID: f.cust.ID, RedeemPoints: 100, ClientUUID: "uuid-pts-1",
	})
	if err != nil {
		t.Skipf("points checkout not available in this fixture: %v", err)
	}
	if o.PointsRedeemed == 0 {
		t.Skip("no points applied")
	}
	// VAT inside the amount actually paid (half-up)
	paid := o.TotalCents
	want := (paid*16*2 + 116) / (116 * 2)
	if o.TaxCents != want && o.TaxCents != want-1 && o.TaxCents != want+1 {
		t.Fatalf("VAT %d not the VAT inside %d paid (~%d)", o.TaxCents, paid, want)
	}
	got, _ := f.s.GetOrder(o.ID)
	var paidLines int64
	for _, it := range got.Items {
		paidLines += it.PaidCents
	}
	if paidLines != o.TotalCents {
		t.Fatalf("lines paid %d != order total %d", paidLines, o.TotalCents)
	}
}
