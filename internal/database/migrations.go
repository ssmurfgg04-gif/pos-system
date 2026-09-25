package database

import (
        "database/sql"
        "fmt"
        "strings"
)

// Migration is one schema step. SQL is split per dialect when needed; Go
// covers data backfills that SQL can't express portably (JSON permission
// merges). A migration may carry SQL, a Go hook, or both.
type Migration struct {
        Version int
        SQLite  string
        Pg      string // falls back to SQLite body when empty
        Go      func(d *DB, tx *sql.Tx) error
}

var migrations = []Migration{
        {
                Version: 1,
                SQLite: `
CREATE TABLE IF NOT EXISTS settings (
        key TEXT PRIMARY KEY,
        value TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS roles (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        name TEXT NOT NULL UNIQUE,
        description TEXT NOT NULL DEFAULT '',
        is_system INTEGER NOT NULL DEFAULT 0,
        permissions TEXT NOT NULL DEFAULT '[]',
        created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS users (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        username TEXT NOT NULL UNIQUE,
        full_name TEXT NOT NULL DEFAULT '',
        password_hash TEXT NOT NULL DEFAULT '',
        pin_hash TEXT NOT NULL DEFAULT '',
        role_id INTEGER NOT NULL REFERENCES roles(id),
        is_active INTEGER NOT NULL DEFAULT 1,
        failed_pin_attempts INTEGER NOT NULL DEFAULT 0,
        pin_locked_until TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS categories (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        name TEXT NOT NULL UNIQUE,
        slug TEXT NOT NULL UNIQUE,
        sort_order INTEGER NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS products (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        sku TEXT NOT NULL UNIQUE,
        barcode TEXT NOT NULL DEFAULT '',
        name TEXT NOT NULL,
        category_id INTEGER NOT NULL REFERENCES categories(id),
        price_cents INTEGER NOT NULL DEFAULT 0,
        cost_cents INTEGER NOT NULL DEFAULT 0,
        stock_qty INTEGER NOT NULL DEFAULT 0,
        track_stock INTEGER NOT NULL DEFAULT 1,
        is_active INTEGER NOT NULL DEFAULT 1,
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_products_barcode ON products(barcode);
CREATE INDEX IF NOT EXISTS idx_products_category ON products(category_id);
CREATE TABLE IF NOT EXISTS orders (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        number TEXT NOT NULL UNIQUE,
        status TEXT NOT NULL DEFAULT 'PENDING',
        subtotal_cents INTEGER NOT NULL DEFAULT 0,
        tax_cents INTEGER NOT NULL DEFAULT 0,
        total_cents INTEGER NOT NULL DEFAULT 0,
        cashier_id INTEGER NOT NULL REFERENCES users(id),
        customer_name TEXT NOT NULL DEFAULT '',
        note TEXT NOT NULL DEFAULT '',
        client_uuid TEXT NOT NULL DEFAULT '',
        discrepancy INTEGER NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        paid_at TEXT NOT NULL DEFAULT '',
        voided_at TEXT NOT NULL DEFAULT '',
        void_reason TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_client_uuid ON orders(client_uuid) WHERE client_uuid != '';
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status);
CREATE INDEX IF NOT EXISTS idx_orders_created ON orders(created_at);
CREATE INDEX IF NOT EXISTS idx_orders_cashier ON orders(cashier_id);
CREATE TABLE IF NOT EXISTS order_items (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
        product_id INTEGER NOT NULL REFERENCES products(id),
        name TEXT NOT NULL,
        sku TEXT NOT NULL DEFAULT '',
        qty INTEGER NOT NULL,
        unit_price_cents INTEGER NOT NULL,
        line_total_cents INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_items_order ON order_items(order_id);
CREATE INDEX IF NOT EXISTS idx_items_product ON order_items(product_id);
CREATE TABLE IF NOT EXISTS payments (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
        method TEXT NOT NULL,
        mode TEXT NOT NULL DEFAULT '',
        amount_cents INTEGER NOT NULL,
        status TEXT NOT NULL DEFAULT 'PENDING',
        phone TEXT NOT NULL DEFAULT '',
        mpesa_receipt TEXT NOT NULL DEFAULT '',
        checkout_request_id TEXT NOT NULL DEFAULT '',
        merchant_request_id TEXT NOT NULL DEFAULT '',
        result_desc TEXT NOT NULL DEFAULT '',
        discrepancy INTEGER NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        completed_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_receipt ON payments(mpesa_receipt) WHERE mpesa_receipt != '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_checkout ON payments(checkout_request_id) WHERE checkout_request_id != '';
CREATE INDEX IF NOT EXISTS idx_payments_order ON payments(order_id);
CREATE INDEX IF NOT EXISTS idx_payments_status ON payments(status);
CREATE TABLE IF NOT EXISTS print_jobs (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
        status TEXT NOT NULL DEFAULT 'queued',
        target TEXT NOT NULL DEFAULT '',
        attempts INTEGER NOT NULL DEFAULT 0,
        last_error TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        printed_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_print_status ON print_jobs(status);
CREATE TABLE IF NOT EXISTS shifts (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        user_id INTEGER NOT NULL REFERENCES users(id),
        opening_float_cents INTEGER NOT NULL DEFAULT 0,
        expected_cents INTEGER NOT NULL DEFAULT 0,
        counted_cents INTEGER NOT NULL DEFAULT 0,
        variance_cents INTEGER NOT NULL DEFAULT 0,
        opened_at TEXT NOT NULL DEFAULT (datetime('now')),
        closed_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_shifts_user ON shifts(user_id);
CREATE TABLE IF NOT EXISTS design_jobs (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        title TEXT NOT NULL,
        product_name TEXT NOT NULL DEFAULT '',
        customer_name TEXT NOT NULL DEFAULT '',
        notes TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL DEFAULT 'queue',
        assignee_id INTEGER REFERENCES users(id),
        created_by TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_design_status ON design_jobs(status);
CREATE TABLE IF NOT EXISTS audit_log (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        user_id INTEGER NOT NULL DEFAULT 0,
        username TEXT NOT NULL DEFAULT '',
        action TEXT NOT NULL,
        entity TEXT NOT NULL DEFAULT '',
        entity_id TEXT NOT NULL DEFAULT '',
        details TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);
`,
                Pg: `
CREATE TABLE IF NOT EXISTS settings (
        key VARCHAR(128) PRIMARY KEY,
        value TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS roles (
        id SERIAL PRIMARY KEY,
        name VARCHAR(64) NOT NULL UNIQUE,
        description TEXT NOT NULL DEFAULT '',
        is_system INTEGER NOT NULL DEFAULT 0,
        permissions TEXT NOT NULL DEFAULT '[]',
        created_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS users (
        id SERIAL PRIMARY KEY,
        username VARCHAR(64) NOT NULL UNIQUE,
        full_name TEXT NOT NULL DEFAULT '',
        password_hash TEXT NOT NULL DEFAULT '',
        pin_hash TEXT NOT NULL DEFAULT '',
        role_id INTEGER NOT NULL REFERENCES roles(id),
        is_active INTEGER NOT NULL DEFAULT 1,
        failed_pin_attempts INTEGER NOT NULL DEFAULT 0,
        pin_locked_until TEXT NOT NULL DEFAULT '',
        created_at TIMESTAMP NOT NULL DEFAULT NOW(),
        updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS categories (
        id SERIAL PRIMARY KEY,
        name VARCHAR(128) NOT NULL UNIQUE,
        slug VARCHAR(128) NOT NULL UNIQUE,
        sort_order INTEGER NOT NULL DEFAULT 0,
        created_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS products (
        id SERIAL PRIMARY KEY,
        sku VARCHAR(64) NOT NULL UNIQUE,
        barcode VARCHAR(64) NOT NULL DEFAULT '',
        name TEXT NOT NULL,
        category_id INTEGER NOT NULL REFERENCES categories(id),
        price_cents BIGINT NOT NULL DEFAULT 0,
        cost_cents BIGINT NOT NULL DEFAULT 0,
        stock_qty INTEGER NOT NULL DEFAULT 0,
        track_stock INTEGER NOT NULL DEFAULT 1,
        is_active INTEGER NOT NULL DEFAULT 1,
        created_at TIMESTAMP NOT NULL DEFAULT NOW(),
        updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_products_barcode ON products(barcode);
CREATE INDEX IF NOT EXISTS idx_products_category ON products(category_id);
CREATE TABLE IF NOT EXISTS orders (
        id SERIAL PRIMARY KEY,
        number VARCHAR(32) NOT NULL UNIQUE,
        status VARCHAR(16) NOT NULL DEFAULT 'PENDING',
        subtotal_cents BIGINT NOT NULL DEFAULT 0,
        tax_cents BIGINT NOT NULL DEFAULT 0,
        total_cents BIGINT NOT NULL DEFAULT 0,
        cashier_id INTEGER NOT NULL REFERENCES users(id),
        customer_name TEXT NOT NULL DEFAULT '',
        note TEXT NOT NULL DEFAULT '',
        client_uuid VARCHAR(64) NOT NULL DEFAULT '',
        discrepancy INTEGER NOT NULL DEFAULT 0,
        created_at TIMESTAMP NOT NULL DEFAULT NOW(),
        paid_at TEXT NOT NULL DEFAULT '',
        voided_at TEXT NOT NULL DEFAULT '',
        void_reason TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_orders_client_uuid ON orders(client_uuid) WHERE client_uuid != '';
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status);
CREATE INDEX IF NOT EXISTS idx_orders_created ON orders(created_at);
CREATE INDEX IF NOT EXISTS idx_orders_cashier ON orders(cashier_id);
CREATE TABLE IF NOT EXISTS order_items (
        id SERIAL PRIMARY KEY,
        order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
        product_id INTEGER NOT NULL REFERENCES products(id),
        name TEXT NOT NULL,
        sku VARCHAR(64) NOT NULL DEFAULT '',
        qty INTEGER NOT NULL,
        unit_price_cents BIGINT NOT NULL,
        line_total_cents BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_items_order ON order_items(order_id);
CREATE INDEX IF NOT EXISTS idx_items_product ON order_items(product_id);
CREATE TABLE IF NOT EXISTS payments (
        id SERIAL PRIMARY KEY,
        order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
        method VARCHAR(16) NOT NULL,
        mode VARCHAR(16) NOT NULL DEFAULT '',
        amount_cents BIGINT NOT NULL,
        status VARCHAR(16) NOT NULL DEFAULT 'PENDING',
        phone VARCHAR(20) NOT NULL DEFAULT '',
        mpesa_receipt VARCHAR(16) NOT NULL DEFAULT '',
        checkout_request_id VARCHAR(64) NOT NULL DEFAULT '',
        merchant_request_id VARCHAR(64) NOT NULL DEFAULT '',
        result_desc TEXT NOT NULL DEFAULT '',
        discrepancy INTEGER NOT NULL DEFAULT 0,
        created_at TIMESTAMP NOT NULL DEFAULT NOW(),
        completed_at TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_receipt ON payments(mpesa_receipt) WHERE mpesa_receipt != '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_payments_checkout ON payments(checkout_request_id) WHERE checkout_request_id != '';
CREATE INDEX IF NOT EXISTS idx_payments_order ON payments(order_id);
CREATE INDEX IF NOT EXISTS idx_payments_status ON payments(status);
CREATE TABLE IF NOT EXISTS print_jobs (
        id SERIAL PRIMARY KEY,
        order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
        status VARCHAR(16) NOT NULL DEFAULT 'queued',
        target TEXT NOT NULL DEFAULT '',
        attempts INTEGER NOT NULL DEFAULT 0,
        last_error TEXT NOT NULL DEFAULT '',
        created_at TIMESTAMP NOT NULL DEFAULT NOW(),
        printed_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_print_status ON print_jobs(status);
CREATE TABLE IF NOT EXISTS shifts (
        id SERIAL PRIMARY KEY,
        user_id INTEGER NOT NULL REFERENCES users(id),
        opening_float_cents BIGINT NOT NULL DEFAULT 0,
        expected_cents BIGINT NOT NULL DEFAULT 0,
        counted_cents BIGINT NOT NULL DEFAULT 0,
        variance_cents BIGINT NOT NULL DEFAULT 0,
        opened_at TIMESTAMP NOT NULL DEFAULT NOW(),
        closed_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_shifts_user ON shifts(user_id);
CREATE TABLE IF NOT EXISTS design_jobs (
        id SERIAL PRIMARY KEY,
        title TEXT NOT NULL,
        product_name TEXT NOT NULL DEFAULT '',
        customer_name TEXT NOT NULL DEFAULT '',
        notes TEXT NOT NULL DEFAULT '',
        status VARCHAR(16) NOT NULL DEFAULT 'queue',
        assignee_id INTEGER REFERENCES users(id),
        created_by TEXT NOT NULL DEFAULT '',
        created_at TIMESTAMP NOT NULL DEFAULT NOW(),
        updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_design_status ON design_jobs(status);
CREATE TABLE IF NOT EXISTS audit_log (
        id SERIAL PRIMARY KEY,
        user_id INTEGER NOT NULL DEFAULT 0,
        username TEXT NOT NULL DEFAULT '',
        action TEXT NOT NULL,
        entity TEXT NOT NULL DEFAULT '',
        entity_id TEXT NOT NULL DEFAULT '',
        details TEXT NOT NULL DEFAULT '',
        created_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);
`,
        },
        {
                Version: 2,
                SQLite: `
CREATE TABLE IF NOT EXISTS customers (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        name TEXT NOT NULL,
        phone TEXT NOT NULL DEFAULT '',
        credit_limit_cents INTEGER NOT NULL DEFAULT 0,
        loyalty_points INTEGER NOT NULL DEFAULT 0,
        balance_cents INTEGER NOT NULL DEFAULT 0,
        is_active INTEGER NOT NULL DEFAULT 1,
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_customers_name ON customers(name);
CREATE INDEX IF NOT EXISTS idx_customers_phone ON customers(phone);
CREATE TABLE IF NOT EXISTS customer_ledger (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        customer_id INTEGER NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
        order_id INTEGER NOT NULL DEFAULT 0,
        kind TEXT NOT NULL,
        amount_cents INTEGER NOT NULL DEFAULT 0,
        points_delta INTEGER NOT NULL DEFAULT 0,
        note TEXT NOT NULL DEFAULT '',
        created_by INTEGER NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_ledger_customer ON customer_ledger(customer_id);
ALTER TABLE orders ADD COLUMN customer_id INTEGER NOT NULL DEFAULT 0;
`,
                Pg: `
CREATE TABLE IF NOT EXISTS customers (
        id SERIAL PRIMARY KEY,
        name TEXT NOT NULL,
        phone TEXT NOT NULL DEFAULT '',
        credit_limit_cents BIGINT NOT NULL DEFAULT 0,
        loyalty_points INTEGER NOT NULL DEFAULT 0,
        balance_cents BIGINT NOT NULL DEFAULT 0,
        is_active INTEGER NOT NULL DEFAULT 1,
        created_at TIMESTAMP NOT NULL DEFAULT NOW(),
        updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_customers_name ON customers(name);
CREATE INDEX IF NOT EXISTS idx_customers_phone ON customers(phone);
CREATE TABLE IF NOT EXISTS customer_ledger (
        id SERIAL PRIMARY KEY,
        customer_id INTEGER NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
        order_id INTEGER NOT NULL DEFAULT 0,
        kind TEXT NOT NULL,
        amount_cents BIGINT NOT NULL DEFAULT 0,
        points_delta INTEGER NOT NULL DEFAULT 0,
        note TEXT NOT NULL DEFAULT '',
        created_by INTEGER NOT NULL DEFAULT 0,
        created_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_ledger_customer ON customer_ledger(customer_id);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS customer_id INTEGER NOT NULL DEFAULT 0;
`,
        },
        {
                // v3: shops created before tabs existed have system roles without
                // the customers.* permissions — union the seeded set in so
                // cashiers keep working after upgrade. Runs once (admin edits
                // made afterwards are never touched).
                Version: 3,
                Go:      backfillRolePerms,
        },
        {
                // v4: forced credential rotation — seeded defaults stop working
                // until changed. Existing rows are flagged so every current user
                // rotates once on next login.
                Version: 4,
                SQLite: `
ALTER TABLE users ADD COLUMN must_rotate INTEGER NOT NULL DEFAULT 0;
UPDATE users SET must_rotate = 1;
`,
                Pg: `
ALTER TABLE users ADD COLUMN IF NOT EXISTS must_rotate INTEGER NOT NULL DEFAULT 0;
UPDATE users SET must_rotate = 1;
`,
        },
        {
                // v5: suppliers & stock-in — supplier records, purchase orders
                // (receive posts stock + weighted-average cost), stock takes.
                // Also re-runs the role backfill so upgraded Admins gain the new
                // suppliers.* permissions (same once-only union semantics as v3).
                Version: 5,
                Go:      backfillRolePerms,
                SQLite: `
CREATE TABLE IF NOT EXISTS suppliers (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        name TEXT NOT NULL,
        phone TEXT NOT NULL DEFAULT '',
        email TEXT NOT NULL DEFAULT '',
        address TEXT NOT NULL DEFAULT '',
        notes TEXT NOT NULL DEFAULT '',
        is_active INTEGER NOT NULL DEFAULT 1,
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_suppliers_name ON suppliers(name);
CREATE TABLE IF NOT EXISTS purchase_orders (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        number TEXT NOT NULL UNIQUE,
        supplier_id INTEGER NOT NULL REFERENCES suppliers(id),
        status TEXT NOT NULL DEFAULT 'PENDING',
        subtotal_cents INTEGER NOT NULL DEFAULT 0,
        note TEXT NOT NULL DEFAULT '',
        created_by INTEGER NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        received_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS purchase_order_items (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        po_id INTEGER NOT NULL REFERENCES purchase_orders(id) ON DELETE CASCADE,
        product_id INTEGER NOT NULL REFERENCES products(id),
        name TEXT NOT NULL DEFAULT '',
        sku TEXT NOT NULL DEFAULT '',
        qty INTEGER NOT NULL DEFAULT 0,
        cost_cents INTEGER NOT NULL DEFAULT 0,
        line_total_cents INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS stock_takes (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        number TEXT NOT NULL UNIQUE,
        status TEXT NOT NULL DEFAULT 'OPEN',
        note TEXT NOT NULL DEFAULT '',
        created_by INTEGER NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        applied_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS stock_take_items (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        take_id INTEGER NOT NULL REFERENCES stock_takes(id) ON DELETE CASCADE,
        product_id INTEGER NOT NULL REFERENCES products(id),
        name TEXT NOT NULL DEFAULT '',
        sku TEXT NOT NULL DEFAULT '',
        expected_qty INTEGER NOT NULL DEFAULT 0,
        counted_qty INTEGER NOT NULL DEFAULT 0
);
`,
                Pg: `
CREATE TABLE IF NOT EXISTS suppliers (
        id SERIAL PRIMARY KEY,
        name TEXT NOT NULL,
        phone TEXT NOT NULL DEFAULT '',
        email TEXT NOT NULL DEFAULT '',
        address TEXT NOT NULL DEFAULT '',
        notes TEXT NOT NULL DEFAULT '',
        is_active INTEGER NOT NULL DEFAULT 1,
        created_at TIMESTAMP NOT NULL DEFAULT NOW(),
        updated_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_suppliers_name ON suppliers(name);
CREATE TABLE IF NOT EXISTS purchase_orders (
        id SERIAL PRIMARY KEY,
        number TEXT NOT NULL UNIQUE,
        supplier_id INTEGER NOT NULL REFERENCES suppliers(id),
        status TEXT NOT NULL DEFAULT 'PENDING',
        subtotal_cents BIGINT NOT NULL DEFAULT 0,
        note TEXT NOT NULL DEFAULT '',
        created_by INTEGER NOT NULL DEFAULT 0,
        created_at TIMESTAMP NOT NULL DEFAULT NOW(),
        received_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS purchase_order_items (
        id SERIAL PRIMARY KEY,
        po_id INTEGER NOT NULL REFERENCES purchase_orders(id) ON DELETE CASCADE,
        product_id INTEGER NOT NULL REFERENCES products(id),
        name TEXT NOT NULL DEFAULT '',
        sku TEXT NOT NULL DEFAULT '',
        qty INTEGER NOT NULL DEFAULT 0,
        cost_cents BIGINT NOT NULL DEFAULT 0,
        line_total_cents BIGINT NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS stock_takes (
        id SERIAL PRIMARY KEY,
        number TEXT NOT NULL UNIQUE,
        status TEXT NOT NULL DEFAULT 'OPEN',
        note TEXT NOT NULL DEFAULT '',
        created_by INTEGER NOT NULL DEFAULT 0,
        created_at TIMESTAMP NOT NULL DEFAULT NOW(),
        applied_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS stock_take_items (
        id SERIAL PRIMARY KEY,
        take_id INTEGER NOT NULL REFERENCES stock_takes(id) ON DELETE CASCADE,
        product_id INTEGER NOT NULL REFERENCES products(id),
        name TEXT NOT NULL DEFAULT '',
        sku TEXT NOT NULL DEFAULT '',
                expected_qty INTEGER NOT NULL DEFAULT 0,
                counted_qty INTEGER NOT NULL DEFAULT 0
);
`,
        },
        {
                // v6: token invalidation on credential change — sessions issued
                // before the last password/PIN change stop working. Empty means
                // pre-feature (existing sessions survive the upgrade once).
                Version: 6,
                SQLite: `
ALTER TABLE users ADD COLUMN password_changed_at TEXT NOT NULL DEFAULT '';
`,
                Pg: `
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_changed_at TEXT NOT NULL DEFAULT '';
`,
        },
        {
                // v7: VAT historization — store tax percent + inclusive flag per order
                // so reports never recalculate with current settings (per DBA best
                // practice: store calculated values at posting time).
                Version: 7,
                SQLite: `
ALTER TABLE orders ADD COLUMN tax_percent REAL NOT NULL DEFAULT 16;
ALTER TABLE orders ADD COLUMN tax_included INTEGER NOT NULL DEFAULT 1;
UPDATE orders SET tax_percent = 16 WHERE tax_percent = 16;
`,
                Pg: `
ALTER TABLE orders ADD COLUMN IF NOT EXISTS tax_percent DOUBLE PRECISION NOT NULL DEFAULT 16;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS tax_included INTEGER NOT NULL DEFAULT 1;
`,
        },
        {
                // v8: retail expansion pack —
                //   product photos (image_url), order discounts + loyalty
                //   redemption audit fields, store credit (prepaid money the
                //   shop owes the customer), parked/held sales, a void-reason
                //   catalog, design-job file attachments, per-role dashboard
                //   config (landing page + visible nav), and the team-sync
                //   outbox/state tables that link standalone tills through
                //   the shop's Supabase project.
                Version: 8,
                SQLite: `
ALTER TABLE products ADD COLUMN image_url TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN discount_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN points_redeemed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN discount_label TEXT NOT NULL DEFAULT '';
ALTER TABLE customers ADD COLUMN store_credit_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE payments ADD COLUMN email TEXT NOT NULL DEFAULT ''; -- paystack: address the charge was opened for
CREATE TABLE IF NOT EXISTS held_sales (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        ref_name TEXT NOT NULL DEFAULT '',
        items_json TEXT NOT NULL,
        customer_id INTEGER NOT NULL DEFAULT 0,
        customer_name TEXT NOT NULL DEFAULT '',
        note TEXT NOT NULL DEFAULT '',
        device_id TEXT NOT NULL DEFAULT '',
        created_by INTEGER NOT NULL DEFAULT 0,
        created_by_name TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS void_reasons (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        label TEXT NOT NULL UNIQUE,
        is_active INTEGER NOT NULL DEFAULT 1,
        sort_order INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO void_reasons (label, sort_order) VALUES
        ('Wrong item', 1),
        ('Customer changed mind', 2),
        ('Duplicate order', 3),
        ('Price dispute', 4),
        ('Out of stock', 5),
        ('Training / test', 6);
CREATE TABLE IF NOT EXISTS design_files (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        job_id INTEGER NOT NULL REFERENCES design_jobs(id) ON DELETE CASCADE,
        filename TEXT NOT NULL,
        mime TEXT NOT NULL DEFAULT '',
        size INTEGER NOT NULL DEFAULT 0,
        data BLOB NOT NULL,
        uploaded_by INTEGER NOT NULL DEFAULT 0,
        uploaded_by_name TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_design_files_job ON design_files(job_id);
ALTER TABLE roles ADD COLUMN home_page TEXT NOT NULL DEFAULT '';
ALTER TABLE roles ADD COLUMN dashboard_config TEXT NOT NULL DEFAULT '{}';
CREATE TABLE IF NOT EXISTS sync_outbox (
        seq INTEGER PRIMARY KEY AUTOINCREMENT,
        entity TEXT NOT NULL,
        op TEXT NOT NULL DEFAULT 'upsert',
        client_uuid TEXT NOT NULL UNIQUE,
        payload TEXT NOT NULL,
        pushed_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_outbox_pending ON sync_outbox(pushed_at);
CREATE TABLE IF NOT EXISTS sync_state (
        key TEXT PRIMARY KEY,
        value TEXT NOT NULL DEFAULT ''
);
`,
                Pg: `
ALTER TABLE products ADD COLUMN IF NOT EXISTS image_url TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS discount_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS points_redeemed BIGINT NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS discount_label TEXT NOT NULL DEFAULT '';
ALTER TABLE customers ADD COLUMN IF NOT EXISTS store_credit_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE payments ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS held_sales (
        id SERIAL PRIMARY KEY,
        ref_name TEXT NOT NULL DEFAULT '',
        items_json TEXT NOT NULL,
        customer_id BIGINT NOT NULL DEFAULT 0,
        customer_name TEXT NOT NULL DEFAULT '',
        note TEXT NOT NULL DEFAULT '',
        device_id TEXT NOT NULL DEFAULT '',
        created_by BIGINT NOT NULL DEFAULT 0,
        created_by_name TEXT NOT NULL DEFAULT '',
        created_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS void_reasons (
        id SERIAL PRIMARY KEY,
        label TEXT NOT NULL UNIQUE,
        is_active INTEGER NOT NULL DEFAULT 1,
        sort_order INTEGER NOT NULL DEFAULT 0
);
INSERT INTO void_reasons (label, sort_order) VALUES
        ('Wrong item', 1), ('Customer changed mind', 2), ('Duplicate order', 3),
        ('Price dispute', 4), ('Out of stock', 5), ('Training / test', 6)
ON CONFLICT (label) DO NOTHING;
CREATE TABLE IF NOT EXISTS design_files (
        id SERIAL PRIMARY KEY,
        job_id INTEGER NOT NULL REFERENCES design_jobs(id) ON DELETE CASCADE,
        filename TEXT NOT NULL,
        mime TEXT NOT NULL DEFAULT '',
        size BIGINT NOT NULL DEFAULT 0,
        data BYTEA NOT NULL,
        uploaded_by BIGINT NOT NULL DEFAULT 0,
        uploaded_by_name TEXT NOT NULL DEFAULT '',
        created_at TIMESTAMP NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_design_files_job ON design_files(job_id);
ALTER TABLE roles ADD COLUMN IF NOT EXISTS home_page TEXT NOT NULL DEFAULT '';
ALTER TABLE roles ADD COLUMN IF NOT EXISTS dashboard_config TEXT NOT NULL DEFAULT '{}';
CREATE TABLE IF NOT EXISTS sync_outbox (
        seq BIGSERIAL PRIMARY KEY,
        entity TEXT NOT NULL,
        op TEXT NOT NULL DEFAULT 'upsert',
        client_uuid TEXT NOT NULL UNIQUE,
        payload TEXT NOT NULL,
        pushed_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_outbox_pending ON sync_outbox(pushed_at);
CREATE TABLE IF NOT EXISTS sync_state (
        key TEXT PRIMARY KEY,
        value TEXT NOT NULL DEFAULT ''
);
`,
        },
        {
                // v9: union the new retail permissions into existing roles'
                // saved sets (Admin gains everything new; Cashier gains
                // discount + loyalty redemption + credit as befits a till).
                // Same once-only semantics as v3/v5: admin edits after this
                // run are never touched.
                Version: 9,
                Go:      backfillRolePermsV9,
        },
        {
                // v10: stocktake (count sessions with variance report) and
                // gift cards (sellable products that mint redeemable codes
                // loading prepaid store credit on a customer account).
                Version: 10,
                SQLite: `
CREATE TABLE IF NOT EXISTS stock_counts (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        number TEXT NOT NULL UNIQUE,
        status TEXT NOT NULL DEFAULT 'OPEN',
        note TEXT NOT NULL DEFAULT '',
        counted_by INTEGER NOT NULL DEFAULT 0,
        counted_by_name TEXT NOT NULL DEFAULT '',
        opened_at TEXT NOT NULL DEFAULT '',
        closed_at TEXT NOT NULL DEFAULT '',
        lines_total INTEGER NOT NULL DEFAULT 0,
        lines_counted INTEGER NOT NULL DEFAULT 0,
        variance_units INTEGER NOT NULL DEFAULT 0,
        variance_value_cents INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS stock_count_lines (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        count_id INTEGER NOT NULL REFERENCES stock_counts(id) ON DELETE CASCADE,
        product_id INTEGER NOT NULL,
        sku TEXT NOT NULL DEFAULT '',
        name TEXT NOT NULL DEFAULT '',
        expected_qty INTEGER NOT NULL DEFAULT 0,
        counted_qty INTEGER,
        system_qty INTEGER NOT NULL DEFAULT 0,
        unit_cost_cents INTEGER NOT NULL DEFAULT 0,
        applied INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_stock_count_lines_count ON stock_count_lines(count_id);
ALTER TABLE products ADD COLUMN is_gift_card INTEGER NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS gift_cards (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        code TEXT NOT NULL UNIQUE,
        order_id INTEGER NOT NULL DEFAULT 0,
        initial_cents INTEGER NOT NULL DEFAULT 0,
        remaining_cents INTEGER NOT NULL DEFAULT 0,
        status TEXT NOT NULL DEFAULT 'ACTIVE',
        issued_at TEXT NOT NULL DEFAULT '',
        redeemed_at TEXT NOT NULL DEFAULT '',
        redeemed_by_customer INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_gift_cards_code ON gift_cards(code);
`,
                Pg: `
CREATE TABLE IF NOT EXISTS stock_counts (
        id SERIAL PRIMARY KEY,
        number TEXT NOT NULL UNIQUE,
        status TEXT NOT NULL DEFAULT 'OPEN',
        note TEXT NOT NULL DEFAULT '',
        counted_by BIGINT NOT NULL DEFAULT 0,
        counted_by_name TEXT NOT NULL DEFAULT '',
        opened_at TEXT NOT NULL DEFAULT '',
        closed_at TEXT NOT NULL DEFAULT '',
        lines_total BIGINT NOT NULL DEFAULT 0,
        lines_counted BIGINT NOT NULL DEFAULT 0,
        variance_units BIGINT NOT NULL DEFAULT 0,
        variance_value_cents BIGINT NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS stock_count_lines (
        id SERIAL PRIMARY KEY,
        count_id BIGINT NOT NULL REFERENCES stock_counts(id) ON DELETE CASCADE,
        product_id BIGINT NOT NULL,
        sku TEXT NOT NULL DEFAULT '',
        name TEXT NOT NULL DEFAULT '',
        expected_qty BIGINT NOT NULL DEFAULT 0,
        counted_qty BIGINT,
        system_qty BIGINT NOT NULL DEFAULT 0,
        unit_cost_cents BIGINT NOT NULL DEFAULT 0,
        applied INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_stock_count_lines_count ON stock_count_lines(count_id);
ALTER TABLE products ADD COLUMN IF NOT EXISTS is_gift_card INTEGER NOT NULL DEFAULT 0;
CREATE TABLE IF NOT EXISTS gift_cards (
        id SERIAL PRIMARY KEY,
        code TEXT NOT NULL UNIQUE,
        order_id BIGINT NOT NULL DEFAULT 0,
        initial_cents BIGINT NOT NULL DEFAULT 0,
        remaining_cents BIGINT NOT NULL DEFAULT 0,
        status TEXT NOT NULL DEFAULT 'ACTIVE',
        issued_at TEXT NOT NULL DEFAULT '',
        redeemed_at TEXT NOT NULL DEFAULT '',
        redeemed_by_customer BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_gift_cards_code ON gift_cards(code);
`,
        },
}

