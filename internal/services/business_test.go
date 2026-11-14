package services

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// v1.9: a join link / owner sign-in is for a FRESH till. A till that holds a
// shop's data is refused BEFORE the cloud is called (the cloud call consumes
// the link and moves the device, so a late check would be too late).
func TestJoinRefusesTillThatHoldsShopData(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write([]byte(`{"ok":true,"team_code":"OTHER-SHOP","store_name":"Other","role_name":"Cashier"}`))
	}))
	defer srv.Close()
	oldURL := cloudBaseURL
	cloudBaseURL = srv.URL
	t.Cleanup(func() { cloudBaseURL = oldURL })

	s := newSyncTestService(t)
	if _, err := s.db.Exec(`INSERT INTO customers (name, phone) VALUES ('Existing Customer', '0700000001')`); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	_, err := s.TeamJoinWithLink("https://x/join#t=OTHER&r=Cashier&k=abcdefghijklmnop", "New Worker", "4826")
	if !errors.Is(err, ErrTillHasShopData) {
		t.Fatalf("join on a till with data: got %v, want ErrTillHasShopData", err)
	}
	if _, err := s.OwnerCloudSignin("owner", "owner-pass-1", "till"); !errors.Is(err, ErrTillHasShopData) {
		t.Fatalf("owner sign-in on a till with data: got %v, want ErrTillHasShopData", err)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("the cloud must not be called for a refused join (calls=%d)", n)
	}
	if got := s.settings.Get("sync_team_code"); got == "OTHER-SHOP" {
		t.Fatal("the till's team must not change")
	}
}

// A fresh till (no products, orders or customers) may still join.
func TestJoinAllowsFreshTill(t *testing.T) {
	s := newSyncTestService(t)
	if s.hasShopData() {
		t.Fatal("a fresh database must count as having no shop data")
	}
	if err := s.guardFreshTillForJoin(); err != nil {
		t.Fatalf("fresh till refused: %v", err)
	}
}

// A till the cloud approved into a team keeps that team when the cloud
// config refreshes (before v1.9 the legacy single-store fallback flipped a
// second business's till back to the founding store's team).
func TestApprovedTillKeepsItsTeamOnRefresh(t *testing.T) {
	fake := newFakeSupabase(t)
	oldURL := cloudBaseURL
	cloudBaseURL = fake.srv.URL
	t.Cleanup(func() { cloudBaseURL = oldURL })

	s := newSyncTestService(t)
	_ = s.settings.Set("sync_team_code", "SECOND-BIZ")
	_ = s.settings.Set("sync_approved", "true")
	bs := s.resolveCloudStore()
	if bs == nil || bs.TeamCode != "SECOND-BIZ" {
		t.Fatalf("approved till lost its team: %+v", bs)
	}
}

// Registering a new business: refused once the till already belongs to one.
func TestRegisterBusinessRefusedForLinkedTill(t *testing.T) {
	s := newSyncTestService(t)
	_ = s.settings.Set("sync_team_code", "KIAMBU-MAIN")
	_ = s.settings.Set("sync_approved", "true")
	_ = s.settings.Set("sync_source", "cloud")
	if _, _, err := s.TeamRegisterBusiness("Second Shop"); err == nil {
		t.Fatal("a linked till must not register another business")
	}
}

// The cloud's new-business answer links the till to its OWN team and
// switches it to till-number M-Pesa until its STK route is verified.
func TestRegisterBusinessLinksOwnTeam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/rpc/sync_register_business" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"ok":true,"team_code":"TNEW-0001","store_name":"Second Shop","business_id":2}`))
	}))
	defer srv.Close()
	oldURL := cloudBaseURL
	cloudBaseURL = srv.URL
	t.Cleanup(func() { cloudBaseURL = oldURL })

	s := newSyncTestService(t)
	team, store, err := s.TeamRegisterBusiness("Second Shop")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if team != "TNEW-0001" || store != "Second Shop" {
		t.Fatalf("got %s / %s", team, store)
	}
	if s.settings.Get("sync_team_code") != "TNEW-0001" || !s.settings.GetBool("sync_approved", false) {
		t.Fatal("till must be linked + approved in its own team")
	}
	if s.settings.GetBool("stk_available", true) {
		t.Fatal("a new business takes M-Pesa on its own till number until STK is verified")
	}
}

// Payment numbers are digits only; a local save works offline.
func TestPaymentProfileValidation(t *testing.T) {
	s := newSyncTestService(t)
	_ = s.settings.Set("sync_disabled", "1")
	if _, err := s.PaymentProfileSet(PaymentProfileUpdate{TillNumber: "12a45"}); err == nil {
		t.Fatal("letters in a till number must be refused")
	}
	p, err := s.PaymentProfileSet(PaymentProfileUpdate{STKMode: "manual", TillNumber: "123 456"})
	if err != nil {
		t.Fatalf("offline manual save: %v", err)
	}
	if s.settings.Get("till_number") != "123456" || p.TillNumber != "123456" {
		t.Fatalf("till number not saved locally: %q", s.settings.Get("till_number"))
	}
	if _, err := s.PaymentProfileSet(PaymentProfileUpdate{STKMode: "own", OrgShortcode: "5551234"}); err == nil {
		t.Fatal("own STK needs the cloud link; offline must say so")
	}
}
