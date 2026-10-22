package services

// teamsync.go — links standalone tills into one team through the shop's
// own Supabase project (the same project off-site backups use).
//
// Model: append-only event log. Every mutating device writes an event to
// its local outbox (seq-ordered); a background loop pushes outbox events
// to the Supabase `sync_events` table and pulls other devices' events,
// applying each exactly once (cursor + unique (team_code, client_uuid)).
//
// Entity semantics (cross-device identities, because local ids differ):
//   category   upsert by slug
//   product    upsert by SKU (fallback barcode); catalog fields LWW by
//              updated_at; stock_set only when an admin set stock directly
//   stock      signed delta applied on every device (sales, voids, counts)
//   customer   upsert by phone (fallback name); identity fields LWW
//   ledger     append + recompute the customer columns (exactly-once per
//              event, so balances converge)
//   order      insert-by-client_uuid; numbers remapped on collision;
//              PENDING→PAID completion adopts payments (stock moves only
//              via the checkout's per-line stock deltas — never inline)
//   void       apply void to the referenced order (stock restore, refunds)
//   design     design-board job upsert
//
// Sync never blocks a sale: emits are best-effort, the loop swallows
// network errors, and every apply is idempotent.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"posapp/internal/models"
)

const (
	syncInterval    = 20 * time.Second
	syncHTTPTimeout = 30 * time.Second
	syncBatchSize   = 200
)

// Emit appends one change to the sync outbox (best-effort: sync must
// never break a sale — errors are logged, not returned).
//
// The outbox is written even while sync is disabled: enabling sync later
// (or joining a store after running standalone) replays everything that
// happened meanwhile instead of silently dropping it. The enabled gate
// lives in syncConfig, i.e. on the push/pull path where it belongs.

func (s *Service) Emit(entity, op string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[sync] marshal %s: %v", entity, err)
		s.noteOutboxFailure("marshal " + entity)
		return
	}
	_, err = s.db.Exec(s.db.Rebind(`INSERT INTO sync_outbox (entity, op, client_uuid, payload) VALUES (?, ?, ?, ?)`),
		entity, op, randToken(12), string(raw))
	if err != nil {
		// Any error here just means one missed event — count it and let the
		// reconciliation backfill re-emit the row's current state later.
		log.Printf("[sync] outbox write: %v", err)
		s.noteOutboxFailure(err.Error())
	}
}

// noteOutboxFailure persists a persistent-visible counter for the Settings
// → Team panel: an outbox write that failed must NEVER stay silent (the
// committed mutation would otherwise be invisible to the cloud until the
// backfill catches it).

func (s *Service) noteOutboxFailure(cause string) {
	var n int64
	_ = s.db.QueryRow(`SELECT CAST(value AS INTEGER) FROM sync_state WHERE key = 'sync_outbox_failures'`).Scan(&n)
	_, _ = s.db.Exec(s.db.Rebind(`INSERT INTO sync_state (key, value) VALUES ('sync_outbox_failures', ?)
                ON CONFLICT(key) DO UPDATE SET value = excluded.value`), fmt.Sprintf("%d", n+1))
	_, _ = s.db.Exec(s.db.Rebind(`INSERT INTO sync_state (key, value) VALUES ('sync_outbox_last_error', ?)
                ON CONFLICT(key) DO UPDATE SET value = excluded.value`), truncStr(cause, 200))
}

// ---- PostgREST client ----

type syncClient struct {
	base       string
	key        string
	team       string
	device     string
	secretHash string
	mode       string // "rest" = direct PostgREST tables (manual key), "rpc" = cloud identity
	hc         *http.Client
}

// syncConfig resolves the team link. Cloud identity is the default: a till
// that finds its team in the sync_bootstrap row is linked — no settings
// screen, no join codes. Explicit manual configuration still wins.

