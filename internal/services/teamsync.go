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
//              PENDING→PAID completion replays stock + ledger
//   void       apply void to the referenced order (stock restore, refunds)
//   design     design-board job upsert
//
// Sync never blocks a sale: emits are best-effort, the loop swallows
// network errors, and every apply is idempotent.

import (
        "bytes"
        "context"
        "database/sql"
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
        "posapp/internal/settings"
)

const (
        syncInterval    = 20 * time.Second
        syncHTTPTimeout = 30 * time.Second
        syncBatchSize   = 200
)

// Emit appends one change to the sync outbox (best-effort: sync must
// never break a sale — errors are logged, not returned).
func (s *Service) Emit(entity, op string, payload any) {
        if !s.settings.GetBool("sync_enabled", false) {
                return
        }
        raw, err := json.Marshal(payload)
        if err != nil {
                log.Printf("[sync] marshal %s: %v", entity, err)
                return
        }
        _, err = s.db.Exec(s.db.Rebind(`INSERT INTO sync_outbox (entity, op, client_uuid, payload) VALUES (?, ?, ?, ?)`),
                entity, op, randToken(12), string(raw))
        if err != nil {
                // Unique collision on client_uuid is astronomically unlikely;
                // any error here just means one missed event.
                log.Printf("[sync] outbox write: %v", err)
        }
}

// ---- PostgREST client ----

type syncClient struct {
        base   string
        key    string
        team   string
        device string
        hc     *http.Client
}

