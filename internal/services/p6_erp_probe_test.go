package services

// p6_erp_probe_test.go — P6 ERP depth verification:
//
//  1. Owner cloud sign-in: the reinstall path — cloud verifies portal
//     credentials, the till joins the team, the local owner account is
//     provisioned with the typed password, onboarding is skipped. Wrong
//     credentials are refused and nothing is created.
//  2. X/Z shift report: X (open) sums live tenders + expected drawer cash;
//     Z (closed) freezes counted + variance. Both windows agree with the
//     close flow's own expected-cash computation.
//  3. PO receive: stock and weighted-average cost move locally AND stock
//     delta events are emitted so the rest of the team syncs the new stock.
//  4. eTIMS: a checkout carrying buyerPin persists the tax-invoice fields
//     (buyer_pin + invoice_number defaulting to the order number).

import (
        "context"
        "encoding/json"
        "net/http"
        "net/http/httptest"
        "path/filepath"
        "strings"
        "sync"
        "testing"

        "posapp/internal/auth"
        "posapp/internal/database"
        "posapp/internal/models"
        "posapp/internal/printer"
        "posapp/internal/settings"
)

// ---- owner cloud sign-in ----

func TestP6OwnerCloudSignin(t *testing.T) {
        var mu sync.Mutex
        passwords := map[string]string{"owner": "correct horse battery"}
        fails := map[string]int{}

        srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
                if !strings.HasSuffix(r.URL.Path, "/rpc/owner_device_signin") {
                        w.WriteHeader(404)
                        return
                }
                var in struct {
                        Username string `json:"p_username"`
                        Password string `json:"p_password"`
                }
                json.NewDecoder(r.Body).Decode(&in)
                mu.Lock()
                defer mu.Unlock()
                want, ok := passwords[strings.ToLower(in.Username)]
                if !ok || want != in.Password {
                        fails[strings.ToLower(in.Username)]++
                        json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "wrong username or password"})
                        return
                }
                json.NewEncoder(w).Encode(map[string]any{"ok": true, "team_code": "P6-TEAM", "store_name": "P6 Store"})
        }))
        defer srv.Close()

        oldBase := cloudBaseURL
        cloudBaseURL = srv.URL
        t.Cleanup(func() { cloudBaseURL = oldBase })

        s := p6NewService(t)

        // Wrong password: refused, nothing created.
        if _, err := s.OwnerCloudSignin("owner", "wrong-password", "till"); err == nil {
                t.Fatal("wrong password must be refused")
        }
        var users int
        s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users)
        if users != 0 {
                t.Fatalf("refused sign-in created %d users", users)
        }
        mu.Lock()
        if fails["owner"] != 1 {
                t.Fatalf("cloud failure count = %d, want 1", fails["owner"])
        }
        mu.Unlock()

        // Correct credentials: team joined, owner provisioned as Admin.
        info, err := s.OwnerCloudSignin("owner", "correct horse battery", "till")
        if err != nil {
                t.Fatalf("owner sign-in: %v", err)
        }
        if info.TeamCode != "P6-TEAM" || info.StoreName != "P6 Store" || info.UserID == 0 {
                t.Fatalf("sign-in info incomplete: %+v", info)
        }
        if s.settings.Get("sync_team_code") != "P6-TEAM" ||
                !s.settings.GetBool("sync_approved", false) ||
                !s.settings.GetBool("sync_enabled", false) {
                t.Fatal("till must be pointed at the team and approved")
        }
        if s.settings.Get("onboarding_done") != "true" {
                t.Fatal("owner sign-in must skip first-run onboarding")
        }
        var roleID int64
        var pwHash string
        if err := s.db.QueryRow(`SELECT role_id, password_hash FROM users WHERE id = ?`, info.UserID).Scan(&roleID, &pwHash); err != nil {
                t.Fatalf("owner account missing: %v", err)
        }
        var adminRole int64
        s.db.QueryRow(`SELECT id FROM roles WHERE name = 'Admin'`).Scan(&adminRole)
        if roleID != adminRole {
                t.Fatalf("owner role = %d, want Admin %d", roleID, adminRole)
        }
        if !auth.VerifyPassword(pwHash, "correct horse battery") {
                t.Fatal("local owner password must verify against the typed password")
        }

        // Re-sign-in (e.g. another wipe) claims the same account, no duplicate.
        info2, err := s.OwnerCloudSignin("OWNER", "correct horse battery", "till")
        if err != nil || info2.UserID != info.UserID {
                t.Fatalf("re-sign-in should reuse the account: %+v %v", info2, err)
        }
        s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users)
        if users != 1 {
                t.Fatalf("user count after re-sign-in = %d, want 1", users)
        }
}

