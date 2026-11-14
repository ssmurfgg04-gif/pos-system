package services

// business.go — one cloud, many businesses (v1.9, db/deploy-v190-multitenant.sql).
//
// LedgerPOS's cloud was built for one business: every fresh install queued
// under the founding business's only store, and a till moved between
// stores kept the old shop's products, stock and customers. Three rules
// now hold on the till side (the cloud enforces its own half):
//
//  1. A new business gets its OWN cloud store (TeamRegisterBusiness).
//  2. A till that already holds shop data never joins another shop
//     (guardFreshTillForJoin): join links and owner sign-in exist for
//     fresh machines, and refusing here is what stops one shop's data
//     leaking into another — including on a multi-shop server, where a
//     public join used to land in (and re-point) the default shop.
//  3. Each store decides where its M-Pesa money goes (payment profile);
//     the platform's shared till is the founding business's alone.

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// ErrTillHasShopData: this till's database already holds another shop's
// business data, so it may not be joined to a (possibly different) shop.
var ErrTillHasShopData = errors.New("this till already holds a shop's products, sales or customers")

// ErrSTKNotAvailable: the cloud refused an M-Pesa prompt for this store
// (its payments go to the shop's own till number, not a platform till).
var ErrSTKNotAvailable = errors.New("M-Pesa prompts are not set up for this shop yet, ask the customer to pay your till number and enter the M-Pesa code")

// hasShopData reports whether this database holds any business data of
// its own: a product, an order or a customer. A fresh install has none
// (the demo catalog is opt-in via SEED_DEMO).
func (s *Service) hasShopData() bool {
	for _, q := range []string{
		`SELECT COUNT(*) FROM products`,
		`SELECT COUNT(*) FROM orders`,
		`SELECT COUNT(*) FROM customers`,
	} {
		var n int
		if err := s.db.QueryRow(q).Scan(&n); err != nil {
			// unknown schema state: treat as "has data" (fail closed)
			return true
		}
		if n > 0 {
			return true
		}
	}
	return false
}

// guardFreshTillForJoin is called BEFORE a join link or owner sign-in
// reaches the cloud (both consume or move the device server-side, so the
// check cannot wait for the answer).
func (s *Service) guardFreshTillForJoin() error {
	if s.hasShopData() {
		return fmt.Errorf("%w. Joining it to a team would mix two shops' data. Use this link on a new install, or ask the owner to add this till from the shop it already belongs to", ErrTillHasShopData)
	}
	return nil
}

// TeamRegisterBusiness gives this till's shop its own business + store in
// the cloud (first run, or later from Settings → Team for a shop that set
// up offline). The till becomes that store's approved owner device.
func (s *Service) TeamRegisterBusiness(businessName string) (teamCode, storeName string, err error) {
	businessName = strings.TrimSpace(businessName)
	if businessName == "" {
		businessName = strings.TrimSpace(s.settings.Get("store_name"))
	}
	if len(businessName) < 2 || len(businessName) > 60 {
		return "", "", fmt.Errorf("give your business a name (2-60 characters) first")
	}
	if s.settings.GetBool("sync_approved", false) && s.settings.Get("sync_team_code") != "" &&
		s.settings.Get("sync_source") != "manual" {
		return "", "", fmt.Errorf("this till is already connected to a business in LedgerPOS Cloud")
	}
	tz := strings.TrimSpace(s.settings.Get("business_timezone"))
	if tz == "" {
		tz = "Africa/Nairobi"
	}
	if _, lerr := time.LoadLocation(tz); lerr != nil {
		tz = "Africa/Nairobi"
	}
	var res struct {
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
		TeamCode  string `json:"team_code"`
		StoreName string `json:"store_name"`
	}
	if err := rpcCall(strings.TrimRight(cloudBaseURL, "/"), "sync_register_business", map[string]any{
		"p_device_id":     s.deviceID(),
		"p_secret_hash":   secretHash(s.deviceSecret()),
		"p_device_name":   s.deviceName(),
		"p_app_version":   s.AppVersion(),
		"p_business_name": businessName,
		"p_business_tz":   tz,
	}, &res); err != nil {
		return "", "", fmt.Errorf("could not reach the LedgerPOS cloud, your shop works offline and you can connect it later in Settings → Team")
	}
	if !res.OK {
		return "", "", fmt.Errorf("%s", res.Error)
	}
	// The till may have queued events under a team it adopted from the
	// legacy bootstrap row while it waited; they are THIS shop's own
	// events, so they stay in the outbox and go to the new team.
	_ = s.settings.Set("sync_endpoint", strings.TrimRight(cloudProjectURL, "/"))
	_ = s.settings.Set("sync_source", "cloud")
	_ = s.settings.Set("sync_team_code", res.TeamCode)
	_ = s.settings.Set("sync_auto_approve", "false")
	_ = s.settings.Set("sync_enabled", "true")
	_ = s.settings.Set("sync_registered", "true")
	_ = s.settings.Set("sync_approved", "true")
	_ = s.settings.Set("sync_store_pending", "false")
	_ = s.settings.Set("sync_bootstrap_at", nowStamp())
	// New businesses take M-Pesa on their own till number until an STK
	// route is set up and verified for them.
	_ = s.settings.Set("stk_available", "false")
	s.Audit(0, "system", "BUSINESS_REGISTERED", "settings", "",
		truncStr(res.StoreName+" ("+res.TeamCode+")", 120))
	log.Printf("[cloud] registered business %q as %s", res.StoreName, res.TeamCode)
	return res.TeamCode, res.StoreName, nil
}