func (s *Service) syncConfig() (*syncClient, bool) {
        if !s.settings.GetBool("sync_enabled", false) {
                return nil, false
        }
        endpoint := strings.TrimRight(s.settings.Get("sync_endpoint"), "/")
        key := s.settings.Get("sync_service_key")
        team := strings.TrimSpace(s.settings.Get("sync_team_code"))
        if endpoint == "" || key == "" || team == "" {
                return nil, false
        }
        return &syncClient{
                base:   endpoint + "/rest/v1",
                key:    key,
                team:   team,
                device: s.deviceID(),
                hc:     &http.Client{Timeout: syncHTTPTimeout},
        }, true
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

// push uploads pending outbox rows (oldest first) and marks them pushed.
func (c *syncClient) push(s *Service) (int, error) {
        rows, err := s.db.Query(s.db.Rebind(`
                SELECT seq, entity, op, client_uuid, payload FROM sync_outbox
                WHERE pushed_at = '' ORDER BY seq LIMIT ?`), syncBatchSize)
        if err != nil {
                return 0, err
        }
        type outRow struct {
                seq    int64
                entity string
                event  wireEvent
        }
        var batch []outRow
        for rows.Next() {
                var r outRow
                var entity, op, uuid, payload string
                if err := rows.Scan(&r.seq, &entity, &op, &uuid, &payload); err != nil {
                        rows.Close()
                        return 0, err
                }
                r.entity = entity
                r.event = wireEvent{TeamCode: c.team, DeviceID: c.device, Entity: entity, Op: op,
                        ClientUUID: uuid, Payload: json.RawMessage(payload)}
                batch = append(batch, r)
        }
        rows.Close()
        if err := rows.Err(); err != nil {
                return 0, err
        }
        if len(batch) == 0 {
                return 0, nil
        }
        events := make([]wireEvent, len(batch))
        for i, b := range batch {
                events[i] = b.event
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
        now := nowStamp()
        for _, b := range batch {
                s.db.Exec(s.db.Rebind(`UPDATE sync_outbox SET pushed_at = ? WHERE seq = ?`), now, b.seq)
        }
        return len(batch), nil
}

// pull fetches other devices' events past the cursor and applies them.
func (c *syncClient) pull(s *Service) (int, error) {
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
        var events []struct {
                ID         int64           `json:"id"`
                DeviceID   string          `json:"device_id"`
                Entity     string          `json:"entity"`
                Op         string          `json:"op"`
                ClientUUID string          `json:"client_uuid"`
                Payload    json.RawMessage `json:"payload"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
                return 0, err
        }
        applied := 0
        for _, ev := range events {
                if err := s.applyEvent(ev.Entity, ev.Op, ev.Payload); err != nil {
                        // A bad event must not wedge the stream: log, advance past it.
                        log.Printf("[sync] apply %s/%s: %v", ev.Entity, ev.Op, err)
                }
                s.syncSetInt("last_event_"+c.team, ev.ID)
                applied++
                if len(events) == syncBatchSize && applied == len(events) {
                        // More pages may exist; the loop runs again on the next tick.
                }
        }
        return applied, nil
}

// heartbeat registers this device (upsert) so the team list is live.
func (c *syncClient) heartbeat(s *Service, version string) error {
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
        st := &models.TeamSyncStatus{
                Enabled:    s.settings.GetBool("sync_enabled", false),
                TeamCode:   s.settings.Get("sync_team_code"),
                DeviceID:   s.deviceID(),
                DeviceName: s.deviceName(),
                LastPush:   s.settings.Get("sync_last_push"),
                LastPull:   s.settings.Get("sync_last_pull"),
                LastError:  s.settings.Get("sync_last_error"),
                Devices:    []models.TeamDevice{},
        }
        var pending int64
        s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE pushed_at = ''`).Scan(&pending)
        st.Pending = pending
        if c, ok := s.syncConfig(); ok {
                if devs, err := c.fetchDevices(s); err == nil {
                        st.Devices = devs
                }
        }
        return st, nil
}

func (s *Service) deviceName() string {
        name, _ := os.Hostname()
        if name == "" {
                name = "till"
        }
        return name
}

func (c *syncClient) fetchDevices(s *Service) ([]models.TeamDevice, error) {
        q := url.Values{}
        q.Set("team_code", "eq." + c.team)
        q.Set("order", "last_seen.desc")
        req, err := http.NewRequest("GET", c.base+"/sync_devices?"+q.Encode(), nil)
        if err != nil {
                return nil, err
        }
        req.Header = c.headers()
        resp, err := c.hc.Do(req)
        if err != nil {
                return nil, err
        }
        defer resp.Body.Close()
        if resp.StatusCode != 200 {
                return nil, fmt.Errorf("devices: status %d", resp.StatusCode)
        }
        var rows []struct {
                DeviceID   string `json:"device_id"`
                DeviceName string `json:"device_name"`
                AppVersion string `json:"app_version"`
                LastSeen   string `json:"last_seen"`
        }
        if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
                return nil, err
        }
        out := make([]models.TeamDevice, 0, len(rows))
        for _, r := range rows {
                out = append(out, models.TeamDevice{
                        DeviceID: r.DeviceID, DeviceName: r.DeviceName,
                        AppVersion: r.AppVersion, LastSeen: r.LastSeen,
                        ThisDevice: r.DeviceID == c.device,
                })
        }
        return out, nil
}

// TeamSyncConfigure stores endpoint + key + team code (admin action).
func (s *Service) TeamSyncConfigure(req models.TeamSyncConfigRequest) error {
        if req.ProjectURL != "" && !strings.HasPrefix(req.ProjectURL, "https://") {
                return fmt.Errorf("project URL must be an https Supabase URL")
        }
        if req.ServiceKey != "" && req.ServiceKey != settings.MaskToken {
                _ = s.settings.Set("sync_service_key", req.ServiceKey)
        }
        if req.ProjectURL != "" {
                _ = s.settings.Set("sync_endpoint", strings.TrimRight(req.ProjectURL, "/"))
        }
        if req.TeamCode != "" {
                _ = s.settings.Set("sync_team_code", strings.ToUpper(strings.TrimSpace(req.TeamCode)))
        }
        if req.Enabled != nil {
                if *req.Enabled {
                        if s.settings.Get("sync_endpoint") == "" || s.settings.Get("sync_service_key") == "" || s.settings.Get("sync_team_code") == "" {
                                return fmt.Errorf("endpoint, service key, and team code are all required to enable sync")
                        }
                }
                _ = s.settings.Set("sync_enabled", boolStr(*req.Enabled))
        }
        return nil
}

func boolStr(b bool) string {
        if b {
                return "true"
        }
        return "false"
}

// TeamSyncCreate mints a fresh team code and enables sync (first device).
func (s *Service) TeamSyncCreate() (string, error) {
        code := "TEAM-" + strings.ToUpper(randToken(3))
        _ = s.settings.Set("sync_team_code", code)
        if s.settings.Get("sync_endpoint") == "" || s.settings.Get("sync_service_key") == "" {
                return "", fmt.Errorf("save the Supabase endpoint and service key first")
        }
        _ = s.settings.Set("sync_enabled", "true")
        return code, nil
}

// TeamSyncLoop runs until ctx closes, syncing every interval when enabled.
func (s *Service) TeamSyncLoop(ctx context.Context, version string) {
        t := time.NewTicker(syncInterval)
        defer t.Stop()
        for {
                select {
                case <-ctx.Done():
                        return
                case <-t.C:
                        if _, _, err := s.TeamSyncNow(version); err != nil {
                                // logged in syncError; keep ticking
                        }
                }
        }
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

// ---- Event application ----

func (s *Service) applyEvent(entity, op string, payload json.RawMessage) error {
        switch entity {
        case "category":
                if op == "delete" {
                        return s.applyCategoryDelete(payload)
                }
                return s.applyCategory(payload)
        case "product":
                return s.applyProduct(payload)
        case "stock":
                return s.applyStockDelta(payload)
        case "customer":
                return s.applyCustomer(payload)
        case "ledger":
                return s.applyLedger(payload)
        case "order":
                return s.applyOrder(payload)
        case "void":
                return s.applyVoid(payload)
        case "design":
                return s.applyDesign(payload)
        case "config":
                return s.applyConfig(payload)
        case "voidreason":
                if op == "delete" {
                        return s.applyVoidReasonDelete(payload)
                }
                return s.applyVoidReason(payload)
        default:
                return nil // unknown entity types are skipped (forward compatibility)
        }
}

// ---- Config sync ("updates over the air") ----

// configSyncWhitelist is the set of settings that propagate to every team
// device through the sync log — the "update via database, not reinstall"
// story: a price list change, tax tweak, loyalty rule, or branding refresh
// made on one till lands on the others within a sync tick. Secrets
// (keys, passkeys, JWT, storage) and per-device preferences (printer,
// device id) are deliberately absent — they must never leave the machine
// they were entered on.
var configSyncWhitelist = map[string]bool{
        "app_name": true, "store_name": true, "store_address": true,
        "store_phone": true, "receipt_footer": true,
        "currency_code": true, "currency_symbol": true,
        "tax_percent": true, "tax_included": true, "brand_color": true,
        "payment_mode": true, "till_number": true, "paybill_number": true,
        "loyalty_enabled": true, "loyalty_earn_per_cents": true,
        "loyalty_point_cents": true, "loyalty_max_percent": true,
        "credit_enabled": true, "low_stock_threshold": true,
        "paystack_enabled": true, "paystack_public_key": true,
        "paystack_currency": true, "paystack_callback_url": true,
}

// EmitConfig queues a config delta (called from the settings API with the
// keys that actually changed).
func (s *Service) EmitConfig(keys []string) {
        if len(keys) == 0 {
                return
        }
        values := map[string]string{}
        for _, k := range keys {
                if configSyncWhitelist[k] {
                        values[k] = s.settings.Get(k)
                }
        }
        if len(values) == 0 {
                return
        }
        s.Emit("config", "upsert", map[string]any{"values": values, "updatedAt": nowStamp()})
}

// applyConfig merges a remote config delta. Only whitelisted keys apply;
// tax_percent is re-validated (defence in depth against a poisoned event).
func (s *Service) applyConfig(payload json.RawMessage) error {
        var cfg struct {
                Values map[string]string `json:"values"`
        }
        if err := json.Unmarshal(payload, &cfg); err != nil {
                return err
        }
        for k, v := range cfg.Values {
                if !configSyncWhitelist[k] {
                        continue
                }
                if k == "tax_percent" {
                        var f float64
                        if _, err := fmt.Sscanf(v, "%g", &f); err != nil || f < 0 || f > 100 {
                                continue
                        }
                }
                if err := s.settings.Set(k, v); err != nil {
                        return err
                }
        }
        s.broadcast(EventSettingsUpdate, s.settings.Branding())
        return nil
}

// ---- Void-reason catalog sync ----

// EmitVoidReason queues a reason catalog change so every till offers the
// same void reasons (audit consistency).
func (s *Service) EmitVoidReason(label string, active bool, sortOrder int) {
        s.Emit("voidreason", "upsert", map[string]any{
                "label": label, "active": active, "sortOrder": sortOrder, "updatedAt": nowStamp(),
        })
}

func (s *Service) applyVoidReason(payload json.RawMessage) error {
        var r struct {
                Label     string `json:"label"`
                Active    bool   `json:"active"`
                SortOrder int    `json:"sortOrder"`
        }
        if err := json.Unmarshal(payload, &r); err != nil {
                return err
        }
        if strings.TrimSpace(r.Label) == "" || len(r.Label) > 120 {
                return fmt.Errorf("void reason label invalid")
        }
        var id int64
        err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM void_reasons WHERE label = ?`), r.Label).Scan(&id)
        if err == nil {
                _, err = s.db.Exec(s.db.Rebind(`UPDATE void_reasons SET is_active = ?, sort_order = ? WHERE id = ?`),
                        btoi(r.Active), r.SortOrder, id)
                return err
        }
        if err != sql.ErrNoRows {
                return err
        }
        _, err = s.db.Exec(s.db.Rebind(`INSERT INTO void_reasons (label, is_active, sort_order) VALUES (?, ?, ?)`),
                r.Label, btoi(r.Active), r.SortOrder)
        return err
}

func (s *Service) applyVoidReasonDelete(payload json.RawMessage) error {
        var r struct {
                Label string `json:"label"`
        }
        if err := json.Unmarshal(payload, &r); err != nil {
                return err
        }
        _, err := s.db.Exec(s.db.Rebind(`DELETE FROM void_reasons WHERE label = ?`), r.Label)
        return err
}

func (s *Service) applyCategory(payload json.RawMessage) error {
        var cat struct {
                Name string `json:"name"`
                Slug string `json:"slug"`
        }
        if err := json.Unmarshal(payload, &cat); err != nil {
                return err
        }
        if cat.Slug == "" || cat.Name == "" {
                return fmt.Errorf("category event missing name/slug")
        }
        var id int64
        err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM categories WHERE slug = ?`), cat.Slug).Scan(&id)
        if err == nil {
                _, err = s.db.Exec(s.db.Rebind(`UPDATE categories SET name = ? WHERE id = ?`), cat.Name, id)
                return err
        }
        if err != sql.ErrNoRows {
                return err
        }
        _, err = s.db.Exec(s.db.Rebind(`INSERT INTO categories (name, slug, sort_order) VALUES (?, ?, (SELECT COALESCE(MAX(sort_order),0)+1 FROM categories))`), cat.Name, cat.Slug)
        return err
}