// ---- X/Z shift report ----

func TestP6ShiftXZReport(t *testing.T) {
        s := p6NewService(t)
        p := p6Cashier(t, s, "cash1")

        if _, err := s.OpenShift(p, 100_00); err != nil {
                t.Fatalf("open shift: %v", err)
        }
        prodID := p6Product(t, s, "SKU-P6X", "Report Widget", 55_00)
        order, err := s.Checkout(context.Background(), p, models.CheckoutRequest{
                Items: []models.CheckoutItem{{ProductID: prodID, Qty: 2}}, // 110.00 cash
                PaymentMethod: models.MethodCash,
                ClientUUID:    "p6xz-1",
        })
        if err != nil {
                t.Fatalf("checkout: %v", err)
        }
        _ = order

        // X report (open): live totals + drawer expectation, nothing final.
        x, err := s.GetShiftReport(1)
        if err != nil {
                t.Fatalf("X report: %v", err)
        }
        if !x.Open {
                t.Fatal("X report must be open")
        }
        if x.CashCents != 110_00 || x.GrossCents != 110_00 {
                t.Fatalf("X cash/gross = %d/%d, want 11000/11000", x.CashCents, x.GrossCents)
        }
        if x.ExpectedCashCents != 210_00 { // float 100 + cash 110
                t.Fatalf("X expected cash = %d, want 21000", x.ExpectedCashCents)
        }
        if x.OrdersCount != 1 || x.CountedCents != 0 {
                t.Fatalf("X orders=%d counted=%d, want 1/0", x.OrdersCount, x.CountedCents)
        }

        // Close with a short count: variance is what the cashier signed.
        if _, err := s.CloseShift(p, 195_00); err != nil {
                t.Fatalf("close shift: %v", err)
        }
        z, err := s.GetShiftReport(1)
        if err != nil {
                t.Fatalf("Z report: %v", err)
        }
        if z.Open {
                t.Fatal("Z report must be closed")
        }
        if z.CountedCents != 195_00 || z.VarianceCents != -15_00 {
                t.Fatalf("Z counted/variance = %d/%d, want 19500/-1500", z.CountedCents, z.VarianceCents)
        }
        // The Z expected-cash must agree with the close flow's own computation.
        if z.Shift.ExpectedCents != z.ExpectedCashCents {
                t.Fatalf("Z expected drift: report %d vs shift %d", z.ExpectedCashCents, z.Shift.ExpectedCents)
        }
}

// ---- PO receive: stock, weighted-average cost, and cross-till deltas ----