func (s *Service) syncConfig() (*syncClient, bool) {
	// An owner's explicit OFF must win over everything: the zero-config
	// bootstrap used to fall through and keep syncing (with the Team
	// panel claiming sync was disabled) — a silent data leak.
	if s.settings.Get("sync_disabled") == "1" {
		return nil, false
	}
	if !s.settings.GetBool("sync_enabled", false) {
		if s.ensureCloudBootstrap() == nil {
			return nil, false
		}
	}
	if s.settings.Get("sync_source") == "manual" {
		if c := manualSyncClient(s); c != nil {
			return c, true
		}
		return nil, false
	}
	if bs := s.ensureCloudBootstrap(); bs != nil {
		return &syncClient{
			base:       strings.TrimRight(bs.ProjectURL, "/"),
			key:        cloudAnonKey,
			team:       bs.TeamCode,
			device:     s.deviceID(),
			secretHash: secretHash(s.deviceSecret()),
			mode:       "rpc",
			hc:         &http.Client{Timeout: syncHTTPTimeout},
		}, true
	}
	// Legacy fallback: manual values saved before cloud mode existed.
	if c := manualSyncClient(s); c != nil {
		return c, true
	}
	return nil, false
}

// PublicSyncIdentity returns this till's non-secret cloud identity for the
// LAN discovery endpoint: device id and team only — never the secret or its
// hash. A sibling till uses the device id to find the right peer for the
// PIN-reset code hand-off; both values are already visible to every team
// member in the cloud roster.

func (s *Service) PublicSyncIdentity() (deviceID, team string, ok bool) {
	c, ok := s.syncConfig()
	if !ok || c.device == "" {
		return "", "", false
	}
	return c.device, c.team, true
}

// DeviceName exposes the roster name (hostname) for LAN discovery.

func (s *Service) DeviceName() string { return s.deviceName() }

func manualSyncClient(s *Service) *syncClient {
	endpoint := strings.TrimRight(s.settings.Get("sync_endpoint"), "/")
	key := s.settings.Get("sync_service_key")
	team := strings.TrimSpace(s.settings.Get("sync_team_code"))
	if endpoint == "" || key == "" || team == "" {
		return nil
	}
	return &syncClient{
		base:   endpoint + "/rest/v1",
		key:    key,
		team:   team,
		device: s.deviceID(),
		mode:   "rest",
		hc:     &http.Client{Timeout: syncHTTPTimeout},
	}
}

func (c *syncClient) headers() http.Header {
	h := http.Header{}
	h.Set("apikey", c.key)
	h.Set("Authorization", "Bearer "+c.key)
	h.Set("Content-Type", "application/json")
	return h
}

type wireEvent struct {
	TeamCode   string          `json:"team_code"`
	DeviceID   string          `json:"device_id"`
	Entity     string          `json:"entity"`
	Op         string          `json:"op"`
	ClientUUID string          `json:"client_uuid"`
	Payload    json.RawMessage `json:"payload"`
}

// outboxRow is one pending outbox entry queued for push.

type outboxRow struct {
	seq        int64
	entity     string
	op         string
	clientUUID string
	payload    string
	attempts   int
}

const maxPushAttempts = 5 // a poison row is quarantined after this many failed cycles

func (s *Service) loadPendingOutbox(limit int) ([]outboxRow, error) {
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT seq, entity, op, client_uuid, payload, COALESCE(attempts,0) FROM sync_outbox
                WHERE pushed_at = '' ORDER BY seq LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batch []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.seq, &r.entity, &r.op, &r.clientUUID, &r.payload, &r.attempts); err != nil {
			return nil, err
		}
		batch = append(batch, r)
	}
	return batch, rows.Err()
}

// markPushed stamps the given outbox rows as pushed.

func (s *Service) markPushed(seqs []int64) {
	now := nowStamp()
	for _, seq := range seqs {
		s.db.Exec(s.db.Rebind(`UPDATE sync_outbox SET pushed_at = ? WHERE seq = ?`), now, seq)
	}
}

