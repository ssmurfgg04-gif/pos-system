package services

// quotes_probe_test.go — acceptance for the quotation pipeline (v1.3.0):
// creation math (same tax rules as checkout), pipeline guardrails,
// convert-to-till via held sale, and the cross-device apply side
// (insert-by-client_uuid exactly once, LWW merge, number-collision
// renumbering).

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"posapp/internal/auth"
	"posapp/internal/database"
	"posapp/internal/models"
	"posapp/internal/settings"
)

func quoteTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open("sqlite", filepath.Join(dir, "test.db"), "")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st, err := settings.New(db)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	return &Service{db: db, settings: st, appVersion: "test"}
}

func quoteTestPrincipal() *auth.Principal {
	return &auth.Principal{
		ID: 1, Username: "frontdesk", RoleName: "Front Desk", Active: true,
		Permissions: map[string]bool{
			"quotes.manage":           true,
			"payments.apply_discount": true,
			"payments.override_price": true,
		},
	}
}

func quoteTestItems() []models.QuoteItem {
	return []models.QuoteItem{
		{ProductID: 11, SKU: "BRAND-KIT", Name: "Brand kit", Qty: 1, UnitPriceCents: 2500000},
		{ProductID: 12, SKU: "FLYERS-500", Name: "Flyers x500", Qty: 2, UnitPriceCents: 150000},
	}
}

func TestQuoteCreateTotalsMirrorCheckoutTax(t *testing.T) {
	s := quoteTestService(t)
	s.settings.Set("tax_percent", "16")
	s.settings.Set("tax_included", "true")

	qy, err := s.CreateQuote(models.QuoteInput{
		CustomerName: "Wanjiku Prints",
		Items:        quoteTestItems(),
	}, quoteTestPrincipal())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// subtotal 2,800,000; VAT-inclusive at 16% → tax = 2800000*16/116
	wantTax := int64(386207) // round(2800000*16/116)
	if qy.SubtotalCents != 2800000 {
		t.Errorf("subtotal = %d, want 2800000", qy.SubtotalCents)
	}
	if qy.TaxCents != wantTax {
		t.Errorf("tax = %d, want %d (VAT-inclusive math must match checkout)", qy.TaxCents, wantTax)
	}
	if qy.TotalCents != 2800000 {
		t.Errorf("total = %d, want 2800000 (inclusive)", qy.TotalCents)
	}
	if qy.QuoteNo == "" || len(qy.QuoteNo) < 5 {
		t.Errorf("quote number not minted: %q", qy.QuoteNo)
	}
	if qy.Status != models.QuoteDraft {
		t.Errorf("status = %s, want draft", qy.Status)
	}
	if qy.Items[1].LineTotalCents != 300000 {
		t.Errorf("line total = %d, want 300000", qy.Items[1].LineTotalCents)
	}
}

func TestQuoteValidationGuardrails(t *testing.T) {
	s := quoteTestService(t)
	p := quoteTestPrincipal()

	// No lines.
	if _, err := s.CreateQuote(models.QuoteInput{CustomerName: "X"}, p); err == nil {
		t.Fatal("empty quote accepted")
	}
	// No customer at all.
	if _, err := s.CreateQuote(models.QuoteInput{Items: quoteTestItems()}, p); err == nil {
		t.Fatal("customerless quote accepted")
	}
	// Discount larger than the subtotal.
	if _, err := s.CreateQuote(models.QuoteInput{
		CustomerName: "X", Items: quoteTestItems(), DiscountCents: 9_999_999,
	}, p); err == nil {
		t.Fatal("over-subtotal discount accepted")
	}
	// Bad quantity.
	bad := quoteTestItems()
	bad[0].Qty = 0
	if _, err := s.CreateQuote(models.QuoteInput{CustomerName: "X", Items: bad}, p); err == nil {
		t.Fatal("zero-qty line accepted")
	}
}

