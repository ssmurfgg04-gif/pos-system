# Receipts, WhatsApp & Notifications — LedgerPOS

What the till prints, what it sends to the customer's WhatsApp, and how
to turn automated sending on. Current as of v1.5.0.

## Thermal receipt printing

The receipt pipeline is fully offline:

- `internal/printer` owns a worker that drains `print_jobs` (queued by
  payment completion inside the same transaction that marks the sale
  PAID). Targets: ESC/POS network printers (`tcp://ip:9100`) or a local
  device file (`file:///dev/usb/lp0`) — configured in Settings
  (`printer_target`, `printer_width` 58/80).
- `internal/escpos` renders the ticket: brand logo (when a logo is on
  file and `receipt_logo` is enabled), store identity, itemized lines,
  VAT breakdown (via the canonical `internal/money` math), payment legs,
  M-Pesa receipt code, loyalty earned, and the eTIMS-ready tax-invoice
  header when the shop's KRA PIN is on file (`kra_pin`).
- Auto-print after payment is a setting (`auto_print_receipts`); cashiers
  can re-print from the order screen. An M-Pesa leg that has not
  confirmed yet prints plain "M-Pesa" — never a fake "Paid".

## Browser print fallback

`/api/v1/orders/:id/receipt` renders a printable HTML receipt for shops
without a thermal printer (ReceiptHTML handler) — same content, browser
print dialog, still fully offline on the LAN.

## WhatsApp receipts (v1.5.0) — automatic only, one gateway

**Policy: automatic-only, one gateway (whapi.cloud), zero cashier
steps.** When a sale completes as PAID and a whapi token is saved on the
till, the completion hook enqueues exactly one job in `whatsapp_jobs`;
a worker (`WhatsappWorker`, one per shop, next to the STK sweeper)
delivers a **branded receipt image** to the customer's number. There is
no manual send button, no provider picker, no cashier prompt anywhere —
the only WhatsApp UI is Settings → WhatsApp (admin). No SMS or email
fallback by design: Safaricom already texts the customer about the
M-Pesa payment; WhatsApp is purely the shop's branded extra.

### The branded receipt image

`internal/services/receipt_image.go` renders a PNG at send time from the
same data the paper receipt uses:

- The shop's uploaded logo (Settings → uploaded logo file) at the top —
  or a brand-colored monogram fallback when no logo is on file.
- Brand-colored header with the store name, then the itemized lines,
  discount, VAT and TOTAL, and the payment legs with the M-Pesa receipt
  code. Automatic sends only happen for PAID orders, so every leg is
  complete — no "(pending)" markers here either.
- The message caption is short English thanks: "Hi {name}! Thank you
  for shopping with {store} — we're glad you chose us." (or the
  no-name variant when the checkout had no customer name).

### Delivery contract

- Gateway: whapi.cloud, `POST https://gate.whapi.cloud/messages/image`,
  Bearer auth, multipart fields `to`, `caption`, `media` (the PNG).
  The customer's number is normalized to 2547… from the checkout's
  phone field / CRM record; malformed numbers are rejected by the job,
  not the sale.
- Bounded retries with backoff (5 attempts, a minute apart), then
  dead-letter with the error in the audit log AND the order's activity
  timeline. Jobs persist in SQLite — a restart never loses a receipt.
- **Guardrails:** sending never blocks or fails a sale (best-effort,
  like printing); no token = no job at all — the sale completes normally
  and nothing is queued; only PAID orders queue; a job whose order is
  no longer PAID (voided before the worker reached it) is cancelled,
  not sent; the gateway key is a masked secret (`__SET__` on read) that
  never team-syncs — each till that completes sales needs its own key.
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

## Open items (tracked)

- Signed public receipt links (browser-openable receipt page) — needs
  the scoped public route + TTL design before implementation.
