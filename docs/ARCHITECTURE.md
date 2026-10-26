# Architecture — LedgerPOS

How the system fits together: what runs where, what talks to what, and
where the trust boundaries are. Every claim here is anchored in the code
(file references are relative to the repo root).

## The three runtimes

**1. The desktop till (primary).** A Go binary (`main.go`) embedding the
built frontend (`all:frontend/dist` — see `main.go`). It owns the local
SQLite store (WAL, single pooled connection — the discipline is documented
in `internal/services/services.go` and `docs/DEADLOCK.md`), serves the SPA
on a LAN port, runs printers (`internal/printer`), the STK sweeper
(`internal/services/sweeper.go`), mDNS discovery (`internal/mdns`), LAN
delivery between tills (`internal/landelivery`), and the encrypted
offsite backup worker (`internal/offsite`). This is the runtime that
handles real money; the binary works fully offline.

**2. The fleet cloud (Supabase project `ledgerpos-backups`).** Three
jobs: team sync relay (`sync_*` Postgres RPCs over PostgREST), the
device-gated config vault (`sync_device_config` + `app_config`), and the
payment proxies — Paystack Edge Functions (`supabase/functions/paystack-*`)
and the Buni Edge Functions (`supabase/functions/buni-stkpush`,
`supabase/functions/buni-callback`). Holds NO business data beyond what
sync replicates; holds payment SECRET keys in Edge Function secrets,
never in tables reachable by tills.

**3. The download site (Netlify, `awesomeposs.netlify.app`).** Landing
page, the in-browser demo (mock M-Pesa only), owner portal, and
checksums. Installer binaries are NOT hosted here — they live on GitHub
Releases and the page links straight at them (`scripts/netlify_build.py`).

## Money flows

### M-Pesa STK — Buni route (MAIN)

```
till (Go)                        cloud                         KCB Buni
 │ buniMpesaSTK                   │                              │
 ├───────────────────────────────>│  POST /functions/v1/buni-stkpush
 │  device id + secret hash       │  (internal/services/payments_buni_cloud.go)
 │                                ├─────────────────────────────>│ OAuth (client_credentials)
 │                                │  BUNI_* are Edge secrets     ├──────────────────────────────>
 │                                │                              │ STK push (operation=STKPush)
 │                                │<─────────────────────────────┤ checkout_request_id
 │<───────────────────────────────┤                              │
 │  store "BUNI-ws_CO_…" on the   │  buni_pushes mapping row     │
 │  payment leg (prefixed)        │  (team_code, client_uuid)    │
 │                                │                              │
 │                          customer answers the prompt          │
 │                                │<─────────────────────────────┤ callback (Daraja envelope)
 │                                │  buni-callback (PUBLIC,      │
 │                                │  ?token= gate, constant-time)│
 │                                │  store in buni_callback_events│
 │ sweeper tick (5s)              │                              │
 ├───────────────────────────────>│  RPC buni_claim_events       │
 │  exactly-once per device,      │                              │
 │  team-scoped by the mapping    │                              │
 │<───────────────────────────────┤                              │
 │ mpesa.ParseCallback (Daraja shape — unchanged)                │
 │ HandleCallback: stored-amount gate (fail closed) → completePayment
```

Key properties:

- **Keys never touch the till.** The Buni consumer key/secret live in
  Edge Function secrets. The desktop binary is effectively public source;
  nothing in it can charge anyone.
- **Completion is callback-driven.** Buni exposes no STK query API
  (verified against the official spec and live production). The sweeper
  claims events instead of polling; a 3-minute timeout backstop fails
  silent legs (`SweepBuni`).
- **The relay cannot complete a sale by itself.** A stored event is only
  a claim candidate; `HandleCallback` (the same fail-closed gate as the
  Daraja public webhook) requires the callback amount to equal the amount
  stored server-side at push time. A forged success for KES 1 settles
  nothing (see `internal/handlers/mpesa_amount_gate_test.go`).
