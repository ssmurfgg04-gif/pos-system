# Receipts, WhatsApp & Notifications — LedgerPOS

What the till prints, what it sends to the customer's WhatsApp, and how
to prove a receipt is genuine. Current as of v1.6.0.

## The four receipt renderers (one content contract)

Every sale has four renderable copies, and they all reconcile the same
way — **Subtotal → Discount (with the reason) → Points redeemed as money
→ VAT (rate + "included" when tax-inclusive) → TOTAL**:

| Renderer | Where | Carries |
| --- | --- | --- |
| Branded image (PNG) | WhatsApp (automatic + Share button) | logo/monogram, brand color, items, discount, points, VAT, TOTAL, M-Pesa code, gift cards, tab balance |
| Thermal paper (ESC/POS) | the counter printer | same rows, honest payment legs, KRA PIN, gift-card codes |
| Browser print (HTML) | `/orders/:id/receipt`, the no-printer fallback | same rows, prints from any browser |
| On-screen modal | the receipt screen after a sale | same rows + Share/Print buttons |

Content rules every copy follows:

- **Contact row** (shop phone · address) and the shop's **KRA PIN** when
  set; a buyer KRA PIN turns the header into **TAX INVOICE** and prints
  the buyer's PIN (eTIMS habit).
- **Honest payment legs:** an unconfirmed M-Pesa leg says
  "M-Pesa (pending)" — never "Paid". A PENDING sale's branded image
  carries a `* * * PENDING * * *` banner; a voided one `* * * VOIDED
  * * *` in red. Neither can be mistaken for proof of payment.
- **Gift-card codes minted by the sale print on every copy** — the code
  IS the value.
- **Tab charges show the customer's balance after this charge.**
- Dates read day-first (Kenya), from the **paid** time where one exists.

## Thermal receipt printing

The receipt pipeline is fully offline:

- `internal/printer` owns a worker that drains `print_jobs` (queued by
  payment completion inside the same transaction that marks the sale
  PAID). Targets: ESC/POS network printers (`tcp://ip:9100`) or a local
  device file (`file:///dev/usb/lp0`) — configured in Settings
  (`printer_target`, `printer_width` 58/80).
- `internal/escpos` renders the ticket: brand logo (when a logo is on
  file and `receipt_logo` is enabled), store identity, itemized lines,
  the reconciling totals block (above), payment legs, M-Pesa receipt
  code, and the eTIMS-ready tax-invoice header when the shop's KRA PIN
  is on file (`kra_pin`).
- Auto-print after payment is a setting (`auto_print_receipts`); cashiers
  can re-print from the order screen.

## Browser print fallback

`/api/v1/orders/:id/receipt` renders a printable HTML receipt for shops
without a thermal printer — same content contract, browser print dialog,
still fully offline on the LAN.

## WhatsApp receipts — automatic, plus the Share button

One gateway (whapi.cloud), two triggers, one delivery pipeline.

### 1. Automatic (v1.5.0): every paid sale sends itself

When a sale completes as PAID and a whapi token is saved on the till,
the completion hook enqueues exactly one job in `whatsapp_jobs`; a
worker (one per shop, next to the STK sweeper) delivers the **branded
receipt image** to the customer's number. Zero cashier steps. The
customer's number is taken from the checkout's phone field / CRM record,
falling back to the **payer phone on the payment leg** — a walk-in STK
sale still gets its receipt.

### 2. Manual (v1.6.0): the Share-via-WhatsApp button

The receipt screen's **Share via WhatsApp** button is the human path —
built for the weeks the gateway sits in review, and for "I didn't get
it" re-sends forever after:

- **Gateway connected** (PAID order): one tap queues the branded image
  to the customer's number through the gateway — the picture arrives on
  the shop's own WhatsApp with the thank-you caption, nothing for the
  cashier to attach. Every attempt lands in the Settings queue list and
  the audit log (`WHATSAPP_MANUAL_SEND`).
- **No gateway:** the button shares the very same PNG from the device —
  the phone's share sheet (image + caption straight into WhatsApp)
  where the browser supports file sharing; otherwise it saves the image
  and opens the customer's `wa.me` chat with the caption typed, and the
  cashier drags the picture in and presses send.
- **Format: PNG on purpose** — for a text receipt it is smaller than a
  JPEG at usable quality, lossless (the watermark's exact layer survives
  every lossless copy), and WhatsApp uploads it like any photo.
- Note: browsers only offer file sharing on HTTPS. On the LAN
  (`http://…`) the Save + attach fallback is the expected path.

### The caption

