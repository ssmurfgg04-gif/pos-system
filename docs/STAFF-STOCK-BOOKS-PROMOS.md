# Timecards, stock transfers, the books and promotions (v1.12)

Four features for shops that have grown past one till and one owner.

## Staff time clock and timecards

**Clocking.** Staff clock in and out with their own PIN on the PIN screen
(the **Clock in / out** tab), without signing in. Signed-in staff can also
use **Time** in the menu, which adds breaks. Clocking out during a break ends
the break.

**Timecards** (needs *See and correct everyone's timecards*): Time → Team
timecards.

- Hours per person: normal hours, overtime and estimated pay.
- **Set rate** stores an hourly rate (KES). With no rate, no pay is shown.
- **Add missed time** and the pencil fix forgotten punches. A reason is
  required and kept, with who made the change. Nothing is ever deleted;
  **void** takes an entry out of the totals.
- An entry still open after 16 hours is flagged ("probably forgot to clock
  out"). It is never closed automatically, because a guess would pay
  someone wrongly.
- **Payroll CSV**: one row per entry, then one summary row per person.

**Overtime.** Kenya's Regulation of Wages (General) Order sets a normal
week of 52 hours. Hours above that in one Monday–Sunday week are overtime at
1.5x the rate (Employment Act). Both numbers are settings
(`timecard_weekly_hours`, `timecard_overtime_rate`) for shops on a
different agreement. To get correct overtime, export whole weeks.

Entries from every till in the shop reach the owner through team sync.

## Stock transfers between shops

For businesses with more than one shop (Settings → Team). Needs *Send and
receive stock between shops*.

1. **Send stock.** Pick the shop, add products and quantities. The goods
   come off this shop's stock at once. With no internet the transfer waits
   and goes out automatically when the connection is back.
2. **The other shop** sees it under **To receive**. They type what actually
   arrived and press **Receive into stock**. Anything short is recorded as
   missing. A product that shop has never stocked is created from the
   sender's details, in the category "Received from other shops".
3. **Cancel** a transfer that nobody has received yet, and the goods come
   back on the shelf. Cancelling needs the internet.

Both sides show in the stock history and go to KRA as stock movements
(13 out, 04 in) when eTIMS is on.

Safety:

- **Only one till receives a transfer.** The cloud decides which till
  receives it; a second till is refused.
- **Crash-safe.** If a till stops halfway, it finishes the job the next time
  it opens Transfers.
- **Cancel is final.** A cancel made before the transfer reached the cloud
  still holds, so a late send can never deliver goods that were cancelled.

## The books: QuickBooks and Xero

Menu → **Books** (needs *Export the books*). The till makes one balanced
journal entry per day, which is how a POS normally hands over to an
accountant:

| What happened | Debit | Credit |
|---|---|---|
| Sales | Cash / M-Pesa / Card / Customer tabs / Store credit (each way it was paid) | Sales (without VAT and gift cards), VAT, Gift cards |
| Returns | Sales, VAT | Cash or Store credit |
| Goods sold | Cost of goods sold | Inventory (less goods returned) |
| Write-offs | Stock write-offs | Inventory |
| Expenses | Expenses | Cash / M-Pesa / Bank |
| Tab payments, credit top-ups | Tab receipts account | Customer tabs / Store credit |
| Gift card turned into credit | Gift cards | Store credit |

Every entry balances to the cent.

- **Rounding.** If a sale was recorded as paid with a different amount than
  its total, the difference goes to Rounding. The page shows it so you can
  check that day.
- **QuickBooks Online:** download **QuickBooks CSV**, then Settings →
  Import data → Journal entries. Accounts are matched by **name**.
- **Xero:** download **Xero CSV**, then Accounting → Manual journals →
  Import. Accounts are matched by **code**. Debits are positive, credits
  negative.
- **Accounts** at the bottom of the page: set the names and codes to match
  your accountant's chart of accounts once.
- **Settings.** `accounting_date_format` defaults to dd/mm/yyyy; set it to
  match how your books show dates. `accounting_xero_tax_rate` defaults to
  "Tax Exempt" (VAT already has its own line).
- **Timing.** Export days that are finished: a sale voided later changes its
  day.

## Promotions by SMS or WhatsApp

Menu → **Promotions** (needs *Send promotions to customers*).

**Consent first.** Kenya's Data Protection Act requires a customer's
agreement for direct marketing. Tick **OK to send offers** on a customer
only when they agree. It is off for everyone until then.

- **Opt someone out** stops offers to a phone number on every till,
  including promotions already running.
- Every message ends with the opt-out line (`promo_footer`, editable under
  Sending setup).

**Sending setup:**

- **SMS:** your own Africa's Talking account (username, API key, and
  optionally an approved sender ID). Use the username `sandbox` to test
  without really sending.
- **WhatsApp:** the connection in Settings → WhatsApp. It sends slowly (a
  few seconds apart) so the number is not flagged.
- **Quiet hours:** default 20:00–08:00 Nairobi time. A running promotion
  waits and resumes in the morning.
- **Test:** send a test to your own phone first.

**A promotion:**

1. **New promotion.** Write the message (`{name}`, `{points}`, `{shop}` and
   `{phone}` fill in per customer). Optionally narrow who gets it: spent at
   least, bought recently, not back for N days, loyalty points, owes on a
   tab.
2. The preview shows how many customers it will reach, how many were left
   out (no consent, bad number) and the SMS count before you send.
3. **Create** makes a draft. Nothing goes out until you press **Send**.
   You can Pause, Resume or Cancel.

**Problems while sending:**

- **Africa's Talking account trouble** (no credit, sender ID not approved):
  the promotion pauses. Fix it in your account, then press Resume.
- **Network errors:** retried with a back-off, at most 3 times per
  customer.
- **Never sent twice:** each customer gets the message at most once. If the
  till stops while a batch is going out, that batch is marked "result
  unknown" instead of being sent again.

## Cloud and database

- Migration v32 adds `time_entries`, `stock_transfers`, `promo_campaigns`,
  `promo_recipients`, `users.hourly_rate_cents` and
  `customers.marketing_opt_in`, and grants the new permissions to Owner and
  Admin (Front Desk gets transfers).
- `db/deploy-v1120-transfers.sql` holds the cloud tables and functions for
  transfers. It is already applied to the cloud project.
