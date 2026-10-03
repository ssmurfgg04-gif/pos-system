# Migration v16 rollback plan

v16 adds two nullable-by-default text columns. No data moves, no rows are
rewritten, no tables are dropped — the rollback is one statement per column.

## What v16 creates

```sql
ALTER TABLE products ADD COLUMN image_synced_at TEXT NOT NULL DEFAULT '';
ALTER TABLE customer_ledger ADD COLUMN synced_at TEXT NOT NULL DEFAULT '';
```

- `products.image_synced_at` — image-sync bookkeeping: rows whose current
  photo bytes are already in the sync outbox (prevents echo + re-emission).
- `customer_ledger.synced_at` — ledger rows already pushed as sync events
  (emit-once for checkout charges/redemptions and completion earns).

## Safeguards

- Both columns are pure bookkeeping: every reader treats `''` as the
  unmarked state. Deleting their contents is always safe.
- **Real staff accounts and historical sales are untouched** — the migration
  writes to no user/order/payment/customer rows.

## Rollback

```sql
ALTER TABLE products DROP COLUMN image_synced_at;
ALTER TABLE customer_ledger DROP COLUMN synced_at;
```

(Run inside a transaction with a backup taken first, same as any migration.
On Postgres use `DROP COLUMN IF EXISTS`.)

Consequence of rollback: the image backfill re-emits every photo once and
the checkout/completion ledger emit-once guard resets — one redundant event
per row until the columns are restored. No money or data is lost: the sync
apply path is idempotent per event.