// quarantineOutboxRow moves a poison row (repeatedly failed to push) out of
// the outbox into the dead-letter table so it can never wedge the stream.
// The payload is preserved — the Team panel lists it and a retry can
// re-emit it once the root cause (e.g. a corrupt payload) is fixed.

func (s *Service) quarantineOutboxRow(r outboxRow, cause string) {
	s.db.Exec(s.db.Rebind(`DELETE FROM sync_outbox WHERE seq = ?`), r.seq)
	s.db.Exec(s.db.Rebind(`
                INSERT INTO sync_dead_letter (direction, event_id, entity, op, client_uuid, payload, error, attempts, created_at, updated_at)
                VALUES ('push', 0, ?, ?, ?, ?, ?, ?, ?, ?)`),
		r.entity, r.op, r.clientUUID, r.payload, truncStr(cause, 300), r.attempts+1, nowStamp(), nowStamp())
	log.Printf("[sync] quarantined poison event %s/%s after %d attempts: %s", r.entity, r.op, r.attempts+1, cause)
}

// push uploads pending outbox rows (oldest first) and marks them pushed.
// A row the server rejects wedges nothing: the batch is bisected until the
// offender is isolated; after maxPushAttempts failed cycles it is
// quarantined to the dead-letter table and the stream resumes.

func (c *syncClient) push(s *Service) (int, error) {
	batch, err := s.loadPendingOutbox(syncBatchSize)
	if err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		return 0, nil
	}
	return c.pushRows(s, batch)
}

// pushRows pushes a set of outbox rows, bisecting on failure.

func (c *syncClient) pushRows(s *Service, batch []outboxRow) (int, error) {
	if len(batch) == 0 {
		return 0, nil
	}
	pushed, err := c.pushBatch(s, batch)
	if err == nil {
		return pushed, nil
	}
	if len(batch) == 1 {
		r := batch[0]
		if r.attempts+1 >= maxPushAttempts {
			s.quarantineOutboxRow(r, err.Error())
			return 0, nil // quarantined: the stream is unstuck
		}
		s.db.Exec(s.db.Rebind(`UPDATE sync_outbox SET attempts = attempts + 1, last_error = ? WHERE seq = ?`),
			truncStr(err.Error(), 300), r.seq)
		return 0, err
	}
	// Split: push each half independently so one bad row cannot block
	// the good ones behind it.
	mid := len(batch) / 2
	n1, err1 := c.pushRows(s, batch[:mid])
	n2, err2 := c.pushRows(s, batch[mid:])
	if err1 != nil && err2 != nil {
		return n1 + n2, err1
	}
	if err1 != nil {
		return n1 + n2, err1
	}
	if err2 != nil {
		return n1 + n2, err2
	}
	return n1 + n2, nil
}

// pushBatch is one wire attempt for the given rows (REST or RPC transport).

func (c *syncClient) pushBatch(s *Service, batch []outboxRow) (int, error) {
	if c.mode == "rpc" {
		return c.pushBatchRPC(s, batch)
	}
	events := make([]wireEvent, len(batch))
	for i, b := range batch {
		events[i] = wireEvent{TeamCode: c.team, DeviceID: c.device, Entity: b.entity, Op: b.op,
			ClientUUID: b.clientUUID, Payload: json.RawMessage(b.payload)}
	}
	body, _ := json.Marshal(events)
	req, err := http.NewRequest("POST", c.base+"/sync_events", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header = c.headers()
	req.Header.Set("Prefer", "resolution=ignore-duplicates,return=minimal")
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 201 && resp.StatusCode != 200 && resp.StatusCode != 409 {
		return 0, fmt.Errorf("push: status %d", resp.StatusCode)
	}
	seqs := make([]int64, len(batch))
	for i, b := range batch {
		seqs[i] = b.seq
	}
	s.markPushed(seqs)
	return len(batch), nil
}

// pushBatchRPC uploads rows through the identity-checked RPC. The server
// stamps team_code + device_id from the validated identity, so events
// carry no spoofable identity fields.

func (c *syncClient) pushBatchRPC(s *Service, batch []outboxRow) (int, error) {
	type rpcEvent struct {
		Entity     string          `json:"entity"`
		Op         string          `json:"op"`
		ClientUUID string          `json:"client_uuid"`
		Payload    json.RawMessage `json:"payload"`
	}
	events := make([]rpcEvent, len(batch))
	for i, b := range batch {
		events[i] = rpcEvent{Entity: b.entity, Op: b.op, ClientUUID: b.clientUUID,
			Payload: json.RawMessage(b.payload)}
	}
	var out struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Pushed int    `json:"pushed"`
	}
	if err := rpcCall(c.base, "sync_push", map[string]any{
		"p_device_id": c.device, "p_secret_hash": c.secretHash, "p_events": events,
	}, &out); err != nil {
		return 0, err
	}
	if !out.OK {
		return 0, fmt.Errorf("push: %s", out.Error)
	}
	seqs := make([]int64, len(batch))
	for i, b := range batch {
		seqs[i] = b.seq
	}
	s.markPushed(seqs)
	return out.Pushed, nil
}