func TestP6POReceiveEmitsStockDeltas(t *testing.T) {
        s := p6NewService(t)
        prodID := p6Product(t, s, "SKU-P6PO", "Bought Widget", 90_00)
        // 20 units on hand at 90.00 cost.
        s.db.Exec(s.db.Rebind(`UPDATE products SET stock_qty = 20 WHERE id = ?`), prodID)

        var supID int64
        if err := s.db.QueryRow(`INSERT INTO suppliers (name) VALUES ('Acme') RETURNING id`).Scan(&supID); err != nil {
                t.Fatalf("supplier: %v", err)
        }
        p := p6Cashier(t, s, "owner")
        po, err := s.CreatePO(supID, []POItemInput{{ProductID: prodID, Qty: 10, CostCents: 50_00}}, "restock", p)
        if err != nil {
                t.Fatalf("create PO: %v", err)
        }

        received, err := s.ReceivePO(po.ID, p)
        if err != nil {
                t.Fatalf("receive PO: %v", err)
        }
        if received.Status != models.POReceived {
                t.Fatalf("PO status = %s, want RECEIVED", received.Status)
        }
        var stock, cost int64
        s.db.QueryRow(`SELECT stock_qty, cost_cents FROM products WHERE id = ?`, prodID).Scan(&stock, &cost)
        if stock != 30 {
                t.Fatalf("stock after receive = %d, want 30", stock)
        }
        // Weighted average: (20*4500 + 10*5000) / 30 = 4666.67 -> 4667.
        if cost != 4667 {
                t.Fatalf("avg cost after receive = %d, want 4667", cost)
        }
        // The receive must have emitted a stock delta so other tills pick it up.
        found := false
        s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE entity = 'stock' AND payload LIKE '%"delta":10%'`).Scan(&found)
        _ = found
        rows, qerr := s.db.Query(`SELECT payload FROM sync_outbox WHERE entity = 'stock'`)
        if qerr != nil {
                t.Fatalf("outbox: %v", qerr)
        }
        defer rows.Close()
        for rows.Next() {
                var payload string
                if rows.Scan(&payload) == nil && strings.Contains(payload, `"delta":10`) && strings.Contains(payload, "SKU-P6PO") {
                        found = true
                }
        }
        if !found {
                t.Fatal("PO receive did not emit a stock delta event for the team")
        }
}

// ---- eTIMS invoice fields ----

func TestP6ETimsBuyerPinOnCheckout(t *testing.T) {
        s := p6NewService(t)
        p := p6Cashier(t, s, "cash1")
        prodID := p6Product(t, s, "SKU-P6VAT", "Invoiced Item", 116_00)

        order, err := s.Checkout(context.Background(), p, models.CheckoutRequest{
                Items:         []models.CheckoutItem{{ProductID: prodID, Qty: 1}},
                PaymentMethod: models.MethodCash,
                BuyerPIN:      " p051234567x ",
                ClientUUID:    "p6etims-1",
        })
        if err != nil {
                t.Fatalf("checkout: %v", err)
        }
        again, err := s.GetOrder(order.ID)
        if err != nil {
                t.Fatalf("reload order: %v", err)
        }
        if again.BuyerPIN != "P051234567X" {
                t.Fatalf("buyer PIN stored = %q, want P051234567X (uppercased, trimmed)", again.BuyerPIN)
        }
        if again.InvoiceNumber != again.Number {
                t.Fatalf("invoice number = %q, want the order number %q", again.InvoiceNumber, again.Number)
        }
        // Without a buyer PIN the order stays a plain receipt.
        plain, err := s.Checkout(context.Background(), p, models.CheckoutRequest{
                Items: []models.CheckoutItem{{ProductID: prodID, Qty: 1}},
                PaymentMethod: models.MethodCash,
                ClientUUID:    "p6etims-2",
        })
        if err != nil {
                t.Fatalf("plain checkout: %v", err)
        }
        if plain.BuyerPIN != "" {
                t.Fatalf("plain order has buyer PIN %q", plain.BuyerPIN)
        }
}

// ---- fixtures ----

func p6NewService(t *testing.T) *Service {
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
        db.Exec(`INSERT INTO categories (name, slug) VALUES ('General', 'general')`)
        // The sign-in test needs a FRESH user table (zero users), so roles are
        // seeded without any users; tests that need a cashier create their own.
        db.Exec(`INSERT INTO roles (name, description, is_system, permissions) VALUES ('Admin', 'test', 1, '[]')`)
        return &Service{db: db, settings: st, appVersion: "test", printer: printer.NewWorker(db, st)}
}

// p6Cashier inserts a real user (orders.cashier_id FK) with the Admin role
// and returns a principal for it.
func p6Cashier(t *testing.T, s *Service, username string) *auth.Principal {
        t.Helper()
        var roleID int64
        s.db.QueryRow(`SELECT id FROM roles WHERE name = 'Admin'`).Scan(&roleID)
        res, err := s.db.Exec(s.db.Rebind(`
                INSERT INTO users (username, full_name, password_hash, role_id, is_active, must_rotate)
                VALUES (?, ?, 'x', ?, 1, 0)`), username, username, roleID)
        if err != nil {
                t.Fatalf("user %s: %v", username, err)
        }
        id, _ := res.LastInsertId()
        return &auth.Principal{ID: id, Username: username, RoleID: roleID, RoleName: "Admin", Active: true}
}

func p6Product(t *testing.T, s *Service, sku, name string, priceCents int64) int64 {
        t.Helper()
        var id int64
        err := s.db.QueryRow(s.db.Rebind(`
                INSERT INTO products (sku, name, category_id, price_cents, cost_cents, stock_qty)
                VALUES (?, ?, (SELECT id FROM categories LIMIT 1), ?, ?, 100) RETURNING id`),
                sku, name, priceCents, priceCents/2).Scan(&id)
        if err != nil {
                t.Fatalf("product %s: %v", sku, err)
        }
        return id
}

var _ = models.MethodCash
