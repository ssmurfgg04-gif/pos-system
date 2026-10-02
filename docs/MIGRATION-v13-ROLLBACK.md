# Migration v13 — Demo-Account Purge: Backup, Rollback & Verification Plan

## What v13 does

1. Creates the `recovery_codes` table (single-use owner recovery codes).
2. **Disables installer-seeded demo accounts** (`admin` / `cashier` /
   `designer`) — and ONLY those whose password **and** PIN still verify
   against the historical public seed values (`admin123` / `1234`, …).
   An account whose credentials were ever rotated belongs to a real
   person and is left completely untouched.

Accounts are **deactivated (`is_active = 0`), never deleted**, so:

- every historical sale keeps its cashier reference (`orders.cashier_id`),
- shifts, ledger entries and audit history stay intact,
- receipt rendering is unchanged.

Every disable action is journalled to `audit_log` with the action
`DEMO_ACCOUNT_DISABLED` (and `DEMO_ACCOUNT_REENABLED_FOR_SETUP` if the
zero-login guardrail ever fired).

## Backup taken before migration

The migration runs automatically on first boot after upgrade. Before
upgrading a production till:

1. Settings → System → **Back up now** (local snapshot), and/or
2. Settings → System → **off-site backup** (encrypted snapshot to the
   cloud bucket), and/or
3. plain file copy: close the app and copy the data directory
   (`pos.db` + `secret.key`).

Because the migration is a row-level flag flip with a journal, the file
copy alone is a complete rollback artifact.

## Rollback plan

### Option A — surgical (recommended; uses the v13 journal)

The journal names exactly which user ids were disabled. Re-enable them:

```sql
-- Inspect what v13 disabled (runs anywhere SQLite is available):
SELECT id, username, created_at, details FROM audit_log
WHERE action = 'DEMO_ACCOUNT_DISABLED';

-- Restore a specific account (replace <id>):
UPDATE users SET is_active = 1 WHERE id = <id>;
```

### Option B — restore the pre-upgrade database file

Close the app, replace `pos.db` with the backup copy taken before the
upgrade, restart. `schema_migrations` rolls back with the file, and the
app re-runs nothing newer than what that file contains.

## Explicit verification after upgrade (production checklist)

Run on the till after the first boot post-upgrade:

1. **Real staff accounts intact**
   `People` page lists every expected active staff member; each can log
   in with their (rotated) password or PIN.
2. **Historical sales intact**
   `Orders` shows the same order count as before the upgrade; open any
   pre-upgrade order — cashier name, items and payment rows render.
3. **Reports match**
   Daily summary totals equal the pre-upgrade figures (same date).
4. **Demo accounts gone from login**
   `admin` / `cashier` / `designer` with the public passwords are
   rejected; PIN pad lists no demo staff.
5. **Audit journal present**
   Settings → Audit shows one `DEMO_ACCOUNT_DISABLED` row per disabled
   account (the rollback reference).
