package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"posapp/internal/hash"
	"posapp/internal/models"
)

// hashVerify wraps bcrypt verification for migration-time credential checks.
func hashVerify(h, plain string) bool {
	if h == "" {
		return false
	}
	return hash.Verify(h, plain)
}

func timeNowUTC() time.Time { return time.Now().UTC() }

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
	{
		// v11: performance indexes (composite cashier/status filters,
		// SKU lookups, NOCASE name ordering) + the fake-money guard:
		// tills upgraded from releases that defaulted mpesa_env to
		// "mock" move to "manual" so no payment can auto-complete
		// without a real provider (Paystack or Daraja credentials).
		Version: 11,
		SQLite: `
CREATE INDEX IF NOT EXISTS idx_orders_cashier_created ON orders(cashier_id, created_at);
CREATE INDEX IF NOT EXISTS idx_orders_status_created ON orders(status, created_at);
CREATE INDEX IF NOT EXISTS idx_products_sku ON products(sku);
CREATE INDEX IF NOT EXISTS idx_products_name_nocase ON products(name COLLATE NOCASE);
UPDATE settings SET value = 'manual' WHERE key = 'mpesa_env' AND value = 'mock';
`,
		Pg: `
CREATE INDEX IF NOT EXISTS idx_orders_cashier_created ON orders(cashier_id, created_at);
CREATE INDEX IF NOT EXISTS idx_orders_status_created ON orders(status, created_at);
CREATE INDEX IF NOT EXISTS idx_products_sku ON products(sku);
CREATE INDEX IF NOT EXISTS idx_products_name_nocase ON products(name);
UPDATE settings SET value = 'manual' WHERE key = 'mpesa_env' AND value = 'mock';
`,
	},
	{
		// v12: sync hardening. Receiver-side exactly-once via
		// sync_applied (cursor regression / backup restore can no
		// longer double-apply stock deltas or ledger rows); a
		// dead-letter table quarantines events that cannot apply
		// (unknown product, unknown customer…) and outgoing poison
		// rows that wedge the push batch, so they surface in the
		// Team panel and can be retried instead of being lost.
		// sync_outbox gains attempts/last_error for quarantine book-
		// keeping (ADD COLUMN guarded for databases created pre-v12).
		Version: 12,
		SQLite: `
CREATE TABLE IF NOT EXISTS sync_applied (
        event_id INTEGER PRIMARY KEY,
        applied_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sync_applied_time ON sync_applied(applied_at);
CREATE TABLE IF NOT EXISTS sync_dead_letter (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        direction TEXT NOT NULL DEFAULT 'apply',
        event_id INTEGER NOT NULL DEFAULT 0,
        entity TEXT NOT NULL DEFAULT '',
        op TEXT NOT NULL DEFAULT '',
        client_uuid TEXT NOT NULL DEFAULT '',
        payload TEXT NOT NULL DEFAULT '',
        error TEXT NOT NULL DEFAULT '',
        attempts INTEGER NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL DEFAULT '',
        updated_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_dead_letter_direction ON sync_dead_letter(direction);
ALTER TABLE sync_outbox ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sync_outbox ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
`,
		Pg: `
CREATE TABLE IF NOT EXISTS sync_applied (
        event_id BIGINT PRIMARY KEY,
        applied_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sync_applied_time ON sync_applied(applied_at);
CREATE TABLE IF NOT EXISTS sync_dead_letter (
        id SERIAL PRIMARY KEY,
        direction TEXT NOT NULL DEFAULT 'apply',
        event_id BIGINT NOT NULL DEFAULT 0,
        entity TEXT NOT NULL DEFAULT '',
        op TEXT NOT NULL DEFAULT '',
        client_uuid TEXT NOT NULL DEFAULT '',
        payload TEXT NOT NULL DEFAULT '',
        error TEXT NOT NULL DEFAULT '',
        attempts BIGINT NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL DEFAULT '',
        updated_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_dead_letter_direction ON sync_dead_letter(direction);
ALTER TABLE sync_outbox ADD COLUMN IF NOT EXISTS attempts BIGINT NOT NULL DEFAULT 0;
ALTER TABLE sync_outbox ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';
`,
	},
	{
		// v13: authentication hardening.
		//
		// recovery_codes: single-use owner recovery codes (hashed at
		// rest) backing the public forgot-password flow and the
		// `ledgerpos reset-owner` escape hatch.
		//
		// Demo-account purge (Go step below, with a rollback plan):
		// the installer seeded admin/cashier/designer with PUBLIC
		// passwords (admin123…) into every database. Only accounts
		// whose password AND PIN still verify against the seed
		// values are disabled — a rotated account is owned by a real
		// person and is left untouched. Deactivation (not deletion)
		// keeps every historical sale, shift and audit row intact;
		// each disabled account is journalled in audit_log for the
		// rollback procedure (docs/MIGRATION-v13-ROLLBACK.md).
		Version: 13,
		SQLite: `
CREATE TABLE IF NOT EXISTS recovery_codes (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        code_hash TEXT NOT NULL,
        created_at TEXT NOT NULL DEFAULT '',
        used_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_recovery_codes_user ON recovery_codes(user_id);
`,
		Pg: `
CREATE TABLE IF NOT EXISTS recovery_codes (
        id SERIAL PRIMARY KEY,
        user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
        code_hash TEXT NOT NULL,
        created_at TEXT NOT NULL DEFAULT '',
        used_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_recovery_codes_user ON recovery_codes(user_id);
`,
		Go: purgeDemoUsersV13,
	},
	{
		// v14: business roles + the connected job lifecycle + CRM
		// enrichment.
		//
		// Jobs: design_jobs gains the sale link (order_id), the
		// promised deadline, a priority, and stage timestamps
		// (assigned/ready/collected) so a job's journey is auditable.
		// orders.assigned_to_id tracks the staff member responsible
		// for fulfilment. order_events is the per-order activity
		// timeline (job moves, WhatsApp contacts, notes) — the
		// "activity history" the field asked for.
		//
		// CRM: customers gains email + notes.
		Version: 14,
		SQLite: `
ALTER TABLE design_jobs ADD COLUMN order_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE design_jobs ADD COLUMN customer_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE design_jobs ADD COLUMN deadline_at TEXT NOT NULL DEFAULT '';
ALTER TABLE design_jobs ADD COLUMN priority TEXT NOT NULL DEFAULT 'normal';
ALTER TABLE design_jobs ADD COLUMN assigned_at TEXT NOT NULL DEFAULT '';
ALTER TABLE design_jobs ADD COLUMN ready_at TEXT NOT NULL DEFAULT '';
ALTER TABLE design_jobs ADD COLUMN collected_at TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_design_jobs_order ON design_jobs(order_id);
ALTER TABLE orders ADD COLUMN assigned_to_id INTEGER NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN notified_at TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS order_events (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        order_id INTEGER NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
        kind TEXT NOT NULL DEFAULT 'note',
        message TEXT NOT NULL DEFAULT '',
        user_id INTEGER NOT NULL DEFAULT 0,
        username TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_order_events_order ON order_events(order_id);
ALTER TABLE customers ADD COLUMN email TEXT NOT NULL DEFAULT '';
ALTER TABLE customers ADD COLUMN notes TEXT NOT NULL DEFAULT '';
`,
		Pg: `
ALTER TABLE design_jobs ADD COLUMN IF NOT EXISTS order_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE design_jobs ADD COLUMN IF NOT EXISTS customer_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE design_jobs ADD COLUMN IF NOT EXISTS deadline_at TEXT NOT NULL DEFAULT '';
ALTER TABLE design_jobs ADD COLUMN IF NOT EXISTS priority TEXT NOT NULL DEFAULT 'normal';
ALTER TABLE design_jobs ADD COLUMN IF NOT EXISTS assigned_at TEXT NOT NULL DEFAULT '';
ALTER TABLE design_jobs ADD COLUMN IF NOT EXISTS ready_at TEXT NOT NULL DEFAULT '';
ALTER TABLE design_jobs ADD COLUMN IF NOT EXISTS collected_at TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_design_jobs_order ON design_jobs(order_id);
ALTER TABLE orders ADD COLUMN IF NOT EXISTS assigned_to_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS notified_at TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS order_events (
        id SERIAL PRIMARY KEY,
        order_id BIGINT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
        kind TEXT NOT NULL DEFAULT 'note',
        message TEXT NOT NULL DEFAULT '',
        user_id BIGINT NOT NULL DEFAULT 0,
        username TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_order_events_order ON order_events(order_id);
ALTER TABLE customers ADD COLUMN IF NOT EXISTS email TEXT NOT NULL DEFAULT '';
ALTER TABLE customers ADD COLUMN IF NOT EXISTS notes TEXT NOT NULL DEFAULT '';
`,
		Go: backfillRolePermsV14,
	},
	{
		// v15: ERP depth (P6). eTIMS-ready invoice fields on every
		// order: buyer_pin holds the customer's KRA PIN when a tax
		// invoice is requested (printed on the receipt, exported in
		// the KRA CSV), invoice_number is the eTIMS document number
		// (defaults to the order number; an eTIMS Control Unit bridge
		// can overwrite it without touching the order number).
		// Rollback: docs/MIGRATION-v15-ROLLBACK.md (columns only —
		// drop them, no data moves).
		Version: 15,
		SQLite: `
ALTER TABLE orders ADD COLUMN buyer_pin TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN invoice_number TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_orders_invoice ON orders(invoice_number);
`,
		Pg: `
ALTER TABLE orders ADD COLUMN IF NOT EXISTS buyer_pin TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN IF NOT EXISTS invoice_number TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_orders_invoice ON orders(invoice_number);
`,
	},
	{
		// v16 — cross-machine photo sync bookkeeping. image_synced_at
		// marks the rows whose CURRENT photo bytes are already in the
		// sync outbox: the backfill only picks up rows where it is
		// still '', and applying a REMOTE image stamps it so received
		// photos are never echoed back (A → B → A ping-pong).
		// customer_ledger.synced_at marks ledger rows already pushed
		// as sync events (checkout charges/redeems + completion earns
		// emit exactly once — a second emission would double-apply
		// money on every receiving till).
		Version: 16,
		SQLite: `
ALTER TABLE products ADD COLUMN image_synced_at TEXT NOT NULL DEFAULT '';
ALTER TABLE customer_ledger ADD COLUMN synced_at TEXT NOT NULL DEFAULT '';
`,
		Pg: `
ALTER TABLE products ADD COLUMN IF NOT EXISTS image_synced_at TEXT NOT NULL DEFAULT '';
ALTER TABLE customer_ledger ADD COLUMN IF NOT EXISTS synced_at TEXT NOT NULL DEFAULT '';
`,
	},
	{
		// v17 — quotations. A quote is a priced offer document sent to
		// a customer BEFORE a sale exists: draft → sent → accepted /
		// declined, with "converted" stamped when its lines move to a
		// till as a parked sale (the sale itself stays a normal
		// checkout — quotes never touch money or stock).
		//   client_uuid  sync identity (insert-by-uuid exactly once)
		//   quote_no     per-team display number (QT-000123; remapped
		//                on collision at apply time, like orders)
		//   items_json   frozen line items (product id/name/qty/price)
		//   synced_at    emit-once bookkeeping, same pattern as orders
		Version: 17,
		SQLite: `
CREATE TABLE IF NOT EXISTS quotes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  quote_no TEXT NOT NULL DEFAULT '',
  client_uuid TEXT NOT NULL UNIQUE,
  customer_id INTEGER NOT NULL DEFAULT 0,
  customer_name TEXT NOT NULL DEFAULT '',
  customer_phone TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'draft',
  items_json TEXT NOT NULL DEFAULT '[]',
  subtotal_cents INTEGER NOT NULL DEFAULT 0,
  discount_cents INTEGER NOT NULL DEFAULT 0,
  discount_label TEXT NOT NULL DEFAULT '',
  tax_cents INTEGER NOT NULL DEFAULT 0,
  total_cents INTEGER NOT NULL DEFAULT 0,
  notes TEXT NOT NULL DEFAULT '',
  valid_until TEXT NOT NULL DEFAULT '',
  created_by INTEGER NOT NULL DEFAULT 0,
  created_by_name TEXT NOT NULL DEFAULT '',
  converted_held_sale_id INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT '',
  synced_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_quotes_status ON quotes(status);
CREATE INDEX IF NOT EXISTS idx_quotes_customer ON quotes(customer_id);
CREATE INDEX IF NOT EXISTS idx_quotes_created ON quotes(created_at);
`,
		Pg: `
CREATE TABLE IF NOT EXISTS quotes (
  id BIGSERIAL PRIMARY KEY,
  quote_no TEXT NOT NULL DEFAULT '',
  client_uuid TEXT NOT NULL UNIQUE,
  customer_id BIGINT NOT NULL DEFAULT 0,
  customer_name TEXT NOT NULL DEFAULT '',
  customer_phone TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'draft',
  items_json TEXT NOT NULL DEFAULT '[]',
  subtotal_cents BIGINT NOT NULL DEFAULT 0,
  discount_cents BIGINT NOT NULL DEFAULT 0,
  discount_label TEXT NOT NULL DEFAULT '',
  tax_cents BIGINT NOT NULL DEFAULT 0,
  total_cents BIGINT NOT NULL DEFAULT 0,
  notes TEXT NOT NULL DEFAULT '',
  valid_until TEXT NOT NULL DEFAULT '',
  created_by BIGINT NOT NULL DEFAULT 0,
  created_by_name TEXT NOT NULL DEFAULT '',
  converted_held_sale_id BIGINT NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT '',
  synced_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_quotes_status ON quotes(status);
CREATE INDEX IF NOT EXISTS idx_quotes_customer ON quotes(customer_id);
CREATE INDEX IF NOT EXISTS idx_quotes_created ON quotes(created_at);
`,
		Go: backfillRolePermsV17,
	},
	{
		// v18 — money-path index backstop. The X/Z cash close and the
		// daily reports filter payments by (status, completed_at
		// range); the existing single-column idx_payments_status has
		// COMPLETED as the dominant value, so every close/report
		// degenerated toward a full scan as history grew. Composite
		// index makes the date-range probe index-driven. Same SQL on
		// both dialects.
		Version: 18,
		SQLite: `
CREATE INDEX IF NOT EXISTS idx_payments_status_completed ON payments(status, completed_at);
`,
		Pg: `
CREATE INDEX IF NOT EXISTS idx_payments_status_completed ON payments(status, completed_at);
`,
	},
	{
		// v19 — sales attribution + visibility scoping.
		//
		// 1) orders.device_id records the ORIGINATING TILL for every
		//    sale (stamped at checkout, carried in sync events so
		//    remote tills keep the original attribution). Completes
		//    the traceability chain: till + cashier + timestamp +
		//    payment legs + status, and enables sales-by-till.
		// 2) orders.view.all is the cross-till oversight permission
		//    (Admin/Owner only). From v19 the whole order surface
		//    scopes non-holders to their OWN sales — staff can no
		//    longer browse others' sale records via URL/ID probes.
		// 3) Indexes keep the scoped queries and daily reports
		//    index-driven. Role union runs in backfillRolePermsV19
		//    so existing shops gain the new key without touching
		//    admin-edited role JSON.
		Version: 19,
		SQLite: `
ALTER TABLE orders ADD COLUMN device_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_orders_device_created ON orders(device_id, created_at);
CREATE INDEX IF NOT EXISTS idx_orders_cashier_created ON orders(cashier_id, created_at);
`,
		Pg: `
ALTER TABLE orders ADD COLUMN device_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_orders_device_created ON orders(device_id, created_at);
CREATE INDEX IF NOT EXISTS idx_orders_cashier_created ON orders(cashier_id, created_at);
`,
		Go: backfillRolePermsV19,
	},
	{
		// v20 — two independent upgrades:
		//  1) shifts.client_uuid: shifts were the last major entity
		//     that never rode team sync (a shift opened on one till
		//     was invisible on the admin's device). The uuid makes
		//     replay idempotent exactly like orders.client_uuid.
		//  2) whatsapp_jobs: persistent delivery queue for WhatsApp
		//     receipt sends (survives restarts, retried with cap).
		Version: 20,
		SQLite: `
ALTER TABLE shifts ADD COLUMN client_uuid TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_shifts_uuid ON shifts(client_uuid) WHERE client_uuid <> '';
CREATE TABLE IF NOT EXISTS whatsapp_jobs (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        order_id INTEGER NOT NULL DEFAULT 0,
        order_number TEXT NOT NULL DEFAULT '',
        phone TEXT NOT NULL DEFAULT '',
        body TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL DEFAULT 'PENDING',
        attempts INTEGER NOT NULL DEFAULT 0,
        last_error TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        sent_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_whatsapp_jobs_status ON whatsapp_jobs(status, id);
`,
		Pg: `
ALTER TABLE shifts ADD COLUMN client_uuid TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_shifts_uuid ON shifts(client_uuid) WHERE client_uuid <> '';
CREATE TABLE IF NOT EXISTS whatsapp_jobs (
        id SERIAL PRIMARY KEY,
        order_id INTEGER NOT NULL DEFAULT 0,
        order_number TEXT NOT NULL DEFAULT '',
        phone TEXT NOT NULL DEFAULT '',
        body TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL DEFAULT 'PENDING',
        attempts INTEGER NOT NULL DEFAULT 0,
        last_error TEXT NOT NULL DEFAULT '',
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        sent_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_whatsapp_jobs_status ON whatsapp_jobs(status, id);
`,
	},
	{
		// v1.6.2: the "Contact customer on WhatsApp" interface came up
		// admin-only on live tills — the till roles (Cashier on the
		// cyber-café machines above all) were created before
		// orders.notify existed and the v14 union predates it for
		// DBs already on that version. Union-only, idempotent;
		// custom roles and admin edits stay untouched.
		Version: 21,
		Go: func(d *DB, tx *sql.Tx) error {
			extra := map[string][]string{
				"Owner":      {"orders.notify"},
				"Admin":      {"orders.notify"},
				"Front Desk": {"orders.notify"},
				"Branding":   {"orders.notify"},
				"Cyber":      {"orders.notify"},
				"Cashier":    {"orders.notify"},
			}
			return unionRolePerms(d, tx, extra)
		},
	},
	{
		// v1.6.3 (v22) — the stock ledger. Until now stock_qty was a bare
		// counter on products: a wrong number had no history, no who, no
		// why. Every stock change now lands in an append-only
		// stock_movements row (kind + signed delta + balance_after + who),
		// the OSPOS/"quantity is a projection" pattern the whole industry
		// runs on. Migration backfills one OPENING anchor per tracked
		// product so day-one stock is auditable. Also: per-product
		// low-stock thresholds and the hot-path indexes the reports and
		// order lists were missing.
		Version: 22,
		SQLite: `
CREATE TABLE IF NOT EXISTS stock_movements (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        -- No FK on purpose: this is an append-only audit trail. It must
        -- survive product deletion/deactivation and never block an
        -- operational delete (the LEFT JOINs resolve names, dangling rows
        -- just show "(deleted product)").
        product_id INTEGER NOT NULL,
        delta INTEGER NOT NULL,
        balance_after INTEGER NOT NULL DEFAULT 0,
        kind TEXT NOT NULL,
        reference TEXT NOT NULL DEFAULT '',
        note TEXT NOT NULL DEFAULT '',
        user_id INTEGER NOT NULL DEFAULT 0,
        username TEXT NOT NULL DEFAULT '',
        device_id TEXT NOT NULL DEFAULT '',
        client_uuid TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_stock_movements_product_created ON stock_movements(product_id, id);
CREATE INDEX IF NOT EXISTS idx_stock_movements_kind_created ON stock_movements(kind, created_at);
ALTER TABLE products ADD COLUMN low_stock_units INTEGER NOT NULL DEFAULT 10;
CREATE INDEX IF NOT EXISTS idx_orders_created ON orders(created_at);
CREATE INDEX IF NOT EXISTS idx_orders_status_created ON orders(status, created_at);
CREATE INDEX IF NOT EXISTS idx_order_items_order ON order_items(order_id);
CREATE INDEX IF NOT EXISTS idx_order_items_product ON order_items(product_id);
CREATE INDEX IF NOT EXISTS idx_payments_order ON payments(order_id);
CREATE INDEX IF NOT EXISTS idx_payments_status ON payments(status);
CREATE INDEX IF NOT EXISTS idx_customer_ledger_cust_created ON customer_ledger(customer_id, id);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);
CREATE INDEX IF NOT EXISTS idx_products_category ON products(category_id);
`,
		Pg: `
CREATE TABLE IF NOT EXISTS stock_movements (
        id SERIAL PRIMARY KEY,
        -- No FK on purpose: append-only audit trail (see SQLite comment).
        product_id INTEGER NOT NULL,
        delta INTEGER NOT NULL,
        balance_after INTEGER NOT NULL DEFAULT 0,
        kind TEXT NOT NULL,
        reference TEXT NOT NULL DEFAULT '',
        note TEXT NOT NULL DEFAULT '',
        user_id INTEGER NOT NULL DEFAULT 0,
        username TEXT NOT NULL DEFAULT '',
        device_id TEXT NOT NULL DEFAULT '',
        client_uuid TEXT NOT NULL DEFAULT '',
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_stock_movements_product_created ON stock_movements(product_id, id);
CREATE INDEX IF NOT EXISTS idx_stock_movements_kind_created ON stock_movements(kind, created_at);
ALTER TABLE products ADD COLUMN low_stock_units INTEGER NOT NULL DEFAULT 10;
CREATE INDEX IF NOT EXISTS idx_orders_created ON orders(created_at);
CREATE INDEX IF NOT EXISTS idx_orders_status_created ON orders(status, created_at);
CREATE INDEX IF NOT EXISTS idx_order_items_order ON order_items(order_id);
CREATE INDEX IF NOT EXISTS idx_order_items_product ON order_items(product_id);
CREATE INDEX IF NOT EXISTS idx_payments_order ON payments(order_id);
CREATE INDEX IF NOT EXISTS idx_payments_status ON payments(status);
CREATE INDEX IF NOT EXISTS idx_customer_ledger_cust_created ON customer_ledger(customer_id, id);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_log(created_at);
CREATE INDEX IF NOT EXISTS idx_products_category ON products(category_id);
`,
		Go: func(d *DB, tx *sql.Tx) error {
			// One OPENING anchor per product so the ledger starts at the
			// quantity the till already trusts. delta = current stock.
			_, err := tx.Exec(d.Rebind(`INSERT INTO stock_movements
                                (product_id, delta, balance_after, kind, reference, note, user_id, username, device_id, client_uuid, created_at)
                                SELECT id, stock_qty, stock_qty, 'OPENING', '', 'ledger start (migration v22)', 0, '', '', '', ` + d.nowExpr() + `
                                FROM products
                                WHERE NOT EXISTS (SELECT 1 FROM stock_movements WHERE stock_movements.product_id = products.id)`))
			if err != nil {
				return fmt.Errorf("backfill stock_movements openings: %w", err)
			}
			// Seeded roles pick up the new permissions (union-only).
			return unionRolePerms(d, tx, map[string][]string{
				"Admin":      models.SeededRolePermissions["Admin"],
				"Owner":      models.SeededRolePermissions["Owner"],
				"Front Desk": {"returns.manage", "expenses.view"},
				"Cashier":    {"returns.manage", "expenses.view"},
			})
		},
	},
	{
		// v1.6.3 (v23) — returns and refunds. Tills could only void whole
		// orders before; a customer walking back with one wrong item had
		// no path. returns/return_items mirror the sale (RET- numbering),
		// restock lands in the v22 ledger, and store-credit refunds ride
		// the existing customer ledger.
		Version: 23,
		SQLite: `
CREATE TABLE IF NOT EXISTS returns (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        number TEXT NOT NULL UNIQUE,
        order_id INTEGER NOT NULL REFERENCES orders(id),
        order_number TEXT NOT NULL DEFAULT '',
        total_cents INTEGER NOT NULL DEFAULT 0,
        method TEXT NOT NULL DEFAULT 'CASH',
        reason TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL DEFAULT 'COMPLETED',
        client_uuid TEXT NOT NULL DEFAULT '',
        created_by INTEGER NOT NULL DEFAULT 0,
        created_by_name TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS return_items (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        return_id INTEGER NOT NULL REFERENCES returns(id) ON DELETE CASCADE,
        order_item_id INTEGER NOT NULL DEFAULT 0,
        product_id INTEGER NOT NULL DEFAULT 0,
        name TEXT NOT NULL DEFAULT '',
        sku TEXT NOT NULL DEFAULT '',
        qty INTEGER NOT NULL DEFAULT 0,
        unit_price_cents INTEGER NOT NULL DEFAULT 0,
        line_total_cents INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_returns_order ON returns(order_id);
CREATE INDEX IF NOT EXISTS idx_returns_created ON returns(created_at);
CREATE INDEX IF NOT EXISTS idx_return_items_return ON return_items(return_id);
`,
		Pg: `
CREATE TABLE IF NOT EXISTS returns (
        id SERIAL PRIMARY KEY,
        number TEXT NOT NULL UNIQUE,
        order_id INTEGER NOT NULL REFERENCES orders(id),
        order_number TEXT NOT NULL DEFAULT '',
        total_cents INTEGER NOT NULL DEFAULT 0,
        method TEXT NOT NULL DEFAULT 'CASH',
        reason TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL DEFAULT 'COMPLETED',
        client_uuid TEXT NOT NULL DEFAULT '',
        created_by INTEGER NOT NULL DEFAULT 0,
        created_by_name TEXT NOT NULL DEFAULT '',
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS return_items (
        id SERIAL PRIMARY KEY,
        return_id INTEGER NOT NULL REFERENCES returns(id) ON DELETE CASCADE,
        order_item_id INTEGER NOT NULL DEFAULT 0,
        product_id INTEGER NOT NULL DEFAULT 0,
        name TEXT NOT NULL DEFAULT '',
        sku TEXT NOT NULL DEFAULT '',
        qty INTEGER NOT NULL DEFAULT 0,
        unit_price_cents INTEGER NOT NULL DEFAULT 0,
        line_total_cents INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_returns_order ON returns(order_id);
CREATE INDEX IF NOT EXISTS idx_returns_created ON returns(created_at);
CREATE INDEX IF NOT EXISTS idx_return_items_return ON return_items(return_id);
`,
	},
	{
		// v1.6.3 (v24) — expenses. Day-end reports showed sales with no
		// cost side: rent, power, airtime, transport all lived outside
		// the till. expense_types is a small seeded catalog; expenses is
		// the money log (EXP- numbering) that the daily/monthly reports
		// now net against sales.
		Version: 24,
		SQLite: `
CREATE TABLE IF NOT EXISTS expense_types (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        name TEXT NOT NULL UNIQUE,
        is_active INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS expenses (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        number TEXT NOT NULL UNIQUE,
        expense_type_id INTEGER NOT NULL REFERENCES expense_types(id),
        amount_cents INTEGER NOT NULL,
        method TEXT NOT NULL DEFAULT 'CASH',
        reference TEXT NOT NULL DEFAULT '',
        note TEXT NOT NULL DEFAULT '',
        spent_on TEXT NOT NULL DEFAULT '',
        client_uuid TEXT NOT NULL DEFAULT '',
        created_by INTEGER NOT NULL DEFAULT 0,
        created_by_name TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_expenses_spenton ON expenses(spent_on);
CREATE INDEX IF NOT EXISTS idx_expenses_type ON expenses(expense_type_id);
`,
		Pg: `
CREATE TABLE IF NOT EXISTS expense_types (
        id SERIAL PRIMARY KEY,
        name TEXT NOT NULL UNIQUE,
        is_active INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS expenses (
        id SERIAL PRIMARY KEY,
        number TEXT NOT NULL UNIQUE,
        expense_type_id INTEGER NOT NULL REFERENCES expense_types(id),
        amount_cents INTEGER NOT NULL,
        method TEXT NOT NULL DEFAULT 'CASH',
        reference TEXT NOT NULL DEFAULT '',
        note TEXT NOT NULL DEFAULT '',
        spent_on TEXT NOT NULL DEFAULT '',
        client_uuid TEXT NOT NULL DEFAULT '',
        created_by INTEGER NOT NULL DEFAULT 0,
        created_by_name TEXT NOT NULL DEFAULT '',
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_expenses_spenton ON expenses(spent_on);
CREATE INDEX IF NOT EXISTS idx_expenses_type ON expenses(expense_type_id);
`,
		Go: func(d *DB, tx *sql.Tx) error {
			for _, name := range []string{
				"Rent", "Electricity", "Internet / Airtime", "Transport",
				"Salaries", "Supplies", "Maintenance", "Other",
			} {
				if _, err := tx.Exec(d.Rebind(`INSERT INTO expense_types (name) VALUES (?) ON CONFLICT DO NOTHING`), name); err != nil {
					return fmt.Errorf("seed expense type %s: %w", name, err)
				}
			}
			return unionRolePerms(d, tx, map[string][]string{
				"Admin": {"expenses.manage"},
				"Owner": {"expenses.manage"},
			})
		},
	},
	{
		// v1.6.3 (v25) — wholesale price tiers. The friend's SmartPOS
		// keeps retail/wholesale per product and a per-customer price
		// type; the POS page flips between them with one tap. We store
		// wholesale_price_cents on the product (0 = same as retail) and
		// price_tier on the customer ('retail' default), and the till
		// auto-applies the tier price when a customer is on the cart.
		Version: 25,
		SQLite: `
ALTER TABLE products ADD COLUMN wholesale_price_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE customers ADD COLUMN price_tier TEXT NOT NULL DEFAULT 'retail';
`,
		Pg: `
ALTER TABLE products ADD COLUMN wholesale_price_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE customers ADD COLUMN price_tier TEXT NOT NULL DEFAULT 'retail';
`,
	},
	{
		// v1.7.0 (v26) — the shopkeeper's ledger, informed by studying
		// the friend's SmartPOS schema (54 tables) and the OSPOS /
		// FloCafe / managiha research:
		//
		//   1. order_items.cost_cents — line cost snapshot frozen at sale
		//      time. Every mature POS does this (OSPOS sales_items
		//      item_cost_price, SmartPOS batches, FloCafe order lines);
		//      without it gross margin is only reconstructible while the
		//      product's CURRENT cost happens to match the historic one.
		//      Backfill: existing lines adopt the product's current cost
		//      (best available approximation, clearly marked by being
		//      identical to cost_cents today).
		//   2. customer_ledger.balance_after — running balance snapshot
		//      on every ledger row (SmartPOS customer_account_transactions
		//      pattern). The customers.balance_cents projection can drift
		//      (SmartPOS needed a whole customer_balance_drift watchdog);
		//      the snapshot makes drift provable instead of invisible.
		//      Nullable: rows before v26 have no recorded balance.
		//   3. customer_ledger.reversal_of — soft reversals (SmartPOS
		//      credit_receipts reversed_by/at/reason quartet, reduced to
		//      the minimal foreign key: a reversal IS a new row that
		//      points at the entry it cancels; money rows are never
		//      updated or deleted).
		//   4. Composite indexes for the two new report shapes: the
		//      customer statement (one customer, in order) and the
		//      disposal report (one movement kind, by date).
		Version: 26,
		SQLite: `
ALTER TABLE order_items ADD COLUMN cost_cents INTEGER NOT NULL DEFAULT 0;
UPDATE order_items SET cost_cents = (SELECT p.cost_cents FROM products p WHERE p.id = order_items.product_id)
 WHERE cost_cents = 0 AND EXISTS (SELECT 1 FROM products p WHERE p.id = order_items.product_id AND p.cost_cents > 0);
ALTER TABLE customer_ledger ADD COLUMN balance_after INTEGER;
ALTER TABLE customer_ledger ADD COLUMN reversal_of INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_ledger_customer_order ON customer_ledger(customer_id, id);
CREATE INDEX IF NOT EXISTS idx_stock_movements_kind ON stock_movements(kind, created_at);
`,
		Pg: `
ALTER TABLE order_items ADD COLUMN IF NOT EXISTS cost_cents BIGINT NOT NULL DEFAULT 0;
UPDATE order_items SET cost_cents = (SELECT p.cost_cents FROM products p WHERE p.id = order_items.product_id)
 WHERE cost_cents = 0 AND EXISTS (SELECT 1 FROM products p WHERE p.id = order_items.product_id AND p.cost_cents > 0);
ALTER TABLE customer_ledger ADD COLUMN IF NOT EXISTS balance_after BIGINT;
ALTER TABLE customer_ledger ADD COLUMN IF NOT EXISTS reversal_of BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_ledger_customer_order ON customer_ledger(customer_id, id);
CREATE INDEX IF NOT EXISTS idx_stock_movements_kind ON stock_movements(kind, created_at);
`,
	},
	{
		// v1.8.0 (v27) — fractional (weighable) quantities. Until now
		// every quantity was a whole number: sugar came in 1-kg bags
		// even when the customer asked for 250 g. All quantity columns
		// gain an integer MILLI-UNIT twin (thousandths) — the same
		// minor-unit pattern as money cents, exact and float-free
		// (OSPOS carries decimal(15,3) columns; SQLite has no decimal,
		// so integers are the honest equivalent). The migration converts
		// stored data ×1000 while the whole-unit columns stay frozen
		// in place (additive-only: the no-destructive-DDL guardrail,
		// and a pre-1.8 binary can still roll back and read its own
		// columns). v1.8+ code reads only the _milli columns — one
		// source of truth in practice. Also: products.unit
		// ('pc', 'kg', …) and products.allow_fraction for the till UX.
		// JSON payloads that carry quantities (held carts, quotes,
		// pending sync events) are rewritten by the Go hook so nothing
		// crosses the upgrade boundary with unit semantics.
		Version: 27,
		SQLite: `
ALTER TABLE products ADD COLUMN stock_qty_milli INTEGER NOT NULL DEFAULT 0;
UPDATE products SET stock_qty_milli = stock_qty * 1000;
ALTER TABLE products ADD COLUMN unit TEXT NOT NULL DEFAULT 'pc';
ALTER TABLE products ADD COLUMN allow_fraction INTEGER NOT NULL DEFAULT 0;
ALTER TABLE order_items ADD COLUMN qty_milli INTEGER NOT NULL DEFAULT 0;
UPDATE order_items SET qty_milli = qty * 1000;
ALTER TABLE return_items ADD COLUMN qty_milli INTEGER NOT NULL DEFAULT 0;
UPDATE return_items SET qty_milli = qty * 1000;
ALTER TABLE purchase_order_items ADD COLUMN qty_milli INTEGER NOT NULL DEFAULT 0;
UPDATE purchase_order_items SET qty_milli = qty * 1000;
ALTER TABLE stock_take_items ADD COLUMN expected_qty_milli INTEGER NOT NULL DEFAULT 0;
UPDATE stock_take_items SET expected_qty_milli = expected_qty * 1000;
ALTER TABLE stock_take_items ADD COLUMN counted_qty_milli INTEGER NOT NULL DEFAULT 0;
UPDATE stock_take_items SET counted_qty_milli = counted_qty * 1000;
ALTER TABLE stock_count_lines ADD COLUMN expected_qty_milli INTEGER NOT NULL DEFAULT 0;
UPDATE stock_count_lines SET expected_qty_milli = expected_qty * 1000;
-- counted stays NULL-able: 'not counted yet' is a real state.
ALTER TABLE stock_count_lines ADD COLUMN counted_qty_milli INTEGER;
UPDATE stock_count_lines SET counted_qty_milli = counted_qty * 1000 WHERE counted_qty IS NOT NULL;
ALTER TABLE stock_count_lines ADD COLUMN system_qty_milli INTEGER NOT NULL DEFAULT 0;
UPDATE stock_count_lines SET system_qty_milli = system_qty * 1000;
ALTER TABLE stock_movements ADD COLUMN delta_milli INTEGER NOT NULL DEFAULT 0;
UPDATE stock_movements SET delta_milli = delta * 1000;
ALTER TABLE stock_movements ADD COLUMN balance_after_milli INTEGER NOT NULL DEFAULT 0;
UPDATE stock_movements SET balance_after_milli = balance_after * 1000;
`,
		Pg: `
ALTER TABLE products ADD COLUMN IF NOT EXISTS stock_qty_milli BIGINT NOT NULL DEFAULT 0;
UPDATE products SET stock_qty_milli = stock_qty * 1000;
ALTER TABLE products ADD COLUMN IF NOT EXISTS unit TEXT NOT NULL DEFAULT 'pc';
ALTER TABLE products ADD COLUMN IF NOT EXISTS allow_fraction INTEGER NOT NULL DEFAULT 0;
ALTER TABLE order_items ADD COLUMN IF NOT EXISTS qty_milli BIGINT NOT NULL DEFAULT 0;
UPDATE order_items SET qty_milli = qty * 1000;
ALTER TABLE return_items ADD COLUMN IF NOT EXISTS qty_milli BIGINT NOT NULL DEFAULT 0;
UPDATE return_items SET qty_milli = qty * 1000;
ALTER TABLE purchase_order_items ADD COLUMN IF NOT EXISTS qty_milli BIGINT NOT NULL DEFAULT 0;
UPDATE purchase_order_items SET qty_milli = qty * 1000;
ALTER TABLE stock_take_items ADD COLUMN IF NOT EXISTS expected_qty_milli BIGINT NOT NULL DEFAULT 0;
UPDATE stock_take_items SET expected_qty_milli = expected_qty * 1000;
ALTER TABLE stock_take_items ADD COLUMN IF NOT EXISTS counted_qty_milli BIGINT NOT NULL DEFAULT 0;
UPDATE stock_take_items SET counted_qty_milli = counted_qty * 1000;
ALTER TABLE stock_count_lines ADD COLUMN IF NOT EXISTS expected_qty_milli BIGINT NOT NULL DEFAULT 0;
UPDATE stock_count_lines SET expected_qty_milli = expected_qty * 1000;
ALTER TABLE stock_count_lines ADD COLUMN IF NOT EXISTS counted_qty_milli BIGINT;
UPDATE stock_count_lines SET counted_qty_milli = counted_qty * 1000 WHERE counted_qty IS NOT NULL;
ALTER TABLE stock_count_lines ADD COLUMN IF NOT EXISTS system_qty_milli BIGINT NOT NULL DEFAULT 0;
UPDATE stock_count_lines SET system_qty_milli = system_qty * 1000;
ALTER TABLE stock_movements ADD COLUMN IF NOT EXISTS delta_milli BIGINT NOT NULL DEFAULT 0;
UPDATE stock_movements SET delta_milli = delta * 1000;
ALTER TABLE stock_movements ADD COLUMN IF NOT EXISTS balance_after_milli BIGINT NOT NULL DEFAULT 0;
UPDATE stock_movements SET balance_after_milli = balance_after * 1000;
`,
		Go: func(d *DB, tx *sql.Tx) error {
			return backfillMilliQuantityJSON(d, tx)
		},
	},
	{
		// v1.8.0 (v28) — batch / expiry tracking with FEFO allocation
		// (First-Expired-First-Out). Milk, flour, medicine and soda all
		// expire; a bare stock counter cannot warn the shopkeeper or
		// say WHICH carton a sale came from. product_batches carries
		// one row per received lot (batch no, expiry date, remaining
		// quantity, cost at receipt, supplier); sales of batch-tracked
		// products allocate across ACTIVE lots nearest-expiry-first and
		// freeze the split into order_item_batches; disposals can
		// target a specific lot. products.track_batches opts a product
		// in (0 by default — tills keep working exactly as before);
		// enabling it wraps existing stock into an OPENING batch so
		// the batch sum always reconciles with the shelf projection.
		Version: 28,
		SQLite: `
CREATE TABLE IF NOT EXISTS product_batches (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        product_id INTEGER NOT NULL,
        batch_no TEXT NOT NULL DEFAULT '',
        expiry_date TEXT NOT NULL DEFAULT '',
        qty_received_milli INTEGER NOT NULL DEFAULT 0,
        qty_remaining_milli INTEGER NOT NULL DEFAULT 0,
        cost_cents INTEGER NOT NULL DEFAULT 0,
        supplier_id INTEGER NOT NULL DEFAULT 0,
        reference TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL DEFAULT 'ACTIVE',
        client_uuid TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_batches_product ON product_batches(product_id, status, expiry_date, id);
CREATE INDEX IF NOT EXISTS idx_batches_expiry ON product_batches(status, expiry_date);
CREATE UNIQUE INDEX IF NOT EXISTS idx_batches_client_uuid ON product_batches(client_uuid) WHERE client_uuid != '';
CREATE TABLE IF NOT EXISTS order_item_batches (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        order_item_id INTEGER NOT NULL,
        product_id INTEGER NOT NULL,
        batch_id INTEGER NOT NULL,
        qty_milli INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_oib_item ON order_item_batches(order_item_id);
CREATE INDEX IF NOT EXISTS idx_oib_batch ON order_item_batches(batch_id);
ALTER TABLE products ADD COLUMN track_batches INTEGER NOT NULL DEFAULT 0;
ALTER TABLE purchase_order_items ADD COLUMN expiry_date TEXT NOT NULL DEFAULT '';
ALTER TABLE purchase_order_items ADD COLUMN batch_no TEXT NOT NULL DEFAULT '';
`,
		Pg: `
CREATE TABLE IF NOT EXISTS product_batches (
        id SERIAL PRIMARY KEY,
        product_id BIGINT NOT NULL,
        batch_no TEXT NOT NULL DEFAULT '',
        expiry_date TEXT NOT NULL DEFAULT '',
        qty_received_milli BIGINT NOT NULL DEFAULT 0,
        qty_remaining_milli BIGINT NOT NULL DEFAULT 0,
        cost_cents BIGINT NOT NULL DEFAULT 0,
        supplier_id BIGINT NOT NULL DEFAULT 0,
        reference TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL DEFAULT 'ACTIVE',
        client_uuid TEXT NOT NULL DEFAULT '',
        created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_batches_product ON product_batches(product_id, status, expiry_date, id);
CREATE INDEX IF NOT EXISTS idx_batches_expiry ON product_batches(status, expiry_date);
CREATE UNIQUE INDEX IF NOT EXISTS idx_batches_client_uuid ON product_batches(client_uuid) WHERE client_uuid != '';
CREATE TABLE IF NOT EXISTS order_item_batches (
        id SERIAL PRIMARY KEY,
        order_item_id BIGINT NOT NULL,
        product_id BIGINT NOT NULL,
        batch_id BIGINT NOT NULL,
        qty_milli BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_oib_item ON order_item_batches(order_item_id);
CREATE INDEX IF NOT EXISTS idx_oib_batch ON order_item_batches(batch_id);
ALTER TABLE products ADD COLUMN IF NOT EXISTS track_batches INTEGER NOT NULL DEFAULT 0;
ALTER TABLE purchase_order_items ADD COLUMN IF NOT EXISTS expiry_date TEXT NOT NULL DEFAULT '';
ALTER TABLE purchase_order_items ADD COLUMN IF NOT EXISTS batch_no TEXT NOT NULL DEFAULT '';
`,
	},
	{
		// v29 — index the child side of foreign keys that had none.
		// Each was a full-table scan on a hot or growing path:
		//   - print_jobs(order_id): ON DELETE CASCADE from orders (purge,
		//     test resets) and the per-order reprint lookup.
		//   - purchase_order_items(po_id), stock_take_items(take_id): every
		//     open of a PO / stock take loads its lines; both cascade.
		//   - purchase_order_items(product_id), stock_take_items(product_id),
		//     purchase_orders(supplier_id), design_jobs(assignee_id): the
		//     "history for this product / supplier / designer" lookups and
		//     the FK check when a parent row is deleted.
		// Pure additions, idempotent, same SQL on SQLite and Postgres.
		Version: 29,
		SQLite: `
CREATE INDEX IF NOT EXISTS idx_print_jobs_order ON print_jobs(order_id);
CREATE INDEX IF NOT EXISTS idx_po_items_po ON purchase_order_items(po_id);
CREATE INDEX IF NOT EXISTS idx_po_items_product ON purchase_order_items(product_id);
CREATE INDEX IF NOT EXISTS idx_purchase_orders_supplier ON purchase_orders(supplier_id);
CREATE INDEX IF NOT EXISTS idx_stock_take_items_take ON stock_take_items(take_id);
CREATE INDEX IF NOT EXISTS idx_stock_take_items_product ON stock_take_items(product_id);
CREATE INDEX IF NOT EXISTS idx_design_jobs_assignee ON design_jobs(assignee_id);
`,
	},
	{
		// v30 (v1.10.0): KRA eTIMS. etims_receipts is both the outbox of
		// sales / credit notes waiting to be signed by KRA (through the
		// cloud `etims` function) and the store of the signature each
		// receipt prints. One row per (kind, sale or return). Products gain
		// their KRA tax type (A-E, '' = shop default) and item class code
		// ('' = shop default). Pure additions.
		Version: 30,
		SQLite: `
ALTER TABLE products ADD COLUMN etims_tax_type TEXT NOT NULL DEFAULT '';
ALTER TABLE products ADD COLUMN etims_cls_cd TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS etims_receipts (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        kind TEXT NOT NULL DEFAULT 'S',
        ref_id INTEGER NOT NULL,
        client_ref TEXT NOT NULL DEFAULT '',
        org_client_ref TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL DEFAULT 'pending',
        invc_no INTEGER NOT NULL DEFAULT 0,
        cur_rcpt_no INTEGER NOT NULL DEFAULT 0,
        tot_rcpt_no INTEGER NOT NULL DEFAULT 0,
        intrl_data TEXT NOT NULL DEFAULT '',
        rcpt_sign TEXT NOT NULL DEFAULT '',
        sdc_date_time TEXT NOT NULL DEFAULT '',
        sdc_id TEXT NOT NULL DEFAULT '',
        mrc_no TEXT NOT NULL DEFAULT '',
        qr TEXT NOT NULL DEFAULT '',
        last_error TEXT NOT NULL DEFAULT '',
        attempts INTEGER NOT NULL DEFAULT 0,
        next_try_at TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
        updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_etims_receipts_ref ON etims_receipts(kind, ref_id);
CREATE INDEX IF NOT EXISTS idx_etims_receipts_status ON etims_receipts(status, next_try_at);
`,
		Pg: `
ALTER TABLE products ADD COLUMN IF NOT EXISTS etims_tax_type TEXT NOT NULL DEFAULT '';
ALTER TABLE products ADD COLUMN IF NOT EXISTS etims_cls_cd TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS etims_receipts (
        id SERIAL PRIMARY KEY,
        kind TEXT NOT NULL DEFAULT 'S',
        ref_id BIGINT NOT NULL,
        client_ref TEXT NOT NULL DEFAULT '',
        org_client_ref TEXT NOT NULL DEFAULT '',
        status TEXT NOT NULL DEFAULT 'pending',
        invc_no BIGINT NOT NULL DEFAULT 0,
        cur_rcpt_no BIGINT NOT NULL DEFAULT 0,
        tot_rcpt_no BIGINT NOT NULL DEFAULT 0,
        intrl_data TEXT NOT NULL DEFAULT '',
        rcpt_sign TEXT NOT NULL DEFAULT '',
        sdc_date_time TEXT NOT NULL DEFAULT '',
        sdc_id TEXT NOT NULL DEFAULT '',
        mrc_no TEXT NOT NULL DEFAULT '',
        qr TEXT NOT NULL DEFAULT '',
        last_error TEXT NOT NULL DEFAULT '',
        attempts BIGINT NOT NULL DEFAULT 0,
        next_try_at TEXT NOT NULL DEFAULT '',
        created_at TEXT NOT NULL DEFAULT to_char(now() at time zone 'utc', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"'),
        updated_at TEXT NOT NULL DEFAULT to_char(now() at time zone 'utc', 'YYYY-MM-DD"T"HH24:MI:SS.MS"Z"')
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_etims_receipts_ref ON etims_receipts(kind, ref_id);
CREATE INDEX IF NOT EXISTS idx_etims_receipts_status ON etims_receipts(status, next_try_at);
`,
	},
	{
		// v31 (v1.11.0): per-line VAT. Each sale line freezes its KRA tax
		// type, VAT rate, VAT share and the amount actually paid for it
		// (after discount + points), so mixed-rate sales (exempt, 8%, gift
		// cards) print, report and go to KRA with the same figures. Old
		// lines keep 0 / '' and fall back to the order-level VAT.
		Version: 31,
		SQLite: `
ALTER TABLE order_items ADD COLUMN tax_type TEXT NOT NULL DEFAULT '';
ALTER TABLE order_items ADD COLUMN tax_rate REAL NOT NULL DEFAULT 0;
ALTER TABLE order_items ADD COLUMN tax_cents INTEGER NOT NULL DEFAULT 0;
ALTER TABLE order_items ADD COLUMN paid_cents INTEGER NOT NULL DEFAULT 0;
`,
		Pg: `
ALTER TABLE order_items ADD COLUMN IF NOT EXISTS tax_type TEXT NOT NULL DEFAULT '';
ALTER TABLE order_items ADD COLUMN IF NOT EXISTS tax_rate DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE order_items ADD COLUMN IF NOT EXISTS tax_cents BIGINT NOT NULL DEFAULT 0;
ALTER TABLE order_items ADD COLUMN IF NOT EXISTS paid_cents BIGINT NOT NULL DEFAULT 0;
`,
	},
}

