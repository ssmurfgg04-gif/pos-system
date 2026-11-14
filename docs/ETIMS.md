# KRA eTIMS in LedgerPOS (v1.10)

LedgerPOS sends every paid sale to KRA eTIMS through LedgerPOS Cloud, as an
**OSCU** (Online Sales Control Unit) integration. Each receipt becomes a
**TAX INVOICE** with the control-unit (SCU) number, KRA's signature and a
verification QR. Returns go to KRA as credit notes against the original
invoice.

## What the shop needs

1. An eTIMS **OSCU** registration for the business on the KRA eTIMS portal
   (etims.kra.go.ke), which gives a **device serial number**.
2. The business **KRA PIN** and the **branch ID** (`00` for the head office).
3. The till linked to LedgerPOS Cloud (Settings → Team shows "connected").

Test with KRA's sandbox first if KRA issued test credentials.

## Connect (owner, once per shop)

Settings → **eTIMS** → choose *Test (sandbox)* or *Live*, enter the PIN,
branch and device serial → **Connect to KRA**. Then set the defaults:

- **Tax type**: B (16%) for most VAT-registered shops; D for shops not
  registered for VAT; A/C/E for exempt, zero-rated and 8% goods.
- **KRA item class**: search KRA's list and pick the class that fits most
  products. KRA refuses an item without one.

Products can override both in Inventory → edit product → *KRA eTIMS*.

If the device already sent invoices from another system, enter the last
invoice number it used, so numbering continues without a clash.

## How it works

- Checkout never waits on KRA. Paid sales queue on the till and are sent
  about once a minute; offline sales go when the internet is back.
- The cloud gives invoice numbers to the whole store, so several tills
  share one gapless sequence. Re-sending the same sale reuses its number.
- Products are registered with KRA automatically the first time they sell.
- Weighed quantities that don't fit KRA's 2 decimals (0.125 kg) are sent as
  one line at the exact amount, with the real quantity in the item name.
- KRA's key (cmcKey) stays in the cloud; tills never hold it.
- Only sales made after eTIMS was switched on are sent.

## When KRA refuses a sale

Settings → eTIMS → *Send queue* shows KRA's message. Fix the cause
(usually the item class or tax type), then **Send again now**. Network
problems are retried automatically with backoff.

## Not covered yet

- The WhatsApp receipt image shows the SCU details but no QR yet (paper and
  screen receipts carry the QR).
- Per-product tax type and class are set on each till (they don't sync
  between tills yet); the shop defaults live in the cloud and apply to all.
- Purchases, imports and stock reports to KRA (`insertTrnsPurchase`,
  `insertStockIO`, `saveStockMaster`) are not sent.

Spec: KRA OSCU Specification v2.0. Cloud: `supabase/functions/etims`,
`db/deploy-v1100-etims.sql`. Till: `internal/services/etims.go`.
