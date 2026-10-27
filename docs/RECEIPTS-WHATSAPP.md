# Receipts, WhatsApp & Notifications — LedgerPOS

What the till prints, what it sends to the customer's WhatsApp, and how
to turn automated sending on. Current as of v1.4.6.

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

## WhatsApp receipts (v1.4.6) — the strategy

**Best path chosen: text receipts over WhatsApp, two delivery paths, one
message source (`internal/services/whatsapp.go`).** The receipt travels
as a formatted WhatsApp chat message (bold store name, monospaced
item/totals block, M-Pesa code) — it opens natively in any WhatsApp, no
PDF/JPG attachment to fail. Meta's official WABA API was deliberately
NOT used: business verification, template approval and per-conversation
billing are not justified for a single-shop receipt flow.

### Path 1 — Manual (always available, zero config)

Every receipt gains a **WhatsApp button** (receipt screen, next to
Print). Pressing it:

1. Asks the server to build the receipt text + a `wa.me` deep link.
2. Opens the SHOP's own WhatsApp (phone app or WhatsApp Desktop linked
   to the business number, e.g. 0181 630 570) with the receipt already
   typed to the customer's number.
3. The cashier presses send. No vendor, no API key, nothing can break
   the money path.

The customer's number comes from the checkout (phone field / CRM
record), normalized to 2547… — no typing at send time.

### Path 2 — Automated (opt-in, gateway)

When a linked-number gateway is configured in **Settings → WhatsApp**,
payment completion queues the receipt in `whatsapp_jobs` and a worker
(`WhatsappWorker`, one per shop, next to the STK sweeper) delivers it to
the customer's number:

- Presets: **whapi.cloud** and **ultramsg** (linked-number gateways —
  sign up, scan the QR with the shop's WhatsApp number, paste the API
  key), or a **custom** HTTP endpoint (`POST {to, text, order}`,
  Bearer auth).
- Bounded retries with backoff (5 attempts, 1 min apart), then
  dead-letter with the error in the audit log AND the order's activity
  timeline. Jobs persist in SQLite — a restart never loses a receipt.
- **Guardrails:** sending never blocks or fails a sale (best-effort,
  like printing); only COMPLETED orders queue; the gateway key is a
  masked secret (`__SET__` on read) that never team-syncs — each till
  that completes sales needs its own key; auto-send defaults OFF.
- A pending M-Pesa leg is honestly labelled "M-Pesa (pending)" in the
  text instead of "Paid M-Pesa".

### Setup checklist (Creative Divine)

1. Decide the sender number (the WhatsApp-linked business number).
2. For automated sending: create a whapi.cloud (or ultramsg) account,
   link the number by scanning the QR, copy the API token.
3. On the till(s): Settings → WhatsApp → pick the provider, paste the
   token, flip "Auto-send" on. Manual button needs nothing.
4. Caution: this is a normal business-use pattern, but the number CAN
   be banned for spam — receipts to paying customers only, never bulk.

## Other notifications

- **WS events** (`internal/services/services.go` Event* constants) push
  live updates to every open till/POS tab: ORDER_PAID, ORDER_VOIDED,
  SHIFT_UPDATED, SETTINGS_UPDATED, TEAM_SYNC_UPDATED. Receipts, order
  lists and settings screens re-render without polling.
- **LAN delivery** between tills (stock changes, orders, shifts) rides
  the same sync events — a sale or shift opened on till A shows on till
  B without a cloud round trip (v1.4.6 added shifts to team sync).
- **Print failure surfacing**: stuck print jobs keep their attempt count
  and last error on `print_jobs`; the till surfaces them instead of
  silently swallowing a receipt.

## Open items (tracked)

- PDF/image receipt attachment over WhatsApp (a document/file send via
  the gateway's media endpoint) — only if a customer segment asks.
- Signed public receipt links (browser-openable receipt page) — needs
  the scoped public route + TTL design before implementation.
