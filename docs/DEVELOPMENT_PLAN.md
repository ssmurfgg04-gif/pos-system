# Creative Divine Concepts — LedgerPOS Development Plan

> Status: recon complete (read-only). No code changed yet. Companion to `REVIEW-3-pos-system.md`.
> Verified against source on HEAD `af1eefb` — every claim below was re-checked, not copied.

## 1. Verified findings (re-checked vs source, 2026-10-22)

| Review claim | Verified | Where |
|---|---|---|
| `.gitignore` UTF-16LE corrupted, wrong patterns (`pos-app` vs `posapp`, `/resource_windows_*.syso` vs `rsrc_windows_amd64.syso`), `db/cloud_schema_v114.sql` ignored+tracked | ✅ | `.gitignore` (mojibake on read) |
| 40 MB `posapp` binary + installers committed | ✅ | `posapp` (40M), `downloads/` (36M) |
| CORS `*` on the Go server | ✅ | `internal/router/router.go` `corsMiddleware()` |
| `/auth/pin-users` public, unauthenticated | ✅ | `internal/router/router.go` (public list) |
| Public M-Pesa callback path + LAN sweeper re-queries Daraja | ✅ | `internal/handlers/orders.go:166` → `svc.HandleCallback`; `internal/mpesa/provider.go` `QuerySTK`; sweeper completes via re-query |
| `SEED_DEMO=true` in `.env.example` | ✅ | `.env.example` line 9 |
| JWT in `localStorage`, no CSP | ✅ | `frontend/src/lib/api.ts:111,139` |
| `teamsync.go` god file | ✅ | `internal/services/teamsync.go` = 2,279 lines |
| Agent-swarm artifacts as product | ✅ | `agent2_adoption_probe_test.go`, `agent5_adversarial_probe_test.go`, `docs/superpowers/` |

Netlify hosts **download site + in-browser demo (mock M-Pesa)** — `netlify.toml`. Real payments run on the Go server (LAN till), which is why KCB cannot reach it directly (see §3).

## 2. Architecture decision — callback standardisation (answers owner question)

**Recommendation: do NOT move POS payment callbacks to creativedivineconcepts.com.**

- KCB requires a public HTTPS `callbackUrl`. The POS's Go server is a LAN appliance — unreachable from KCB.
- Pointing LedgerPOS callbacks at CDC's site couples two products: a CDC redeploy could break client POS payments, and per-client isolation (multiple clients on this POS) becomes messy.
- **Standardise the code, not the URL.** Each product keeps its own endpoint. For LedgerPOS the natural public endpoint is a **Supabase Edge Function** — Supabase is already the cloud sync layer, gives free HTTPS, per-client isolation (RLS), and secret storage.

### Proposed KCB Buni flow on LedgerPOS (stage 3 of the owner's plan)

1. **Till creates order** → calls Supabase Edge Function `buni-stkpush` with its Supabase auth token (existing auth, no new login surface).
2. **Edge function holds Buni credentials as Supabase secrets** (per client). Keys never ship inside the Go binary or till env — desktop apps are effectively distributed source.
3. Edge function performs OAuth + STK push exactly like the proven CDC client (`buni.ts` port): 5-field payload, `invoiceNumber = till-orderref` ≤ 12, integer string amounts, `operation: STKPush`, `routeCode` header, cached token.
4. **Callback lands on Edge Function `buni-callback`** (also accepts Buni IPN schemas: till-notification / account-notification / validation with documented acks — code already proven at CDC).
5. Result written to Supabase → till's existing idempotent offline-sync engine reconciles it (replaces the Daraja STK-Query sweeper; **Buni has no query API**, so callback-driven completion is required, not optional).
6. Daraja path stays for rollback (it already works, keys stay per-till for the legacy flow).

Distribution reality (owner note): desktop changes ship via version releases + OTA; Netlify site deploys on push to main; edge functions deploy via Supabase CLI. Each release train must be tagged separately.

## 3. Fix batches (execution order)