- **Team scoping is established at push time.** The claim RPC filters
  events by the `team_code` recorded on the device-authenticated push
  mapping — an event can never leak across teams.

### M-Pesa STK — Paystack route (FALLBACK)

`MpesaRoute()` priority: **buni → paystack → daraja → mock → manual**
(`internal/services/services.go`). Paystack legs ride
`paystack-init/-charge/-verify` Edge Functions with the same device
identity, complete through verify + the HMAC webhoook relay
(`internal/services/payments_paystack_cloud.go`).

### Daraja route (per-shop direct)

Shops with their own Safaricom credentials push directly from the till
(`internal/mpesa/daraja.go`); completion rides the public
`/api/v1/payments/mpesa/callback` endpoint (fail-closed amount gate) plus
the STK query sweeper. Buni legs are excluded from this sweeper by the
`BUNI-` checkout-id prefix.

### Manual receipt entry

Always available, even offline: the cashier types the 10-character
M-Pesa code (`mpesa.ValidateReceiptCode`), which completes through the
same guarded `completePayment`.

## Sync engine

Local-first with idempotent replication. Writes land in SQLite first;
an outbox (`Emit*` builders in `teamsync_emit.go`) queues wire events;
the sync loop pushes/pulls through PostgREST RPCs
(`teamsync.go`, `teamsync_apply.go`). Apply handlers are replay-tolerant;
`sync_events` dedupes; poison rows quarantine after 5 failed cycles.
Multi-store ownership (create store, assign/approve/remove devices) runs
through owner-capability-gated RPCs (`teamsync_devices.go`).

## OTA updates

The cloud manifest (app_config `update_manifest_*` rows) is the primary
channel; GitHub Releases is the fallback. **Manifests are ed25519-signed**
(`internal/update/signing.go`): the public key is compiled into every
binary, auto-OTA refuses unsigned/tampered manifests, downloads are
sha256-verified byte-for-byte, and installs wait for a quiet till
(`HasUnfinishedWork`). Owner tooling: `scripts/sign_manifest.go`.

## Trust boundaries

| Boundary | Trust model |
|---|---|
| Till → cloud | device identity (id + SHA-256 secret, approved + non-revoked) validated on every RPC and Edge call |
| KCB → buni-callback | shared URL token (constant-time), fail-closed when unset; the event alone cannot complete a sale |
| Paystack → paystack-webhook | HMAC-SHA512 verified server-side before storage |
| Owner dashboard/SQL → tills | manifest signature (ed25519) + per-asset sha256 |
| Browser → till API | Bearer JWT, permission-gated routes, strict CORS allowlist, CSP (`internal/router/router.go secureHeaders`) |

## Single-connection discipline

SQLite runs with one pooled connection. Never issue a query while rows
from another query are open — collect, close, then enrich. This is the
bug class the codebase is hardened against; see `docs/DEADLOCK.md` and
the comments in `completePayment` for the canonical example.

---

## Addendum — v1.4.4 sales visibility & attribution

- `orders.device_id` (migration v19) records the ORIGINATING TILL of every
  sale, stamped at checkout and carried through sync events. The daily
  report's by-till table aggregates it and reconciles to the orders list
  by construction.
- `orders.view.all` (Admin/Owner only) is the cross-till oversight
  permission. Every order-reading path — list, detail, receipt, events,
  notes, void, notify, customer history, held carts — scopes non-holders
  to their own sales through `GetOrderScoped`/`AssertOrderVisible`
  (`internal/services/queries.go`). WS broadcasts carry only
  `{id, number, status, cashierName, deviceId}`.
- The Paystack/Daraja rails retired; Buni M-Pesa via Edge Functions is
  the only STK rail (route priority: buni → mock → manual).
- The Android client (`frontend/android`) is a thin LAN terminal for a
  till in serve mode; its WebView origin is allowlisted in CORS.