// PaymentProfile is where this store's M-Pesa money goes (never the passkey).
type PaymentProfile struct {
	STKMode       string `json:"stkMode"` // shared | own | manual
	STKAvailable  bool   `json:"stkAvailable"`
	TillNumber    string `json:"tillNumber"`
	PaybillNumber string `json:"paybillNumber"`
	DisplayName   string `json:"displayName"`
	OrgShortcode  string `json:"orgShortcode"`
	OwnRequested  bool   `json:"ownRequested"`
	OwnVerified   bool   `json:"ownVerified"`
	HasPasskey    bool   `json:"hasPasskey"`
	Linked        bool   `json:"linked"` // false: not connected to the cloud yet
}

type paymentProfileWire struct {
	OK            bool   `json:"ok"`
	Error         string `json:"error"`
	STKMode       string `json:"stk_mode"`
	STKAvailable  bool   `json:"stk_available"`
	TillNumber    string `json:"till_number"`
	PaybillNumber string `json:"paybill_number"`
	DisplayName   string `json:"display_name"`
	OrgShortcode  string `json:"org_shortcode"`
	OwnRequested  bool   `json:"own_requested"`
	OwnVerified   bool   `json:"own_verified"`
	HasPasskey    bool   `json:"has_passkey"`
}

func (w paymentProfileWire) profile() PaymentProfile {
	return PaymentProfile{
		STKMode: w.STKMode, STKAvailable: w.STKAvailable, TillNumber: w.TillNumber,
		PaybillNumber: w.PaybillNumber, DisplayName: w.DisplayName, OrgShortcode: w.OrgShortcode,
		OwnRequested: w.OwnRequested, OwnVerified: w.OwnVerified, HasPasskey: w.HasPasskey,
		Linked: true,
	}
}

// localPaymentProfile is the offline answer: the till numbers this till
// shows customers, no cloud STK.
func (s *Service) localPaymentProfile() PaymentProfile {
	return PaymentProfile{
		STKMode:       "manual",
		STKAvailable:  false,
		TillNumber:    s.settings.Get("till_number"),
		PaybillNumber: s.settings.Get("paybill_number"),
		DisplayName:   s.settings.Get("store_name"),
	}
}

// remember mirrors the cloud's answer locally: the customer display and
// receipts read till_number / paybill_number, and buniUsable reads
// stk_available so a store without an STK route never pushes.
func (s *Service) rememberPaymentProfile(p PaymentProfile) {
	_ = s.settings.Set("stk_available", boolStr(p.STKAvailable))
	if p.TillNumber != "" || p.PaybillNumber != "" {
		_ = s.settings.Set("till_number", p.TillNumber)
		_ = s.settings.Set("paybill_number", p.PaybillNumber)
	}
}

