// v160_legs_test.go — P1-3/P1-4 of the adversarial receipt review: the
// paper receipt prints EVERY payment leg (tab/credit/card) and an
// unconfirmed M-Pesa leg never says "PAID".
package printer

import (
	"strings"
	"testing"

	"posapp/internal/models"
)

// TestPaperReceiptPrintsEveryLeg — P1-3/P1-4: tab, credit and card legs
// print; a pending M-Pesa leg must NOT say PAID.
func TestPaperReceiptPrintsEveryLeg(t *testing.T) {
	o := &models.Order{
		Number: "ORD202609110002", Status: models.OrderPending,
		SubtotalCents: 1200000, TaxCents: 0, TotalCents: 1200000,
		CashierName: "Alex", CustomerName: "Jane",
		CreatedAt: "2026-09-11T10:00:00Z", PaidAt: "",
		Items: []models.OrderItem{
			{ID: 1, ProductID: 1, Name: "Framing", SKU: "FR-1", QtyMilli: 1000, UnitPriceCents: 1200000, LineTotalCents: 1200000},
		},
		Payments: []models.Payment{
			{ID: 1, Method: "account", AmountCents: 700000, Status: "COMPLETED"},
			{ID: 2, Method: models.MethodMpesa, AmountCents: 500000, Status: models.PaymentPending, Phone: "254722123456"},
		},
	}
	data := BuildReceiptData(o, "My Store", "", "", "Thank you!", "KES")
	raw, err := Render(data, 48)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	text := extractPrintable(raw)
	if !strings.Contains(text, "Tab charge") {
		t.Fatalf("paper receipt dropped the tab leg:\n%s", text)
	}
	if strings.Contains(text, "PAID: M-Pesa") {
		t.Fatalf("unconfirmed M-Pesa leg printed PAID:\n%s", text)
	}
	if !strings.Contains(text, "M-Pesa (pending)") {
		t.Fatalf("pending M-Pesa leg missing its honest label:\n%s", text)
	}
	// A completed card leg prints too.
	o2 := *o
	o2.Payments = []models.Payment{{ID: 3, Method: "paystack", AmountCents: 1200000, Status: "COMPLETED"}}
	data2 := BuildReceiptData(&o2, "My Store", "", "", "", "KES")
	raw2, _ := Render(data2, 48)
	if !strings.Contains(extractPrintable(raw2), "PAID: Card / mobile money") {
		t.Fatalf("card leg missing:\n%s", extractPrintable(raw2))
	}
	// Date reads Kenya-first (DD/MM/YYYY), not ISO.
	if strings.Contains(text, "2026-09-11") {
		t.Fatalf("paper date still ISO:\n%s", text)
	}
	if !strings.Contains(text, "11/09/2026") {
		t.Fatalf("paper date not DD/MM/YYYY:\n%s", text)
	}
}

// TestPaperReceiptReconcilesPointsAndTaxLabel — criticism fixes P1-3/P1-4:
// the paper rows must reconcile like the branded image (Subtotal −
// Discount − Points ± VAT = TOTAL), the VAT row names its rate and says
// "(incl.)" under Kenya's default tax-inclusive pricing, and a VAT-off
// shop never prints a "KES 0.00" tax row.
func TestPaperReceiptReconcilesPointsAndTaxLabel(t *testing.T) {
	// Inclusive arithmetic (Kenya default): 1,300.00 shelf total − 50.00
	// staff discount − 120.00 points (1,200 pts… kept at 120 pts × 100c)
	// = 1,130.00 changing hands, with VAT 155.86 (16% of the gross)
	// INSIDE that total — the label must say so.
	o := &models.Order{
		Number: "ORD202611040001", Status: models.OrderPaid,
		SubtotalCents: 130000, DiscountCents: 5000, TaxCents: 15586, TotalCents: 113000,
		TaxPercent: 16, TaxIncluded: true,
		CashierName: "Alex", CustomerName: "Jane", PointsRedeemed: 120,
		CreatedAt: "2026-11-04T10:00:00Z", PaidAt: "2026-11-04T10:05:00Z",
		Items: []models.OrderItem{
			{ID: 1, ProductID: 1, Name: "Framing", SKU: "FR-1", QtyMilli: 1000, UnitPriceCents: 130000, LineTotalCents: 130000},
		},
		Payments: []models.Payment{
			{ID: 1, Method: "cash", AmountCents: 113000, Status: "COMPLETED"},
		},
	}
	d := BuildReceiptData(o, "Creative Divine", "Nairobi", "0700 000 000", "", "KES")
	d.PointsPts, d.PointsCents = 120, 12000 // 120 pts × 100 cents

	out, err := Render(d, 48)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"Points redeemed (120 pts)", "-120.00",
		"VAT (16%, incl.)", // inside the total, never added on top
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("paper receipt missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "Tax KES") || strings.Contains(s, "\nTax ") {
		t.Fatalf("tax row printed for a VAT-inclusive zero-tax order:\n%s", s)
	}

	// VAT-off shop: no tax row at all, not "KES 0.00".
	o2 := *o
	o2.TaxCents, o2.TaxPercent, o2.TaxIncluded = 0, 0, false
	d2 := BuildReceiptData(&o2, "S", "", "", "", "KES")
	out2, _ := Render(d2, 48)
	if strings.Contains(string(out2), "VAT") {
		t.Fatalf("VAT row printed when VAT is off:\n%s", string(out2))
	}
}
