# Admin & Management Guide

For the owner, managers and back-office staff. Configuration lives in
**Settings**; oversight lives in **Orders**, **Reports** and **Shifts**.
Anything about installing/updating tills or the cloud is in
`docs/DEPLOYMENT.md` and `docs/OPERATIONS.md`.

## Roles and permissions

Roles are editable per shop (**Settings → Staff/Roles**). The catalog has
30 server-enforced permission keys — hiding a button in the UI is never the
security boundary; the server checks every request.

The one that matters most since v1.4.4:

- **`orders.view.all` — "Browse all individual sales (every till and
  cashier)"**. Admin/Owner have it by default. Everyone else — Cashier,
  Front Desk, Branding, Cyber, Designer — sees **only their own sales**,
  no matter what they type into the URL. Grant it only to people who
  should have cross-till oversight.
- `reports.view` — dashboards and daily/monthly aggregates (including
  other staff's shift Z reports). Aggregates, not individual sale records:
  Front Desk has it by default for the day's shape of business.
- `payments.manual` — manual M-Pesa receipt entry. `payments.apply_discount`
  / `payments.override_price` — money-affecting checkout powers.
- `audit.view` — the audit log. Check it after any "something is off"
  report; every money path writes there with the acting user's name.

## Sales oversight and reconciliation

- **Orders** (with `orders.view.all`): browse every sale by status, date
  range, cashier, customer or receipt. Each order carries its full
  traceability chain — originating till, cashier, timestamps, every
  payment leg with status, void reason. The detail drawer shows the
  timeline (notes, events) and lets you void or record payments.
- **By till**: **Reports → daily** has a "By till" table since v1.4.4. Its
  rows are derived from the same orders table as the sales list, so the
  per-till totals reconcile with the orders screen by construction. Sales
  dated before v1.4.4 group under "Unattributed (pre-update)".
- **Reports**:
  - *Daily*: sales, paid/open/voided counts, tender breakdown (cash,
    M-Pesa, card-history, credit), top products, per-staff, by-till, new
    customers. Tenders are keyed to the parent order's day — the money
    report matches the Orders screen 1:1 in the same window.
  - *Monthly*: the same shape per month, CSV export for the books.
  - *KRA*: per-order historic tax rates for VAT filing; buyer KRA PIN
    (tax invoice) support is live at checkout. eTIMS integration is a
    known gap — see CHANGELOG "Known limitations".
- **Shifts**: X/Z reports for any cashier (with `reports.view`), variance
  review. Investigate recurring variances the same day; the audit log and
  the order list for that window usually explain them.
- **Discrepancies**: orders whose recorded tender did not match the amount
  due (double-completed legs, forged callbacks — which the system refuses)
  carry a discrepancy flag; the daily report counts them. Each one is an
  admin to-do: confirm what happened in the payment legs, then void/adjust
  deliberately.

## Staff management

- **Users**: create staff, assign roles, deactivate leavers. Deactivation
  keeps every historical sale attributed correctly (never delete users).
- **Passwords/PINs**: admins never see passwords. Reset flows hand the
  user a one-time path; seeded default passwords (admin/admin123 etc.)
  are force-rotated and the app refuses any account that still uses them.
- **Join links**: new tills/staff join through invite links
  (`docs/REMOTE-SETUP.md`).

## Inventory and purchasing

- **Products/SKUs/categories**: CSV import/export (template in the
  screen). Photos sync across tills through the cloud.
- **Purchasing**: create a supplier, raise a PO (draft → receive). Stock
  moves only when you **receive** — the draft PO commits nothing. Weighted
  average cost updates on receive.
- **Stocktakes**: count sessions with a variance report. There is one
  tested flow (Inventory → Stock count); the older supplier-screen
  stocktake still exists but is legacy — prefer the stock count screen.
- **Adjustments**: the small +/- adjust action on a product (audited). Use
  it for breakage/spoilage; use stocktakes for real counts.
- **Low stock**: the low-stock view is the reorder list; there is no
  automatic purchase-order generation (deliberate — see ROADMAP).
- Known gap: there is **no per-product stock movements ledger** yet
  (stock truth is the guarded quantity updates + audit rows). See
  CHANGELOG "Known limitations".

## Configuration

- **Branding**: store name, logo, colors, receipt footer — syncs to every
  till through the cloud (no per-machine setup).
- **Payments**: M-Pesa route (Buni STK is the main rail; the Paystack card
  rail retired in v1.4.4), manual mode for shops that only key receipt
  codes, Paybill/till numbers shown on receipts and the manual screen.
- **Taxes**: VAT percent + inclusive/exclusive, canonical rounding in
  `internal/money` (half-away-from-zero). Per-order tax snapshots make
  historic KRA reports stable when the rate changes.
- **Receipts/printing**: printer target, width, auto-print toggle.
- **Backups**: automatic daily local backup + AES-256-GCM encrypted
  offsite copy — see `docs/OPERATIONS.md` for restore and drills.

## Working with other tills

- Tills sync through the cloud every few seconds (orders, products,
  customers, stock). A till that was offline replays its queued sales on
  reconnect — sales are never lost and never double-counted (idempotency
  keys).
- Stock is decremented at the SELLING till and replicated; the cloud is
  the tiebreaker for conflicts.
- The Android client (optional): any Android phone/tablet becomes another
  terminal on the shop Wi-Fi — see `docs/DEPLOYMENT.md` "Android client".
