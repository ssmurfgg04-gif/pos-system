package services

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// systemhealth.go — v1.7.0 "System health" (the friend's SmartPOS ships a
// settings/health.php page that self-audits the database; FloCafe verifies
// its schema against a declared ideal). This is the same idea for
// LedgerPOS: one read-only endpoint that proves the shape and the
// projections are sound, before the owner needs them at day-end.

// ExpectedIndex lists every index the migrations declare. The health check
// compares this against sqlite_master / pg_indexes so a dropped or
// never-created index is VISIBLE instead of silently slowing reports.
var ExpectedIndexes = []string{
	"idx_products_barcode", "idx_products_category",
	"idx_orders_client_uuid", "idx_orders_status", "idx_orders_created", "idx_orders_cashier", "idx_orders_invoice",
	"idx_items_order", "idx_items_product",
	"idx_payments_receipt", "idx_payments_checkout", "idx_payments_order", "idx_payments_status",
	"idx_print_status", "idx_shifts_user", "idx_design_status", "idx_audit_created",
	"idx_customers_name", "idx_customers_phone", "idx_ledger_customer", "idx_ledger_customer_order",
	"idx_suppliers_name", "idx_stock_movements_kind",
	// v29 foreign-key child indexes.
	"idx_print_jobs_order", "idx_po_items_po", "idx_po_items_product", "idx_purchase_orders_supplier",
	"idx_stock_take_items_take", "idx_stock_take_items_product", "idx_design_jobs_assignee",
}

// SystemHealth is the /system/health payload. Everything is read-only.
type SystemHealth struct {
	SchemaVersion  int64    `json:"schemaVersion"`
	Migrations     int      `json:"migrationsApplied"`
	IndexesOK      bool     `json:"indexesOk"`
	IndexesFound   []string `json:"indexesFound"`
	IndexesMissing []string `json:"indexesMissing"`
	DBSizeBytes    int64    `json:"dbSizeBytes"`
	JournalMode    string   `json:"journalMode"`
	// Sync queue depth: how much is waiting to leave this till, and how
	// much arrived so broken it had to be parked (dead-letter).
	OutboxPending int64 `json:"outboxPending"`
	OutboxFailed  int64 `json:"outboxFailed"`
	DeadLetters   int64 `json:"deadLetters"`
	// Money integrity: the customers.balance_cents projection vs the
	// ledger that is supposed to explain it (SmartPOS's drift watchdog,
	// condensed into the health page).
	CustomersChecked   int64    `json:"customersChecked"`
	CustomerDriftCount int64    `json:"customerDriftCount"`
	CustomerDrift      []string `json:"customerDrift,omitempty"` // "name: balance 12000 vs ledger 11500"
	// Stock integrity: last ledger row's balance_after vs the product's
	// stock_qty projection. Pre-v22 products have no ledger and are
	// excluded (reported as LedgerCoverage).
	StockChecked    int64    `json:"stockChecked"`
	StockDriftCount int64    `json:"stockDriftCount"`
	StockDrift      []string `json:"stockDrift,omitempty"`
	StockNoLedger   int64    `json:"stockNoLedger"`
	// v1.8.0: batch integrity — for every batch-tracked product the sum
	// of its lots must equal the shelf projection. Also: how many lots
	// are already expired with goods still on them (the expiry report
	// shows the money; this counts the alerts).
	BatchesChecked    int64    `json:"batchesChecked"`
	BatchDriftCount   int64    `json:"batchDriftCount"`
	BatchDrift        []string `json:"batchDrift,omitempty"`
	ExpiredBatches    int64    `json:"expiredBatches"`
	ExpiredValueCents int64    `json:"expiredValueCents"`
	// Backups: newest local snapshot (the 02:00 worker + manual runs).
	LastBackupFile string `json:"lastBackupFile"`
	LastBackupAt   string `json:"lastBackupAt"`
	BackupCount    int    `json:"backupCount"`
}