func (s *Service) applyCategoryDelete(payload json.RawMessage) error {
        var cat struct {
                Slug string `json:"slug"`
        }
        if err := json.Unmarshal(payload, &cat); err != nil {
                return err
        }
        var count int
        if err := s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM products WHERE category_id = (SELECT id FROM categories WHERE slug = ?)`), cat.Slug).Scan(&count); err != nil {
                return err
        }
        if count > 0 {
                return nil // local products reference it — keep the category
        }
        _, err := s.db.Exec(s.db.Rebind(`DELETE FROM categories WHERE slug = ?`), cat.Slug)
        return err
}

func (s *Service) applyProduct(payload json.RawMessage) error {
        var pr struct {
                SKU          string `json:"sku"`
                Barcode      string `json:"barcode"`
                Name         string `json:"name"`
                CategorySlug string `json:"categorySlug"`
                PriceCents   int64  `json:"priceCents"`
                CostCents    int64  `json:"costCents"`
                StockQty     int    `json:"stockQty"`
                StockSet     bool   `json:"stockSet"`
                TrackStock   bool   `json:"trackStock"`
                Active       bool   `json:"active"`
                UpdatedAt    string `json:"updatedAt"`
        }
        if err := json.Unmarshal(payload, &pr); err != nil {
                return err
        }
        if pr.SKU == "" && pr.Barcode == "" {
                return fmt.Errorf("product event has no sku/barcode")
        }
        // Resolve category by slug (create on demand — categories sync first).
        var catID int64
        if pr.CategorySlug != "" {
                err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM categories WHERE slug = ?`), pr.CategorySlug).Scan(&catID)
                if err != nil {
                        s.db.Exec(s.db.Rebind(`INSERT INTO categories (name, slug) VALUES (?, ?)`), pr.CategorySlug, pr.CategorySlug)
                        s.db.QueryRow(s.db.Rebind(`SELECT id FROM categories WHERE slug = ?`), pr.CategorySlug).Scan(&catID)
                }
        }
        if catID == 0 {
                s.db.QueryRow(`SELECT id FROM categories ORDER BY sort_order, id LIMIT 1`).Scan(&catID)
        }
        var localID int64
        var localUpdated string
        q := `SELECT id, COALESCE(updated_at,'') FROM products WHERE sku = ?`
        args := []any{pr.SKU}
        if pr.SKU == "" {
                q = `SELECT id, COALESCE(updated_at,'') FROM products WHERE barcode = ?`
                args = []any{pr.Barcode}
        }
        err := s.db.QueryRow(s.db.Rebind(q), args...).Scan(&localID, &localUpdated)
        if err == nil {
                // LWW on catalog fields; stock only when explicitly set (admin edit).
                if pr.UpdatedAt != "" && localUpdated != "" && pr.UpdatedAt < localUpdated {
                        return nil // stale
                }
                if _, err := s.db.Exec(s.db.Rebind(`
                        UPDATE products SET barcode = ?, name = ?, category_id = ?, price_cents = ?, cost_cents = ?,
                                track_stock = ?, is_active = ?, updated_at = ?
                        WHERE id = ?`),
                        pr.Barcode, pr.Name, catID, pr.PriceCents, pr.CostCents, btoi(pr.TrackStock), btoi(pr.Active), pr.UpdatedAt, localID); err != nil {
                        return err
                }
                if pr.StockSet {
                        _, err = s.db.Exec(s.db.Rebind(`UPDATE products SET stock_qty = ? WHERE id = ?`), pr.StockQty, localID)
                }
                return err
        }
        if err != sql.ErrNoRows {
                return err
        }
        sku := pr.SKU
        if sku == "" {
                sku = "SKU-SYNC-" + randToken(4)
        }
        _, err = s.db.Exec(s.db.Rebind(`
                INSERT INTO products (sku, barcode, name, category_id, price_cents, cost_cents, stock_qty, track_stock, is_active, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
                sku, pr.Barcode, pr.Name, catID, pr.PriceCents, pr.CostCents, pr.StockQty, btoi(pr.TrackStock), btoi(pr.Active), pr.UpdatedAt)
        return err
}

func (s *Service) applyStockDelta(payload json.RawMessage) error {
        var d struct {
                SKU   string `json:"sku"`
                Delta int    `json:"delta"`
        }
        if err := json.Unmarshal(payload, &d); err != nil {
                return err
        }
        if d.SKU == "" || d.Delta == 0 {
                return nil
        }
        _, err := s.db.Exec(s.db.Rebind(`
                UPDATE products SET stock_qty = MAX(0, stock_qty + ?), updated_at = ? WHERE sku = ?`),
                d.Delta, nowStamp(), d.SKU)
        return err
}

func (s *Service) applyCustomer(payload json.RawMessage) error {
        var cu struct {
                Phone       string `json:"phone"`
                Name        string `json:"name"`
                CreditLimit int64  `json:"creditLimitCents"`
                Active      bool   `json:"active"`
                UpdatedAt   string `json:"updatedAt"`
        }
        if err := json.Unmarshal(payload, &cu); err != nil {
                return err
        }
        if cu.Name == "" {
                return fmt.Errorf("customer event has no name")
        }
        var id int64
        var updated string
        var err error
        if cu.Phone != "" {
                err = s.db.QueryRow(s.db.Rebind(`SELECT id, COALESCE(updated_at,'') FROM customers WHERE phone = ?`), cu.Phone).Scan(&id, &updated)
        } else {
                err = s.db.QueryRow(s.db.Rebind(`SELECT id, COALESCE(updated_at,'') FROM customers WHERE name = ? AND (phone = '' OR phone IS NULL)`), cu.Name).Scan(&id, &updated)
        }
        if err == nil {
                if cu.UpdatedAt != "" && updated != "" && cu.UpdatedAt < updated {
                        return nil // stale
                }
                _, err = s.db.Exec(s.db.Rebind(`
                        UPDATE customers SET name = ?, credit_limit_cents = ?, is_active = ?, updated_at = ? WHERE id = ?`),
                        cu.Name, cu.CreditLimit, btoi(cu.Active), cu.UpdatedAt, id)
                return err
        }
        if err != sql.ErrNoRows {
                return err
        }
        _, err = s.db.Exec(s.db.Rebind(`
                INSERT INTO customers (name, phone, credit_limit_cents, is_active, updated_at) VALUES (?, ?, ?, ?, ?)`),
                cu.Name, cu.Phone, cu.CreditLimit, btoi(cu.Active), cu.UpdatedAt)
        return err
}

func (s *Service) applyLedger(payload json.RawMessage) error {
        var le struct {
                Phone     string `json:"phone"`
                Name      string `json:"name"`
                Kind      string `json:"kind"`
                Amount    int64  `json:"amountCents"`
                Points    int64  `json:"pointsDelta"`
                Note      string `json:"note"`
                CreatedAt string `json:"createdAt"`
        }
        if err := json.Unmarshal(payload, &le); err != nil {
                return err
        }
        var custID int64
        var err error
        if le.Phone != "" {
                err = s.db.QueryRow(s.db.Rebind(`SELECT id FROM customers WHERE phone = ?`), le.Phone).Scan(&custID)
        } else if le.Name != "" {
                err = s.db.QueryRow(s.db.Rebind(`SELECT id FROM customers WHERE name = ?`), le.Name).Scan(&custID)
        } else {
                return fmt.Errorf("ledger event has no customer key")
        }
        if err != nil {
                return fmt.Errorf("ledger customer unknown: %w", err)
        }
        _, err = s.db.Exec(s.db.Rebind(`
                INSERT INTO customer_ledger (customer_id, order_id, kind, amount_cents, points_delta, note, created_by, created_at)
                VALUES (?, 0, ?, ?, ?, ?, 0, ?)`),
                custID, le.Kind, le.Amount, le.Points, le.Note, le.CreatedAt)
        if err != nil {
                return err
        }
        if le.Kind == models.LedgerCreditTopup || le.Kind == models.LedgerCreditRedeem {
                _, err = s.db.Exec(s.db.Rebind(`UPDATE customers SET store_credit_cents = store_credit_cents + ?, updated_at = ? WHERE id = ?`),
                        le.Amount, nowStamp(), custID)
                return err
        }
        _, err = s.db.Exec(s.db.Rebind(`UPDATE customers SET balance_cents = balance_cents + ?, loyalty_points = loyalty_points + ?, updated_at = ? WHERE id = ?`),
                le.Amount, le.Points, nowStamp(), custID)
        return err
}

// orderEvent is the wire form of a synced order.
type orderEvent struct {
        ClientUUID    string `json:"clientUuid"`
        Number        string `json:"number"`
        Status        string `json:"status"`
        SubtotalCents int64  `json:"subtotalCents"`
        DiscountCents int64  `json:"discountCents"`
        DiscountLabel string `json:"discountLabel"`
        PointsRedeemed int64 `json:"pointsRedeemed"`
        TaxCents      int64  `json:"taxCents"`
        TotalCents    int64  `json:"totalCents"`
        TaxPercent    float64 `json:"taxPercent"`
        TaxIncluded   bool   `json:"taxIncluded"`
        CashierName   string `json:"cashierName"`
        CustomerName  string `json:"customerName"`
        CustomerPhone string `json:"customerPhone"`
        CreatedAt     string `json:"createdAt"`
        PaidAt        string `json:"paidAt"`
        Items         []struct {
                SKU       string `json:"sku"`
                Name      string `json:"name"`
                Qty       int    `json:"qty"`
                UnitPrice int64  `json:"unitPriceCents"`
                LineTotal int64  `json:"lineTotalCents"`
        } `json:"items"`
        Payments []struct {
                Method      string `json:"method"`
                Amount      int64  `json:"amountCents"`
                Status      string `json:"status"`
                Receipt     string `json:"mpesaReceipt"`
                Phone       string `json:"phone"`
                CreatedAt   string `json:"createdAt"`
                CompletedAt string `json:"completedAt"`
        } `json:"payments"`
}

func (s *Service) applyOrder(payload json.RawMessage) error {
        var oe orderEvent
        if err := json.Unmarshal(payload, &oe); err != nil {
                return err
        }
        if oe.ClientUUID == "" {
                return fmt.Errorf("order event missing clientUuid")
        }
        var existing int64
        err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM orders WHERE client_uuid = ?`), oe.ClientUUID).Scan(&existing)
        if err == nil {
                // Known order: the only meaningful transition to replay is a
                // remote completion (PENDING → PAID, e.g. a tab settled or an
                // M-Pesa push finished on another till).
                var status string
                if err := s.db.QueryRow(`SELECT status FROM orders WHERE id = ?`, existing).Scan(&status); err != nil {
                        return err
                }
                if status == models.OrderPending && oe.Status == models.OrderPaid {
                        return s.remoteComplete(existing, oe)
                }
                return nil
        }
        if err != sql.ErrNoRows {
                return err
        }

        // Resolve the customer by phone (ids differ across devices).
        var custID int64
        if oe.CustomerPhone != "" {
                s.db.QueryRow(s.db.Rebind(`SELECT id FROM customers WHERE phone = ?`), oe.CustomerPhone).Scan(&custID)
        }

        // Order number collision (two tills allocated the same sequence):
        // suffix with a short device tag so history stays readable.
        number := oe.Number
        var n int
        s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM orders WHERE number = ?`), number).Scan(&n)
        if n > 0 {
                number = fmt.Sprintf("%s-%s", number, strings.ToUpper(randToken(2)))
        }

        tx, err := s.db.Begin()
        if err != nil {
                return err
        }
        defer tx.Rollback()
        taxInc := 0
        if oe.TaxIncluded {
                taxInc = 1
        }
        paidAt := oe.PaidAt
        status := oe.Status
        if status == models.OrderVoided {
                return nil // void events carry their own payload; skip dead orders
        }
        res, err := tx.Exec(s.db.Rebind(`
                INSERT INTO orders (number, status, subtotal_cents, tax_cents, total_cents, tax_percent, tax_included,
                        discount_cents, discount_label, points_redeemed, cashier_id, customer_name, note, client_uuid, created_at, paid_at, customer_id)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, '', ?, ?, ?, ?)`),
                number, status, oe.SubtotalCents, oe.TaxCents, oe.TotalCents, oe.TaxPercent, taxInc,
                oe.DiscountCents, oe.DiscountLabel, oe.PointsRedeemed, oe.CustomerName, oe.ClientUUID, oe.CreatedAt, paidAt, custID)
        if err != nil {
                return err
        }
        orderID, _ := res.LastInsertId()
        for _, it := range oe.Items {
                var pid int64
                if it.SKU != "" {
                        s.db.QueryRow(s.db.Rebind(`SELECT id FROM products WHERE sku = ?`), it.SKU).Scan(&pid)
                }
                if _, err := tx.Exec(s.db.Rebind(`
                        INSERT INTO order_items (order_id, product_id, name, sku, qty, unit_price_cents, line_total_cents)
                        VALUES (?, ?, ?, ?, ?, ?, ?)`),
                        orderID, pid, it.Name, it.SKU, it.Qty, it.UnitPrice, it.LineTotal); err != nil {
                        return err
                }
                // Stock moves with the sale on every device (only when completed).
                if status == models.OrderPaid && pid != 0 {
                        tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty = stock_qty - ? WHERE id = ? AND stock_qty >= ?`),
                                it.Qty, pid, it.Qty)
                }
        }
        for _, pm := range oe.Payments {
                if _, err := tx.Exec(s.db.Rebind(`
                        INSERT INTO payments (order_id, method, mode, amount_cents, status, phone, mpesa_receipt, created_at, completed_at)
                        VALUES (?, ?, '', ?, ?, ?, ?, ?, ?)`),
                        orderID, pm.Method, pm.Amount, pm.Status, pm.Phone, pm.Receipt, pm.CreatedAt, pm.CompletedAt); err != nil {
                        return err
                }
        }
        if err := tx.Commit(); err != nil {
                return err
        }
        s.broadcast(EventOrderCreated, map[string]any{"number": number, "synced": true})
        return nil
}

