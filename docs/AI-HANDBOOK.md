# LedgerPOS AI Handbook, read this before touching anything

The single orientation doc for any AI (or human) continuing work on this
repo. Everything here was learned the hard way; skim it top to bottom.

## What this project is

LedgerPOS, a local-first retail/services POS for Creative Divine Concepts
LTD (Kiambu Main): retail, cyber, branding, designer services. Two runtimes
from one Go binary:

- **desktop** (default): a self-contained till. Go + Gin serving an embedded
  React SPA on localhost, SQLite per shop, go-webview2 shell (not Electron) around it,
  auto-updates via OTA manifests.
- **server**: the same binary multi-shop (`SHOPS_DIR` + tenancy registry).

Live surfaces: tills run the desktop app; https://awesomeposs.netlify.app
is the download/demo site, **Netlify deploys when the repo's NETLIFY_* secrets are set (CI warns on the run summary when the live site is stale) whatever is
pushed to GitHub main, no secrets needed** (push = deploy; oversized
artifacts in a commit are the classic way deploys silently fail, keep
installers out of any push unless they belong there, see the downloads/
note in README).

## The three data planes (do not mix them)

1. **Local SQLite** (`shops/<id>.db`): products, orders, customers, users,
   quotes, held sales… Users and PINs are LOCAL ONLY, they never sync.
   Anything that must "appear on the other till" is a sync event, not a
   table you forgot.
2. **Cloud sync fabric** (Supabase Postgres, project ixxiqrobcwkvyjtxdkvh):
   `sync_events` partitioned by team_code. Identity is (device_id,
   secret_hash) validated by SECURITY DEFINER RPCs on EVERY call. Anon
   reads only sync_stores + sync_bootstrap + app_config (OTA manifest).
   Secrets (Paystack sk) live in app_config vault, served only to
   approved+non-revoked devices via `sync_device_config`.
3. **AuthN/Z**: 4-digit PIN (bcrypt, lockout) + password + recovery codes +
   cloud one-time-code PIN reset (v1.3.0) + join links + owner cloud
   sign-in. Permissions: `internal/models/permissions.go` is the catalog;
   seeded roles union-in new keys via a migration `Go:` step (v17 pattern)
  , new permission = catalog entry + SeededRolePermissions + backfill
   migration, or existing installs never get it.

## Sync rules that will bite you

- **Emit pattern**: `s.Emit(entity, op, payload)` → `sync_outbox` → pushed
  every 20s. Apply side: `applyEvent` switch in teamsync.go. Add an entity =
  emit + apply + (for docs) `quoteEvent`-style wire struct. Insert-by-
  client_uuid exactly once; LWW by fixed-width `updated_at` string compare;
  numbers (QT-/ORD-) are per-till and RENUMBER on collision with a random
  suffix at apply time.
- **Timestamps**: EVERYTHING app-written uses `nowStamp()` =
  `2006-01-02T15:04:05.000Z` fixed width, string compares are the LWW and
  shift-window logic. Never write CURRENT_TIMESTAMP into a column that
  participates in a string compare (mixed formats silently break ordering).
  Window end-paddings parse with `nowStampLayout`.
- **Money on receivers**: stock moves ONLY via stock-delta events; ledger
  moves ONLY via emitLedgerForOrder (emit-once via synced_at). applyVoid
  recomputes reversals locally (tab, points refund, credit refund, loyalty
  earn reversal), never emit reversal rows.
- **Team changes quarantine**: a till joining a DIFFERENT team code must
  call `QuarantineOutboxForTeamChange`, pending events would otherwise be
  stamped with the new team (cross-tenant leak). v1.3.0 rule.
- **Revoked devices**: cut off from every RPC, but the three owner-consented
  acts heal them (valid join token, valid portal credentials, owner
  Approve). Revocation is a gate, not a brick, that was the Salma-PC field
  incident and it's permanent design now.

## The release chain (v1.3.0 runbook)

1. Bump version: `scripts/bump-version.sh X.Y.Z` (fixes up version.go,
   package.json, electron builder files, read the script if it changes).
2. `cd frontend && npm run build` (embed needs `frontend/dist`).
3. Build 4 installers (windows NSIS + linux deb/AppImage/tar), scripts/
   has the builders. **NSIS check**: the Windows installer must be ~8MB
   (solid LZMA with the real app); a ~30MB exe means makensis was missing
   and the portable binary got shipped instead (v1.2.1 root-caused this;
   `MAKENSIS` env overrides discovery).
4. Commit + push main → Netlify auto-deploys (verify the deploy goes READY
   and downloads/ serve the new checksums, content-length check).
