# Operations Guide — Backup, Restore, Recovery, Security

The practical runbook for the owner: what protects the data, how to get it
back, and the security posture of the whole fleet. Reference material:
`docs/REMOTE-OWNER.md` (remote oversight), `docs/DEPLOYMENT.md` (release
train), `docs/ACCESS-CONTROL.md` (who can reach what).

## What exists and where

| Layer | What | Where |
|---|---|---|
| Live data | Each till's SQLite (`pos.db`), WAL mode | till disk, per-OS user data dir |
| Local backups | Daily automatic `VACUUM INTO` snapshot, `backup_keep` days retained | till: Settings → System → Backups |
| Offsite backups | AES-256-GCM encrypted snapshot uploaded to Supabase Storage daily, `offsite_keep` days | cloud bucket (per-shop keys, enforced unique per shop) |
| Cloud state | sync_events, devices, invites, payment callbacks, OTA manifest keys | Supabase project (RLS on all tables; anon gets reads of the OTA keys only) |
| Audit trail | Every money-affecting action with actor + details | local `audit_log` per till |

Offsite uploads use a per-shop random key (`offsite_secret_key`); the key
never leaves the settings vault unencrypted. Restore requires the same
key — the owner keeps a copy (password manager), because a till that dies
WITH its key makes its own offsite backups unreadable.

## Day-to-day checks (2 minutes, weekly)

1. Settings → System on the main till: **last backup** within 24h,
   **offsite** shows a recent successful upload, sync shows no growing
   failure count.
2. Reports → daily reconciles (sales == tenders in the same window; the
   by-till table sums to the day total).
3. Glance at the audit log after any staff change or oddity.

## Restore (single till lost / disk replaced)

1. Install the app on the replacement (GitHub Releases; see DEPLOYMENT).
2. Either sign in and let the cloud sync rebuild products/customers, or
   restore a snapshot: `ledgerpos restore-backup <file>` (CLI subcommand,
   documented in `main.go` usage) pointing at the newest offsite/local
   backup. You need the offsite key if the snapshot came from the bucket.
3. Verify: Settings → System (sync green), Reports → daily matches the
   last known good day, one test STK payment.

## Disaster drill (do it twice a year)

The drill in `docs/REMOTE-OWNER.md` §4 is the script: download the newest
offsite backup, restore it into a scratch install, confirm the books open
and a checkout works, then discard. Time it — if it takes more than 30
minutes, the runbook is out of date.

## Till dies mid-sale (crash / power cut)

- SQLite is crash-safe (WAL): the app reopens to the last committed
  state. A sale that was mid-commit either fully exists or fully does not.
- Queued offline sales live in the browser's IndexedDB and replay on next
  boot (idempotent — no double sales).
- An interrupted STK payment surfaces as a PENDING order; the claim sweep
  or **Check payment status** completes it when the money actually moved.

## Owner lockout recovery

- Forgot password: the login screen's reset path (one-time code via a
  device already signed in, or `ledgerpos reset-owner` from the CLI on the
  till — requires physical access, by design).
- App will not boot: install the previous version from Releases (the OTA
  rollback path in `internal/services/update_rollback.go` also stages the
  last good version), then report the bug.

## Security posture (what an attacker gets)

- **Till API binds 127.0.0.1** in desktop mode; the LAN listener exposes
  only `lan/whoami` + PIN-reset code handoff. Appliance/serve mode exposes
  the full API to the LAN with JWT+RBAC — run it on a trusted shop network
  only.
- **Cloud**: anon key holders can read the OTA manifest keys and nothing
  else (RLS deny-all + revoked grants; TRUNCATE revoked explicitly).
  Payment secrets live in Edge Function secrets; Buni keys never touch
  tills.
- **Sales privacy (v1.4.4)**: individual sale records require
  `orders.view.all` (Admin/Owner). Every sale is attributed to its till +
  cashier. WS broadcasts carry no line-item/total detail to non-admin
  clients.
- **Known limits**: the local SQLite file is unencrypted (full-disk
  encryption on the till OS is the practical control), sync replicates all
  tills' orders to every till's disk (staff can only READ their own via
  the API; a stolen till is a physical-data risk), and the OTA signing key
  is the crown jewel — it lives only in GitHub secrets + the owner's
  offline copy.
- **Rotation**: rotate the Supabase anon/service keys and the OTA signing
  key per `docs/DEPLOYMENT.md` (key rotation runbook); rotate staff
  credentials by deactivating + re-creating users (history stays).

## Incident cheat-sheet

| Incident | First move |
|---|---|
| Suspected stolen till | Deactivate its device in cloud settings; rotate JWT secret if staff PINs may be known; restore its data elsewhere from offsite |
| Wrong refund/void | Audit log shows actor+time; void reversals are audited too — fix forward, never edit history |
| Suspicious payment leg | Stop using manual entry for that order; check the callback event in the cloud; report to the owner |
| Ransomware/garbled DB | Do NOT run the app again on the corrupt file; restore from offsite into a fresh install |
