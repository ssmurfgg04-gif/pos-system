package services

// payments_config.go — the till-facing payment capability snapshot and the
// small payment helpers shared by the surviving rails. Since v1.4.4 the
// Paystack integration is retired: KCB Buni (via the cloud Edge Functions)
// is the ONLY real-money STK rail, alongside manual receipt entry, cash,
// store credit, loyalty and split tenders (cash/credit/mpesa).

import (
	"strings"
	"time"

	"posapp/internal/models"
)

// PaymentConfig powers the till's tender buttons (public data only).
func (s *Service) PaymentConfig() *models.PaymentConfig {
	cfg := &models.PaymentConfig{
		CreditEnabled:  s.settings.GetBool("credit_enabled", true),
		LoyaltyEnabled: s.settings.GetBool("loyalty_enabled", true),
	}
	cfg.Mpesa.Env = s.settings.GetString("mpesa_env", "manual")
	cfg.Mpesa.Route = s.MpesaRoute()
	cfg.Mpesa.Till = s.settings.Get("till_number")
	cfg.Mpesa.Paybill = s.settings.Get("paybill_number")
	return cfg
}

// mpesaPhoneOK accepts the normalized 2547/2541 form and local 07/01 forms.
func mpesaPhoneOK(p string) bool {
	p = strings.TrimSpace(p)
	if len(p) == 12 && (strings.HasPrefix(p, "2547") || strings.HasPrefix(p, "2541")) {
		return true
	}
	if len(p) == 10 && (strings.HasPrefix(p, "07") || strings.HasPrefix(p, "01")) {
		return true
	}
	return false
}

// parseStamp accepts the created_at layouts the app has written over time
// (current UTC RFC3339 stamp and legacy SQLite datetime).
func parseStamp(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t, true
	}
	return time.Time{}, false
}
