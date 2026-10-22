# Testing — LedgerPOS

What the suites cover, how to run them, and the invariants they pin.
Go 1.24+ required (`GOROOT` per your install); Node 24 for the frontend.

## Running everything

```bash
go vet ./...
go test ./... -count=1          # sqlite path — the default
cd frontend && npm ci && npm test   # vitest (jsdom)
```

Frontend build + site assembly (what CI's deploy job runs):

```bash
cd frontend && python3 ../scripts/netlify_build.py
```

Postgres dialect (CI runs this against a postgres:16 service; locally:

```bash
docker run -p 5432:5432 -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=ledgerpos_test postgres:16-alpine
POSTGRES_TEST_DSN='postgres://postgres:postgres@localhost:5432/ledgerpos_test?sslmode=disable' \
  go test ./internal/database/ -run TestPostgresMigrations -count=1 -v
```

Skips cleanly when `POSTGRES_TEST_DSN` is unset.

## The layers

**Unit/invariant tests** (fast, hermetic):

- `internal/money/tax_test.go` — VAT golden fixtures. **Cross-language
  contract**: `frontend/src/lib/money.test.ts` pins the IDENTICAL table.
  Change one, change both.
- `internal/update/signing_test.go` — manifest signatures (round-trip,
  tamper, unsigned-refused, embedded key parses).
- `internal/mpesa` — phone normalization, receipt codes, Daraja/Buni
  callback parsing, provider mapping (`buni_test.go`,
  `daraja`/`mock` tests).

**Service tests** (`internal/services`) — the money core:

- `payments_buni_test.go` — the Buni relay end-to-end against a fake
  cloud: real production fixtures (1037 timeout captured live,
  0/1032/2001 spec shapes), exactly-once claim, duplicate-delivery
  inertness, forged-amount fail-closed, 3-minute backstop, route
  priority + kill-switch, Daraja legs never claimed by the Buni sweep.
- `payments_paystack_cloud.go` tests (`v133_charge_proxy_test.go`,
  `v134_stk_grace_test.go`) — the proxy charge/verify mapping and the
  STK cancellation grace.
- `deepreview_money_test.go`, `fake_money_guard_test.go` — the
  completion guard and the "nothing completes by itself" invariant.
- `vault_probe_test.go` (formerly agent3) — device approval/revocation,
  vault secrecy, offline boot, sync push retry.
- `p0_sync_hardening_test.go`, `orders_alloc_test.go` — sync replay and
  order-number allocation under retry races.

**Handler tests** (`internal/handlers`) — HTTP surface:

- `mpesa_amount_gate_test.go` — the forged-callback suite (the Batch A
  critical: wrong/zero amounts can never complete; the real-amount heal
  still works).
- `security_headers_test.go` — CSP/nosniff/referrer present on API AND
  the public callback.
- `adversarial_probe_test.go` (formerly agent5) — credential spray,
  privilege escalation attempts, lockouts.
- `paystack_probe_test.go`, `persistence_probe_test.go` (formerly
  agent1/agent2) — money-flow probes and shop-adoption persistence.
- `split_tender_test.go`, `tenants_test.go`, `shop_binding_test.go` —
  tender math, cross-shop token rejection.

## Invariants worth knowing before you change money code

1. **One completion path.** Everything funnels through
   `completePayment`. Adding a second "mark paid" is how double-charge
   bugs are born.
2. **Untrusted amounts fail closed.** The public callback (and the Buni
   relay feeding it) must match the stored amount exactly. Trusted
   callers may complete-with-flag.
3. **Fake money needs a flag.** The mock provider exists only under
   `ALLOW_MOCK_PAYMENTS=true`. Tests that need money movement either use
   that flag or drive the real guards directly.
4. **One pooled connection.** Never query while rows are open
   (single-connection SQLite discipline — `docs/DEADLOCK.md`).
5. **Single-connection + guarded transitions** mean tests can race
   completions and assert exactly-one-winner.

## Conventions

- New fixtures for provider payloads go in as captured (the real Buni
  1037 body is in `payments_buni_test.go` verbatim) — spec-shaped
  fixtures for the rest.
- Tests must never touch production hosts: `guardProduction` blocks the
  real cloud project from test binaries (both `internal/update` and
  `internal/services`); point `cloudmeta.BaseURL` at an httptest server.
- The `*_probe_test.go` files are adversarial suites kept from the review
  process — renamed to domain names, coverage retained.
- CI (`.github/workflows/ci.yml`): vet → sqlite suite → postgres
  migration chain → vitest → site build/deploy + live checksum
  verification, on every push to main.
