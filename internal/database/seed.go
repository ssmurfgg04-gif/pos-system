package database

import (
        "crypto/rand"
        "database/sql"
        "encoding/hex"
        "encoding/json"
        "fmt"

        "posapp/internal/hash"
        "posapp/internal/models"
)

// DefaultSettings are the white-label defaults. Everything user-visible
// comes from here — zero hardcoded branding anywhere in the app.
// Secrets are masked by the settings API ("__SET__").
var DefaultSettings = map[string]string{
        "app_name":              "Point of Sale",
        "store_name":            "My Store",
        "store_address":         "",
        "store_phone":           "",
        "receipt_footer":        "Thank you for your business!",
        "currency_code":         "KES",
        "currency_symbol":       "KES",
        "tax_percent":           "16",
        "tax_included":          "true",
        "brand_color":           "#10B981",
        "brand_logo":            "", // "1" when brand-logo.png is present
        "payment_mode":          "auto", // auto | stk | manual
        "till_number":           "",
        "paybill_number":        "",
        "mpesa_env":             "mock", // mock | sandbox | production
        "mpesa_shortcode":       "",
        "mpesa_passkey":         "",
        "mpesa_consumer_key":    "",
        "mpesa_consumer_secret": "",
        "mpesa_callback_url":    "",
        "mpesa_mock_delay_ms":   "4000",
        "mpesa_mock_result_code": "0",
        "printer_target":        "", // e.g. tcp://192.168.1.200:9100 or file:///dev/usb/lp0
        "printer_width":         "80",
        "auto_print_receipts":   "true",
        "receipt_logo":            "true", // print brand-logo.png atop receipts when present
        "low_stock_threshold":   "5",
        "backup_auto":           "true", // daily 02:00 VACUUM INTO snapshot
        "backup_keep":           "7",    // snapshots retained
        "onboarding_done":       "false", // guided first-run wizard completed
        "update_channel":        "stable", // stable | off (auto-update checks)
        "update_api_base":       "https://api.github.com", // overridable for tests
        "offsite_enabled":       "false", // encrypted push of each snapshot
        "offsite_endpoint":      "",     // Supabase project URL, e.g. https://xyzcompany.supabase.co
        "offsite_bucket":        "",     // private storage bucket (one project per shop)
        "offsite_secret_key":    "", // SECRET — project service_role key, masked in API
        "offsite_prefix":        "", // defaults to OS hostname
        "offsite_keep":          "14", // remote copies retained (free tier is 500MB)
        "offsite_passphrase":    "", // SECRET — encrypts snapshots; owner keeps a copy
}

type seedProduct struct {
        sku, barcode, name, category string
        price, cost, stock           int64
        track                        bool
}

var seedCategories = []struct{ name, slug string }{
        {"T-Shirts", "tshirts"},
        {"Mugs & Bottles", "mugs-bottles"},
        {"Accessories", "accessories"},
        {"Services", "services"},
}

var seedProducts = []seedProduct{
        {"TS-001", "4001234500011", "Classic Cotton Tee — Black", "tshirts", 55000, 32000, 40, true},
        {"TS-002", "4001234500028", "Classic Cotton Tee — White", "tshirts", 55000, 32000, 35, true},
        {"TS-003", "4001234500035", "Premium Heavyweight Tee", "tshirts", 85000, 48000, 22, true},
        {"TS-004", "4001234500042", "Oversized Streetwear Tee", "tshirts", 90000, 52000, 18, true},
        {"MG-001", "4001234500059", "Ceramic Mug — 11oz", "mugs-bottles", 45000, 22000, 50, true},
        {"MG-002", "4001234500066", "Enamel Camping Mug", "mugs-bottles", 60000, 34000, 25, true},
        {"MG-003", "4001234500073", "Insulated Travel Tumbler", "mugs-bottles", 120000, 70000, 15, true},
        {"AC-001", "4001234500080", "Silicone Wristband", "accessories", 15000, 5000, 200, true},
        {"AC-002", "4001234500097", "Fabric Wristband", "accessories", 20000, 8000, 150, true},
        {"AC-003", "4001234500103", "Canvas Tote Bag", "accessories", 55000, 28000, 30, true},
        {"AC-004", "4001234500110", "Embroidered Cap", "accessories", 70000, 40000, 20, true},
        {"SV-001", "", "Custom Print Run — per design", "services", 25000, 0, 0, false},
        {"SV-002", "", "Artwork & Branding Setup", "services", 100000, 0, 0, false},
}

var seedUsers = []struct {
        username, fullName, password, pin, role string
}{
        {"admin", "System Admin", "admin123", "1234", "Admin"},
        {"cashier", "Cashier Demo", "cashier123", "2222", "Cashier"},
        {"designer", "Designer Demo", "designer123", "3333", "Designer"},
}

// Seed idempotently inserts defaults (roles, demo users, catalog, settings).
// It never overwrites existing rows — safe on every boot.
func (d *DB) Seed(demoData bool) error {
        if err := d.seedSettings(); err != nil {
                return fmt.Errorf("seed settings: %w", err)
        }
        if err := d.seedRoles(); err != nil {
                return fmt.Errorf("seed roles: %w", err)
        }
        if err := d.seedUsers(); err != nil {
                return fmt.Errorf("seed users: %w", err)
        }
        if demoData {
                if err := d.seedCatalog(); err != nil {
                        return fmt.Errorf("seed catalog: %w", err)
                }
        }
        return nil
}