// PaymentProfileGet reads this store's profile from the cloud (and caches
// the parts the till needs); offline or unlinked → the local view.
func (s *Service) PaymentProfileGet() PaymentProfile {
	c, ok := s.syncConfig()
	if !ok || c.mode != "rpc" || !s.settings.GetBool("sync_approved", false) {
		return s.localPaymentProfile()
	}
	var w paymentProfileWire
	if err := rpcCall(c.base, "payment_profile_get", map[string]any{
		"p_device_id": c.device, "p_secret_hash": c.secretHash,
	}, &w); err != nil || !w.OK {
		p := s.localPaymentProfile()
		p.Linked = true
		return p
	}
	p := w.profile()
	s.rememberPaymentProfile(p)
	return p
}

// PaymentProfileUpdate is the owner saving where customers pay.
type PaymentProfileUpdate struct {
	STKMode       string  `json:"stkMode"`
	TillNumber    string  `json:"tillNumber"`
	PaybillNumber string  `json:"paybillNumber"`
	DisplayName   string  `json:"displayName"`
	OrgShortcode  string  `json:"orgShortcode"`
	OrgPasskey    *string `json:"orgPasskey"` // nil = keep the stored one
}

func digitsOnly(v string, max int) (string, bool) {
	v = strings.Join(strings.Fields(v), "")
	if len(v) > max {
		return v, false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return v, false
		}
	}
	return v, true
}

// PaymentProfileSet saves the profile: always locally (the till numbers
// customers see), and in the cloud when this till is linked.
func (s *Service) PaymentProfileSet(u PaymentProfileUpdate) (PaymentProfile, error) {
	till, ok1 := digitsOnly(u.TillNumber, 10)
	paybill, ok2 := digitsOnly(u.PaybillNumber, 10)
	short, ok3 := digitsOnly(u.OrgShortcode, 10)
	if !ok1 || !ok2 || !ok3 {
		return PaymentProfile{}, fmt.Errorf("till, paybill and short code are digits only (up to 10)")
	}
	mode := strings.TrimSpace(u.STKMode)
	if mode == "" {
		mode = "manual"
	}
	if mode != "manual" && mode != "own" && mode != "shared" {
		return PaymentProfile{}, fmt.Errorf("unknown payment mode")
	}
	if mode == "own" && short == "" {
		return PaymentProfile{}, fmt.Errorf("enter the short code M-Pesa prompts should pay")
	}
	name := strings.TrimSpace(u.DisplayName)
	if len(name) > 40 {
		name = name[:40]
	}
	_ = s.settings.Set("till_number", till)
	_ = s.settings.Set("paybill_number", paybill)

	c, ok := s.syncConfig()
	if !ok || c.mode != "rpc" || !s.settings.GetBool("sync_approved", false) {
		if mode != "manual" {
			return s.localPaymentProfile(), fmt.Errorf("saved your till number on this till; connect the shop to LedgerPOS Cloud (Settings → Team) before turning on M-Pesa prompts")
		}
		return s.localPaymentProfile(), nil
	}
	args := map[string]any{
		"p_stk_mode":       mode,
		"p_till_number":    till,
		"p_paybill_number": paybill,
		"p_display_name":   name,
		"p_org_shortcode":  short,
	}
	if u.OrgPasskey != nil {
		args["p_org_passkey"] = strings.TrimSpace(*u.OrgPasskey)
	}
	full := map[string]any{"p_device_id": c.device, "p_secret_hash": c.secretHash,
		"p_owner_capability": nil}
	for k, v := range args {
		full[k] = v
	}
	// a cached owner approval rides along (required once the shop has a
	// cloud owner login; see payment_profile_set)
	if tok := ownerCapabilityTokenFor(c); tok != "" {
		full["p_owner_capability"] = tok
	}
	var w paymentProfileWire
	if err := rpcCall(c.base, "payment_profile_set", full, &w); err != nil {
		return s.localPaymentProfile(), fmt.Errorf("saved locally, but the cloud could not be reached: %v", err)
	}
	if !w.OK {
		return s.localPaymentProfile(), fmt.Errorf("%s", w.Error)
	}
	p := w.profile()
	s.rememberPaymentProfile(p)
	s.Audit(0, "system", "PAYMENT_PROFILE_SAVED", "settings", "",
		truncStr("mode "+mode+" till "+till+" paybill "+paybill, 120))
	return p, nil
}
