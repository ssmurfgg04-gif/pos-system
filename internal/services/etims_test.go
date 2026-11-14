package services

// etims_test.go — v1.10.0 eTIMS outbox on the till, against a fake cloud:
// a paid sale is queued and signed (stamp on the order, KRA invoice number
// as the document number), network trouble backs off and stays pending,
// a KRA rejection becomes "failed" until retried, a return of a signed
// sale goes out as a credit note pointing at the original, and sales from
// before eTIMS was switched on are never sent.

import (
	"errors"
	"testing"

	"posapp/internal/models"
)

type fakeEtims struct {
	calls []map[string]any
	reply func(body map[string]any) (map[string]any, int, error)
}

func useFakeEtims(t *testing.T, f *fakeEtims) {
	t.Helper()
	orig := etimsCall
	etimsCall = func(s *Service, action string, body map[string]any) (map[string]any, int, error) {
		cp := map[string]any{"action": action}
		for k, v := range body {
			cp[k] = v
		}
		f.calls = append(f.calls, cp)
		return f.reply(cp)
	}
	t.Cleanup(func() { etimsCall = orig })
}

func signedReply(n float64) map[string]any {
	return map[string]any{"ok": true, "stamp": map[string]any{
		"invcNo": n, "curRcptNo": n, "totRcptNo": n, "intrlData": "INTRL", "rcptSign": "SIGN",
		"sdcDateTime": "20261114143005", "sdcId": "KRACU0100000001", "mrcNo": "WIS01000001", "qr": "https://etims-sbx/qr",
	}}
}

func etimsOn(f *moneyFixture) {
	_ = f.s.settings.Set("etims_enabled", "true")
	_ = f.s.settings.Set("etims_enabled_at", "2000-01-01T00:00:00.000Z")
}

func TestEtimsSaleSignedAndStamped(t *testing.T) {
	f := newMoneyTill(t)
	fake := &fakeEtims{reply: func(b map[string]any) (map[string]any, int, error) { return signedReply(1), 200, nil }}
	useFakeEtims(t, fake)
	etimsOn(f)
	o, err := f.checkout("cash", 0, "uuid-etims-1")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	f.s.EtimsTick()
	if len(fake.calls) != 1 {
		t.Fatalf("want 1 cloud call, got %d", len(fake.calls))
	}
	call := fake.calls[0]
	if call["action"] != "sale" || call["kind"] != "S" || call["clientRef"] != "uuid-etims-1" {
		t.Fatalf("bad call: %v", call)
	}
	sale := call["sale"].(map[string]any)
	lines := sale["lines"].([]map[string]any)
	if len(lines) != 1 || lines[0]["itemKey"] != "sku:SKU-M1" || lines[0]["taxTy"] != "B" || lines[0]["lineTotalCents"] != int64(60000) {
		t.Fatalf("bad line: %v", lines)
	}
	if sale["payment"] != "cash" || sale["discountCents"] != int64(0) {
		t.Fatalf("bad header: %v", sale)
	}
	got, _ := f.s.GetOrder(o.ID)
	if got.Etims == nil || got.Etims.Status != "signed" || got.Etims.RcptSign != "SIGN" || got.Etims.SdcID != "KRACU0100000001" {
		t.Fatalf("stamp: %+v", got.Etims)
	}
	if got.InvoiceNumber != "KRACU0100000001/1" {
		t.Fatalf("invoice number = %q", got.InvoiceNumber)
	}
	// a second tick sends nothing new
	f.s.EtimsTick()
	if len(fake.calls) != 1 {
		t.Fatalf("signed sale was sent again (%d calls)", len(fake.calls))
	}
}

