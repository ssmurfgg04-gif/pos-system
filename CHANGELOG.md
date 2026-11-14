# Changelog

Release notes and known limitations. The full commit history is on GitHub;
this file records what each release means for the shop.

## v1.9.1 — Review fixes for the multi-business release

- **Payment routes stay current.** Tills re-check where their M-Pesa money
  goes when sync starts and every 30 minutes. A shop verified for its own
  short code starts getting prompts without anyone opening Settings, and
  a till that was refused once recovers on its own.
- **New founding-business stores work out of the box.** A store our own
  business creates later uses the shared KCB till by default (v1.9.0 only
  set this up for Main Store, so a second store would have been refused
  M-Pesa prompts). Cloud: `db/deploy-v191-platform-default.sql`, applied.
- **Safer updates.** The release job now prints ready-to-run OTA SQL built
  from the exact signed bytes, and tills accept a manifest stored with the
  release file's trailing newline instead of refusing the update.
- A long shop name with non-English letters is shortened without breaking
  a character.

## v1.9.0 — One cloud, many businesses; weighed goods and expiry dates

The first release where LedgerPOS can be sold to a second business. Until
now the cloud assumed one shop: every new install joined Main Store, new
shops showed the first shop's demo stock, and every M-Pesa prompt said the
same name and paid the same till. This release also ships the v1.8.0 work
(weighed quantities, batches and expiry) that was never released on its own.

- **Each business is its own cloud.** A shop set up from scratch now
  registers as a new business with its own store and owner till. Store
  lists, store creation and the waiting-device pool only show your own
  business, and the store registry is no longer readable by anyone with
  the app key. Fresh devices never auto-join someone else's shop.
- **Join links join the right team.** A join link or owner sign-in is
  refused on a till that already holds products, sales or customers, so
  a working shop can't be pulled into another team by accident, and an
  approved till keeps its team. If the cloud does move a till, its unsent
  events are held back instead of landing in the new team.
- **New shops start empty.** No demo print-shop products or stock in a
  new shop: one "General" category and nothing else.
- **Each shop is paid on its own numbers.** Settings → Payments has a new
  **Where customers pay** panel: your till or paybill and the name
  customers see. Our own stores keep the shared KCB till as before.
  Another business gets prompts to its own short code once that short
  code is verified, and until then the prompt is refused and the
  customer pays the shop's till directly, so no money lands on the wrong
  till.
- **Owner login can't be taken over.** Once an owner account exists, no
  till can overwrite it, and owner usernames are unique.
- **Weighed goods (from v1.8.0).** Products have a unit (pc, kg, g, l,
  ml, pack, box, dozen, m) and the till sells fractions: 0.5 kg, 250 g.
  Quantities are stored as whole thousandths, like cents for money, so
  totals never drift. Receipts, reports and CSV all show the real amount.
- **Batches and expiry (from v1.8.0).** Goods-in can record a batch
  number and expiry date. Sales take the batch that expires first, a loss
  can name the batch, and a new expiry report shows what is about to go
  off.
- **Customer screen.** A second monitor facing the customer shows each
  item and the total, the change, and for M-Pesa the exact amount and the
  till or paybill to pay. Open it from the monitor icon in the cart.
- **Receipts are honest about tax.** A receipt with a buyer KRA PIN now
  says **INVOICE** and "Not an eTIMS tax invoice" instead of TAX INVOICE,
  until real eTIMS submission lands.
- **Phones and screens.** The app now fits on a phone. Stock badges read
  "46 in stock" and "3 left", the 7-day chart no longer clips, dates are
  day-first everywhere (14 Nov 2026), and inventory actions are clearer.
- **Under the hood:** migrations v27/v28 (milli quantities, batches) and
  v29 (indexes on 7 foreign-key columns), cloud schema
  `db/deploy-v190-multitenant.sql` and `db/deploy-v171.sql` (internal
  sign-in helpers locked), `buni-stkpush` routes per store, CI deploys
  only from main and only after tests pass, and the nightly COGS test
  flake is fixed.

## v1.7.0 — Shopkeeper's ledger: margin, opening balances, reversals, disposals

The second wave of the deep research (the friend's SmartPOS schema — 54
tables and a 37 MB production dump — plus OSPOS, FloCafe, managiha,
BizYangu and the Tuma M-Pesa fork). Where v1.6.3 made the database an
accountant, this one makes it an auditor that can prove its own numbers.