// backfillRolePermsV9 unions the v8 permission additions into seeded roles:
// Admin gets all, Cashier gets the till-side additions (discount, loyalty
// redemption, store credit), Designer is unchanged.
func backfillRolePermsV9(d *DB, tx *sql.Tx) error {
        extra := map[string][]string{
                "Admin":   {"payments.apply_discount", "loyalty.redeem", "credit.manage"},
                "Cashier": {"payments.apply_discount", "loyalty.redeem", "credit.manage"},
        }
        return unionRolePerms(d, tx, extra)
}

// Migrate applies pending migrations in order.
func (d *DB) Migrate() error {
        if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
                version INTEGER PRIMARY KEY,
                applied_at TEXT NOT NULL DEFAULT (datetime('now'))
        )`); err != nil {
                return fmt.Errorf("create schema_migrations: %w", err)
        }
        var current int
        if err := d.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
                return fmt.Errorf("read migration version: %w", err)
        }
        for _, m := range migrations {
                if m.Version <= current {
                        continue
                }
                body := m.SQLite
                if !d.IsSQLite() && m.Pg != "" {
                        body = m.Pg
                }
                tx, err := d.Begin()
                if err != nil {
                        return err
                }
                if strings.TrimSpace(body) != "" {
                        if _, err := tx.Exec(body); err != nil {
                                tx.Rollback()
                                return fmt.Errorf("migration %d: %w", m.Version, err)
                        }
                }
                if m.Go != nil {
                        if err := m.Go(d, tx); err != nil {
                                tx.Rollback()
                                return fmt.Errorf("migration %d: %w", m.Version, err)
                        }
                }
                if _, err := tx.Exec(d.Rebind(`INSERT INTO schema_migrations (version) VALUES (?)`), m.Version); err != nil {
                        tx.Rollback()
                        return fmt.Errorf("record migration %d: %w", m.Version, err)
                }
                if err := tx.Commit(); err != nil {
                        return err
                }
        }
        return nil
}
