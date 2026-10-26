# Cashier Guide (Front Office)

Everything a till operator needs day to day. If you are setting up a new
till computer instead, hand it to an admin — this guide assumes the app is
already installed and you have a login (password or 4-digit PIN).

## Signing in

- Open the POS app on the till. Enter your username + password, or tap your
  name on the PIN screen and type your 4-digit PIN.
- Wrong PIN five times locks the screen for a moment — wait it out; the
  lockout grows if guessing continues.
- If your account shows "rotate password", set a new password when asked.
  That is normal for first logins and after an admin reset.
- Your session lasts 12 hours. If the app logs you out mid-sale, the basket
  is kept — sign in again and pick up where you stopped.

## Your sales visibility (important, v1.4.4)

Staff see **their own** sales: your Orders list, your receipts, your
refunds. You cannot open another cashier's or another till's sale records —
those need an admin login. This is deliberate: sale records are money
records. If a customer needs help with a sale you did not ring up, get an
admin (they can see everything).

## Opening a shift

1. Go to **Shifts** and tap **Open shift**.
2. Count the float (the opening cash in the drawer) and enter it. The float
   anchors every cash check at close — take it seriously.
3. Sell. Shifts are per cashier: if two of you share one till, each opens
   their own shift and each closes their own drawer.

## Making a sale

1. **Find items**: tap a category, type in the search box, or scan a
   barcode (scanners type the code — the app fills the basket by itself).
   The F2 key jumps to the search box.
2. **Quantities**: tap an item repeatedly or use the +/− on the cart line.
   Two identical items merge into one line.
3. **Discounts** (if you have permission): tap the discount field, enter
   the amount and a label ("staff sale", "loyal customer"). Order-level
   discounts are always audited with your name.
4. **Price overrides** (if you have permission): change a line's price —
   the override is recorded separately from a discount.
5. **Charge** and pick the tender:
   - **M-Pesa STK** — enter the customer's phone (07XX / 2547XX / 2541XX),
     press charge. A prompt pops on their phone; they enter their PIN. The
     screen watches the payment for 15 minutes and completes by itself when
     the money lands.
   - **Manual M-Pesa** — the customer paid to the Paybill/till themselves.
     Take the 10-character receipt code from their SMS and key it in.
   - **Cash** — enter what they handed you; the app shows change.
   - **Store credit / tab** — pick the customer. Tabs must fit their credit
     limit; the server checks, not the screen.
   - **Split** — "Split payment" lets you combine (e.g. cash + M-Pesa, or
     cash + credit). The legs must add up exactly. One M-Pesa leg can wait
     for its PIN while the cash part is already taken.
6. **Receipt** prints automatically when the sale completes (if the till
   has a printer). Use the receipt button to reprint.

## While an M-Pesa prompt is on the customer's phone

- The **Resend** button is locked on purpose. Sending a second prompt while
  one is open is rejected by the phone ("Duplicated MSISDN") and confuses
  the customer.
- Ask them to check "My SIP messages" if they do not see the prompt.
- They can cancel the prompt — the screen unlocks for a retry the moment
  the cancel lands.
- Wrong number? Leave the order pending and start the sale again (the
  pending order can be voided from Orders — see below).

## Refunds, cancellations, voids

- **Voids**: your own sales can be voided from the order detail — pick a
  reason (wrong item, customer changed mind, duplicate…). Voiding a PAID
  sale restores the stock and, for store credit/loyalty, reverses the
  points. If real money had already landed (M-Pesa/card), the system flags
  a **refund owed** so the admin returns the money through the provider.
- **Someone else's sale**: you cannot void it — that is an admin action.
- **Returns**: there is no separate "returns" document yet. Practice today:
  void the wrong sale (admin if it is not yours) and ring up the corrected
  one. The audit trail keeps both actions.

## Closing a shift

1. **Shifts → your open shift** shows the X report anytime (expected cash
   so far, tenders, sales).
2. **Close shift**: count the drawer and enter what is actually there.
3. The Z report shows **expected vs counted** and the variance. Small
   variances happen (change given without recording, dropped coins); if you
   cannot explain one, tell the admin the same day — do not adjust the
   drawer silently.

## If something breaks

See **docs/TROUBLESHOOTING.md** (the admin usually keeps it printed at the
back office): M-Pesa failures, offline selling, printer jams, payment
discrepancies. The short version: offline sales are saved on the device and
sync when the network returns — keep selling; nothing is lost.