// pulledEvent is the wire shape of a remote change (both transports).

type pulledEvent struct {
	ID         int64           `json:"id"`
	DeviceID   string          `json:"device_id"`
	Entity     string          `json:"entity"`
	Op         string          `json:"op"`
	ClientUUID string          `json:"client_uuid"`
	Payload    json.RawMessage `json:"payload"`
}

// applyPulled applies a batch, always advancing the cursor past every event
// (a bad event is logged, quarantined to the dead-letter table and retried
// on a later cycle — never allowed to wedge the stream, never silently
// lost). Receiver-side exactly-once: every applied cloud event id is
// recorded in sync_applied, so a cursor regression (restored backup, wiped
// sync_state) replays safely instead of double-applying stock and ledger.

func applyPulled(s *Service, team string, events []pulledEvent) int {
	applied := 0
	own := s.deviceID()
	for _, ev := range events {
		if ev.DeviceID == own {
			continue // never replay own events
		}
		if s.isSyncApplied(ev.ID) {
			continue // already applied (cursor regression / redelivery)
		}
		if err := s.applyEvent(ev.Entity, ev.Op, ev.Payload); err != nil {
			log.Printf("[sync] apply %s/%s: %v", ev.Entity, ev.Op, err)
			s.deadLetterApply(ev, err)
		} else {
			s.markSyncApplied(ev.ID)
			applied++
		}
		s.syncSetInt("last_event_"+team, ev.ID)
	}
	return applied
}

// isSyncApplied reports whether a cloud event id was already applied.

func (s *Service) isSyncApplied(eventID int64) bool {
	if eventID <= 0 {
		return false
	}
	var one int
	s.db.QueryRow(s.db.Rebind(`SELECT 1 FROM sync_applied WHERE event_id = ?`), eventID).Scan(&one)
	return one == 1
}

func (s *Service) markSyncApplied(eventID int64) {
	if eventID <= 0 {
		return
	}
	s.db.Exec(s.db.Rebind(`INSERT INTO sync_applied (event_id, applied_at) VALUES (?, ?) ON CONFLICT (event_id) DO NOTHING`),
		eventID, nowStamp())
}

// deadLetterApply quarantines an event that could not be applied (unknown
// product, unknown customer…). The apply loop retries it on later cycles —
// e.g. a ledger row whose customer has not synced yet applies itself once
// the customer event lands, instead of being dropped forever.

func (s *Service) deadLetterApply(ev pulledEvent, cause error) {
	// One row per event id: a re-delivery of a still-failing event
	// updates the existing quarantine entry instead of duplicating it.
	s.db.Exec(s.db.Rebind(`DELETE FROM sync_dead_letter WHERE direction = 'apply' AND event_id = ?`), ev.ID)
	s.db.Exec(s.db.Rebind(`
                INSERT INTO sync_dead_letter (direction, event_id, entity, op, client_uuid, payload, error, attempts, created_at, updated_at)
                VALUES ('apply', ?, ?, ?, ?, ?, ?, 1, ?, ?)`),
		ev.ID, ev.Entity, ev.Op, ev.ClientUUID, string(ev.Payload), truncStr(cause.Error(), 300), nowStamp(), nowStamp())
}

