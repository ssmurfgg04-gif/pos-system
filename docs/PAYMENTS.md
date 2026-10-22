# Payments — LedgerPOS

How money moves, what completes a sale, and every guard in the path.
Anchored to the code at commit `b4d4426` or later.

## The one completion path

Every completion funnels through `completePayment`
(`internal/services/orders.go`), the single guarded PENDING → PAID
transition:

1. Load the payment; already-COMPLETED is a no-op (idempotent).
2. A VOIDED order can never complete — the payment is marked FAILED with
   "refund required" and an audit row.
3. Receipt uniqueness: a second payment claiming the same M-Pesa receipt
   is refused (`ErrDuplicateReceipt`).
4. Amount semantics differ by caller trust:
   - **Trusted callers** (Paystack verify/webhook, sweeper re-query,
     staff recheck): a differing amount completes the leg AND flags the
     order (`discrepancy = 1`) for staff review — fees/rounding/superseded
     legs land here, visibly.
   - **Untrusted callers** (the public M-Pesa callback, and by extension
     the Buni cloud relay): a result whose amount differs from the stored
     amount is a hard REFUSAL — payment FAILED, order stays PENDING,
     discrepancy flagged, `PAYMENT_AMOUNT_MISMATCH` audited. Real
     providers charge exactly the requested amount, so this only ever
     blocks tampered results.
5. The order transition is a guarded `UPDATE … WHERE status = 'PENDING'`
   — exactly one winner; a loser flags double-payment discrepancies.
6. Stock deducts once inside the same transaction (guarded per-line;
   short stock flags a discrepancy but keeps the sale paid).
7. Loyalty earns once, inside the same transition.

## Route selection

`MpesaRoute()` (`internal/services/services.go`) reports the active
M-Pesa STK path, checked in order:

1. **`buni`** — the fleet cloud's Buni route is armed
   (`buni_via_cloud` vault row) and not locally killed
   (`buni_enabled`). THE MAIN RAIL.
2. **`paystack`** — a Paystack integration is configured (local key or
   the cloud proxy) and enabled. The fallback.
3. **`daraja`** — the shop's own Safaricom credentials
   (`mpesa_env = sandbox|production` with complete creds).
4. **`mock`** — training wheels; requires `ALLOW_MOCK_PAYMENTS=true`
   env (the fake-money guard: a till must never "confirm" money nobody
   paid).
5. **`manual`** — receipt-code entry only.

Incomplete Daraja credentials are a LOUD error, never a silent fallback
to fake money.

## Buni route detail

- **Push** (`buniMpesaSTK`, `internal/services/payments_buni_cloud.go`):
  the till calls the `buni-stkpush` Edge Function with device identity +
  phone + whole-shilling cents + invoice ref; the function holds the KCB
  OAuth secret, performs the push (`operation: STKPush`, `routeCode`,
  `invoiceNumber = TILL-REF` ≤ 12 chars, integer-KES amount STRING,
  `sharedShortCode: true`), stores the push→team mapping, and returns the
  checkout id. The till stores it prefixed: `BUNI-ws_CO_…`.
- **Callback**: KCB posts the Daraja-shaped envelope to the public
  `buni-callback` function (URL token, constant-time compare, fail-closed
  when unset). The event is stored team-scoped; Buni gets a fast 200.
- **Completion**: the sweeper's `SweepBuni` claims events exactly-once
  per device (`buni_claim_events` RPC) and feeds each through
  `ParseCallback` → `HandleCallback` — the fail-closed gate applies.
  Duplicate deliveries (server claims return nothing the second time)
  cannot complete anything twice.
- **Timeouts**: a Buni leg older than 3 minutes with no callback is
  failed with `STK timeout (no Buni callback)`; the order stays PENDING
  for retry/manual/void. Late money still heals: success events for
  recently-FAILED legs complete the sale (same as the Paystack
  late-PIN recovery).
- **No query API**: Buni has none (verified against the official spec
  and production); polling is deliberately unsupported
  (`mpesa.ErrQueryUnsupported`). The Daraja sweeper skips `BUNI-` legs.

## Paystack route detail

`paystackMpesaSTK` charges the customer's M-Pesa through Paystack's
mobile-money API — via the local key when present, otherwise the
`paystack-charge` Edge Function (the secret never descends to the till).
Completion is owned by server-side verify (`PaystackVerify`) and the
HMAC-verified webhook (direct or via the cloud relay's exactly-once
claim). The STK grace window (`stkGraceExpired`) keeps an abandoned-
looking verify from failing the leg while the customer is still typing
their PIN.

## Operation runbook

**"Customer says they paid but the till shows Pending"**
1. Wait ~30 seconds — the sweeper claims Buni callbacks every 5s.
2. Use Recheck (Order → Recheck): it re-queries the provider
   authoritatively and completes if the money actually arrived.
3. If the customer entered a wrong PIN (Buni result 2001) the leg is
   already FAILED — push again (Retry with phone).
4. Never complete by hand-typing a receipt code the customer CLAIMS —
   manual entry is for the code that actually appears on their M-Pesa
   message.

**"Order flagged with a discrepancy"**
The order was paid/stock-adjusted abnormally (double payment, forged or
mismatched callback, stock shortfall). Check the audit log
(`PAYMENT_AMOUNT_MISMATCH`, `PAYMENT_AFTER_VOID` entries) and the
payments table; reconcile in the provider dashboard before refunding.

**"Rolling out Buni to a new shop"**
1. Set the Edge Function secrets once per fleet (already done for
   `ledgerpos-backups`): `BUNI_CONSUMER_KEY`, `BUNI_CONSUMER_SECRET`,
   `BUNI_TILL` (7853959), `BUNI_CALLBACK_TOKEN`, `BUNI_CALLBACK_URL`.
2. Add the shop's `app_config` row `buni_via_cloud = true` (public).
3. Tills pick it up at boot or within 6h; `PaymentConfig.mpesa.route`
   flips to `buni`. The local kill-switch is the `buni_enabled` setting.

**"KCB till / credentials change"**
Update the Edge Function secrets (Management API → secrets); nothing
changes on tills. The callback URL token rotates the same way — rotate
`BUNI_CALLBACK_TOKEN` and `BUNI_CALLBACK_URL` together.

## What the tests prove

- Forged public callbacks (KES 1 for a KES 50,000 sale) fail closed and
  leave the order recoverable via recheck
  (`internal/handlers/mpesa_amount_gate_test.go`).
- The Buni relay completes only on exact amounts; duplicates are inert;
  the real production fixtures (1032/1037/2001/0) parse and route
  correctly (`internal/services/payments_buni_test.go`).
- Route priority and the local kill-switch behave as documented
  (`TestBuniRoutePriorityDemotesPaystack`).
- VAT math is pinned to golden fixtures in both languages
  (`internal/money/tax_test.go`, `frontend/src/lib/money.test.ts`).