// remoteComplete replays a remote PENDING→PAID transition: deduct stock
// once and adopt the completed payments (ledger effects ride separately
// as ledger events, so they are not duplicated here).
func (s *Service) remoteComplete(orderID int64, oe orderEvent) error {
        tx, err := s.db.Begin()
        if err != nil {
                return err
        }
        defer tx.Rollback()
        res, err := tx.Exec(`UPDATE orders SET status = 'PAID', paid_at = ? WHERE id = ? AND status = 'PENDING'`,
                oe.PaidAt, orderID)
        if err != nil {
                return err
        }
        if n, _ := res.RowsAffected(); n != 1 {
                return nil // someone else completed it first
        }
        items, err := s.loadItemsTx(tx, orderID)
        if err != nil {
                return err
        }
        for _, it := range items {
                var track int
                if err := tx.QueryRow(`SELECT COALESCE(track_stock,1) FROM products WHERE id = ?`, it.ProductID).Scan(&track); err != nil {
                        continue
                }
                if track != 1 {
                        continue
                }
                tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty = stock_qty - ? WHERE id = ? AND stock_qty >= ?`),
                        it.Qty, it.ProductID, it.Qty)
        }
        for _, pm := range oe.Payments {
                tx.Exec(s.db.Rebind(`UPDATE payments SET status = ?, completed_at = COALESCE(NULLIF(?,''), completed_at) WHERE order_id = ? AND method = ? AND status = 'PENDING'`),
                        pm.Status, pm.CompletedAt, orderID, pm.Method)
        }
        if err := tx.Commit(); err != nil {
                return err
        }
        if order, err := s.GetOrder(orderID); err == nil {
                s.broadcast(EventOrderPaid, order)
        }
        return nil
}

func (s *Service) applyVoid(payload json.RawMessage) error {
        var v struct {
                OrderUUID   string `json:"orderClientUuid"`
                OrderNumber string `json:"orderNumber"`
                VoidedAt    string `json:"voidedAt"`
                Reason      string `json:"reason"`
        }
        if err := json.Unmarshal(payload, &v); err != nil {
                return err
        }
        var orderID int64
        var err error
        if v.OrderUUID != "" {
                err = s.db.QueryRow(s.db.Rebind(`SELECT id FROM orders WHERE client_uuid = ?`), v.OrderUUID).Scan(&orderID)
        } else if v.OrderNumber != "" {
                err = s.db.QueryRow(s.db.Rebind(`SELECT id FROM orders WHERE number = ?`), v.OrderNumber).Scan(&orderID)
        } else {
                return fmt.Errorf("void event has no order reference")
        }
        if err != nil {
                return nil // order unknown here (never synced) — nothing to void
        }
        var status string
        if err := s.db.QueryRow(`SELECT status FROM orders WHERE id = ?`, orderID).Scan(&status); err != nil {
                return err
        }
        if status == models.OrderVoided {
                return nil
        }
        tx, err := s.db.Begin()
        if err != nil {
                return err
        }
        defer tx.Rollback()
        res, err := tx.Exec(`UPDATE orders SET status = 'VOIDED', voided_at = ?, void_reason = ? WHERE id = ? AND status != 'VOIDED'`,
                v.VoidedAt, v.Reason, orderID)
        if err != nil {
                return err
        }
        if n, _ := res.RowsAffected(); n != 1 {
                return nil
        }
        items, err := s.loadItemsTx(tx, orderID)
        if err != nil {
                return err
        }
        if status == models.OrderPaid {
                for _, it := range items {
                        var track int
                        tx.QueryRow(`SELECT COALESCE(track_stock,1) FROM products WHERE id = ?`, it.ProductID).Scan(&track)
                        if track != 1 {
                                continue
                        }
                        if _, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty = stock_qty + ? WHERE id = ?`), it.Qty, it.ProductID); err != nil {
                                return err
                        }
                }
        }
        if _, err := tx.Exec(`UPDATE payments SET status = 'VOIDED', result_desc = ? WHERE order_id = ? AND status IN ('PENDING','COMPLETED')`,
                "synced void: "+truncStr(v.Reason, 160), orderID); err != nil {
                return err
        }
        // Reverse tab charges + refund points/credit (mirrors local Void).
        var custID, tabTotal int64
        var tabCount int
        if err := tx.QueryRow(`SELECT COALESCE(customer_id,0), total_cents FROM orders WHERE id = ?`, orderID).
                Scan(&custID, &tabTotal); err != nil {
                return err
        }
        if custID != 0 {
                tx.QueryRow(`SELECT COUNT(*) FROM payments WHERE order_id = ? AND method = 'account'`, orderID).Scan(&tabCount)
                if tabCount > 0 {
                        if err := recordLedgerTx(tx, s.db.Rebind, custID, orderID, models.LedgerAdjust, -tabTotal, 0, "void reversal (synced)", 0); err != nil {
                                return err
                        }
                }
                var pts int64
                tx.QueryRow(`SELECT COALESCE(points_redeemed,0) FROM orders WHERE id = ?`, orderID).Scan(&pts)
                if pts > 0 {
                        if err := recordLedgerTx(tx, s.db.Rebind, custID, orderID, models.LedgerLoyalty, 0, pts, "points refund (synced void)", 0); err != nil {
                                return err
                        }
                }
                var creditUsed int64
                tx.QueryRow(`SELECT COALESCE(SUM(amount_cents),0) FROM payments WHERE order_id = ? AND method = 'credit' AND status IN ('PENDING','COMPLETED')`, orderID).Scan(&creditUsed)
                if creditUsed > 0 {
                        if err := recordLedgerTx(tx, s.db.Rebind, custID, orderID, models.LedgerCreditTopup, creditUsed, 0, "store credit refund (synced void)", 0); err != nil {
                                return err
                        }
                }
        }
        if err := tx.Commit(); err != nil {
                return err
        }
        if order, err := s.GetOrder(orderID); err == nil {
                s.broadcast(EventOrderVoided, order)
        }
        return nil
}

