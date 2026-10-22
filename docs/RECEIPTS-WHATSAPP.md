# Receipts, WhatsApp & Notifications — LedgerPOS

What the till prints, what it could send, and where the roadmap stands.
Current state as of commit `b4d4426` or later.

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
  can re-print from the order screen.

## Browser print fallback

`/api/v1/orders/:id/receipt` renders a printable HTML receipt for shops
without a thermal printer (ReceiptHTML handler) — same content, browser
print dialog, still fully offline on the LAN.

## WhatsApp — current status and the decision

Official WhatsApp Business API (WABA) is a PAID channel: Meta bills per
conversation, requires business verification, and the integration
upfront cost (template approval, phone number hosting, webhook plumbing)
is not justified for the current fleet. **Decision: do NOT build WABA
into the product now**; revisit if a customer segment explicitly demands
automated customer receipts.

What makes sense when we revisit (fastest first):

1. **Share-as-link (zero cost, works today via the browser):** the
   receipt HTML endpoint can be exposed through a signed, expiring
   public link so a cashier can tap "Share" → WhatsApp with a URL the
   customer opens. No Meta account, no per-message cost. Needs a
   scoped public route (token in path, short TTL) — the M-Pesa callback
   pattern (unguessable id + rate limit) already covers the security
   shape.
2. **Click-to-chat deep links (zero cost):** prefill a
   `wa.me/<phone>?text=<summary>` with the sale summary. No API, no
   template approval; limited to ~1800 chars and manual send, which is
   fine for "here's your receipt summary".
3. **SMS via the M-Pesa rails' own confirmation** is already the
   customer's canonical proof — the till should not duplicate it.
4. **WABA** only if automated branded receipts become a selling point.

Registration note (owner): portal signups that hit Cloudflare blocks can
be completed through a temp-mail service; the owner's own numbers stay
on real mail.

## Other notifications

- **WS events** (`internal/services/services.go` Event* constants) push
  live updates to every open till/POS tab: ORDER_PAID, ORDER_VOIDED,
  SHIFT_UPDATED, SETTINGS_UPDATED, TEAM_SYNC_UPDATED. Receipts, order
  lists and settings screens re-render without polling.
- **LAN delivery** between tills (stock changes, orders) rides the same
  sync events — a sale on till A shows on till B's stock view without a
  cloud round trip.
- **Print failure surfacing**: stuck print jobs keep their attempt count
  and last error on `print_jobs`; the till surfaces them instead of
  silently swallowing a receipt.

## Open items (tracked)

- Signed public receipt links (item 1 above) — needs the scoped public
  route + TTL design before implementation.
- If WABA is ever revisited: template approval + verification paperwork
  should start 2–3 weeks before the customer's go-live.
