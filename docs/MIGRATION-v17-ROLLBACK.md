# Migration v17 rollback plan

v17 creates the `quotes` table (a brand-new table, no existing table is
altered, no data moves, nothing is dropped) and unions the two new
permission keys (`quotes.view`, `quotes.manage`) into seeded roles.

## What v17 creates

```sql
CREATE TABLE IF NOT EXISTS quotes (...);
CREATE INDEX IF NOT EXISTS idx_quotes_status / idx_quotes_customer / idx_quotes_created ON quotes(...);
```

Plus `backfillRolePermsV17` (union-only): adds the two quote permissions to
Admin/Owner/Front Desk/Branding/Cyber/Designer/Cashier IF missing. Union
semantics: existing custom roles are untouched; no permission is ever
removed.

## Preconditions (check before migrating production)

1. Backup exists: Settings → System → Backups (or
   `POST /api/v1/system/backup`), file listed under
   `data/backups/` with today's date.
2. Real staff accounts intact: `SELECT id, username, is_active FROM users;`
   matches expectations (v17 touches nothing here, this is the standard
   pre-migration gate per the client's standing order).
3. Historical sales intact: `SELECT COUNT(*) FROM orders;` recorded before
   upgrade (v17 touches nothing here either).

## Rollback (only if v17 itself misbehaves)

v17 is purely additive; the one-statement rollback is:

```sql
DROP TABLE IF EXISTS quotes;
```

Role permissions: leave them, extra permissions on seeded roles are
harmless and re-running v17's backfill is a no-op (union-only,
`schema_migrations` records the version so it never runs twice).

## Cloud-side note

v17 does NOT change the cloud schema. The v1.3.0 cloud changes live in
`db/cloud_schema_v117.sql` (join resurrect, PIN-reset RPCs) and are
individually reversible by re-applying the previous function definitions
(`create or replace function` semantics, the old bodies are in git
history at db/cloud_schema.sql@v1.2.1). The `sync_pin_resets` table can be
dropped with no local impact (in-flight requests would simply error
cleanly).

## Verification after upgrade

1. App boots, Settings → System shows no migration errors.
2. Create a quote → appears in the list with number QT-000001.
3. On a second synced till: the same quote appears within ~20s (one sync
   tick).
4. Existing orders/reports/customers unchanged.
