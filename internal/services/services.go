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
        "log"
        "sync"
        "time"

        "posapp/internal/database"
        "posapp/internal/mpesa"
        "posapp/internal/offsite"
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

        orderSeqMu sync.Mutex
}

func New(db *database.DB, st *settings.Store, hub *ws.Hub, pw *printer.Worker) *Service {
        return &Service{db: db, settings: st, hub: hub, printer: pw, mock: mpesa.NewMock(4*time.Second, 0)}
}

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

// broadcast pushes a ws event if the hub is wired.
func (s *Service) broadcast(event string, data any) {
        if s.hub != nil {
                s.hub.BroadcastJSON(event, data)
        }
}

// ---- Events ----

const (
        EventOrderCreated = "ORDER_CREATED"
        EventOrderPaid    = "ORDER_PAID"
        EventOrderVoided  = "ORDER_VOIDED"
        EventDesignUpdate = "DESIGN_JOB_UPDATED"
        EventShiftUpdate  = "SHIFT_UPDATED"
        EventSettingsUpdate = "SETTINGS_UPDATED"
)

// ---- M-Pesa provider factory ----

// GetProvider returns the active payment provider per settings. The mock
// is a shared singleton (its in-memory pushes must be visible to the
// sweeper); Daraja instances are cached until credentials change.
func (s *Service) GetProvider() mpesa.Provider {
        env := s.settings.GetString("mpesa_env", "mock")
        switch env {
        case "sandbox", "production":
                key := s.settings.Get("mpesa_consumer_key")
                secret := s.settings.Get("mpesa_consumer_secret")
                shortcode := s.settings.Get("mpesa_shortcode")
                passkey := s.settings.Get("mpesa_passkey")
                callback := s.settings.Get("mpesa_callback_url")
                if key == "" || secret == "" || shortcode == "" || passkey == "" {
                        log.Printf("[mpesa] env=%s but credentials incomplete — falling back to mock", env)
                        s.applyMockConfig()
                        return s.mock
                }
                cacheKey := env + "|" + shortcode + "|" + key + "|" + secret + "|" + passkey + "|" + callback
                if s.darajaKey != cacheKey {
                        s.darajaKey = cacheKey
                        s.daraja = mpesa.NewDaraja(env, shortcode, passkey, key, secret, callback)
                }
                return s.daraja
        default: // mock
                s.applyMockConfig()
                return s.mock
        }
}

func (s *Service) applyMockConfig() {
        delay := time.Duration(s.settings.GetInt("mpesa_mock_delay_ms", 4000)) * time.Millisecond
        rc := s.settings.GetInt("mpesa_mock_result_code", 0)
        s.mock.Configure(delay, rc)
}