// retryFailedApplies re-runs quarantined apply events. Returns how many
// recovered. Called automatically once per sync cycle and from the
// Team panel's "retry failed" button.

func (s *Service) retryFailedApplies() int {
	rows, err := s.db.Query(s.db.Rebind(`
                SELECT id, event_id, entity, op, client_uuid, payload, attempts FROM sync_dead_letter
                WHERE direction = 'apply' ORDER BY id LIMIT 200`))
	if err != nil {
		return 0
	}
	type dlRow struct {
		id       int64
		eventID  int64
		entity   string
		op       string
		uuid     string
		payload  string
		attempts int
	}
	var pending []dlRow
	for rows.Next() {
		var r dlRow
		if err := rows.Scan(&r.id, &r.eventID, &r.entity, &r.op, &r.uuid, &r.payload, &r.attempts); err == nil {
			pending = append(pending, r)
		}
	}
	rows.Close()
	recovered := 0
	for _, r := range pending {
		if err := s.applyEvent(r.entity, r.op, json.RawMessage(r.payload)); err != nil {
			s.db.Exec(s.db.Rebind(`UPDATE sync_dead_letter SET attempts = attempts + 1, error = ?, updated_at = ? WHERE id = ?`),
				truncStr(err.Error(), 300), nowStamp(), r.id)
			continue
		}
		s.db.Exec(s.db.Rebind(`DELETE FROM sync_dead_letter WHERE id = ?`), r.id)
		if r.eventID > 0 {
			s.markSyncApplied(r.eventID)
		}
		recovered++
		log.Printf("[sync] dead-letter recovered %s/%s", r.entity, r.op)
	}
	return recovered
}

// pullRPC fetches other approved devices' events through the RPC gateway.

func (c *syncClient) pullRPC(s *Service) (int, error) {
	cursor := s.syncGetInt("last_event_" + c.team)
	var out struct {
		OK     bool          `json:"ok"`
		Error  string        `json:"error"`
		Events []pulledEvent `json:"events"`
	}
	if err := rpcCall(c.base, "sync_pull", map[string]any{
		"p_device_id": c.device, "p_secret_hash": c.secretHash,
		"p_since_id": cursor, "p_limit": syncBatchSize,
	}, &out); err != nil {
		return 0, err
	}
	if !out.OK {
		return 0, fmt.Errorf("pull: %s", out.Error)
	}
	return applyPulled(s, c.team, out.Events), nil
}

// pull fetches other devices' events past the cursor and applies them.

func (c *syncClient) pull(s *Service) (int, error) {
	if c.mode == "rpc" {
		return c.pullRPC(s)
	}
	cursor := s.syncGetInt("last_event_" + c.team)
	q := url.Values{}
	q.Set("team_code", "eq."+c.team)
	q.Set("device_id", "neq."+c.device)
	q.Set("id", fmt.Sprintf("gt.%d", cursor))
	q.Set("order", "id.asc")
	q.Set("limit", fmt.Sprint(syncBatchSize))
	req, err := http.NewRequest("GET", c.base+"/sync_events?"+q.Encode(), nil)
	if err != nil {
		return 0, err
	}
	req.Header = c.headers()
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, resp.Body)
		return 0, fmt.Errorf("pull: status %d", resp.StatusCode)
	}
	var events []pulledEvent
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return 0, err
	}
	return applyPulled(s, c.team, events), nil
}

// heartbeat registers this device (upsert) so the team list is live.

