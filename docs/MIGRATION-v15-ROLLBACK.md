# Migration v15 — Rollback Plan (ERP depth: eTIMS invoice fields)

**Applies to:** every production till upgrading to v1.2.0.
**Migration:** `internal/database/migrations.go` → `Version: 15`.

## What v15 changes

Schema (additive only — no data moves, no rewrites, no drops):

```sql
ALTER TABLE orders ADD COLUMN buyer_pin TEXT NOT NULL DEFAULT '';
ALTER TABLE orders ADD COLUMN invoice_number TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_orders_invoice ON orders(invoice_number);
```

Behavior changes shipped alongside (none of them touch existing rows):

- Checkout writes `buyer_pin` only when the cashier enters one (new,
  optional rail field that only appears when the shop sets a KRA PIN in
  Settings). `invoice_number` is stamped with the order number for every
  new order — existing orders keep `''` and keep working everywhere
  (the read path is `COALESCE(...,'')`).
- New `settings` key `kra_pin` (default `''`).
- PO receive now emits `stock` delta events to the team (previously the
  receive was local-only). Harmless on single-till shops.
- Shift X/Z report endpoints added (`GET /shifts/report`,
  `?shift=id`), `GET /reports/daily?shop=all` consolidated view.

## Pre-migration guarantees (already enforced by the app)

- Every v1.2.0 boot runs the **daily auto-backup** (`VACUUM INTO`
  snapshot, `backup_keep` retained) BEFORE upgrades apply on the next
  start — verify a snapshot newer than the upgrade exists in
  `Settings → System → Backups` after updating.
- `schema_migrations` records the version, so v15 runs **exactly once**;
  a second boot re-runs nothing.
- Staff accounts and historical sales are untouched: v15 contains no
  user/order/statement DML — only new nullable-default columns.

## Rollback procedure (if you must return to v1.1.x)

1. **Back up first.** Copy the latest `pos-backup-*.db` snapshot off the
   machine (Settings → System → Backups shows the folder).
2. Downgrade the app to the previous installer (v1.1.10 release asset).
   The v1.1.x binary opens a v15 database fine: it ignores the two new
   `orders` columns (SELECTs are column-named) and keeps
   `schema_migrations` at 15 — it will not attempt to re-run anything.
   **No rollback is required to run the old app.**
3. Only if you want the schema physically back to v14 (e.g. a migration
   audit), run once in the sqlite shell:

```sql
BEGIN;
DROP INDEX IF EXISTS idx_orders_invoice;
-- SQLite cannot DROP COLUMN on old versions; 3.35+ supports it:
ALTER TABLE orders DROP COLUMN buyer_pin;
ALTER TABLE orders DROP COLUMN invoice_number;
DELETE FROM schema_migrations WHERE version = 15;
COMMIT;
```

   Rollback risk assessment: none of the above touches user rows besides
   the two empty-by-default columns; `invoice_number` values (order
   numbers, regenerated on next checkout) are the only data discarded.

## Verification after upgrade (30 seconds)

1. Log in — real staff accounts still exist (no demo accounts appear).
2. Orders → open any historical sale — totals, payments, receipts render.
3. Reports → Daily — numbers match the day before the upgrade.
4. Settings → System → Backups — a fresh snapshot exists.

If any of those fail: restore the pre-upgrade snapshot from step 1 of the
rollback procedure, then re-run the v1.2.0 installer.