### Batch A — criticals (first push; small, reviewable, reversible)
1. **Rewrite `.gitignore` as UTF-8**: correct `posapp`, `rsrc_windows_*.syso`, keep `downloads/` ignored for new files, un-ignore tracked-but-needed files deliberately, drop the ignored+tracked `db/cloud_schema_v114.sql` paradox.
2. **CORS lockdown**: same-origin + explicit LAN scheme allowance; remove `Access-Control-Allow-Origin: *` (`internal/router/router.go`).
3. **`/auth/pin-users` behind auth** (or shop-scoped short-lived PIN-session token): close cross-origin staff-PIN enumeration. Keep the PIN-pad UX: terminal fetches PIN list *after* login or via a scoped endpoint — verify the front-end flow before changing the route contract.
4. **M-Pesa callback verify-then-harden**: in `svc.HandleCallback`, before completing payment, verify amount against the stored order (and re-query via provider when available). Add the named adversarial test (forged callback, matching `CheckoutRequestID`, wrong amount → must NOT complete; must flag discrepancy).
5. **`.env.example` → `SEED_DEMO=false`** with a comment; keep demo creds only in the in-browser demo path.

### Batch B — KCB Buni integration (stage 3; after Batch A)
6. Port proven CDC Buni client → Supabase edge functions (`buni-stkpush`, `buni-callback`) + secrets; per-client credential mapping.
7. Go side: provider interface already exists (`internal/mpesa/provider.go`) — add `Buni` provider + callback-driven completion path + tests with recorded fixtures (cancel 1032, timeout 1037, success 0 with metadata, duplicate delivery).
8. Sandbox/UAT-first testing; live push only behind an explicit flag, exactly like CDC's `RUN_LIVE_PUSH=1` guard.

### Batch C — repo & release hygiene
9. **Binary history purge** (full mirror clone already downloaded; needs owner go-ahead + force-push + all clones re-cloned + tags rebuilt). Coordinate with OTA/checksum gate (`scripts/` regeneration helper).
10. Fix release process: installers to GitHub Releases only; checksums signed (later: OTA manifest signing).

### Batch D — high/medium
11. Cloud schema → migration engine (stop hand-applied snapshots; v136b onwards via proper migrations).
12. Split `teamsync.go` (2,279 lines/75 funcs) by feature.
13. JWT: move out of `localStorage` (httpOnly cookie or memory+refresh), add CSP.
14. Demo parity → golden fixtures; delete 2,711-line shadow backend drift risk.
15. Remove agent-swarm artifacts (`agent*_probe_test.go` → renamed meaningful tests or deleted; `docs/superpowers/`, `.codegraph/`, committer personas).
16. Empty `catch {}` on offline money queue → log + surface.
17. Node version alignment (CI 24 vs Netlify 20); run Postgres dialect in CI.
18. VAT rounding shared helper; cleartext-LAN threat model doc.

### Batch E — docs & DoD (owner's §5)
`docs/architecture.md`, `docs/access-control.md`, `docs/payments.md`, `docs/receipts-whatsapp.md`, `docs/testing.md`, `docs/deployment.md`, README refresh.

**DoD (owner's §7):** operator cannot reach another till's data; admin oversight works; payment records reliable under duplicate/forged/late callbacks; receipts generated without exposing private data; WhatsApp only after foundations stable.

## 4. Credentials the owner must supply later (client switch to "ledger pos")

Values go ONLY into GitHub Actions secrets / Supabase secrets / local gitignored files — never into commits.

1. **Client KCB Buni**: `BUNI_CONSUMER_KEY` + `BUNI_CONSUMER_SECRET` (client's own app on the Buni devportal, production keys), client's `BUNI_TILL` (KCB till number), and whether it's shared-shortcode or own shortcode (if own: `BUNI_ORG_SHORTCODE` + `BUNI_ORG_PASSKEY`), `routeCode` if KCB assigns one.
2. **Supabase**: a **fresh personal access token** (the one provided returns `Invalid access token` — regenerate in Supabase dashboard → Account → Access Tokens), plus the client project ref (or create one) — I can hold `BUNI_*` there as edge-function secrets myself once I have access.
3. **GitHub**: fine-grained PAT with push rights to `ssmurfgg04-gif` (anonymous access is read-only; needed for pushing fixes, releases, and the history purge).
4. **WhatsApp Business Platform** (stage 5, later): WABA phone-number ID, permanent system-user token, business ID, approved template name(s).
5. **Legacy Daraja (rollback path)**: client's Daraja consumer key/secret, shortcode, passkey — only if we keep the legacy flow live for their tills.

Until then, integration is built and tested against the **existing CDC Buni keys** (owner instruction) — real-money pushes gated behind explicit flags only.