// SystemHealth computes the whole payload. Never fails hard: a probe that
// cannot run reports its zero value rather than taking the page down.
func (s *Service) SystemHealth() *SystemHealth {
	h := &SystemHealth{}

	// Schema version + migration count.
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(version),0), COUNT(*) FROM schema_migrations`).Scan(&h.SchemaVersion, &h.Migrations)

	// Index audit.
	h.IndexesFound = []string{}
	h.IndexesMissing = []string{}
	found := map[string]bool{}
	if rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type='index'`); err == nil {
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil && strings.HasPrefix(n, "idx_") {
				found[n] = true
			}
		}
		rows.Close()
	}
	for _, want := range ExpectedIndexes {
		if found[want] {
			h.IndexesFound = append(h.IndexesFound, want)
		} else {
			h.IndexesMissing = append(h.IndexesMissing, want)
		}
	}
	sort.Strings(h.IndexesFound)
	h.IndexesOK = len(h.IndexesMissing) == 0

	// DB file size + journal mode.
	if p := s.db.Path(); p != "" {
		if st, err := os.Stat(p); err == nil {
			h.DBSizeBytes = st.Size()
		}
	}
	_ = s.db.QueryRow(`PRAGMA journal_mode`).Scan(&h.JournalMode)

	// Sync queue.
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox`).Scan(&h.OutboxPending)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE attempts >= 5`).Scan(&h.OutboxFailed)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sync_dead_letter`).Scan(&h.DeadLetters)

	// Customer ledger reconciliation: every customer with ledger rows must
	// have balance_cents == SUM(amount_cents) over those rows.
	h.CustomerDrift = []string{}
	if rows, err := s.db.Query(`
		SELECT c.name, c.balance_cents, COALESCE(SUM(l.amount_cents),0)
		FROM customers c JOIN customer_ledger l ON l.customer_id = c.id
		GROUP BY c.id, c.name, c.balance_cents`); err == nil {
		for rows.Next() {
			var name string
			var bal, sum int64
			if rows.Scan(&name, &bal, &sum) == nil {
				h.CustomersChecked++
				if bal != sum {
					h.CustomerDriftCount++
					if len(h.CustomerDrift) < 10 {
						h.CustomerDrift = append(h.CustomerDrift, fmt.Sprintf("%s: balance %d vs ledger %d", name, bal, sum))
					}
				}
			}
		}
		rows.Close()
	}

	// Stock projection check: the newest movement per product must agree
	// with stock_qty_milli. Products with no ledger rows predate v22 —
	// counted, not flagged (their baseline predates the ledger).
	h.StockDrift = []string{}
	if rows, err := s.db.Query(`
		SELECT p.name, p.stock_qty_milli, m.balance_after_milli
		FROM products p
		JOIN (SELECT product_id, MAX(id) AS mid FROM stock_movements GROUP BY product_id) last
		  ON last.product_id = p.id
		JOIN stock_movements m ON m.id = last.mid`); err == nil {
		for rows.Next() {
			var name string
			var qty, bal int64
			if rows.Scan(&name, &qty, &bal) == nil {
				h.StockChecked++
				if qty != bal {
					h.StockDriftCount++
					if len(h.StockDrift) < 10 {
						h.StockDrift = append(h.StockDrift, fmt.Sprintf("%s: stock %d vs ledger %d", name, qty, bal))
					}
				}
			}
		}
		rows.Close()
	}
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM products WHERE track_stock = 1 AND is_active = 1 AND id NOT IN (SELECT DISTINCT product_id FROM stock_movements)`).Scan(&h.StockNoLedger)

	// v1.8.0 batch integrity: SUM(lots) must equal the shelf projection
	// for every batch-tracked product, and expired lots holding stock are
	// counted with their write-off value (expiry report carries the rest).
	h.BatchDrift = []string{}
	if rows, err := s.db.Query(`
		SELECT p.name, p.stock_qty_milli,
			(SELECT COALESCE(SUM(b.qty_remaining_milli),0) FROM product_batches b WHERE b.product_id = p.id)
		FROM products p WHERE COALESCE(p.track_batches,0) = 1`); err == nil {
		for rows.Next() {
			var name string
			var proj, lots int64
			if rows.Scan(&name, &proj, &lots) == nil {
				h.BatchesChecked++
				if proj != lots {
					h.BatchDriftCount++
					if len(h.BatchDrift) < 10 {
						h.BatchDrift = append(h.BatchDrift, fmt.Sprintf("%s: shelf %d milli vs lots %d milli", name, proj, lots))
					}
				}
			}
		}
		rows.Close()
	}
	_ = s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(b.qty_remaining_milli * b.cost_cents / 1000),0)
		FROM product_batches b
		WHERE b.expiry_date != '' AND b.expiry_date < date('now')
		  AND b.qty_remaining_milli > 0 AND b.status != 'RECALLED'`).Scan(&h.ExpiredBatches, &h.ExpiredValueCents)

	// Newest backup snapshot.
	if p := s.db.Path(); p != "" {
		dir := filepath.Join(filepath.Dir(p), "backups")
		if ents, err := os.ReadDir(dir); err == nil {
			names := []string{}
			for _, e := range ents {
				if !e.IsDir() && strings.HasPrefix(e.Name(), "pos-backup-") && strings.HasSuffix(e.Name(), ".db") {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			h.BackupCount = len(names)
			if len(names) > 0 {
				h.LastBackupFile = names[len(names)-1]
				if st, err := os.Stat(filepath.Join(dir, h.LastBackupFile)); err == nil {
					h.LastBackupAt = st.ModTime().UTC().Format("2006-01-02T15:04:05Z")
				}
			}
		}
	}
	return h
}
