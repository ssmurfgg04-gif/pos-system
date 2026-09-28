// Package services holds the transactional business logic: checkout,
// payment completion (STK callback/query + manual receipt), voiding,
// offline sync, the STK sweeper, shifts, design board, and reports.
//
// CONCURRENCY DISCIPLINE (mandatory): SQLite runs with a single pooled
// connection. Never issue a query while rows from another query are open —
// collect first, close rows, then enrich. Settings reads go through the
// in-memory cache (settings.Store) and are always safe mid-iteration.
package services

import (
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"posapp/internal/database"
	"posapp/internal/mpesa"
	"posapp/internal/offsite"
	"posapp/internal/paystack"
	"posapp/internal/printer"
	"posapp/internal/settings"
	"posapp/internal/ws"
)

type Service struct {
	db       *database.DB
	settings *settings.Store
	hub      *ws.Hub
	printer  *printer.Worker
	offsite  *offsite.Worker
	mock     *mpesa.Mock

	darajaKey string
	daraja    *mpesa.Daraja

	paystackKey string
	paystack    *paystack.Client

	appVersion string

	shopID string // ws broadcast scoping (set by the ShopPool)

	orderSeqMu sync.Mutex
}

func New(db *database.DB, st *settings.Store, hub *ws.Hub, pw *printer.Worker) *Service {
	return &Service{db: db, settings: st, hub: hub, printer: pw, mock: mpesa.NewMock(4*time.Second, 0)}
}

// Settings exposes the shop's settings store (per-tenant).
func (s *Service) Settings() *settings.Store { return s.settings }

// DB exposes the shop's database handle.
func (s *Service) DB() *database.DB { return s.db }

// SetPrinter swaps the shared printer worker (pool wiring after construction).
func (s *Service) SetPrinter(pw *printer.Worker) { s.printer = pw }

// AttachOffsite wires the encrypted-upload worker (set by main; nil = local only).
func (s *Service) AttachOffsite(w *offsite.Worker) {
	s.offsite = w
}

// enqueueOffsite schedules an encrypted push; no-op unless enabled + attached.
func (s *Service) enqueueOffsite(snapshotPath string) {
	if s.offsite == nil || !s.settings.GetBool("offsite_enabled", false) {
		return
	}
	s.offsite.Enqueue(snapshotPath)
}

// Audit records an action in the audit log (best-effort).
func (s *Service) Audit(userID int64, username, action, entity, entityID, details string) {
	_, err := s.db.Exec(s.db.Rebind(
		`INSERT INTO audit_log (user_id, username, action, entity, entity_id, details) VALUES (?, ?, ?, ?, ?, ?)`),
		userID, username, action, entity, entityID, details)
	if err != nil {
		log.Printf("[audit] write failed: %v", err)
	}
}

// SetShopID tags this service's broadcasts with the shop (ws routing).
func (s *Service) SetShopID(id string) { s.shopID = id }

// broadcast pushes a ws event if the hub is wired. Scoped to this shop —
// another shop's till or browser tab must never see it.
func (s *Service) broadcast(event string, data any) {
	if s.hub != nil {
		s.hub.BroadcastJSONForShop(event, data, s.shopID)
	}
}

// ---- Events ----

const (
	EventOrderCreated   = "ORDER_CREATED"
	EventOrderPaid      = "ORDER_PAID"
	EventOrderVoided    = "ORDER_VOIDED"
	EventDesignUpdate   = "DESIGN_JOB_UPDATED"
	EventShiftUpdate    = "SHIFT_UPDATED"
	EventSettingsUpdate = "SETTINGS_UPDATED"
	EventHeldUpdate     = "HELD_SALES_UPDATED"
	EventSyncUpdate     = "TEAM_SYNC_UPDATED"
)

// ---- M-Pesa provider factory ----

// GetProvider returns the active STK provider per settings.
//
// FAKE-MONEY GUARD: the auto-succeeding mock provider is ONLY available
// when ALLOW_MOCK_PAYMENTS=true (demos/tests). Shops that never set Daraja
// credentials get "manual" mode — cashier types the M-Pesa receipt code;
// nothing ever completes by itself. Incomplete Daraja credentials are a
// loud error, never a silent fall back to fake money.
func (s *Service) GetProvider() (mpesa.Provider, error) {
	env := s.settings.GetString("mpesa_env", "manual")
	switch env {
	case "sandbox", "production":
		key := s.settings.Get("mpesa_consumer_key")
		secret := s.settings.Get("mpesa_consumer_secret")
		shortcode := s.settings.Get("mpesa_shortcode")
		passkey := s.settings.Get("mpesa_passkey")
		callback := s.settings.Get("mpesa_callback_url")
		if key == "" || secret == "" || shortcode == "" || passkey == "" {
			return nil, fmt.Errorf("mpesa_env=%s but Daraja credentials are incomplete — set them (or switch M-Pesa to Paystack) in settings; refusing to simulate payments", env)
		}
		cacheKey := env + "|" + shortcode + "|" + key + "|" + secret + "|" + passkey + "|" + callback
		if s.darajaKey != cacheKey {
			s.darajaKey = cacheKey
			s.daraja = mpesa.NewDaraja(env, shortcode, passkey, key, secret, callback)
		}
		return s.daraja, nil
	case "mock":
		// Training wheels: only with the explicit env opt-in. This is what
		// keeps a till from "confirming" payments nobody made.
		if os.Getenv("ALLOW_MOCK_PAYMENTS") == "true" {
			s.applyMockConfig()
			return s.mock, nil
		}
		return nil, fmt.Errorf("mock M-Pesa is disabled (ALLOW_MOCK_PAYMENTS!=true) — real shops never auto-complete payments")
	default: // "manual", "paystack", or anything else: no local STK provider.
		// When Paystack is configured, STK rides the Paystack mobile-money
		// charge (see InitiateSTK) and verifies through SweepPaystack.
		return nil, nil
	}
}

// stkProviderReady reports whether a non-Paystack STK provider is usable.
func (s *Service) stkProviderReady() bool {
	p, err := s.GetProvider()
	return err == nil && p != nil
}

// MpesaRoute tells the till how M-Pesa STK currently runs:
//   - "paystack": real STK through the shop's Paystack integration
//   - "daraja":   real STK through Safaricom Daraja credentials
//   - "mock":     training mode (ALLOW_MOCK_PAYMENTS=true only)
//   - "manual":   no STK — cashier enters the receipt code
func (s *Service) MpesaRoute() string {
	if s.paystackConfigured() && s.settings.GetBool("paystack_enabled", true) {
		return "paystack"
	}
	if s.stkProviderReady() {
		if p, _ := s.GetProvider(); p != nil {
			return p.Name()
		}
	}
	return "manual"
}

func (s *Service) applyMockConfig() {
	delay := time.Duration(s.settings.GetInt("mpesa_mock_delay_ms", 4000)) * time.Millisecond
	rc := s.settings.GetInt("mpesa_mock_result_code", 0)
	s.mock.Configure(delay, rc)
}

// SetVersion stamps the build version (used in team-sync heartbeats so
// the roster shows which till runs which release). Main calls this once.
func (s *Service) SetVersion(v string) { s.appVersion = v }

// AppVersion returns the stamped build version ("dev" when unset).
func (s *Service) AppVersion() string {
	if s.appVersion == "" {
		return "dev"
	}
	return s.appVersion
}
