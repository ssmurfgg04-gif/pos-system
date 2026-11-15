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
  within a few seconds; offline sales go when the internet is back.
- The cloud gives invoice numbers to the whole store, so several tills
  share one gapless sequence. Re-sending the same sale reuses its number.
- **Strict order (v1.11).** Every send holds a short per-store lease. Inside
  it, any earlier invoice still waiting (KRA was unreachable) is sent
  first, and nothing newer is numbered until it gets through. The request
  is saved before it is sent, so a send that dies half way is re-sent with
  the same number and content. A number KRA refuses for good is handed
  back, so the sequence never has a gap.
- **Per-product VAT (v1.11).** Each product's tax type sets its rate:
  B = shop rate (16%), E = 8%, A/C/D = 0%. One sale can mix rates; the
  receipt shows a VAT line per rate, and KRA gets exactly the per-line
  discount and VAT the receipt printed. Loyalty points reduce the VAT to
  the VAT inside what the customer actually paid.
- **Gift cards** are not sales for VAT: selling one is never sent to KRA
  and is not counted as profit (it becomes income when it is spent).
- **Stock (v1.11).** Stock received, adjusted, written off and returned is
  reported with `insertStockIO` (its own gapless number per store) and the
  item's remaining stock with `saveStockMaster`, after the related invoice.
- Products are registered with KRA automatically the first time they sell.
  Their tax type and item class sync to every till in the shop.
- Weighed quantities that don't fit KRA's 2 decimals (0.125 kg) are sent as
  one line at the exact amount, with the real quantity in the item name.
- KRA's key (cmcKey) stays in the cloud; tills never hold it.
- Only sales made after eTIMS was switched on are sent.

## The QR code

Paper, screen and WhatsApp receipts all carry KRA's verification QR.
Thermal printers get it as a picture by default (`GS v 0`), which every
ESC/POS printer prints. If a printer prints the picture badly, Settings →
Printer → *KRA QR code* → **Printer's own QR** uses the printer's built-in
QR command (`GS ( k`) instead.

## When KRA refuses a sale

Settings → eTIMS → *Send queue* shows KRA's message. Fix the cause
(usually the item class or tax type), then **Send again now**. Network
problems are retried automatically with backoff.

## Not covered yet

- Supplier purchases and imports (`insertTrnsPurchase`, `selectImportItemList`)
  are not sent. Goods received are reported as stock movements.
- Not yet run against the KRA sandbox: that needs the shop's PIN and a
  sandbox OSCU device from the eTIMS portal.

Spec: KRA OSCU Specification v2.0. Cloud: `supabase/functions/etims`,
`db/deploy-v1100-etims.sql`, `db/deploy-v1110-etims-order.sql`. Till: `internal/services/etims.go`.
