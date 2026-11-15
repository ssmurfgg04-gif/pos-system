package services

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

type promoFixture struct {
	*moneyFixture
	smsCalls [][]string
	smsFail  func(phones []string) ([]promoSendResult, error)
}

func newPromoTill(t *testing.T) *promoFixture {
	f := &promoFixture{moneyFixture: newMoneyTill(t)}
	_ = f.s.settings.Set("sms_username", "duka")
	_ = f.s.settings.Set("sms_api_key", "k")
	_ = f.s.settings.Set("store_name", "Duka Bora")
	_ = f.s.settings.Set("store_phone", "0711000000")
	_ = f.s.settings.Set("promo_quiet_start", "00:00")
	_ = f.s.settings.Set("promo_quiet_end", "00:00") // no quiet window in tests unless set
	f.s.promoSMSForTest = func(phones []string, body string) ([]promoSendResult, error) {
		f.smsCalls = append(f.smsCalls, append([]string{body}, phones...))
		if f.smsFail != nil {
			return f.smsFail(phones)
		}
		out := []promoSendResult{}
		for _, p := range phones {
			out = append(out, promoSendResult{phone: p, ok: true, ref: "ATXid_" + p})
		}
		return out, nil
	}
	return f
}

func (f *promoFixture) addCustomer(t *testing.T, name, phone string, optIn bool) int64 {
	t.Helper()
	res, err := f.s.db.Exec(`INSERT INTO customers (name, phone, marketing_opt_in, is_active) VALUES (?, ?, ?, 1)`, name, phone, btoi(optIn))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestPromoOnlyConsentingCustomersWithOptOutLine(t *testing.T) {
	f := newPromoTill(t)
	p := f.principal()
	f.addCustomer(t, "Wanjiku Kamau", "0722 111 222", true)
	f.addCustomer(t, "Otieno", "+254733444555", true)
	f.addCustomer(t, "No Consent", "0744000111", false)
	f.addCustomer(t, "Bad Phone", "12345", true)
	f.addCustomer(t, "Twin", "254722111222", true) // same number as Wanjiku: once

	a, err := f.s.PromoAudiencePreview(PromoSegment{}, "Hi {name}, 10% off at {shop} this weekend!")
	if err != nil {
		t.Fatal(err)
	}
	if a.Count != 2 || a.NoConsent < 1 || a.BadPhone != 1 {
		t.Fatalf("audience %+v", a)
	}
	if !strings.Contains(a.PreviewBody, "To stop offers") || !strings.Contains(a.PreviewBody, "0711000000") {
		t.Fatalf("no opt-out line: %q", a.PreviewBody)
	}
	c, err := f.s.CreatePromo(CreatePromoRequest{Name: "Weekend", Channel: "sms", Body: "Hi {name}, 10% off at {shop} this weekend!"}, p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != "draft" || c.Total != 2 {
		t.Fatalf("campaign %+v", c)
	}
	// nothing goes out before Start
	if f.s.DrainPromos(time.Now()) != 0 || len(f.smsCalls) != 0 {
		t.Fatal("draft sent")
	}
	if _, err := f.s.PromoAction(c.ID, "start", p); err != nil {
		t.Fatal(err)
	}
	if n := f.s.DrainPromos(time.Now()); n != 2 {
		t.Fatalf("sent %d", n)
	}
	f.s.DrainPromos(time.Now()) // finishes
	got, _ := f.s.GetPromo(c.ID)
	if got.Status != "done" || got.Sent != 2 || got.Failed != 0 {
		t.Fatalf("after drain %+v", got)
	}
	bodies := strings.Join(func() []string {
		var b []string
		for _, call := range f.smsCalls {
			b = append(b, call[0])
		}
		return b
	}(), "|")
	if !strings.Contains(bodies, "Hi Wanjiku, 10% off at Duka Bora") || !strings.Contains(bodies, "Hi Otieno,") {
		t.Fatalf("personalisation: %s", bodies)
	}
	// running again sends nobody twice
	f.s.DrainPromos(time.Now())
	if len(f.smsCalls) != 2 {
		t.Fatalf("calls %d", len(f.smsCalls))
	}
}

func TestPromoOptOutStopsPendingAndSyncs(t *testing.T) {
	f := newPromoTill(t)
	p := f.principal()
	f.addCustomer(t, "Amina", "0722000001", true)
	f.addCustomer(t, "Brian", "0722000002", true)
	c, _ := f.s.CreatePromo(CreatePromoRequest{Name: "x", Channel: "sms", Body: "Offer"}, p)
	n, err := f.s.OptOutPhone("+254 722 000 002", p)
	if err != nil || n != 1 {
		t.Fatalf("opt out %d %v", n, err)
	}
	f.s.PromoAction(c.ID, "start", p)
	f.s.DrainPromos(time.Now())
	f.s.DrainPromos(time.Now())
	if len(f.smsCalls) != 1 || f.smsCalls[0][1] != "254722000001" {
		t.Fatalf("calls %v", f.smsCalls)
	}
	var ev int
	f.s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE entity = 'consent'`).Scan(&ev)
	if ev != 1 {
		t.Fatalf("consent events %d", ev)
	}
	// another till's newer opt-in wins; an older one does not
	raw, _ := json.Marshal(map[string]any{"phone": "0722000002", "optIn": true, "at": "2000-01-01T00:00:00.000Z"})
	_ = f.s.applyConsent(raw)
	var opt int
	f.s.db.QueryRow(`SELECT marketing_opt_in FROM customers WHERE phone = '0722000002'`).Scan(&opt)
	if opt != 0 {
		t.Fatal("stale consent event re-subscribed the customer")
	}
}

func TestPromoQuietHoursAndBatching(t *testing.T) {
	f := newPromoTill(t)
	p := f.principal()
	for i := 0; i < 250; i++ {
		f.addCustomer(t, "C", "07220"+leftPad(i, 5), true)
	}
	c, err := f.s.CreatePromo(CreatePromoRequest{Name: "big", Channel: "sms", Body: "Sale today at {shop}"}, p)
	if err != nil || c.Total != 250 {
		t.Fatalf("%v %+v", err, c)
	}
	f.s.PromoAction(c.ID, "start", p)
	_ = f.s.settings.Set("promo_quiet_start", "20:00")
	_ = f.s.settings.Set("promo_quiet_end", "08:00")
	night := time.Date(2026, 11, 15, 22, 30, 0, 0, eastAfrica)
	if f.s.DrainPromos(night) != 0 || len(f.smsCalls) != 0 {
		t.Fatal("sent during quiet hours")
	}
	morning := time.Date(2026, 11, 16, 9, 0, 0, 0, eastAfrica)
	if n := f.s.DrainPromos(morning); n != 250 {
		t.Fatalf("sent %d", n)
	}
	// same body for everyone → one call per 100 numbers
	if len(f.smsCalls) != 3 || len(f.smsCalls[0]) != 101 {
		t.Fatalf("calls %d first %d", len(f.smsCalls), len(f.smsCalls[0]))
	}
}

func leftPad(i, w int) string {
	s := strings.Repeat("0", w) + strconv.Itoa(i)
	return s[len(s)-w:]
}

func TestPromoProviderTroublePausesAndRetries(t *testing.T) {
	f := newPromoTill(t)
	p := f.principal()
	f.addCustomer(t, "A", "0722000001", true)
	f.addCustomer(t, "B", "0722000002", true)
	c, _ := f.s.CreatePromo(CreatePromoRequest{Name: "x", Channel: "sms", Body: "Offer"}, p)
	f.s.PromoAction(c.ID, "start", p)
	// out of credit: the campaign pauses, nobody is marked sent
	f.smsFail = func(phones []string) ([]promoSendResult, error) {
		out := []promoSendResult{}
		for _, ph := range phones {
			out = append(out, promoSendResult{phone: ph, retry: true, stopping: true, errMsg: "InsufficientBalance"})
		}
		return out, nil
	}
	f.s.DrainPromos(time.Now())
	got, _ := f.s.GetPromo(c.ID)
	if got.Status != "paused" || got.Sent != 0 || !strings.Contains(got.LastError, "InsufficientBalance") {
		t.Fatalf("after no credit %+v", got)
	}
	// topped up: resume, network error once, then fine
	f.smsFail = func([]string) ([]promoSendResult, error) { return nil, errors.New("timeout") }
	f.s.PromoAction(c.ID, "resume", p)
	f.s.DrainPromos(time.Now())
	f.smsFail = nil
	f.s.DrainPromos(time.Now().Add(30 * time.Minute)) // after the back-off
	f.s.DrainPromos(time.Now().Add(31 * time.Minute))
	got, _ = f.s.GetPromo(c.ID)
	if got.Sent != 2 || got.Status != "done" {
		t.Fatalf("after retry %+v", got)
	}
}

// A till that stops while a batch is out never sends that batch again.
func TestPromoCrashMidSendNeverResends(t *testing.T) {
	f := newPromoTill(t)
	p := f.principal()
	f.addCustomer(t, "A", "0722000001", true)
	c, _ := f.s.CreatePromo(CreatePromoRequest{Name: "x", Channel: "sms", Body: "Offer"}, p)
	f.s.PromoAction(c.ID, "start", p)
	// simulate the crash: the row was claimed 'sending' 20 minutes ago
	old := time.Now().Add(-20 * time.Minute).UTC().Format(nowStampLayout)
	f.s.db.Exec(`UPDATE promo_recipients SET status = 'sending', attempts = 1, next_try_at = ? WHERE campaign_id = ?`, old, c.ID)
	f.s.DrainPromos(time.Now())
	f.s.DrainPromos(time.Now())
	if len(f.smsCalls) != 0 {
		t.Fatal("re-sent a message whose result was unknown")
	}
	got, _ := f.s.GetPromo(c.ID)
	if got.Failed != 1 || got.Status != "done" {
		t.Fatalf("%+v", got)
	}
}

func TestPromoNeedsProviderAndConsent(t *testing.T) {
	f := newPromoTill(t)
	p := f.principal()
	if _, err := f.s.CreatePromo(CreatePromoRequest{Name: "x", Channel: "sms", Body: "Offer"}, p); err == nil {
		t.Fatal("campaign with nobody who agreed")
	}
	f.addCustomer(t, "A", "0722000001", true)
	_ = f.s.settings.Set("sms_api_key", "")
	c, err := f.s.CreatePromo(CreatePromoRequest{Name: "x", Channel: "sms", Body: "Offer"}, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PromoAction(c.ID, "start", p); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("start without SMS account: %v", err)
	}
	if _, err := f.s.CreatePromo(CreatePromoRequest{Name: "x", Channel: "fax", Body: "Offer"}, p); err == nil {
		t.Fatal("unknown channel")
	}
}

func TestSMSParts(t *testing.T) {
	if smsParts(strings.Repeat("a", 160)) != 1 || smsParts(strings.Repeat("a", 161)) != 2 {
		t.Fatal("gsm")
	}
	if smsParts(strings.Repeat("a", 69)+"😀") != 1 || smsParts(strings.Repeat("a", 70)+"😀") != 2 {
		t.Fatal("unicode")
	}
	if smsParts(strings.Repeat("€", 81)) != 2 { // € takes two places
		t.Fatal("escape")
	}
}

// The Africa's Talking call: form fields, apiKey header, per-number codes.
func TestAfricasTalkingRequestAndCodes(t *testing.T) {
	f := newMoneyTill(t)
	var gotForm url.Values
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version1/messaging" {
			http.NotFound(w, r)
			return
		}
		gotKey = r.Header.Get("apiKey")
		_ = r.ParseForm()
		gotForm = r.PostForm
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"SMSMessageData":{"Message":"Sent to 2/3","Recipients":[
			{"number":"+254711000001","statusCode":101,"status":"Success","messageId":"ATXid_1"},
			{"number":"+254711000002","statusCode":403,"status":"InvalidPhoneNumber"},
			{"number":"+254711000003","statusCode":405,"status":"InsufficientBalance"}]}}`))
	}))
	defer srv.Close()
	_ = f.s.settings.Set("sms_api_url", srv.URL)
	_ = f.s.settings.Set("sms_username", "duka")
	_ = f.s.settings.Set("sms_api_key", "secret-key")
	_ = f.s.settings.Set("sms_sender_id", "DUKABORA")
	res, err := f.s.sendSMSBatch([]string{"254711000001", "254711000002", "254711000003"}, "Hi")
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "secret-key" || gotForm.Get("username") != "duka" || gotForm.Get("from") != "DUKABORA" ||
		gotForm.Get("to") != "+254711000001,+254711000002,+254711000003" || gotForm.Get("message") != "Hi" {
		t.Fatalf("request: key %q form %v", gotKey, gotForm)
	}
	if !res[0].ok || res[0].ref != "ATXid_1" || res[1].ok || res[1].retry || !res[2].stopping {
		t.Fatalf("codes: %+v", res)
	}
}
