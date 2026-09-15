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
