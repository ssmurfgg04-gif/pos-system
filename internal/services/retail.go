package services

// retail.go — the retail expansion services: parked/held sales, the
// void-reason catalog, prepaid store credit top-ups, the org team
// overview, and per-role dashboard configuration.

import (
        "database/sql"
        crand "crypto/rand"
        "encoding/hex"
        "encoding/json"
        "fmt"
        "os"
        "strings"
        "time"

        "posapp/internal/auth"
        "posapp/internal/models"
)

func btoi(b bool) int {
        if b {
                return 1
        }
        return 0
}

// randToken returns n random bytes hex-encoded (device ids, codes).
func randToken(n int) string {
        b := make([]byte, n)
        if _, err := crand.Read(b); err != nil {
                return fmt.Sprintf("%d", time.Now().UnixNano())
        }
        return hex.EncodeToString(b)
}

// ---- Held (parked) sales ----

// HoldSale parks the current cart. Items are frozen as submitted (the
// client sends productId/qty/unitPriceCents triples); nothing is priced,
// stocked, or charged until a normal checkout completes.
func (s *Service) HoldSale(refName string, req models.CheckoutRequest, p *auth.Principal) (*models.HeldSale, error) {
        if len(req.Items) == 0 {
                return nil, fmt.Errorf("nothing to hold — the cart is empty")
        }
        if len(req.Items) > models.MaxOrderLines {
                return nil, fmt.Errorf("too many lines (max %d)", models.MaxOrderLines)
        }
        for _, it := range req.Items {
                if it.Qty <= 0 || it.Qty > models.MaxOrderQty {
                        return nil, fmt.Errorf("invalid quantity")
                }
                if it.UnitPriceCents < 0 || it.UnitPriceCents > models.MaxPriceCents {
                        return nil, fmt.Errorf("invalid price on held line")
                }
        }
        raw, err := json.Marshal(req.Items)
        if err != nil {
                return nil, err
        }
        custName := req.CustomerName
        if req.CustomerID != 0 {
                if c, err := s.GetCustomer(req.CustomerID); err == nil {
                        custName = c.Name
                }
        }
        res, err := s.db.Exec(s.db.Rebind(`
                INSERT INTO held_sales (ref_name, items_json, customer_id, customer_name, note, device_id, created_by, created_by_name, created_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
                truncStr(strings.TrimSpace(refName), 120), string(raw), req.CustomerID, custName,
                truncStr(req.Note, 500), s.deviceID(), p.ID, p.Username, nowStamp())
        if err != nil {
                return nil, err
        }
        id, _ := res.LastInsertId()
        s.Audit(p.ID, p.Username, "SALE_HELD", "held_sale", fmt.Sprint(id), refName)
        s.broadcast(EventHeldUpdate, nil)
        return s.GetHeldSale(id)
}

// ListHeldSales returns parked carts, oldest last.
func (s *Service) ListHeldSales() ([]models.HeldSale, error) {
        rows, err := s.db.Query(`
                SELECT id, COALESCE(ref_name,''), items_json, customer_id, COALESCE(customer_name,''),
                        COALESCE(note,''), COALESCE(device_id,''), created_by, COALESCE(created_by_name,''), created_at
                FROM held_sales ORDER BY id DESC LIMIT 100`)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        out := []models.HeldSale{}
        for rows.Next() {
                var h models.HeldSale
                var raw string
                if err := rows.Scan(&h.ID, &h.RefName, &raw, &h.CustomerID, &h.CustomerName,
                        &h.Note, &h.DeviceID, &h.CreatedBy, &h.CreatedByName, &h.CreatedAt); err != nil {
                        return nil, err
                }
                _ = json.Unmarshal([]byte(raw), &h.Items)
                out = append(out, h)
        }
        return out, rows.Err()
}

// GetHeldSale loads one parked cart.
func (s *Service) GetHeldSale(id int64) (*models.HeldSale, error) {
        var h models.HeldSale
        var raw string
        err := s.db.QueryRow(`
                SELECT id, COALESCE(ref_name,''), items_json, customer_id, COALESCE(customer_name,''),
                        COALESCE(note,''), COALESCE(device_id,''), created_by, COALESCE(created_by_name,''), created_at
                FROM held_sales WHERE id = ?`, id).
                Scan(&h.ID, &h.RefName, &raw, &h.CustomerID, &h.CustomerName,
                        &h.Note, &h.DeviceID, &h.CreatedBy, &h.CreatedByName, &h.CreatedAt)
        if err != nil {
                return nil, ErrNotFound
        }
        _ = json.Unmarshal([]byte(raw), &h.Items)
        return &h, nil
}

// DeleteHeldSale discards a parked cart (cashier changed their mind).
func (s *Service) DeleteHeldSale(id int64, p *auth.Principal) error {
        res, err := s.db.Exec(`DELETE FROM held_sales WHERE id = ?`, id)
        if err != nil {
                return err
        }
        if n, _ := res.RowsAffected(); n != 1 {
                return ErrNotFound
        }
        s.Audit(p.ID, p.Username, "SALE_HELD_DISCARDED", "held_sale", fmt.Sprint(id), "")
        s.broadcast(EventHeldUpdate, nil)
        return nil
}

// deviceID returns a stable per-install identity used to tag held sales
// and sync events (hostname plus a random suffix persisted on first use).
func (s *Service) deviceID() string {
        if id := s.settings.Get("device_id"); id != "" {
                return id
        }
        host, err := os.Hostname()
        if err != nil || host == "" {
                host = "till"
        }
        id := "dev-" + strings.ToLower(host) + "-" + randToken(4)
        _ = s.settings.Set("device_id", id) // best-effort persist
        return id
}

// ---- Void reasons ----

// ListVoidReasons returns the active reason catalog (till dropdown).
func (s *Service) ListVoidReasons(includeInactive bool) ([]models.VoidReason, error) {
        q := `SELECT id, label, is_active, sort_order FROM void_reasons`
        if !includeInactive {
                q += ` WHERE is_active = 1`
        }
        q += ` ORDER BY sort_order, id`
        rows, err := s.db.Query(q)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        out := []models.VoidReason{}
        for rows.Next() {
                var r models.VoidReason
                var active int
                if err := rows.Scan(&r.ID, &r.Label, &active, &r.SortOrder); err != nil {
                        return nil, err
                }
                r.Active = active == 1
                out = append(out, r)
        }
        return out, rows.Err()
}

// CreateVoidReason adds a reason (settings-manage, admin).
func (s *Service) CreateVoidReason(label string, p *auth.Principal) (*models.VoidReason, error) {
        label = strings.TrimSpace(label)
        if label == "" || len(label) > 120 {
                return nil, fmt.Errorf("reason label must be 1-120 characters")
        }
        var n int
        if err := s.db.QueryRow(`SELECT COUNT(*) FROM void_reasons`).Scan(&n); err != nil {
                return nil, err
        }
        res, err := s.db.Exec(s.db.Rebind(`
                INSERT INTO void_reasons (label, is_active, sort_order) VALUES (?, 1, ?)`), label, n+1)
        if err != nil {
                if isUniqueViolation(err) {
                        return nil, fmt.Errorf("reason already exists")
                }
                return nil, err
        }
        id, _ := res.LastInsertId()
        s.Audit(p.ID, p.Username, "VOID_REASON_ADDED", "void_reason", fmt.Sprint(id), label)
        return &models.VoidReason{ID: id, Label: label, Active: true, SortOrder: n + 1}, nil
}

// UpdateVoidReason toggles/renames one reason.
func (s *Service) UpdateVoidReason(id int64, label string, active bool, p *auth.Principal) error {
        label = strings.TrimSpace(label)
        if label == "" || len(label) > 120 {
                return fmt.Errorf("reason label must be 1-120 characters")
        }
        res, err := s.db.Exec(s.db.Rebind(`UPDATE void_reasons SET label = ?, is_active = ? WHERE id = ?`),
                label, btoi(active), id)
        if err != nil {
                if isUniqueViolation(err) {
                        return fmt.Errorf("reason already exists")
                }
                return err
        }
        if n, _ := res.RowsAffected(); n != 1 {
                return ErrNotFound
        }
        s.Audit(p.ID, p.Username, "VOID_REASON_UPDATED", "void_reason", fmt.Sprint(id), label)
        return nil
}

// ---- Store credit (prepaid) ----

// TopUpStoreCredit records cash taken now that becomes shop-owed credit.
// Positive amounts only (corrections go through the adjustment endpoint).
func (s *Service) TopUpStoreCredit(customerID, amountCents int64, note string, p *auth.Principal) (*models.Customer, error) {
        if amountCents <= 0 {
                return nil, fmt.Errorf("top-up amount must be positive")
        }
        if amountCents > models.MaxPriceCents {
                return nil, fmt.Errorf("top-up amount out of range")
        }
        c, err := s.GetCustomer(customerID)
        if err != nil {
                return nil, err
        }
        if !c.Active {
                return nil, fmt.Errorf("customer is inactive")
        }
        tx, err := s.db.Begin()
        if err != nil {
                return nil, err
        }
        defer tx.Rollback()
        if err := recordLedgerTx(tx, s.db.Rebind, customerID, 0, models.LedgerCreditTopup,
                amountCents, 0, note, p.ID); err != nil {
                return nil, err
        }
        if err := tx.Commit(); err != nil {
                return nil, err
        }
        s.Audit(p.ID, p.Username, "CREDIT_TOPUP", "customer", fmt.Sprint(customerID), fmt.Sprintf("%d: %s", amountCents, note))
        return s.GetCustomer(customerID)
}

// ---- Team overview (People) ----

// TeamOverview returns every user enriched with today's sales counts —
// the admin's at-a-glance view of who is on the team and what moved.
func (s *Service) TeamOverview() ([]models.TeamMember, error) {
        rows, err := s.db.Query(`
                SELECT u.id, u.username, COALESCE(u.full_name,''), u.role_id, r.name,
                        COALESCE(u.is_active,1), COALESCE(u.must_rotate,0), COALESCE(u.pin_hash,'') != '',
                        u.created_at
                FROM users u LEFT JOIN roles r ON r.id = u.role_id
                ORDER BY u.is_active DESC, u.id ASC`)
        if err != nil {
                return nil, err
        }
        type row struct {
                m      models.TeamMember
                roleID int64
        }
        var list []row
        for rows.Next() {
                var r row
                var pinSet int
                var active, rotate int
                if err := rows.Scan(&r.m.ID, &r.m.Username, &r.m.FullName, &r.roleID, &r.m.RoleName,
                        &active, &rotate, &pinSet, &r.m.CreatedAt); err != nil {
                        rows.Close()
                        return nil, err
                }
                r.m.Active = active == 1
                r.m.MustRotate = rotate == 1
                r.m.PINSet = pinSet == 1
                r.m.Permissions = []string{}
                list = append(list, r)
        }
        rows.Close()
        if err := rows.Err(); err != nil {
                return nil, err
        }

        // Today's sales per cashier (paid orders only). Bounds computed in Go
        // so the query stays driver-agnostic.
        type stat struct {
                cashier int64
                count   int64
                cents   int64
                last    string
        }
        stats := map[int64]*stat{}
        from, to := dayBounds("")
        srows, err := s.db.Query(s.db.Rebind(`
                SELECT cashier_id, COUNT(*), COALESCE(SUM(total_cents),0), MAX(COALESCE(NULLIF(paid_at,''), created_at))
                FROM orders
                WHERE status = 'PAID' AND COALESCE(NULLIF(paid_at,''), created_at) BETWEEN ? AND ?
                GROUP BY cashier_id`), from, to)
        if err == nil {
                for srows.Next() {
                        var st stat
                        if err := srows.Scan(&st.cashier, &st.count, &st.cents, &st.last); err == nil {
                                stats[st.cashier] = &st
                        }
                }
                srows.Close()
        }

        // Permissions per role, batched.
        permByRole := map[int64][]string{}
        prows, err := s.db.Query(`SELECT id, permissions FROM roles`)
        if err == nil {
                for prows.Next() {
                        var rid int64
                        var raw string
                        if err := prows.Scan(&rid, &raw); err == nil {
                                var perms []string
                                _ = json.Unmarshal([]byte(raw), &perms)
                                permByRole[rid] = perms
                        }
                }
                prows.Close()
        }

        out := make([]models.TeamMember, 0, len(list))
        for _, r := range list {
                r.m.RoleID = r.roleID
                r.m.Permissions = permByRole[r.roleID]
                out = append(out, r.m)
        }
        // Fill per-cashier stats (keyed by user id).
        for i := range out {
                if st, ok := stats[out[i].ID]; ok {
                        out[i].SalesToday = st.count
                        out[i].SalesTodayCents = st.cents
                        out[i].LastOrderAt = st.last
                }
        }
        return out, nil
}

// ---- Role dashboards ----

// SetRoleDashboard stores home page + widget config for a role.
func (s *Service) SetRoleDashboard(roleID int64, homePage string, cfg *models.DashboardConfig) error {
        raw := "{}"
        if cfg != nil {
                b, err := json.Marshal(cfg)
                if err != nil {
                        return err
                }
                raw = string(b)
        }
        res, err := s.db.Exec(s.db.Rebind(`UPDATE roles SET home_page = ?, dashboard_config = ? WHERE id = ?`),
                homePage, raw, roleID)
        if err != nil {
                return err
        }
        if n, _ := res.RowsAffected(); n != 1 {
                return ErrNotFound
        }
        return nil
}

// RoleByID loads one role with its dashboard config parsed.
func (s *Service) RoleByID(roleID int64) (*models.Role, error) {
        var r models.Role
        var permsRaw, dashRaw string
        var system int
        err := s.db.QueryRow(`SELECT id, name, COALESCE(description,''), permissions, is_system,
                COALESCE(home_page,''), COALESCE(dashboard_config,'{}') FROM roles WHERE id = ?`, roleID).
                Scan(&r.ID, &r.Name, &r.Description, &permsRaw, &system, &r.HomePage, &dashRaw)
        if err != nil {
                if err == sql.ErrNoRows {
                        return nil, ErrNotFound
                }
                return nil, err
        }
        r.System = system == 1
        _ = json.Unmarshal([]byte(permsRaw), &r.Permissions)
        var dc models.DashboardConfig
        if err := json.Unmarshal([]byte(dashRaw), &dc); err == nil {
                r.Dashboard = &dc
        }
        return &r, nil
}
