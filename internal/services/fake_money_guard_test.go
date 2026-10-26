package services

// fake_money_guard_test.go — regression tests for the fake-success fix:
// a shop that never configured a real payment provider must NEVER get an
// auto-completing STK provider, and a till still set to the RETIRED Daraja
// environments must fail loudly instead of silently falling back to the
// mock. (v1.4.4: Daraja is gone — real STK rides the Buni cloud route.)

import (
	"path/filepath"
	"strings"
	"testing"

	"posapp/internal/database"
	"posapp/internal/settings"
)

func newProviderTestService(t *testing.T) *Service {
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
	return New(db, st, nil, nil)
}

func TestGetProviderRefusesMockWithoutOptIn(t *testing.T) {
	s := newProviderTestService(t)
	s.Settings().Set("mpesa_env", "mock")
	t.Setenv("ALLOW_MOCK_PAYMENTS", "")

	p, err := s.GetProvider()
	if err == nil || p != nil {
		t.Fatalf("mock must be refused without ALLOW_MOCK_PAYMENTS: got provider=%v err=%v", p, err)
	}
	if !strings.Contains(err.Error(), "mock") {
		t.Fatalf("error should mention mock: %v", err)
	}
	// Route reports manual: the till falls back to receipt-code entry.
	if got := s.MpesaRoute(); got != "manual" {
		t.Fatalf("route with no providers should be manual, got %q", got)
	}
}

func TestGetProviderAllowsMockWithOptIn(t *testing.T) {
	s := newProviderTestService(t)
	s.Settings().Set("mpesa_env", "mock")
	t.Setenv("ALLOW_MOCK_PAYMENTS", "true")

	p, err := s.GetProvider()
	if err != nil || p == nil {
		t.Fatalf("mock should be available with explicit opt-in: %v", err)
	}
	if p.Name() != "mock" {
		t.Fatalf("expected mock provider, got %q", p.Name())
	}
}

func TestGetProviderFailsLoudlyOnRetiredDarajaEnv(t *testing.T) {
	// v1.4.4: Daraja is retired. A till still set to sandbox/production
	// must get a LOUD error — never a provider, never a silent mock.
	s := newProviderTestService(t)
	s.Settings().Set("mpesa_env", "production")
	t.Setenv("ALLOW_MOCK_PAYMENTS", "")

	p, err := s.GetProvider()
	if err == nil || p != nil {
		t.Fatalf("retired Daraja env must error, not fall back to mock: provider=%v", p)
	}
	if !strings.Contains(err.Error(), "Daraja was retired") {
		t.Fatalf("error should name the retirement: %v", err)
	}
	if got := s.MpesaRoute(); got != "manual" {
		t.Fatalf("route with retired env should be manual, got %q", got)
	}

	// Same for the sandbox environment.
	s.Settings().Set("mpesa_env", "sandbox")
	if p, err := s.GetProvider(); err == nil || p != nil {
		t.Fatalf("sandbox env must error too: provider=%v err=%v", p, err)
	}
}

func TestGetProviderNilForManualMode(t *testing.T) {
	s := newProviderTestService(t)
	s.Settings().Set("mpesa_env", "manual")
	t.Setenv("ALLOW_MOCK_PAYMENTS", "")

	p, err := s.GetProvider()
	if err != nil || p != nil {
		t.Fatalf("manual mode has no STK provider (no error): got %v, %v", p, err)
	}
}

func TestGetProviderIgnoresLegacyDarajaCredentials(t *testing.T) {
	// Old databases may still carry Daraja credential rows; with the
	// retirement they must arm NOTHING (the loud-error contract holds even
	// when the credential set would have been complete).
	s := newProviderTestService(t)
	st := s.Settings()
	st.Set("mpesa_env", "production")
	st.Set("mpesa_consumer_key", "ck")
	st.Set("mpesa_consumer_secret", "cs")
	st.Set("mpesa_shortcode", "174379")
	st.Set("mpesa_passkey", "pk")
	t.Setenv("ALLOW_MOCK_PAYMENTS", "")

	p, err := s.GetProvider()
	if err == nil || p != nil {
		t.Fatalf("legacy Daraja credentials must not yield a provider: %v", err)
	}
}