func (s *Service) applyDesign(payload json.RawMessage) error {
        var dj struct {
                Title        string `json:"title"`
                ProductName  string `json:"productName"`
                CustomerName string `json:"customerName"`
                Notes        string `json:"notes"`
                Status       string `json:"status"`
                UpdatedAt    string `json:"updatedAt"`
        }
        if err := json.Unmarshal(payload, &dj); err != nil {
                return err
        }
        // Design jobs sync best-effort by (title, customer) — enough for the
        // production board to stay aligned without a global id.
        var id int64
        err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM design_jobs WHERE title = ? AND customer_name = ?`),
                dj.Title, dj.CustomerName).Scan(&id)
        if err == nil {
                _, err = s.db.Exec(s.db.Rebind(`UPDATE design_jobs SET status = ?, notes = ?, updated_at = ? WHERE id = ?`),
                        dj.Status, dj.Notes, dj.UpdatedAt, id)
                return err
        }
        if err != sql.ErrNoRows {
                return err
        }
        _, err = s.db.Exec(s.db.Rebind(`
                INSERT INTO design_jobs (title, product_name, customer_name, notes, status, created_by, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, 'synced', ?, ?)`),
                dj.Title, dj.ProductName, dj.CustomerName, dj.Notes, dj.Status, dj.UpdatedAt, dj.UpdatedAt)
        return err
}

// ---- Emit payload builders ----

// EmitOrder builds and queues the full order event (checkout + completions).
func (s *Service) EmitOrder(orderID int64) {
        order, err := s.GetOrder(orderID)
        if err != nil {
                return
        }
        phone := ""
        if order.CustomerID != 0 {
                if c, err := s.GetCustomer(order.CustomerID); err == nil {
                        phone = c.Phone
                }
        }
        oe := orderEvent{
                ClientUUID: order.ClientUUID, Number: order.Number, Status: order.Status,
                SubtotalCents: order.SubtotalCents, DiscountCents: order.DiscountCents,
                DiscountLabel: order.DiscountLabel, PointsRedeemed: order.PointsRedeemed,
                TaxCents: order.TaxCents, TotalCents: order.TotalCents,
                TaxPercent: order.TaxPercent, TaxIncluded: order.TaxIncluded,
                CashierName: order.CashierName, CustomerName: order.CustomerName,
                CustomerPhone: phone, CreatedAt: order.CreatedAt, PaidAt: order.PaidAt,
                Items: []struct {
                        SKU       string `json:"sku"`
                        Name      string `json:"name"`
                        Qty       int    `json:"qty"`
                        UnitPrice int64  `json:"unitPriceCents"`
                        LineTotal int64  `json:"lineTotalCents"`
                }{},
                Payments: []struct {
                        Method      string `json:"method"`
                        Amount      int64  `json:"amountCents"`
                        Status      string `json:"status"`
                        Receipt     string `json:"mpesaReceipt"`
                        Phone       string `json:"phone"`
                        CreatedAt   string `json:"createdAt"`
                        CompletedAt string `json:"completedAt"`
                }{},
        }
        for _, it := range order.Items {
                oe.Items = append(oe.Items, struct {
                        SKU       string `json:"sku"`
                        Name      string `json:"name"`
                        Qty       int    `json:"qty"`
                        UnitPrice int64  `json:"unitPriceCents"`
                        LineTotal int64  `json:"lineTotalCents"`
                }{it.SKU, it.Name, it.Qty, it.UnitPriceCents, it.LineTotalCents})
        }
        for _, pm := range order.Payments {
                oe.Payments = append(oe.Payments, struct {
                        Method      string `json:"method"`
                        Amount      int64  `json:"amountCents"`
                        Status      string `json:"status"`
                        Receipt     string `json:"mpesaReceipt"`
                        Phone       string `json:"phone"`
                        CreatedAt   string `json:"createdAt"`
                        CompletedAt string `json:"completedAt"`
                }{pm.Method, pm.AmountCents, pm.Status, pm.MpesaReceipt, pm.Phone, pm.CreatedAt, pm.CompletedAt})
        }
        s.Emit("order", "upsert", oe)
}

// EmitVoid queues a void event for cross-device cancellation.
func (s *Service) EmitVoid(orderID int64, reason string) {
        var uuid, number string
        s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(client_uuid,''), number FROM orders WHERE id = ?`), orderID).Scan(&uuid, &number)
        s.Emit("void", "upsert", map[string]any{
                "orderClientUuid": uuid,
                "orderNumber":     number,
                "voidedAt":        nowStamp(),
                "reason":          reason,
        })
}