5. GitHub Release with the 4 installers + checksums.txt.
6. **Cloud push**: apply `db/cloud_schema_vNNN.sql` via the Supabase
   Management API (the sbp_ key the owner provides, treat as ephemeral,
   never commit). Then publish the OTA manifest row in app_config
   (version, SHA-256 per artifact, download URLs). Verify with an anon
   PostgREST read of app_config, that's the exact read tills perform.
7. Backups before ANY production migration: see docs/MIGRATION-v17-ROLLBACK.md
   (pattern applies to every version).

## Cloud schema versioning

`db/cloud_schema.sql` is the canonical whole-schema dump (later
create-or-replace wins). Each release adds `db/cloud_schema_vNNN.sql` with
ONLY that release's delta, annotated. Apply deltas live via Management API
SQL. Tables the tills never see get `revoke all ... from anon, authenticated`.

## Testing conventions

- Go: table of probe test files per phase (p0_, p6_, agent2/3_). Services
  tests build a `Service{db, settings, appVersion}` on a temp-dir SQLite
  (see quotes_probe_test.go). Migrations tests pin the head version, bump
  the number when you add a migration (agent2_persistence_probe_test.go).
- Frontend: vitest (95 tests), `npx tsc --noEmit` gate, `npm run build`
  gate. Demo mode parity lives in `frontend/src/demo/backend.ts`, a new
  API route without a demo handler 404s on the demo site; either add it or
  throw a clean 501.
- The services suite had a clock-jitter flake (sandbox VMs step the clock
  backward), if a timing-window test fails only in full-suite runs, think
  skew before thinking logic.

## Field-support quick answers (the things owners actually hit)

- "Reinstalled and still locked out / device revoked" → %APPDATA% survives
  reinstall; mint a fresh join link (it resurrects the device) or owner
  sign-in. v1.3.0.
- "Cashier forgot the PIN" → PIN pad → Forgot PIN? → owner approves in
  Settings → Team → PIN reset requests → read the 8-digit code.
- "Images don't sync across machines" → image sync events (v1.2.1) with
  backfill; if photos still missing check sync_dead_letter for
  product/image events.
- "Till didn't update" → Settings → System → check for updates; manifest
  verification failures are usually a SHA-256 mismatch with the GitHub
  release asset.

## Repo map (start here)

- `main.go` / `desktop.go`, wiring, `reset-owner` CLI.
- `internal/services/`, the business logic (orders, payments_paystack,
  teamsync, quotes, pinreset, teamjoin, ownersignin, imagesync, shifts,
  reports, suppliers, stocktake, backup, update).
- `internal/handlers/` + `internal/router/router.go`, HTTP surface; every
  route's permission gate is visible in one file.
- `internal/database/migrations.go`, the local migration chain (head v17).
- `db/`, cloud schema + deploy SQL per release.
- `frontend/src/pages/`, Login/Pin/Join (auth surface), Pos (the till),
  Quotes (v1.3.0), Settings (Team/Payments/System panels).
- `docs/`, ERP-CRM-RESEARCH (7-phase roadmap), SAWAPOS-COMPARISON,
  rollback plans, REMOTE-OWNER/SETUP.

## Non-negotiables

- No secrets in the repo (GitHub push protection blocks sk_live; cloud SQL
  ships placeholders, live keys go in via Management API only).
- Any production DB migration or demo purge: backup first, write the
  rollback doc, verify real staff accounts and historical sales survive.
- Money transitions are single-step guarded UPDATEs (`AND status='...'`)
  with RowsAffected checks, keep it that way.
- Every public auth endpoint is rate-limited and audited; generic errors
  only (no username enumeration); timing-equalized unknowns.

## Secret save semantics (v1.3.1, do not "simplify" these)

- Secret-class keys (name contains secret/passkey/passphrase/consumer_key,
  or jwt_secret) follow: GET → `__SET__` mask; save `__SET__` → keep;
  save `""` → KEEP (accidental clear must never break Paystack); save
  `__CLEAR__` → remove; any other value → replace (encrypted at rest).
- The demo backend (frontend/src/demo/backend.ts) mirrors this exactly.
- `settings.Update`, `handlers.UpdateSettings`, `SecretField` in
  Settings.tsx and docs/SETTINGS-AUDIT.md are the sources of truth.
- Cloud vault (`sync_device_config`) returns ONLY `paystack_*` +
  `update_manifest_*`, add new till-needed key families there deliberately.
- Authority keys (`users.manage`, `settings.manage`, `roles.manage`) can
  only be granted by callers who already hold them (roles.go guards +
  escalation_test.go). Keep it that way.
