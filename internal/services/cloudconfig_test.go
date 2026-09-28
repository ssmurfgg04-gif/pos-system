package services

// cloudconfig_test.go — the central key vault client: cloud rows land in
// the settings store, guarded keys are never overwritten, deployment env
// vars are injected only when unset, and masked sentinels are skipped.

import (
	"os"
	"testing"

	"posapp/internal/settings"
)

func TestApplyCloudConfigWritesSettings(t *testing.T) {
	s := newProviderTestService(t)
	n := s.applyCloudConfig(map[string]string{
		"paystack_public_key":  "pk_test_x",
		"paystack_currency":    "KES",
		"till_number":          "123456",
		"config_synced_at":     "", // empty values are skipped entirely
	})
	if n != 3 {
		t.Fatalf("expected 3 applied rows, got %d", n)
	}
	if got := s.Settings().Get("paystack_public_key"); got != "pk_test_x" {
		t.Fatalf("paystack_public_key = %q", got)
	}
	if got := s.Settings().Get("till_number"); got != "123456" {
		t.Fatalf("till_number = %q", got)
	}
}

func TestApplyCloudConfigSkipsGuardedKeys(t *testing.T) {
	s := newProviderTestService(t)
	_ = s.Settings().Set("jwt_secret", "local-root")
	_ = s.Settings().Set("sync_device_secret", "local-identity")
	s.applyCloudConfig(map[string]string{
		"jwt_secret":         "evil-cloud-value",
		"sync_device_secret": "evil-cloud-identity",
	})
	if got := s.Settings().Get("jwt_secret"); got != "local-root" {
		t.Fatalf("jwt_secret must never be overwritten, got %q", got)
	}
	if got := s.Settings().Get("sync_device_secret"); got != "local-identity" {
		t.Fatalf("sync_device_secret must never be overwritten, got %q", got)
	}
}

func TestApplyCloudConfigSkipsMaskToken(t *testing.T) {
	s := newProviderTestService(t)
	_ = s.Settings().Set("paystack_secret_key", "sk_real")
	s.applyCloudConfig(map[string]string{
		"paystack_secret_key": settings.MaskToken, // echo of a masked secret must never land
	})
	if got := s.Settings().Get("paystack_secret_key"); got != "sk_real" {
		t.Fatalf("mask token must be skipped, key now %q", got)
	}
}

func TestApplyCloudConfigInjectsEnvOnlyWhenUnset(t *testing.T) {
	s := newProviderTestService(t)
	t.Setenv("PAYSTACK_CALLBACK_URL", "https://already-set.example/")
	s.applyCloudConfig(map[string]string{
		"paystack_callback_url": "https://awesomeposs.netlify.app/",
		"paystack_currency":     "KES", // no env mapping — must not appear in env
	})
	if got := os.Getenv("PAYSTACK_CALLBACK_URL"); got != "https://already-set.example/" {
		t.Fatalf("real env must win, got %q", got)
	}
	if _, ok := os.LookupEnv("KES"); ok {
		t.Fatalf("settings keys without env mapping must not be injected")
	}
}

func TestApplyCloudConfigIdempotent(t *testing.T) {
	s := newProviderTestService(t)
	rows := map[string]string{"paystack_public_key": "pk_live_x", "paystack_mode": "live"}
	if n := s.applyCloudConfig(rows); n != 2 {
		t.Fatalf("first apply should write 2, got %d", n)
	}
	if n := s.applyCloudConfig(rows); n != 0 {
		t.Fatalf("second apply must be a no-op, got %d", n)
	}
}