func TestEtimsBackoffAndRejection(t *testing.T) {
	f := newMoneyTill(t)
	mode := "net"
	fake := &fakeEtims{reply: func(b map[string]any) (map[string]any, int, error) {
		switch mode {
		case "net":
			return nil, 0, errors.New("dial tcp: no route")
		case "reject":
			return map[string]any{"ok": false, "error": "KRA: invalid item class", "retry": false}, 422, nil
		}
		return signedReply(5), 200, nil
	}}
	useFakeEtims(t, fake)
	etimsOn(f)
	o, err := f.checkout("cash", 0, "uuid-etims-2")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	f.s.EtimsTick()
	st := f.s.etimsStampFor("S", o.ID)
	if st == nil || st.Status != "pending" || st.Attempts != 1 {
		t.Fatalf("after network error: %+v", st)
	}
	// backed off: an immediate tick does not hammer the cloud
	f.s.EtimsTick()
	if len(fake.calls) != 1 {
		t.Fatalf("backoff ignored: %d calls", len(fake.calls))
	}
	// KRA rejects → failed with KRA's words
	mode = "reject"
	f.s.db.Exec(`UPDATE etims_receipts SET next_try_at = ''`)
	f.s.EtimsTick()
	st = f.s.etimsStampFor("S", o.ID)
	if st.Status != "failed" || st.LastError != "KRA: invalid item class" {
		t.Fatalf("after rejection: %+v", st)
	}
	status := f.s.EtimsStatusNow()
	if status.Failed != 1 || status.LastError == "" {
		t.Fatalf("status: %+v", status)
	}
	// owner fixes it and retries
	mode = "ok"
	if n := f.s.EtimsRetryFailed(); n != 1 {
		t.Fatalf("retry requeued %d", n)
	}
	f.s.EtimsTick()
	if st = f.s.etimsStampFor("S", o.ID); st.Status != "signed" || st.InvcNo != 5 {
		t.Fatalf("after retry: %+v", st)
	}
}

func TestEtimsCreditNoteAndCutoff(t *testing.T) {
	f := newMoneyTill(t)
	fake := &fakeEtims{reply: func(b map[string]any) (map[string]any, int, error) {
		if b["kind"] == "R" {
			return signedReply(2), 200, nil
		}
		return signedReply(1), 200, nil
	}}
	useFakeEtims(t, fake)
	// a sale BEFORE eTIMS is switched on is never sent
	before, err := f.checkout("cash", 0, "uuid-before")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	_ = f.s.settings.Set("etims_enabled", "true")
	_ = f.s.settings.Set("etims_enabled_at", nowStamp())
	o, err := f.checkout("cash", 0, "uuid-etims-3")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	f.s.EtimsTick()
	if f.s.etimsStampFor("S", before.ID) != nil {
		t.Fatal("a sale from before eTIMS was queued")
	}
	if st := f.s.etimsStampFor("S", o.ID); st == nil || st.Status != "signed" {
		t.Fatalf("sale not signed: %+v", st)
	}
	full, _ := f.s.GetOrder(o.ID)
	ret, err := f.s.CreateReturn(models.CreateReturnRequest{
		OrderID: o.ID, Method: "CASH", Reason: "damaged",
		Lines: []models.CreateReturnLine{{OrderItemID: full.Items[0].ID, QtyMilli: 1000}},
	}, f.principal())
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	f.s.EtimsTick()
	var last map[string]any
	for _, c := range fake.calls {
		if c["kind"] == "R" {
			last = c
		}
	}
	if last == nil || last["orgClientRef"] != "uuid-etims-3" {
		t.Fatalf("credit note not sent against the sale: %v", fake.calls)
	}
	if st := f.s.etimsStampFor("R", ret.ID); st == nil || st.Status != "signed" {
		t.Fatalf("credit note: %+v", st)
	}
}

func TestEtimsOffSendsNothing(t *testing.T) {
	f := newMoneyTill(t)
	fake := &fakeEtims{reply: func(b map[string]any) (map[string]any, int, error) { return signedReply(1), 200, nil }}
	useFakeEtims(t, fake)
	if _, err := f.checkout("cash", 0, "uuid-off"); err != nil {
		t.Fatalf("checkout: %v", err)
	}
	f.s.EtimsTick()
	if len(fake.calls) != 0 {
		t.Fatalf("eTIMS off but %d calls", len(fake.calls))
	}
}

func TestKenyaStampOf(t *testing.T) {
	cases := map[string]string{
		"2026-11-14T11:30:05.000Z": "20261114143005",
		"2026-11-14T11:30:05Z":     "20261114143005",
		"2026-11-14 11:30:05":      "20261114143005",
		"2026-11-14 11:30:05+00":   "20261114143005",
	}
	for in, want := range cases {
		if got := kenyaStampOf(in); got != want {
			t.Errorf("kenyaStampOf(%q) = %q, want %q", in, got, want)
		}
	}
}