func TestQuotePipelineTransitions(t *testing.T) {
	s := quoteTestService(t)
	p := quoteTestPrincipal()
	qy, err := s.CreateQuote(models.QuoteInput{CustomerName: "Kamau", Items: quoteTestItems()}, p)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := s.SetQuoteStatus(qy.ID, models.QuoteAccepted, p); err != nil {
		t.Fatalf("draft→accepted (walk-in) should work: %v", err)
	}
	if _, err := s.SetQuoteStatus(qy.ID, models.QuoteSent, p); err == nil {
		t.Fatal("accepted→sent must be refused")
	}
	if _, err := s.UpdateQuote(qy.ID, models.QuoteInput{CustomerName: "Kamau", Items: quoteTestItems()}, p); err == nil {
		t.Fatal("accepted quote must not be editable")
	}
	if _, err := s.SetQuoteStatus(qy.ID, models.QuoteConverted, p); err == nil {
		t.Fatal("direct converted must be refused (conversion goes through ConvertQuote)")
	}
}

func TestQuoteConvertParksHeldSaleAndStampsConverted(t *testing.T) {
	s := quoteTestService(t)
	p := quoteTestPrincipal()
	// The catalog rows ConvertQuote needs (held sales freeze product ids).
	// SKUs match quoteTestItems on purpose — SKU-first resolution is the
	// cross-till-safe path (ids alone can point at different products).
	s.db.Exec(`INSERT INTO categories (name, slug) VALUES ('C','c')`)
	var catID int64
	s.db.QueryRow(`SELECT id FROM categories LIMIT 1`).Scan(&catID)
	seed := []struct {
		id  int64
		sku string
	}{
		{11, "BRAND-KIT"}, {12, "FLYERS-500"},
	}
	for _, p := range seed {
		if _, err := s.db.Exec(s.db.Rebind(
			`INSERT INTO products (id, sku, name, category_id, price_cents, stock_qty, track_stock, is_active)
                         VALUES (?, ?, ?, ?, ?, 0, 0, 1)`),
			p.id, p.sku, "Prod", catID, 1000); err != nil {
			t.Fatalf("seed product: %v", err)
		}
	}
	qy, err := s.CreateQuote(models.QuoteInput{CustomerName: "Salma", Items: quoteTestItems()}, p)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	held, err := s.ConvertQuote(qy.ID, p)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if len(held.Items) != 2 {
		t.Errorf("held items = %d, want 2", len(held.Items))
	}
	after, err := s.GetQuote(qy.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != models.QuoteConverted {
		t.Errorf("status = %s, want converted", after.Status)
	}
	if after.ConvertedHeldSaleID != held.ID {
		t.Errorf("converted held id = %d, want %d", after.ConvertedHeldSaleID, held.ID)
	}
	// Double convert must refuse.
	if _, err := s.ConvertQuote(qy.ID, p); err == nil {
		t.Fatal("double convert accepted")
	}
}

func TestApplyQuoteInsertOnceLWWAndRenumber(t *testing.T) {
	s := quoteTestService(t)

	ev := quoteEvent{
		ClientUUID: "q-abc-1", QuoteNo: "QT-000001", CustomerName: "Njeri",
		Status: models.QuoteDraft,
		Items: []models.QuoteItem{
			{ProductID: 5, SKU: "MUG", Name: "Mug", Qty: 3, UnitPriceCents: 50000, LineTotalCents: 150000},
		},
		SubtotalCents: 150000, TaxCents: 0, TotalCents: 150000,
		CreatedAt: "2026-10-13T08:00:00.000Z", UpdatedAt: "2026-10-13T08:00:00.000Z",
	}
	raw, _ := json.Marshal(ev)
	if err := s.applyQuote(raw); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	var id int64
	var qno string
	if err := s.db.QueryRow(s.db.Rebind(`SELECT id, COALESCE(quote_no,'') FROM quotes WHERE client_uuid = ?`), ev.ClientUUID).Scan(&id, &qno); err != nil {
		t.Fatalf("quote not stored: %v", err)
	}
	if qno != "QT-000001" {
		t.Errorf("quote_no = %s, want QT-000001", qno)
	}

	// Same event again — exactly-once (no duplicate row).
	if err := s.applyQuote(raw); err != nil {
		t.Fatalf("replay: %v", err)
	}
	var n int64
	s.db.QueryRow(`SELECT COUNT(*) FROM quotes`).Scan(&n)
	if n != 1 {
		t.Fatalf("rows = %d, want 1 (replay duplicated)", n)
	}

	// A NEWER event with the same uuid — LWW update lands.
	ev.Status = models.QuoteAccepted
	ev.UpdatedAt = "2026-10-13T09:00:00.000Z"
	newer, _ := json.Marshal(ev)
	if err := s.applyQuote(newer); err != nil {
		t.Fatalf("newer apply: %v", err)
	}
	var status string
	s.db.QueryRow(s.db.Rebind(`SELECT status FROM quotes WHERE client_uuid = ?`), ev.ClientUUID).Scan(&status)
	if status != models.QuoteAccepted {
		t.Errorf("status = %s, want accepted (LWW)", status)
	}

	// An OLDER event must NOT roll the state back.
	ev.Status = models.QuoteDeclined
	ev.UpdatedAt = "2026-10-13T07:00:00.000Z"
	older, _ := json.Marshal(ev)
	if err := s.applyQuote(older); err != nil {
		t.Fatalf("older apply: %v", err)
	}
	s.db.QueryRow(s.db.Rebind(`SELECT status FROM quotes WHERE client_uuid = ?`), ev.ClientUUID).Scan(&status)
	if status != models.QuoteAccepted {
		t.Errorf("older event clobbered newer state (status=%s)", status)
	}

	// Number collision across tills: same quote_no, DIFFERENT uuid → the
	// arriving copy is renumbered with a suffix, nothing is dropped.
	other := ev
	other.ClientUUID = "q-abc-2"
	other.QuoteNo = "QT-000001"
	other.UpdatedAt = "2026-10-13T10:00:00.000Z"
	rawOther, _ := json.Marshal(other)
	if err := s.applyQuote(rawOther); err != nil {
		t.Fatalf("collision apply: %v", err)
	}
	var qno2 string
	s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(quote_no,'') FROM quotes WHERE client_uuid = ?`), other.ClientUUID).Scan(&qno2)
	if qno2 == "QT-000001" || qno2 == "" {
		t.Errorf("collision not renumbered: %q", qno2)
	}
	if len(qno2) <= len("QT-000001") {
		t.Errorf("renumbered quote %q lacks a suffix", qno2)
	}
}

func TestQuoteMoneyPermissionsMirrorCheckout(t *testing.T) {
	s := quoteTestService(t)
	// A clerk with quotes.manage but NO discount/override permissions.
	clerk := &auth.Principal{ID: 2, Username: "clerk", RoleName: "Custom", Active: true,
		Permissions: map[string]bool{"quotes.manage": true}}

	// Discount without payments.apply_discount is refused.
	if _, err := s.CreateQuote(models.QuoteInput{
		CustomerName: "C", Items: quoteTestItems(), DiscountCents: 10000,
	}, clerk); err == nil {
		t.Fatal("discount accepted without payments.apply_discount")
	}
	// Off-catalog line price without payments.override_price is refused.
	s.db.Exec(`INSERT INTO categories (name, slug) VALUES ('C','c')`)
	var catID int64
	s.db.QueryRow(`SELECT id FROM categories LIMIT 1`).Scan(&catID)
	s.db.Exec(s.db.Rebind(`INSERT INTO products (id, sku, name, category_id, price_cents, stock_qty, track_stock, is_active)
                VALUES (21, 'MUG-1', 'Mug', ?, 50000, 0, 0, 1)`), catID)
	if _, err := s.CreateQuote(models.QuoteInput{
		CustomerName: "C",
		Items:        []models.QuoteItem{{ProductID: 21, SKU: "MUG-1", Name: "Mug", Qty: 1, UnitPriceCents: 99000}},
	}, clerk); err == nil {
		t.Fatal("off-catalog price accepted without payments.override_price")
	}
	// At-catalog pricing is fine for the same clerk.
	if _, err := s.CreateQuote(models.QuoteInput{
		CustomerName: "C",
		Items:        []models.QuoteItem{{ProductID: 21, SKU: "MUG-1", Name: "Mug", Qty: 2, UnitPriceCents: 50000}},
	}, clerk); err != nil {
		t.Fatalf("catalog-priced quote should pass: %v", err)
	}
}
