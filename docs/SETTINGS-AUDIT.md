# Settings & Secrets Audit — v1.3.1

The classification pass behind the "hide secrets, keep capability" hardening.
Every settings key was traced: where it is read, saved, encrypted, consumed,
migrated, and synchronized. Nothing was deleted — presentation and access
changed, capability did not.

## 1. Classified inventory

### A. Business settings (owner/admin, kept simple and visible)

| Keys | Purpose | Sync |
|---|---|---|
| `app_name`, `store_name`, `store_address`, `store_phone`, `brand_color`, `brand_logo` | White-labeling | team-synced |
| `receipt_footer`, `receipt_logo`, `auto_print_receipts` | Receipt behavior | footer/logo synced |
| `currency_symbol`, `currency_code`, `tax_percent`, `tax_included` | Money display + VAT math | team-synced (tax re-validated on apply) |
| `low_stock_threshold`, `credit_enabled`, `crm_auto_capture` | Operational behavior | team-synced |
| `loyalty_enabled`, `loyalty_earn_per_cents`, `loyalty_point_cents`, `loyalty_max_percent` | Loyalty program | team-synced |
| `till_number`, `paybill_number`, `payment_mode` | What customers see at checkout | team-synced |
| `kra_pin` | eTIMS tax invoices | local (identity-ish, per shop) |
| `backup_auto`, `backup_keep` | Daily snapshot schedule | local |
| `whatsapp_ready_note`, void-reason catalog | Front-desk workflows | synced / catalog synced |

### B. Secret credentials (server-side only, never echoed)

| Keys | Where it lives | Handling |
|---|---|---|
| `paystack_secret_key` | cloud vault → till settings store, AES-256-GCM at rest | masked `__SET__` in every API response; cloud RPC gated by device identity |
| `sync_service_key` | legacy manual sync (unused in cloud mode) | masked; not synced |
| `offsite_secret_key`, `offsite_passphrase` | per-machine, encrypted | UI shows "Configured ✓" + replace/remove; hidden without the `?infra` support unlock |
| `jwt_secret` | machine-local auth root | NEVER returned by any API, never API-writable, never encrypted (raw-SQL boot read) |
| `mpesa_consumer_secret`, `mpesa_passkey` | future Daraja | masked by the secret-class rule (`secret`/`passkey`/`passphrase`/`consumer_key` substrings) |

Rules enforced (v1.3.1):
- GET never returns a secret value — `__SET__` mask or empty.
- Save with `__SET__` echo → keeps the stored secret.
- Save with `""` → **keeps** the stored secret (an accidental clear can
  never silently break Paystack at checkout). Removal is explicit:
  `__CLEAR__` (the UI's "Remove saved key" button).
- New value → replaces, encrypted before it touches disk.
- Secrets are excluded from team-sync payloads (`configSyncWhitelist`) and
  from the audit log (only key names are logged).

### C. Public configuration (safe to expose, kept readable)

| Keys | Notes |
|---|---|
| `paystack_public_key` | publishable key — checkout popup uses it |
| `paystack_callback_url`, `paystack_currency`, `paystack_mode`, `paystack_enabled` | non-secret integration state |
| `update_manifest_tag/notes/assets` | OTA self-update manifest |
| branding payload (`/settings/branding`) | pre-auth login screen needs it — explicitly secret-free |

These ride the anon-readable `app_config` rows (RLS: `is_secret = false`).
The anon read path is verified in CI-style checks (Paystack secret must
never appear — confirmed live 2026-10-13).

### D. Operational status (read-only, plain language)

| Endpoint | Shows |
|---|---|
| `/payments/config` | Connected / waiting-for-keys / disabled + route (Paystack STK, Daraja, mock) — booleans and public values only |
| `/system/offsite` | enabled, queued uploads, last upload time, last error |
| `/system/update` + `/system/backups` | staged version, backup history |
| `/team-sync` | registered/approved/pending, last push/pull, per-device roster |

### E. Developer / infrastructure details (restricted)

| Item | Restriction |
|---|---|
| Off-site destination (project URL, bucket, prefix, service key) | visible only with the `?infra` support unlock while signed in as Admin |
| `sync_endpoint` / `sync_service_key` (legacy manual mode) | kept for backward compat; cloud identity is the only documented path |
| Raw DB errors (`err.Error()` in some admin endpoints) | acceptable: admin-gated surfaces where the detail helps the owner fix a problem; no secret material in any audited error path |

## 2. What changed in v1.3.1 (and what deliberately did not)

**Changed**
1. Secret save semantics: empty field preserves (was: erased); explicit
   `__CLEAR__` sentinel removes (Go store + demo backend + UI button).
2. `SecretField` UI: never pre-fills a secret, shows "Configured ✓",
   type-to-replace, confirmed "Remove saved key"; passphrase hint rewritten
   in plain language (no `__SET__` dev-speak on screen).
3. Cloud vault blast radius: `sync_device_config` now returns ONLY
   `paystack_*` + `update_manifest_*` keys — device approval can never leak
   unrelated/future infrastructure credentials to every till.
4. Canonical `sync_register` repaired (stray `inv.id` block removed) — fresh
   provisioning from `cloud_schema.sql` works again; deployed live.
5. Privilege-escalation guards: authority keys (`users.manage`,
   `settings.manage`, `roles.manage`) can only be granted by callers who
   already hold them (role create/edit + user create/assign), and the last
   owner-level role cannot be stripped while it is the only owner source.
6. `lastOwnerGuard` fails CLOSED on database errors (was fail-open), while
   "user not found in this shop" still 404s.
7. Rate limiter periodically prunes abandoned keys (memory bounded under
   username-spray brute force).

**Deliberately NOT changed**
- No settings were removed; the Team panel, printer, backups, KRA returns,
  loyalty, and every business workflow kept their exact behavior.
- Paystack flow untouched (keys still arrive from the vault at boot; the
  STK path is byte-identical — verified by the full payments test battery).
- The `?infra` gating of infrastructure config stays (it is the right
  split: support engineers, not shop staff).
- Manual sync mode stays as a legacy fallback but is no longer advertised.

## 3. Remaining risks (honest list)

1. The vault still delivers the Paystack secret to every approved till —
   necessary today (any till can take payment), and revocation cuts access
   instantly. If per-till payment scoping is ever wanted, the RPC allowlist
   is the place to extend (e.g. per-device key grants).
2. Daraja keys do not exist yet; when KCB/Daraja is added, the
   secret-class matcher already covers `consumer_secret`/`passkey`, but the
   vault allowlist in `sync_device_config` must deliberately include any
   new till-needed key family (e.g. `daraja_%`).
3. Some admin endpoints still return raw driver error strings (helpful for
   the owner, ugly for a mom). A future pass could map common SQLite errors
   to friendly sentences without hiding the cause from the audit log.
