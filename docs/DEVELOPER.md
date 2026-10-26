# Developer Guide

Index for working on LedgerPOS. The deep documents it points to are the
source of truth; this file is the map and the quick-setup path.

## Stack at a glance

- **Backend**: Go (1.24), Gin, single binary per OS. Local SQLite per till
  (pure-Go `modernc.org/sqlite`, WAL) with a Postgres dialect used by CI.
  Embedded frontend (`//go:embed all:frontend/dist`), WebView2 window on
  Windows.
- **Frontend**: Vite + React + TypeScript + Tailwind; zustand stores; the
  in-browser demo backend (localStorage) for the download site.
- **Cloud**: Supabase — PostgREST + SECURITY DEFINER RPCs (device-gated)
  for sync, Edge Functions `buni-stkpush`/`buni-callback` as the public
  payment surface, Storage for encrypted offsite backups.
- **Payments**: KCB Buni M-Pesa Express STK via the Edge Functions
  (callback-driven, fail-closed amount gate). Paystack/Daraja retired
  v1.4.4; mock provider gated behind ALLOW_MOCK_PAYMENTS.

## Setup

```bash
# Go 1.24+, Node 24
cd frontend && npm ci && npm run build   # frontend/dist must exist for go build
go build ./...                            # desktop binary (Windows: go-winres for icons)
go test ./...                             # full backend suite
cd frontend && npx vitest run             # frontend suite
```

Env vars (none have secret values in the repo — see `internal/config/config.go`
for the parsed list):

| Var | Effect |
|---|---|
| `PORT` / `LEDGERPOS_PORT` | serve-mode bind port (desktop mode picks 8765–7914 on 127.0.0.1) |
| `ALLOW_MOCK_PAYMENTS=true` | enables the auto-succeeding mock M-Pesa (tests/demos only) |
| `ALLOW_SIGNUP=true` | self-serve owner signup on the login screen |
| `CORS_EXTRA_ORIGINS` | extra browser origins for appliance deployments (the Android client origin is allowlisted already) |
| `POSTGRES_TEST_DSN` | runs the Postgres-dialect test job locally |
| `LEDGERPOS_UPDATE_PRIV_KEY` | OTA manifest signing key (ed25519 seed; CI secret — never on tills) |
| `CLOUD_SYNC_*` | cloud link settings are normally set via Settings/vault, not env |

## Where things live

| Area | File(s) |
|---|---|
| Route table / middleware / CORS / CSP | `internal/router/router.go` |
| Checkout, completion, void (money paths) | `internal/services/orders.go` |
| Buni STK + claim sweep + recheck | `internal/services/payments_buni_cloud.go`, `internal/mpesa/buni.go` |
| Provider factory + route priority | `internal/services/services.go` |
| Sales visibility scoping (v19) | `internal/services/queries.go` (`canViewAllOrders`, `GetOrderScoped`, `AssertOrderVisible`) |
| Permissions catalog + seeded roles | `internal/models/permissions.go` |
| Migrations | `internal/database/migrations.go` (current head **v19**) |
| Sync emit/apply | `internal/services/teamsync*.go` |
| Offline queue/heartbeat | `frontend/src/offline/` |
| Tender UI / payment lifecycle | `frontend/src/components/CheckoutRail.tsx` |
| Android client | `frontend/android/` + `frontend/capacitor.config.ts` + `.github/workflows/android.yml` |
| Edge functions | `supabase/functions/buni-stkpush`, `buni-callback` |
| Cloud schema (tills' RPCs, RLS) | `db/cloud_schema*.sql`, `db/deploy-*.sql` |

## Rules the codebase enforces

1. **Money never trusts the client.** Completion is callback/claim-driven
   with a fail-closed amount gate; the only PENDING→PAID transition is
   `completePayment` (single-winner, discrepancy-flagging).
2. **Sales visibility**: without `orders.view.all` every order read is
   scoped to `cashier_id = caller`. Route new order-reading endpoints
   through `GetOrderScoped`/`AssertOrderVisible`, never raw `GetOrder`.
3. **Single-connection SQLite discipline**: never issue a query while rows
   are open; the deadlock rules live in `docs/DEADLOCK.md` and the header
   of `internal/database/database.go`.
4. **Permissions are server-enforced** — every route gets `perm(...)`;
   the frontend mirrors the catalog from
   `frontend/src/demo/permissions.catalog.json` (regenerate it when
   `PermissionCatalog` changes; a test fails the build if they drift).
5. **Demo never masquerades as real**: the SPA falls back to the demo
   backend ONLY when no API is reachable and no till address is set — and
   never in the APK.

## Migrations

Add to the `migrations` slice (`internal/database/migrations.go`) with a
one-line comment explaining WHY. `SQLite` runs on tills; provide `Pg`
when the SQL differs. Go migrations (role unions, backfills) run inside
the same tx. Test upgrades from a v-old database (see
`internal/handlers/persistence_probe_test.go` for the pattern) and bump
the head-version assertion there.

## Tests that guard the interesting invariants

- `internal/handlers/sales_visibility_test.go` — the v19 scoping rules.
- `internal/services/payments_buni_test.go` — STK lifecycle incl. the 4999
  cooldown, friendly duplicate-session errors, callback fixtures
  (0/1032/1037/2001), forged-amount refusal, exactly-once claims.
- `internal/handlers/adversarial_probe_test.go` — forged callbacks,
  escalation, void-exactly-once.
- `internal/handlers/mpesa_amount_gate_test.go` — fail-closed amounts.
- `internal/settings/crypt_test.go` — secret masking/encryption rules.
- `frontend/src/__tests__/` — money golden fixtures shared with Go,
  offline queue, cart, headers.

## Integration contracts

- **Buni push**: till → `buni-stkpush` Edge Function (device identity +
  vault key) → KCB. Response `checkout_request_id` is stored
  BUNI-prefixed on the payment leg.
- **Buni callback**: KCB → `buni-callback` (token-gated, always 200) →
  event row (team-scoped) → till's `buni_claim_events` RPC claims
  exactly-once → `HandleCallback`.
- **Sync**: outbox rows → `sync_push`; `sync_pull` returns the team's
  events except the device's own; apply functions are idempotent per
  `client_uuid`.
- **OTA**: release train signs `ota-assets.json` (ed25519) → tills verify
  the manifest against the embedded public key before applying.

## Deployment

Release train, OTA signing/rotation, Netlify download site, and the
Android client build are documented in `docs/DEPLOYMENT.md`. Cloud
credentials/secrets handling (what lives where, what to rotate) is in
`docs/ACCESS-CONTROL.md` and `docs/OPERATIONS.md`.