func (c *syncClient) heartbeat(s *Service, version string) error {
	if c.mode == "rpc" {
		return c.registerCloud(s, version)
	}
	host, _ := os.Hostname()
	row := map[string]any{
		"device_id":   c.device,
		"team_code":   c.team,
		"device_name": host,
		"app_version": version,
		"last_seen":   time.Now().UTC().Format(time.RFC3339),
	}
	body, _ := json.Marshal([]map[string]any{row})
	req, err := http.NewRequest("POST", c.base+"/sync_devices", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = c.headers()
	req.Header.Set("Prefer", "resolution=merge-duplicates,return=minimal")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 201 && resp.StatusCode != 200 {
		return fmt.Errorf("heartbeat: status %d", resp.StatusCode)
	}
	return nil
}

// registerCloud identifies this till to the database (device id + secret
// hash). The DB decides who is who: approved/revoked live in sync_devices.
// In a multi-store cloud a fresh till registers as pending and learns its
// store when the owner assigns it — the assignment lands on the next
// heartbeat, when the RPC returns this device's team_code.

func (c *syncClient) registerCloud(s *Service, version string) error {
	var out struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Approved bool   `json:"approved"`
		TeamCode string `json:"team_code"`
		Pending  bool   `json:"pending"`
	}
	err := rpcCall(c.base, "sync_register", map[string]any{
		"p_device_id":   c.device,
		"p_secret_hash": c.secretHash,
		"p_device_name": s.deviceName(),
		"p_app_version": version,
		"p_lan_addr":    s.lanAddr,
	}, &out)
	if err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("register: %s", out.Error)
	}
	_ = s.settings.Set("sync_registered", "true")
	_ = s.settings.Set("sync_approved", boolStr(out.Approved))
	if out.TeamCode != "" {
		_ = s.settings.Set("sync_team_code", out.TeamCode)
		_ = s.settings.Set("sync_store_pending", "false")
	} else if out.Pending {
		_ = s.settings.Set("sync_store_pending", "true")
	}
	if !out.Approved {
		if out.Pending {
			return fmt.Errorf("registered, waiting for the owner to assign this till to a store")
		}
		return fmt.Errorf("awaiting approval in the cloud device list")
	}
	return nil
}

// ---- Status / config API ----

var syncMu sync.Mutex // serialize sync cycles per process

// TeamSyncNow runs one push+pull+heartbeat cycle immediately.

func (s *Service) TeamSyncNow(version string) (pushed, applied int, err error) {
	syncMu.Lock()
	defer syncMu.Unlock()
	c, ok := s.syncConfig()
	if !ok {
		return 0, 0, fmt.Errorf("team sync is not configured/enabled")
	}
	if err := c.heartbeat(s, version); err != nil {
		s.syncError(err)
		return 0, 0, fmt.Errorf("heartbeat: %w", err)
	}
	if pushed, err = c.push(s); err != nil {
		s.syncError(err)
		return pushed, 0, fmt.Errorf("push: %w", err)
	}
	if applied, err = c.pull(s); err != nil {
		s.syncError(err)
		return pushed, applied, fmt.Errorf("pull: %w", err)
	}
	// Give quarantined events a second chance once per cycle (cheap:
	// the table is normally empty; the root cause — an out-of-order
	// event — has usually landed by the next tick).
	if recovered := s.retryFailedApplies(); recovered > 0 {
		applied += recovered
	}
	// Reconciliation backfill: re-emit committed business rows whose outbox
	// event never landed (Emit is best-effort; this closes the gap without
	// manual database work). Bounded + idempotent by stable UUID.
	if n := s.BackfillMissingEvents(); n > 0 {
		log.Printf("[sync] backfill re-emitted %d missing event(s)", n)
	}
	s.settings.Set("sync_last_push", nowStamp())
	s.settings.Set("sync_last_pull", nowStamp())
	s.settings.Set("sync_last_error", "")
	return pushed, applied, nil
}

func (s *Service) syncError(err error) {
	msg := truncStr(err.Error(), 200)
	log.Printf("[sync] %s", msg)
	s.settings.Set("sync_last_error", msg)
}