// EmitProduct queues the full catalog row (admin edits; stock_set marks
// absolute stock changes so other devices follow the last admin write).
func (s *Service) EmitProduct(productID int64, stockSet bool) {
        var pr struct {
                SKU     string
                Barcode string
                Name    string
                Slug    string
                Price   int64
                Cost    int64
                Stock   int
                Track   int
                Active  int
        }
        err := s.db.QueryRow(s.db.Rebind(`
                SELECT COALESCE(p.sku,''), COALESCE(p.barcode,''), p.name, COALESCE(c.slug,''),
                        p.price_cents, p.cost_cents, p.stock_qty, COALESCE(p.track_stock,1), COALESCE(p.is_active,1)
                FROM products p LEFT JOIN categories c ON c.id = p.category_id WHERE p.id = ?`), productID).
                Scan(&pr.SKU, &pr.Barcode, &pr.Name, &pr.Slug, &pr.Price, &pr.Cost, &pr.Stock, &pr.Track, &pr.Active)
        if err != nil {
                return
        }
        s.Emit("product", "upsert", map[string]any{
                "sku": pr.SKU, "barcode": pr.Barcode, "name": pr.Name, "categorySlug": pr.Slug,
                "priceCents": pr.Price, "costCents": pr.Cost, "stockQty": pr.Stock,
                "stockSet": stockSet, "trackStock": pr.Track == 1, "active": pr.Active == 1,
                "updatedAt": nowStamp(),
        })
}