Short English thanks, same on both paths: "Hi {name}! Thank you for
shopping with {store} — we're glad you chose us." (no-name variant when
the checkout had none). No SMS/email fallback by design: Safaricom
already texts the customer about the M-Pesa payment.

### Delivery contract

- Gateway: whapi.cloud, `POST https://gate.whapi.cloud/messages/image`,
  Bearer auth, multipart fields `to`, `caption`, `media` (the PNG).
  Numbers are normalized to 2547…; malformed numbers are rejected by
  the job, not the sale.
- Bounded retries with backoff (5 attempts, a minute apart), then
  dead-letter with the error in the audit log AND the order's activity
  timeline. Jobs persist in SQLite — a restart never loses a receipt.
- **Guardrails:** sending never blocks or fails a sale (best-effort,
  like printing); no token = no automatic job at all — the sale
  completes normally and the Share button falls back to the device;
  only PAID orders send; a job whose order is no longer PAID (voided
  before the worker reached it) is cancelled, not sent; the gateway key
  is a masked secret (`__SET__` on read) that never team-syncs — each
  till that completes sales needs its own key.
- Auto-send defaults ON (`whatsapp_auto_send`) but does nothing until a
  token exists — the flag only controls an already-connected till.

### Admin setup (once per till, Settings → WhatsApp)

1. Create a whapi.cloud account, link the shop's WhatsApp number by
   scanning the QR once, copy the API token.
2. Settings → WhatsApp → paste the token (it saves masked), press
   **Send test** to prove the pipe, done. From the next paid sale on,
   receipts go out on their own.
3. **Failed sends** lists undelivered jobs with their error and a
   **Retry** button — the one place to watch; nothing ever nags the
   cashier.
4. Caution: this is a normal business-use pattern, but the number CAN
   be banned for spam — receipts to paying customers only, never bulk.

## The invisible watermark — proving a receipt is genuine

Every branded image carries a hidden payload: magic + order id + issue
time + total, signed with a truncated HMAC-SHA256 keyed by a per-shop
secret (`receipt_watermark_key`, generated on first use, riding team
sync so any till can verify any till's receipts — never exposed in the
UI, never on the receipt). Two layers, because lossy copies exist:

- **Exact layer** — the payload in the red channel's least significant
  bits: survives every lossless copy (save, forward as document), dies
  on any re-encode.
- **Re-encode-safe layer** — the same payload, every bit tripled, as
  barely-darkened cells (~3%) in a dedicated pure-white strip under the
  footer: invisible at receipt contrast, but a majority of cells
  survive WhatsApp's own JPEG re-encoding and the extractor
  majority-votes each triple.

### Verify a receipt (Settings → WhatsApp → Verify a receipt image)

Upload the screenshot a customer sent "as proof" and read the verdict:

- **Genuine + unmodified + total matches the order + order is PAID** →
  "Genuine — issued by this shop, unmodified".
- **Genuine mark but the watermarked total differs from the order as it
  exists now** → flagged as doctored evidence (the hidden total is
  authoritative: any other amount on the picture was edited in).
- **Issued against a VOIDED/PENDING order** → "not proof of payment".
- **AI-generated, foreign-shop, or cropped-to-nothing images** →
  "no valid watermark — image was not issued by this shop, or was
  modified/re-encoded past recovery" (fail-closed).
- Verification is admin-only for now (Settings); cashiers settle
  disputes by calling the owner.

## Other notifications

- **WS events** (`internal/services/services.go` Event* constants) push
  live updates to every open till/POS tab: ORDER_PAID, ORDER_VOIDED,
  SHIFT_UPDATED, SETTINGS_UPDATED, TEAM_SYNC_UPDATED. Receipts, order
  lists and settings screens re-render without polling.
- **LAN delivery** between tills (stock changes, orders, shifts) rides
  the same sync events — a sale or shift opened on till A shows on till
  B without a cloud round trip (shifts ride team sync since v1.4.6).
- **Print failure surfacing**: stuck print jobs keep their attempt count
  and last error on `print_jobs`; the till surfaces them instead of
  silently swallowing a receipt.
- **Demo boxes stay clean (v1.6.0):** a `SEED_DEMO=true` server never
  pulls the cloud key vault — no production payment keys or WhatsApp
  tokens on a test till, and no throwaway devices in the cloud's
  approval list.

## Open items (tracked)

- Signed public receipt links (browser-openable receipt page) — needs
  the scoped public route + TTL design before implementation.
- Payment-label vocabulary still differs slightly between the on-screen
  modal and the server renderers (cosmetic; one shared label map is
  tracked for v1.6.1).
- Staff-level receipt verification (the fraud playbook currently ends
  with "call the owner").