// backfillRolePermsV19 unions the v19 sales-oversight permission into the
// seeded Admin/Owner roles (union-only, once — admin edits after this run
// are never touched). Non-admin roles intentionally do NOT gain it: from
// v19, browsing other people's sale records requires orders.view.all.
func backfillRolePermsV19(d *DB, tx *sql.Tx) error {
	extra := map[string][]string{
		"Admin": models.SeededRolePermissions["Admin"],
		"Owner": models.SeededRolePermissions["Owner"],
	}
	return unionRolePerms(d, tx, extra)
}

// backfillRolePermsV17 unions the quotation permissions into the seeded
// roles (union-only, idempotent): Admin/Owner get everything (AllPermissions
// already includes the new keys), the front-office roles quote all day,
// Designers see quotes for their jobs, Cashiers can look them up.
func backfillRolePermsV17(d *DB, tx *sql.Tx) error {
	extra := map[string][]string{
		"Admin":      models.SeededRolePermissions["Admin"],
		"Owner":      models.SeededRolePermissions["Owner"],
		"Front Desk": models.SeededRolePermissions["Front Desk"],
		"Branding":   models.SeededRolePermissions["Branding"],
		"Cyber":      models.SeededRolePermissions["Cyber"],
		"Designer":   models.SeededRolePermissions["Designer"],
		"Cashier":    models.SeededRolePermissions["Cashier"],
	}
	return unionRolePerms(d, tx, extra)
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

// backfillRolePermsV14 unions the P2/P3 workflow permissions into existing
// system roles (union-only, once). New role names (Owner, Front Desk,
// Branding, Cyber) are created by seedRoles on the same boot.
func backfillRolePermsV14(d *DB, tx *sql.Tx) error {
	extra := map[string][]string{
		"Admin":      models.SeededRolePermissions["Admin"],
		"Owner":      models.SeededRolePermissions["Owner"],
		"Front Desk": models.SeededRolePermissions["Front Desk"],
		"Branding":   models.SeededRolePermissions["Branding"],
		"Cyber":      models.SeededRolePermissions["Cyber"],
		"Designer":   models.SeededRolePermissions["Designer"],
		"Cashier":    models.SeededRolePermissions["Cashier"],
	}
	return unionRolePerms(d, tx, extra)
}

// seedCredentials mirrors database/seed.go's historical seedUsers — the
// exact public credentials the installer used to ship. The purge verifies
// against these; any account still matching them is a demo account by
// definition, whatever its username is now.
var seedCredentials = []struct {
	username, password, pin string
}{
	{"admin", "admin123", "1234"},
	{"cashier", "cashier123", "2222"},
	{"designer", "designer123", "3333"},
}

// purgeDemoUsersV13 disables installer-seeded demo accounts whose
// credentials are still the public seed values.
//
// SAFEGUARDS (per the client's work order):
//   - a user whose password or PIN was rotated is NEVER touched (they own
//     the account now) — bcrypt verification decides, not usernames;
//   - accounts are DEACTIVATED, never deleted — orders.cashier_id,
//     shifts.user_id and audit history stay fully intact;
//   - every action is journalled to audit_log so the one-SQL-statement
//     rollback in docs/MIGRATION-v13-ROLLBACK.md can restore precisely
//     what was disabled;
//   - if EVERY active user would end up disabled (a box genuinely using
//     only demo accounts), the demo admin is RE-ACTIVATED with must_rotate
//     so the box still has one working login and is forced to set a real
//     password on first boot after upgrade.
func purgeDemoUsersV13(d *DB, tx *sql.Tx) error {
	type demoRow struct {
		id     int64
		pwHash string
		pin    string
	}
	for _, sc := range seedCredentials {
		rows, err := tx.Query(d.Rebind(`
                        SELECT id, COALESCE(password_hash,''), COALESCE(pin_hash,'') FROM users
                        WHERE LOWER(username) = LOWER(?) AND is_active = 1`), sc.username)
		if err != nil {
			return err
		}
		var found []demoRow
		for rows.Next() {
			var r demoRow
			if err := rows.Scan(&r.id, &r.pwHash, &r.pin); err == nil {
				found = append(found, r)
			}
		}
		rows.Close()
		for _, r := range found {
			if !hashVerify(r.pwHash, sc.password) {
				continue // password rotated — a real person owns this account
			}
			if r.pin != "" && !hashVerify(r.pin, sc.pin) {
				continue // PIN rotated — owned
			}
			if _, err := tx.Exec(`UPDATE users SET is_active = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, r.id); err != nil {
				return err
			}
			if _, err := tx.Exec(d.Rebind(`
                                INSERT INTO audit_log (user_id, username, action, entity, entity_id, details, created_at)
                                VALUES (0, 'system', 'DEMO_ACCOUNT_DISABLED', 'user', ?, 'migration v13', ?)`),
				r.id, nowStampV13()); err != nil {
				return err
			}
		}
	}
	// Guardrail: a box must never migrate to zero working logins.
	var active int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE is_active = 1`).Scan(&active); err != nil {
		return err
	}
	if active == 0 {
		res, err := tx.Exec(`
                        UPDATE users SET is_active = 1, must_rotate = 1, updated_at = CURRENT_TIMESTAMP
                        WHERE LOWER(username) = 'admin'`)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			if _, err := tx.Exec(d.Rebind(`
                                INSERT INTO audit_log (user_id, username, action, entity, entity_id, details, created_at)
                                VALUES (0, 'system', 'DEMO_ACCOUNT_REENABLED_FOR_SETUP', 'user', '0', 'migration v13 guardrail', ?)`),
				nowStampV13()); err != nil {
				return err
			}
		}
	}
	return nil
}

// nowStampV13 matches services.nowStamp (database cannot import services).
func nowStampV13() string {
	return timeNowUTC().Format("2006-01-02T15:04:05.000Z")
}

// backfillMilliQuantityJSON (v27) rewrites every JSON payload that carries
// whole-unit quantities so nothing crosses the milli-unit upgrade boundary
// with old semantics:
//
//   - held_sales.items_json  — parked carts resume with milli quantities
//   - quotes.items_json      — quote lines price correctly after upgrade
//   - sync_outbox.payload    — pending LAN-sync events replay in milli:
//     entity=product → "stockQtyMilli", entity=stock → "deltaMilli",
//     entity=order|return → items[]."qtyMilli"
//
// The whole-unit key is RENAMED to the milli key (qty → qtyMilli, stockQty
// → stockQtyMilli, delta → deltaMilli) AND scaled ×1000 — the structs on
// the other side of the upgrade read the new names only. A payload that
// does not parse stays untouched (it is dead history at worst); a hard
// failure on a writable row aborts the migration: half-converted is worse
// than not starting.
func backfillMilliQuantityJSON(d *DB, tx *sql.Tx) error {
	scaleItem := func(items []any) bool {
		changed := false
		for _, raw := range items {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if q, ok := m["qty"].(float64); ok {
				delete(m, "qty")
				m["qtyMilli"] = q * 1000
				changed = true
			}
		}
		return changed
	}

	// 1. held_sales.items_json — parked carts.
	rows, err := tx.Query(d.Rebind(`SELECT id, items_json FROM held_sales`))
	if err != nil {
		return fmt.Errorf("held_sales scan: %w", err)
	}
	type heldFix struct {
		id   int64
		body string
	}
	var held []heldFix
	for rows.Next() {
		var id int64
		var body string
		if err := rows.Scan(&id, &body); err != nil {
			rows.Close()
			return err
		}
		var items []any
		if json.Unmarshal([]byte(body), &items) == nil && scaleItem(items) {
			if nb, err := json.Marshal(items); err == nil {
				held = append(held, heldFix{id, string(nb)})
			}
		}
	}
	rows.Close()
	for _, h := range held {
		if _, err := tx.Exec(d.Rebind(`UPDATE held_sales SET items_json = ? WHERE id = ?`), h.body, h.id); err != nil {
			return fmt.Errorf("held_sales rewrite: %w", err)
		}
	}

	// 2. quotes.items_json — open quotes.
	rows, err = tx.Query(d.Rebind(`SELECT id, items_json FROM quotes`))
	if err != nil {
		return fmt.Errorf("quotes scan: %w", err)
	}
	var quoteFixes []heldFix
	for rows.Next() {
		var id int64
		var body string
		if err := rows.Scan(&id, &body); err != nil {
			rows.Close()
			return err
		}
		var items []any
		if json.Unmarshal([]byte(body), &items) == nil && scaleItem(items) {
			if nb, err := json.Marshal(items); err == nil {
				quoteFixes = append(quoteFixes, heldFix{id, string(nb)})
			}
		}
	}
	rows.Close()
	for _, qf := range quoteFixes {
		if _, err := tx.Exec(d.Rebind(`UPDATE quotes SET items_json = ? WHERE id = ?`), qf.body, qf.id); err != nil {
			return fmt.Errorf("quotes rewrite: %w", err)
		}
	}

	// 3. sync_outbox — pending LAN-sync events replay on sibling tills.
	rows, err = tx.Query(d.Rebind(`SELECT seq, entity, payload FROM sync_outbox WHERE pushed_at = ''`))
	if err != nil {
		return fmt.Errorf("sync_outbox scan: %w", err)
	}
	type outboxFix struct {
		seq  int64
		body string
	}
	var outbox []outboxFix
	for rows.Next() {
		var seq int64
		var entity, body string
		if err := rows.Scan(&seq, &entity, &body); err != nil {
			rows.Close()
			return err
		}
		var payload map[string]any
		if json.Unmarshal([]byte(body), &payload) != nil {
			continue
		}
		changed := false
		switch entity {
		case "product":
			if q, ok := payload["stockQty"].(float64); ok {
				delete(payload, "stockQty")
				payload["stockQtyMilli"] = q * 1000
				changed = true
			}
		case "stock":
			if q, ok := payload["delta"].(float64); ok {
				delete(payload, "delta")
				payload["deltaMilli"] = q * 1000
				changed = true
			}
		case "order", "return":
			if items, ok := payload["items"].([]any); ok && scaleItem(items) {
				payload["items"] = items
				changed = true
			}
		}
		if changed {
			if nb, err := json.Marshal(payload); err == nil {
				outbox = append(outbox, outboxFix{seq, string(nb)})
			}
		}
	}
	rows.Close()
	for _, of := range outbox {
		if _, err := tx.Exec(d.Rebind(`UPDATE sync_outbox SET payload = ? WHERE seq = ?`), of.body, of.seq); err != nil {
			return fmt.Errorf("sync_outbox rewrite: %w", err)
		}
	}
	return nil
}

// Migrate applies pending migrations in order.
func (d *DB) Migrate() error {
	// Dialect note: the default applied_at used to be SQLite's
	// datetime('now') on BOTH dialects — server-mode Postgres could
	// never run Migrate at all ("function datetime(unknown) does not
	// exist"), which the CI postgres job caught on its first run.
	schemaDDL := `CREATE TABLE IF NOT EXISTS schema_migrations (
                version INTEGER PRIMARY KEY,
                applied_at TEXT NOT NULL DEFAULT (datetime('now'))
        )`
	if !d.IsSQLite() {
		schemaDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
                        version INTEGER PRIMARY KEY,
                        applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
                )`
	}
	if _, err := d.Exec(schemaDDL); err != nil {
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
		for _, stmt := range splitSQLStatements(body) {
			if d.IsSQLite() && isAddColumnStmt(stmt) && addColumnAlreadyApplied(d, stmt) {
				// SQLite has no ALTER TABLE ADD COLUMN IF NOT EXISTS. A
				// database created by a pre-migration-tracking build already
				// carries the column; skipping beats aborting the boot.
				continue
			}
			if _, err := tx.Exec(stmt); err != nil {
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

// splitSQLStatements splits a migration body into individual statements,
// keeping semicolons inside SQLite datetime defaults / string literals
// intact (naive Split(";") is wrong for `DEFAULT (datetime('now'))` only
// when quotes contain semicolons — our migrations never do, but quoted
// literals are respected anyway).
func splitSQLStatements(body string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range body {
		switch {
		case r == '\'':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ';' && !inQuote:
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// isAddColumnStmt reports whether stmt is `ALTER TABLE <t> ADD COLUMN …`.
var addColumnRe = regexp.MustCompile(`(?is)^\s*ALTER\s+TABLE\s+(\w+)\s+ADD\s+COLUMN\s+(\w+)\b`)

func isAddColumnStmt(stmt string) bool {
	return addColumnRe.MatchString(stmt)
}

// addColumnAlreadyApplied checks pragma table_info for the target column.
func addColumnAlreadyApplied(d *DB, stmt string) bool {
	m := addColumnRe.FindStringSubmatch(stmt)
	if m == nil {
		return false
	}
	rows, err := d.Query("PRAGMA table_info(" + m[1] + ")")
	if err != nil {
		return false // let the ALTER run and surface the real error
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			continue
		}
		if strings.EqualFold(name, m[2]) {
			return true
		}
	}
	return false
}