- **Gross margin, finally true.** Every order line now freezes the
  product's cost at sale time (`cost_cents` on the line — the OSPOS
  pattern; without it margin is only right while costs stand still).
  Daily and monthly reports gained **cost of goods, gross profit and
  margin %**, and top products now show profit per product, not just
  takings. Existing lines were backfilled with current costs as the best
  available approximation.
- **Opening balances are real documents.** A customer's carried-in debt
  (the old notebook balance) is now a first-class ledger row — receipted,
  disputable, reversible — instead of a bare number nobody can match a
  payment against (SmartPOS learned this the hard way: KES 216,880 of
  unallocated receipts).
- **Soft reversals.** Manual ledger entries (opening balances, walk-in
  payments, adjustments) can be reversed with a reason: the reversal is a
  new mirrored row pointing at the original — money rows are never edited
  or deleted. Double reversals are refused; order charges stay on the
  returns/void path so stock and money move together.
- **The ledger remembers where it stood.** Every customer ledger row now
  snapshots the balance it produced (`balance_after`) — the SmartPOS
  watchdog lesson. A drifted balance is now provable, not invisible.
- **Structured losses.** Inventory gained **Record loss**: expired,
  spoilt, lost, damaged, other — guarded deduction, DISPOSAL ledger rows,
  and a disposal report (and daily/monthly write-off totals) with the
  loss value computed from product cost.
- **System health, self-audited.** Settings → System gained a health
  panel: schema version, all declared indexes verified as present, sync
  queue depth (pending / failing / dead), customer-ledger and stock
  projection reconciliation with the drifted names listed, and the newest
  backup snapshot. Before it breaks your day-end, the till tells you.
- **Under the hood:** migration v26 (two new columns + two composite
  indexes), team-sync carries the cost snapshots and balance snapshots,
  demo parity for all four new surfaces, 5 new Go integration tests, and
  the live smoke suite grew from 84 to 115 checks.

## v1.6.3 — Shop brain: stock ledger, returns, expenses, wholesale prices