// TeamSyncStatus reports config + devices for the Settings → Team tab.

func (s *Service) TeamSyncStatus(version string) (*models.TeamSyncStatus, error) {
	source := s.settings.Get("sync_source")
	if source == "" && s.settings.Get("sync_endpoint") != "" {
		source = "manual"
	}
	st := &models.TeamSyncStatus{
		Enabled:     s.settings.GetBool("sync_enabled", false),
		TeamCode:    s.settings.Get("sync_team_code"),
		DeviceID:    s.deviceID(),
		DeviceName:  s.deviceName(),
		LastPush:    s.settings.Get("sync_last_push"),
		LastPull:    s.settings.Get("sync_last_pull"),
		LastError:   s.settings.Get("sync_last_error"),
		Devices:     []models.TeamDevice{},
		Source:      source,
		Registered:  s.settings.GetBool("sync_registered", false),
		Approved:    s.settings.GetBool("sync_approved", false),
		AutoApprove: s.settings.GetBool("sync_auto_approve", true),
	}
	var pending int64
	s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE pushed_at = ''`).Scan(&pending)
	st.Pending = pending
	var failed int64
	s.db.QueryRow(`SELECT COUNT(*) FROM sync_dead_letter`).Scan(&failed)
	st.FailedEvents = failed
	var outboxFails int64
	s.db.QueryRow(`SELECT CAST(COALESCE(value,'0') AS INTEGER) FROM sync_state WHERE key = 'sync_outbox_failures'`).Scan(&outboxFails)
	st.OutboxFailures = outboxFails
	st.OutboxLastError = s.settings.Get("sync_outbox_last_error")
	if failed > 0 {
		rows, err := s.db.Query(`SELECT direction, entity, op, error, attempts, updated_at FROM sync_dead_letter ORDER BY id DESC LIMIT 10`)
		if err == nil {
			for rows.Next() {
				var f models.SyncFailedEvent
				if err := rows.Scan(&f.Direction, &f.Entity, &f.Op, &f.Error, &f.Attempts, &f.UpdatedAt); err == nil {
					st.Failed = append(st.Failed, f)
				}
			}
			rows.Close()
		}
	} else {
		st.Failed = []models.SyncFailedEvent{}
	}
	if c, ok := s.syncConfig(); ok {
		if devs, err := c.fetchDevices(s); err == nil {
			st.Devices = devs
		}
		// Multi-store owner view: the store registry + unassigned devices.
		// Cloud-identity approved tills only; failures are tolerated (the
		// panel still renders the local state).
		if c.mode == "rpc" && s.settings.GetBool("sync_approved", false) {
			if stores, err := c.fetchStoresRPC(); err == nil {
				st.Stores = stores
				st.MultiStore = len(stores) > 1
			}
			if pdevs, err := c.fetchPendingRPC(s); err == nil {
				st.PendingDevices = pdevs
			}
		}
	}
	st.StorePending = s.settings.GetBool("sync_store_pending", false)
	return st, nil
}

func (s *Service) deviceName() string {
	name, _ := os.Hostname()
	if name == "" {
		name = "till"
	}
	return name
}

// fetchDevicesRPC pulls the team roster through the identity-checked RPC.
// Revoked rows are included (the owner is the authority: the roster must
// show them, with an Unrevoke action, or a mistaken revoke is invisible and
// only fixable by hand in the database).

func (s *Service) TeamSyncLoop(ctx context.Context, version string) {
	t := time.NewTicker(syncInterval)
	defer t.Stop()
	ticks := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, _, err := s.TeamSyncNow(version); err != nil {
				// logged in syncError; keep ticking
			}
			// Photos uploaded before image-sync shipped (or while a
			// backfill is mid-flight) drain at a rate-limited pace.
			s.backfillProductImages()
			ticks++
			if ticks%30 == 0 { // ~every 10 minutes
				s.pruneSyncTables()
			}
		}
	}
}

// pruneSyncTables keeps the sync bookkeeping bounded: pushed outbox rows
// older than 7 days and applied-event ids older than 30 days are removed
// (both are audit-irrelevant — the cloud log is the permanent record).

func (s *Service) pruneSyncTables() {
	pushCutoff := time.Now().UTC().AddDate(0, 0, -7).Format("2006-01-02T15:04:05.000Z")
	s.db.Exec(s.db.Rebind(`DELETE FROM sync_outbox WHERE pushed_at != '' AND pushed_at < ?`), pushCutoff)
	applyCutoff := time.Now().UTC().AddDate(0, 0, -30).Format("2006-01-02T15:04:05.000Z")
	s.db.Exec(s.db.Rebind(`DELETE FROM sync_applied WHERE applied_at != '' AND applied_at < ?`), applyCutoff)
}

// RetryFailedSync re-applies quarantined events now (Team panel button).

func (s *Service) RetryFailedSync() int {
	return s.retryFailedApplies()
}

// QuarantineOutboxForTeamChange moves every not-yet-pushed outbox event
// into the dead-letter table. Called when a till joins (or re-joins) a
// DIFFERENT team: pending events were created under the OLD team, but the
// push path stamps events with the device's CURRENT team — letting them
// ride would book the previous shop's orders/customers/ledger into the
// new shop's event log (a real cross-tenant leak, and newly reachable now
// that v1.3.0 lets revoked tills rejoin via a fresh invite). Quarantined
// rows keep their payloads for inspection and retry only by explicit
// action — they are never auto-pushed to the wrong team.

func (s *Service) QuarantineOutboxForTeamChange(oldTeam, newTeam string) {
	rows, err := s.db.Query(`SELECT seq, entity, op, client_uuid, payload, COALESCE(attempts,0) FROM sync_outbox ORDER BY seq`)
	if err != nil {
		log.Printf("[sync] team-change quarantine read: %v", err)
		return
	}
	type ev struct {
		seq                       int64
		entity, op, uuid, payload string
		attempts                  int64
	}
	var pending []ev
	for rows.Next() {
		var e ev
		if rows.Scan(&e.seq, &e.entity, &e.op, &e.uuid, &e.payload, &e.attempts) == nil {
			pending = append(pending, e)
		}
	}
	rows.Close()
	if len(pending) == 0 {
		return
	}
	cause := fmt.Sprintf("till moved from team %s to %s, events belong to the old team", truncStr(oldTeam, 40), truncStr(newTeam, 40))
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	moved := 0
	for _, e := range pending {
		if _, err := tx.Exec(s.db.Rebind(`
                        INSERT INTO sync_dead_letter (direction, event_id, entity, op, client_uuid, payload, error, attempts, created_at, updated_at)
                        VALUES ('push', 0, ?, ?, ?, ?, ?, ?, ?, ?)`),
			e.entity, e.op, e.uuid, e.payload, truncStr(cause, 300), e.attempts, nowStamp(), nowStamp()); err != nil {
			log.Printf("[sync] team-change quarantine write: %v", err)
			return
		}
		if _, err := tx.Exec(s.db.Rebind(`DELETE FROM sync_outbox WHERE seq = ?`), e.seq); err != nil {
			log.Printf("[sync] team-change quarantine delete: %v", err)
			return
		}
		moved++
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[sync] team-change quarantine commit: %v", err)
		return
	}
	log.Printf("[sync] team change %s→%s: quarantined %d pending event(s), review Settings → Team before re-pushing", oldTeam, newTeam, moved)
}

// syncGetInt / syncSetInt — integer values in sync_state.

func (s *Service) syncGetInt(key string) int64 {
	v := s.settings.Get("syncstate_" + key)
	var n int64
	fmt.Sscanf(v, "%d", &n)
	return n
}

func (s *Service) syncSetInt(key string, n int64) {
	s.settings.Set("syncstate_"+key, fmt.Sprint(n))
}