func (d *DB) seedSettings() error {
        ins := d.Rebind("INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING")
        for k, v := range DefaultSettings {
                if _, err := d.Exec(ins, k, v); err != nil {
                        return fmt.Errorf("setting %s: %w", k, err)
                }
        }
        // Random JWT secret on first boot (never hardcoded — fixes the
        // poinf-of-sales flaw). Persisted so restarts keep sessions valid.
        var secret string
        err := d.QueryRow(`SELECT value FROM settings WHERE key = 'jwt_secret'`).Scan(&secret)
        if err == sql.ErrNoRows || (err == nil && secret == "") {
                buf := make([]byte, 32)
                if _, err := rand.Read(buf); err != nil {
                        return err
                }
                if _, err := d.Exec(ins, "jwt_secret", hex.EncodeToString(buf)); err != nil {
                        return err
                }
        } else if err != nil {
                return err
        }
        return nil
}

// backfillRolePerms unions the seeded permission set into same-named
// system roles (v3 migration — runs once, so later admin edits are safe).
func backfillRolePerms(d *DB, tx *sql.Tx) error {
        rows, err := tx.Query(`SELECT id, name, permissions FROM roles WHERE is_system = 1`)
        if err != nil {
                return err
        }
        type roleRow struct {
                id    int
                name  string
                perms string
        }
        var list []roleRow
        for rows.Next() {
                var r roleRow
                if err := rows.Scan(&r.id, &r.name, &r.perms); err != nil {
                        rows.Close()
                        return err
                }
                list = append(list, r)
        }
        rows.Close()
        for _, r := range list {
                want, ok := models.SeededRolePermissions[r.name]
                if !ok {
                        continue
                }
                var have []string
                if err := json.Unmarshal([]byte(r.perms), &have); err != nil {
                        have = nil
                }
                set := map[string]bool{}
                for _, p := range have {
                        set[p] = true
                }
                changed := false
                for _, p := range want {
                        if !set[p] {
                                set[p] = true
                                have = append(have, p)
                                changed = true
                        }
                }
                if !changed {
                        continue
                }
                merged, _ := json.Marshal(have)
                if _, err := tx.Exec(d.Rebind(`UPDATE roles SET permissions = ? WHERE id = ?`), string(merged), r.id); err != nil {
                        return err
                }
        }
        return nil
}

func (d *DB) seedRoles() error {
        for name, perms := range models.SeededRolePermissions {
                jsonPerms, _ := json.Marshal(perms)
                desc := map[string]string{
                        "Admin":    "Full access to every module",
                        "Cashier":  "Point of sale: checkout, manual M-Pesa entry, shifts",
                        "Designer": "Design & production board, catalog visibility",
                }[name]
                q := d.Rebind(`INSERT INTO roles (name, description, is_system, permissions)
                        SELECT ?, ?, 1, ? WHERE NOT EXISTS (SELECT 1 FROM roles WHERE name = ?)`)
                if _, err := d.Exec(q, name, desc, string(jsonPerms), name); err != nil {
                        return err
                }
        }
        return nil
}

func (d *DB) seedUsers() error {
        for _, u := range seedUsers {
                pwHash, err := hash.Password(u.password)
                if err != nil {
                        return err
                }
                pinHash, err := hash.Password(u.pin)
                if err != nil {
                        return err
                }
                q := d.Rebind(`INSERT INTO users (username, full_name, password_hash, pin_hash, role_id, must_rotate)
                        SELECT ?, ?, ?, ?, (SELECT id FROM roles WHERE name = ?), 1
                        WHERE NOT EXISTS (SELECT 1 FROM users WHERE username = ?)`)
                if _, err := d.Exec(q, u.username, u.fullName, pwHash, pinHash, u.role, u.username); err != nil {
                        return err
                }
        }
        return nil
}

func (d *DB) seedCatalog() error {
        insCat := d.Rebind(`INSERT INTO categories (name, slug, sort_order)
                SELECT ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM categories WHERE slug = ?)`)
        for i, c := range seedCategories {
                if _, err := d.Exec(insCat, c.name, c.slug, i, c.slug); err != nil {
                        return err
                }
        }
        // category_id resolved via subquery on slug (the v1 bug fix — never
        // assume a specific autoincrement value).
        insProd := d.Rebind(`INSERT INTO products (sku, barcode, name, category_id, price_cents, cost_cents, stock_qty, track_stock)
                SELECT ?, ?, ?, (SELECT id FROM categories WHERE slug = ?), ?, ?, ?, ?
                WHERE NOT EXISTS (SELECT 1 FROM products WHERE sku = ?)`)
        for _, p := range seedProducts {
                track := 0
                if p.track {
                        track = 1
                }
                if _, err := d.Exec(insProd, p.sku, p.barcode, p.name, p.category, p.price, p.cost, p.stock, track, p.sku); err != nil {
                        return err
                }
        }
        return nil
}