A deep study of a friend's Kenyan SmartPOS (its full 54-table MySQL dump,
26 page snapshots, screenshots) plus the big open-source POS systems
(OSPOS, Tuma's M-Pesa fork, Store-POS) fed this release. The theme: the
database stops being a counter and becomes an accountant.

- **Stock ledger (the headline).** Every stock change — sale, void
  restore, manual adjust, PO goods-in, stock-take apply, customer
  return — now writes an append-only `stock_movements` row: kind, signed
  delta, balance after, who, which till, what referenced it. Inventory
  got a **History** button per product (the full audit trail), and the
  ledger starts from an OPENING anchor per product so day-one stock is
  auditable. Wrong numbers can now be explained, not just corrected.
- **Returns & refunds.** The Orders page gained a **Return** action on
  paid sales: pick lines and quantities, choose refund by cash or store
  credit (with the customer's account), give a reason, and the till
  mints a `RET-` document, restocks the shelf, and records the refund.
  Partial returns are fine — each line tracks what is still returnable,
  over-returns are refused with a readable message.
- **Expenses.** The money side of the day finally lives in the till:
  seeded types (Rent, Electricity, Internet/Airtime, Transport,
  Salaries, Supplies, Maintenance, Other), an `EXP-`-numbered log, a new
  **Expenses** page, and the daily/monthly reports now show money out,
  money back (returns), **net sales**, stock value on the shelf, and
  low-stock alert counts.
- **Wholesale prices.** Products can carry a wholesale price, customers
  a price tier; pick a wholesale customer at the till and the cart
  auto-prices (manual overrides still win). Inventory marks low stock
  per product (its own threshold, or the shop default).
- **Under the hood:** 11 hot-path indexes (orders, items, payments,
  ledger, audit), returns/expenses/team-sync over the same encrypted
  Supabase events as everything else (older tills skip unknown events
  gracefully), new permissions (`returns.manage`, `expenses.view`,
  `expenses.manage`) unioned into the seeded roles by migration, and the
  smoke suite grew to 101 checks — the whole returns/expenses/ledger
  surface is exercised against a live server on every release.

Schema is at **v25** (v22 ledger + indexes, v23 returns, v24 expenses,
v25 wholesale tiers). Migrations are additive and idempotent; a till can
be updated one device at a time as always.

## v1.6.2 — WhatsApp opens WhatsApp: the app, the contact, every till

Customer complaints on v1.6.1, fixed one by one:

- **"We share on WhatsApp but it doesn't open the app."** The desktop
  Share now asks the **Go host itself** to launch the customer's chat
  (`POST /api/v1/whatsapp-open` — the host process hands
  `whatsapp://send` with phone + caption preloaded straight to the OS,
  no "Open WhatsApp?" dialog, no WebView2 moodiness). WhatsApp Desktop
  opens the conversation ready; if WhatsApp is not installed the screen
  says so honestly. The v1.6.1 in-page deep link and wa.me stay as
  fallbacks for browser-only tills.
- **"This interface is on the admin ones alone, the cyber PC is not
  showing it."** The WhatsApp contact buttons on Orders and the Design
  Board are no longer `orders.notify`-only: any role that can sell
  (`pos.sell`) sees and uses them. Every till's local roles got
  `orders.notify` unioned in by a database migration, and the Cashier
  preset carries it for new shops — the cyber-café machines are first-
  class users now.
- **"It asks which app to use and the contact doesn't load."** The
  contact-a-customer modals (order drawer, design board) now open the
  chat directly through the same app-first flow — number + caption ride
  the link, so the contact is loaded before WhatsApp even appears. The
  OS share sheet is never on the chat path (it stays available for the
  branded PNG where the device supports it).

Also: Kenyan numbers normalize everywhere (07…, 7…, +2547… → 2547…) so
the contact loads whether it came from checkout, CRM, or the payer's
M-Pesa leg; the smoke suite grew to 84 checks with two WhatsApp-app
checks; the schema is at v21 (union-only backfill, admin edits and
custom roles untouched).

## v1.6.1 — Share on WhatsApp that actually shows up on the desktop PC

### What was wrong

v1.6.0 put the Share button back, but it lived *inside* the receipt
modal — and the till a shop runs on Windows was still on v1.5.0, where
the button did not exist at all. From the cashier's chair: "the Share
via WhatsApp button is not showing on the desktop PC." On top of that,
the desktop's no-file-share fallback dead-ended in a downloaded file and
an open browser tab — nothing that opens the WhatsApp **app** itself.

### The fix — one Share, every device

- **The button is now impossible to miss.** "Share on WhatsApp" sits
  directly on the *Sale complete* screen (between "Start a new sale" and
  "View receipt"), and stays in the receipt modal. Same one-tap action,
  no modal hunting.
- **Desktop PCs open the installed WhatsApp app itself.** The receipt
  PNG lands on the **clipboard** (and in Downloads as a backup) and the
  `whatsapp://send?phone=…` deep link jumps the **WhatsApp desktop app**
  straight into the customer's chat with the thank-you typed — the
  cashier presses **Ctrl+V, then Enter** and the picture is on its way.
  First time only, Windows asks "Open WhatsApp?" — tick **Always
  allow**. No WhatsApp app on that PC? WhatsApp Web stays one click away
  ("Open WhatsApp Web instead" under the button).
- **Phones keep the one-tap sheet** (image + caption straight into
  WhatsApp) wherever the browser supports file sharing; the gateway path
  (PAID order + token) is still the silent first leg everywhere.
- The whole tree lives in one tested module (`lib/receipt-share.ts`):
  server queue → native share sheet → desktop app hop → wa.me web.

### Notes for the shop

- The deep link needs the WhatsApp desktop app installed and logged in
  (WhatsApp Desktop from the Microsoft Store or whatsapp.com). Without
  it, the button still saves the image and offers WhatsApp Web.
- Clipboard paste works in the LedgerPOS app and in Chrome/Edge; if the
  clipboard is blocked, the toast says so and the Downloads copy is the
  drag-in fallback.

## v1.6.0 — the Share button is back, receipts you can prove, orders you can find

### Share via WhatsApp — the image, three ways

The whapi gateway is still under review, so the manual path is back —
and it sends the **branded receipt image**, never bare text:

- **Server leg (gateway connected):** the receipt screen's Share button
  queues the branded image straight to the customer's number — one tap,
  the customer receives the picture with the thank-you caption on the
  shop's own WhatsApp. It is an honest re-send: "I didn't get it" is the
  button's reason to exist, and every attempt lands in the Settings
  queue list and the audit log.
- **Device share (no gateway):** the same PNG rides the phone's share
  sheet (image + caption into WhatsApp) where the device supports file
  sharing — on a LAN HTTP till the button saves the image and opens the
  customer's chat with the caption typed; the cashier drags the picture
  in and presses send.
- **Format:** PNG on purpose — for a text receipt it is *smaller* than a
  JPEG at usable quality, it is lossless (the invisible watermark's
  exact layer survives every lossless copy), and WhatsApp accepts it
  like any photo.

### The invisible watermark — proof you can verify

Every branded image now carries a hidden, HMAC-signed payload (order id,
issue time, total) keyed with a per-shop secret that never leaves the
shop (it rides team sync so any till can verify any till's receipts):

- Two layers: an exact one that survives every lossless copy, and a
  JPEG-survivable one (majority-voted cells in a dedicated white strip)
  that rides out WhatsApp's own re-encoding and screenshots of flat
  areas.
- **Verify a receipt:** Settings → WhatsApp → *Verify a receipt image*.
  Upload the screenshot a customer sent "as proof": the shop reads its
  own hidden mark and answers plainly — genuine, or not issued by this
  shop / modified past recovery; for genuine ones it also compares the
  watermarked total against the order **as it exists now** and flags
  doctored amounts, and refuses VOIDED receipts as proof of anything.
  Edited amounts, AI-generated fakes and foreign shops' receipts fail.

### The receipt content audit — every copy reconciles

All four renderers (branded image, thermal paper, browser print, on-screen)
were audited against a Kenyan counter's needs and now agree:

- Contact row (shop phone · address) and **KRA PIN** where set; a buyer
  PIN turns the document into a **TAX INVOICE** with the buyer's PIN on
  it (eTIMS habit).
- **Subtotal → Discount (with the reason) → Points redeemed as money →
  VAT (rate + "included" when tax-inclusive) → TOTAL** reconciles on
  every renderer — points redemption prints the shillings it took off,
  not just "120 pts", and VAT-off shops no longer print "KES 0.00" rows.
- **Honest payment legs everywhere**: an unconfirmed M-Pesa leg says
  "M-Pesa (pending)" on paper, browser and image alike — a pending
  sale's image carries a **\*\*\* PENDING \*\*\*** banner and a voided
  one \*\*\* VOIDED \*\*\*; neither can be mistaken for proof of
  payment.
- Gift-card codes minted by the sale print on every copy (the code IS
  the value); tab charges show the customer's **balance after this
  charge**; the on-screen receipt shows the paid-at time (an STK sale
  paid at 10:02 no longer prints 09:55).

### Orders you can actually find

- The Orders page gained **server-side date filters (Nairobi calendar
  days — a 23:59:59 sale belongs to its own day), a cashier filter**
  (Admin/Owner only — scoped staff see their own sales, and the picker
  refuses to leak the roster) **and pagination** (50 rows a page).
- Case-insensitive customer + quote search on the cloud Postgres (bare
  LIKE was case-sensitive there).

### Fixes for the pages nobody was watching

- **Reports**: dates are Nairobi-local (midnight-to-3am sales no longer
  land on yesterday), best sellers finally render, all-shop view no
  longer trips on per-day series, the payment-split bars are
  proportional (the old 12% floor showed 48% for a 10% share), and the
  consolidated average is total ÷ count (the old average-of-averages
  read 5,500 for a real ~1,089).
- **Inventory**: restoring an archived product no longer wipes its
  stock, tracking flag and barcode.
- **Suppliers**: a purchase-order line's unit cost no longer multiplies
  by 100 twice (a KES 50 part priced as KES 5,000).
- **Design board**: creating a job follows the one true rule
  (`orders.assign` — the front desk hands work to the studio) at every
  layer; staff without it only see jobs assigned to them.
- **WhatsApp queue**: an order that vanished can no longer starve every
  later send (the job fails instead of panic-looping at the head of the
  batch), and demo boxes (SEED_DEMO=true) no longer inherit production
  secrets from the cloud vault.

### For the record

- Smoke suite grew to 82 checks (orders filters + pagination, watermark
  roundtrip, share-button server leg, receipt-HTML audit rows); Go tests
  20 packages, vitest 126.
- Known notes: on a LAN HTTP till the Web-Share sheet is unavailable
  (browsers require HTTPS for file sharing) — the Save + chat fallback
  is the expected path there; payment-label wording still varies
  slightly between modal and server renderers (cosmetic, tracked for
  v1.6.1); receipt verification stays admin-only (Settings) for now.

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