// EmitStockDelta queues a signed stock movement (sales use per-line deltas
// inside the order event; this covers adjusts, PO receives, stock takes).
func (s *Service) EmitStockDelta(sku string, delta int) {
        if sku == "" || delta == 0 {
                return
        }
        s.Emit("stock", "upsert", map[string]any{"sku": sku, "delta": delta})
}

// EmitCustomer queues the identity row (balances ride on ledger events).
func (s *Service) EmitCustomer(customerID int64) {
        var name, phone string
        var limit int64
        var active int
        err := s.db.QueryRow(s.db.Rebind(`SELECT name, COALESCE(phone,''), credit_limit_cents, COALESCE(is_active,1) FROM customers WHERE id = ?`), customerID).
                Scan(&name, &phone, &limit, &active)
        if err != nil {
                return
        }
        s.Emit("customer", "upsert", map[string]any{
                "name": name, "phone": phone, "creditLimitCents": limit, "active": active == 1, "updatedAt": nowStamp(),
        })
}

// EmitLedger queues a balance-moving ledger row.
func (s *Service) EmitLedger(customerID int64, kind string, amount, points int64, note string) {
        var phone, name string
        s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(phone,''), name FROM customers WHERE id = ?`), customerID).Scan(&phone, &name)
        s.Emit("ledger", "upsert", map[string]any{
                "phone": phone, "name": name, "kind": kind,
                "amountCents": amount, "pointsDelta": points, "note": note, "createdAt": nowStamp(),
        })
}

// EmitCategory queues a category upsert/delete.
func (s *Service) EmitCategory(name, slug string, delete bool) {
        if delete {
                s.Emit("category", "delete", map[string]any{"slug": slug, "name": name})
                return
        }
        s.Emit("category", "upsert", map[string]any{"slug": slug, "name": name})
}

// EmitDesign queues a design-board change.
func (s *Service) EmitDesign(title, productName, customerName, notes, status string) {
        s.Emit("design", "upsert", map[string]any{
                "title": title, "productName": productName, "customerName": customerName,
                "notes": notes, "status": status, "updatedAt": nowStamp(),
        })
}
