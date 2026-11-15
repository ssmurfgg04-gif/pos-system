package services

import (
	"context"
	"encoding/csv"
	"fmt"
	"strings"
	"testing"

	"posapp/internal/models"
)

func TestJournalBalancesAndPostsEachSide(t *testing.T) {
	f := newMoneyTill(t)
	p := f.principal()
	f.s.db.Exec(`UPDATE products SET cost_cents = 35000 WHERE id = ?`, f.prodID)
	gift := addProduct(t, f, "GC1", 100000, "", true)
	exempt := addProduct(t, f, "EX1", 20000, "A", false)

	// 1) cash: 2 tees (1,200.00 incl. 16% VAT) + a 1,000 gift card + exempt 200
	o1, err := f.s.Checkout(context.Background(), p, models.CheckoutRequest{
		Items:         []models.CheckoutItem{{ProductID: f.prodID, QtyMilli: 2000}, {ProductID: gift, QtyMilli: 1000}, {ProductID: exempt, QtyMilli: 1000}},
		PaymentMethod: models.MethodCash, ClientUUID: "acc-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 2) on the customer's tab: 1 tee
	if _, err := f.s.Checkout(context.Background(), p, models.CheckoutRequest{
		Items: []models.CheckoutItem{{ProductID: f.prodID, QtyMilli: 1000}}, PaymentMethod: models.MethodAccount,
		CustomerID: f.cust.ID, ClientUUID: "acc-2",
	}); err != nil {
		t.Fatal(err)
	}
	// 3) the customer pays 300 off the tab
	if _, err := f.s.RecordCustomerPayment(f.cust.ID, 30000, "cash", p); err != nil {
		t.Fatal(err)
	}
	// 4) one tee returned for cash
	got, _ := f.s.GetOrder(o1.ID)
	var teeItem int64
	for _, it := range got.Items {
		if it.SKU == "SKU-M1" {
			teeItem = it.ID
		}
	}
	if _, err := f.s.CreateReturn(models.CreateReturnRequest{OrderID: o1.ID, Lines: []models.CreateReturnLine{{OrderItemID: teeItem, QtyMilli: 1000}}}, p); err != nil {
		t.Fatal(err)
	}
	// 5) electricity paid by M-Pesa
	var typeID int64
	f.s.db.QueryRow(`SELECT id FROM expense_types WHERE name = 'Electricity'`).Scan(&typeID)
	if _, err := f.s.CreateExpense(models.CreateExpenseRequest{ExpenseTypeID: typeID, AmountCents: 250000, Method: "MPESA"}, p); err != nil {
		t.Fatal(err)
	}

	today := eatToday()
	j, err := f.s.Journal(today, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Days) != 1 {
		t.Fatalf("days = %d", len(j.Days))
	}
	d := j.Days[0]
	if d.DebitCents != d.CreditCents || d.RoundingCents != 0 {
		t.Fatalf("unbalanced or rounding: %+v", d)
	}
	sum := func(key string) (dr, cr int64) {
		for _, l := range d.Lines {
			if l.Account == key {
				dr += l.DebitCents
				cr += l.CreditCents
			}
		}
		return
	}
	// sales: 1,200 + 1,000 + 200 cash; 600 tab. VAT 16% inside 1,200 and 600:
	// 16552 and 8276. Sales ex VAT and ex gift = 2,000 + 600 - 24,828c.
	if dr, _ := sum("cash"); dr != 240000 {
		t.Errorf("cash in = %d", dr)
	}
	if dr, cr := sum("receivable"); dr != 60000 || cr != 30000 {
		t.Errorf("receivable dr %d cr %d", dr, cr)
	}
	if _, cr := sum("gift_cards"); cr != 100000 {
		t.Errorf("gift cards = %d", cr)
	}
	if _, cr := sum("vat"); cr != 16552+8276 {
		t.Errorf("vat out = %d", cr)
	}
	// return of one tee (600.00 of a 2,400.00 sale with 165.52 VAT): VAT share 41.38
	if dr, _ := sum("vat"); dr != 4138 {
		t.Errorf("vat back = %d", dr)
	}
	if dr, cr := sum("cogs"); dr != 3*35000 || cr != 35000 {
		t.Errorf("cogs dr %d cr %d", dr, cr)
	}
	if dr, cr := sum("expenses"); dr != 250000 || cr != 0 {
		t.Errorf("expense %d", dr)
	}
	if _, cr := sum("mpesa"); cr != 250000 {
		t.Errorf("mpesa out %d", cr)
	}

	for _, format := range []string{"quickbooks", "xero", "detail"} {
		out, name, err := f.s.JournalCSV(today, today, format)
		if err != nil || !strings.HasSuffix(name, ".csv") {
			t.Fatalf("%s: %v", format, err)
		}
		rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
		if err != nil || len(rows) != len(d.Lines)+1 {
			t.Fatalf("%s rows %d: %v", format, len(rows), err)
		}
		if format == "xero" {
			var total float64
			for _, r := range rows[1:] {
				var v float64
				if _, err := fmtSscan(r[5], &v); err != nil {
					t.Fatalf("amount %q", r[5])
				}
				total += v
			}
			if total > 0.001 || total < -0.001 {
				t.Fatalf("xero journal does not net to zero: %v", total)
			}
		}
	}
	if _, _, err := f.s.JournalCSV(today, today, "sage"); err == nil {
		t.Fatal("unknown format accepted")
	}
}

func TestAccountingAccountsSave(t *testing.T) {
	f := newMoneyTill(t)
	if err := f.s.SaveAccountingAccounts(map[string]AccountRef{"sales": {Name: "Shop sales", Code: "4000"}, "bogus": {Name: "x"}}); err != nil {
		t.Fatal(err)
	}
	for _, a := range f.s.AccountingAccounts() {
		if a.Key == "sales" && (a.Name != "Shop sales" || a.Code != "4000") {
			t.Fatalf("sales = %+v", a)
		}
		if a.Key == "vat" && a.Name != "VAT Payable" {
			t.Fatalf("default lost: %+v", a)
		}
	}
	if err := f.s.SaveAccountingAccounts(map[string]AccountRef{"vat": {Code: "VERYLONGCODE1"}}); err == nil {
		t.Fatal("long code accepted")
	}
}

func fmtSscan(s string, v *float64) (int, error) { return fmt.Sscan(s, v) }
