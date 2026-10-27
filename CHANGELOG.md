# Changelog

Release notes and known limitations. The full commit history is on GitHub;
this file records what each release means for the shop.

## v1.5.0 — tabs for anyone, WhatsApp receipts that send themselves

### Tab: charge anyone, no setup (the "button not clickable" fix)

- **Root cause of the dead button:** until v1.4.7 a customer needed a
  credit limit before tab charges unlocked — nobody had one, so the
  button never lit up. That gate is gone for good.
- **New policy: a tab is always allowed for any customer.** No
  eligibility flag, no credit-limit requirement, no blocklist. Pick the
  customer, press the button, done — whether or not they were picked
  beforehand. The credit-limit field stays on the Customers page as an
  advisory number only; refusing a tab is a human call at the counter,
  never a software stop.
- **The customer list is right there when you tap Tab** — the roster
  loads the moment the tab panel opens (no typing needed), and typing
  just narrows it. Enter picks the first match. Store credit stays a
  completely separate tender with its own balance rule.
- Backend: the charge moves the customer's balance atomically inside
  the checkout transaction (two tills can never lose an increment);
  stock is still checked before the tab is created and deducted once at
  settle.

### WhatsApp: branded receipt images, sent automatically

- **Automatic-only.** The manual WhatsApp button is gone; a paid sale
  enqueues exactly one WhatsApp job by itself and the customer receives
  a **branded receipt image** — the shop's logo (or a brand-colored
  monogram), itemized lines, discount, VAT, TOTAL, and the M-Pesa code —
  with a short English caption ("Thank you for shopping with {store} —
  we're glad you chose us"). No SMS/email fallback: Safaricom already
  texts the customer about the payment.
- **One gateway: whapi.cloud.** Admin pastes the API token once per till
  in Settings → WhatsApp (the number is QR-linked on whapi's side),
  presses Send test, and every paid sale after that delivers on its
  own. No token = nothing is queued and the sale completes normally.
- **Zero cashier steps, zero risk to the sale.** Sending is best-effort
  like printing: it never blocks or fails a checkout. Retries are
  bounded (5 attempts, a minute apart); permanent failures land in
  Settings → WhatsApp → Failed sends with a Retry button. Voided sales
  cancel their queued receipt. The token never team-syncs.
- Receipt images render server-side (`receipt_image.go`) from the same
  data as the paper receipt, so the two can never disagree.

### Also

- Settings → WhatsApp is the only WhatsApp surface: token, auto-send
  toggle, one-click test send, failed-sends list with retry. The old
  provider presets (ultramsg/custom) and shop-number fields are gone.
- Regression sweep: full Go suite + 108 vitest + E2E smoke extended
  with v1.5.0 checks (zero-limit tab charge, WhatsApp admin endpoints).

## v1.4.7 — daily report hotfix (found by the 58-check E2E sweep)

- **The daily report 500'd on any day that HAD sales.** SQLite's AVG()
  returns a float and the summary scanned it into an integer — quiet
  days worked (NULL → 0), busy days broke. Fixed with an integer CAST;
  a regression test locks a sales-day + an empty-day summary.
- Design-board refusals are 403s now, not 500s: designers cannot create
  jobs (front desk/owners do) and cannot move jobs assigned to others —
  both are policy, not server faults.
- The E2E smoke suite was realigned with the product (seed product names,
  design-board roles, retired Daraja settings keys) and taught to switch
  the Buni route off locally; it runs 58/58 against a live server.

## v1.4.6 — WhatsApp receipts, team-visible shifts, honest tab guidance

### WhatsApp receipts to the customer's number (the headline)

- **Every receipt gains a WhatsApp button.** One tap opens the shop's own
  WhatsApp with the receipt already typed to the customer's number — the
  cashier presses send. Zero configuration, works from day one, cannot
  break the money path.
- **Automated sending is opt-in**: Settings → WhatsApp → pick a gateway
  (whapi.cloud / ultramsg presets or a custom endpoint), paste its API
  key, flip auto-send on. Every completed sale with a customer phone then
  delivers its receipt without anyone touching a phone.
- Delivery is a persistent queue (`whatsapp_jobs`) drained by a worker
  with bounded retries — a restart never loses a receipt; permanent
  failures land in the audit log and the order's timeline. The gateway
  key is a masked secret that never team-syncs.
- Receipt text is one source of truth shared by both paths: bold store
  name, aligned item/totals block, M-Pesa code, VAT, served-by. An
  unconfirmed M-Pesa leg reads "M-Pesa (pending)" — never "Paid".

### Shifts are now team-wide (the "not visible on Admin" report)

- A shift opened on one till reaches every other till's Shifts page the
  moment it opens, and its close-out travels too (replay-keyed, exactly
  once). Before this release shifts were the one major entity that never
  left the machine they were opened on — an admin on another device
  simply could not see them.

### Tab (charge-to-account) guidance

- The tab charge button is guarded by design: pick a customer who has a
  credit limit, or the button stays disabled. What changed is that the
  rail now SAYS what unlocks it — every blocked state (no customer yet,
  no match, cash-only customer, offline) names the exact fix instead of
  a dead button.
- Pressing Enter in the tab-customer search picks the first match.

### Also

- The paper receipt no longer prints "(pending)" next to an unconfirmed
  M-Pesa leg; the M-Pesa code row prints only once the code exists.
- New database migration v20 (shifts.client_uuid + whatsapp_jobs).

## v1.4.5 — product creation unblocked (SKU 2067)

- **"Can't add products — product failed sku 2067" is fixed.** The old
  generator drew blank-SKU product codes from a fixed window that the
  fleet had exhausted; every new product hit the unique constraint. The
  generator is now date+random (unbounded), duplicate SKUs answer with a
  clear 409, and CSV imports fall back safely. Existing SKUs untouched.

## v1.4.4 — sales privacy, till attribution, single payment rail

### Sales visibility & accountability (the headline)

- **Only Admin/Owner browse individual sale records.** A new
  `orders.view.all` permission gates the whole order surface (list,
  detail, receipt, timeline, notes, void, WhatsApp notify, customer
  purchase history, held carts). Staff without it see only their own
  sales — probing URLs or ids answers "not found" and reveals nothing.
- **Every sale now carries its originating till** (`device_id`), completing
  the traceability chain: till → cashier → timestamps → payment legs →
  status. The daily report gains a **By till** breakdown that reconciles
  to the orders screen by construction.
- Live reports reconcile: daily/monthly tenders are now keyed to the
  parent order's window (the midnight-crossing skew is gone). Shift X/Z
  keeps real cash-drawer semantics.
- WebSocket broadcasts no longer carry line items or totals — a staff
  client sees "Order ORD… paid", nothing more.
- Other staff's shift Z reports need `reports.view`.

### Payments

- **"Duplicated MSISDN" fixed at the source**: a 75-second per-order
  cooldown refuses a second STK push while a prompt may still be on the
  customer's phone (KCB error 4999), with a friendly counter message; the
  raw KCB error text is translated everywhere; the Resend button is locked
  while a prompt is live.
- **Paystack and Daraja rails retired** (owner decision — unused since the
  Buni migration). Removed: providers, routes, CSP entries, edge-function
  mirrors, the Card tender tile, card split legs, and all config/secrets
  (edge secret + cloud config rows deleted). Historical card payments stay
  readable everywhere (orders, receipts, reports). M-Pesa STK (Buni),
  manual M-Pesa, cash, store credit, loyalty and splits (cash/credit/
  M-Pesa) are the tenders. If card acquiring is ever needed again, it
  returns as a NEW integration — the old one was removed, not disabled.
- Unconfirmed payments still never complete by themselves: callback-only
  completion, fail-closed amount gate, exactly-once claims.

### Performance (the ones that pay rent at fleet size)

- Offline queue: failed-sale bookkeeping went from rewriting the entire
  queue per failure to a single-record update; badge counts no longer
  re-walk the queue three times per flush.
- Checkout number allocation: the daily "heal" probe uses the index range
  instead of a LIKE scan inside every sale's write transaction.
- Receipt printer worker no longer takes the checkout write lock every 2s
  when idle.
- POS screen: product reloads can no longer stack on a reconnect.

### Android client (new)

- `ledgerpos-android-<version>.apk`: a thin LAN terminal for a till in
  serve mode. First-launch Connect screen captures the till address; the
  app never falls back to the demo backend. Built by CI on every frontend
  change; release-signed once the keystore secrets are configured.

### Docs

- New: `docs/GUIDE-CASHIER.md`, `docs/GUIDE-ADMIN.md`,
  `docs/TROUBLESHOOTING.md`, `docs/DEVELOPER.md`, `docs/OPERATIONS.md`,
  `docs/DEADLOCK.md`, and this changelog.

## v1.4.3 — hardening release

- STK supersede is now one transaction on all rails; a completed push can
  no longer be resurrected by a retry.
- Admin password change refuses the seeded default passwords.
- Paystack webhook edge function gained a 256KB body cap (DoS guard).
- payments(status, completed_at) index for X/Z and daily reports.
- The download-site version is derived from the release manifest (the
  site can no longer drift from the release train).

## v1.4.2 — fleet + brand

- Buni account reference on the customer prompt became the stable brand
  tag `7853959-CDC` (was `-ORD2` fragments).
- Cloud DB hardening: RLS closed on 8 tables, anon write grants revoked
  (incl. TRUNCATE), app_config writes admin-only.

## v1.4.1 — install fix

- The OTA offer could loop forever ("install v1.4" after installing) —
  fixed; store-name edits persisted reliably.

## v1.4.0 — KCB Buni is the M-Pesa rail

- Buni STK via Supabase Edge Functions (keys never on tills), callback-
  driven completion, real-money E2E verified. Paystack demoted to
  fallback (later retired in v1.4.4). Signed OTA update train.

## Known limitations (honest list)

- **No returns document** — refunds of taken money are a void + refund-
  owed flag; the money moves on the provider dashboard. A returns flow is
  the top roadmap item.
- **No cash pay-in/payout ledger on shifts** — drawer drops/expenses make
  Z variances look worse than they are; note them on paper.
- **No per-product stock movements ledger** — stock truth is guarded
  quantity updates + audit rows; FEATURES.md's old claim of a movements
  ledger was removed.
- **eTIMS not integrated** — buyer KRA PIN and VAT exports are ready;
  integration waits for KRA enforcement timelines.
- **Local till DB is unencrypted** — use OS disk encryption; a stolen till
  can read synced data at rest (staff API access is still scoped).
- **Sync replicates all tills' orders to every till's disk** (needed for
  offline ops) — reading is scoped per user; at-rest scoping is a future
  sync-filter feature.
- **`sync_events` grows unbounded** — pruning is an owner decision away.
- **Discount reporting is thin** — order-level discounts are stored and
  audited but not yet aggregated as a report line (sales CSV next).
- **Two stocktake systems exist** — prefer Inventory → Stock count; the
  supplier-screen stocktake is legacy and will be retired.
- **Print via the OS dialog on Windows** uses driver rendering, not raw
  ESC/POS — set the printer target to the ESC/POS device for best
  receipts.
- **The Android APK is a LAN terminal** — it needs a till running in serve
  mode on the shop network; it is not a standalone cloud POS (deliberate:
  money logic stays in the audited Go binary).
