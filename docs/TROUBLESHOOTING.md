# Troubleshooting

Field guide for the counter. Sorted by what you SEE, not by what broke.
Anything marked ⚙ needs an admin.

## M-Pesa STK problems

### "An M-Pesa prompt was sent to this phone moments ago…" (409)
The till refused to send a second prompt while the first session may still
be open (KCB rejects doubles with "Duplicated MSISDN"). Wait for the
customer to enter their PIN or cancel the prompt; the retry unlocks
automatically. Nothing was charged.

### "The customer's phone already has an active M-Pesa session (KCB: …)"
The phone had a session from somewhere else (another till, another app, an
unfinished USSD menu). Let them finish/cancel it, then retry. The failed
attempt is marked FAILED with the reason — no money moved.

### Prompt never arrives on the phone
1. Check the phone number format (07XX / 2547XX / 2541XX).
2. Ask the customer to look under **"My SIP messages"** — KCB prompts land
   there on some phones.
3. Wait ~30s; some networks deliver late.
4. If nothing: **Check payment status** (asks the server to re-verify),
   or switch to **manual** — customer pays to the Paybill/till and you key
   the 10-character receipt code. The pending order keeps the cart intact;
   void the dead attempt from Orders ⚙ if it was not yours.
5. Recurring "prompt sent, nothing arrives" on MANY customers ⚙: check
   Settings → Payments shows route **buni** and the till can reach the
   internet; then contact the owner (Buni account/shortcode side).

### "Payment not confirmed yet" after the customer says they PAID
- Press **Check payment status** first — late payments land there.
- Then **manual entry**: the receipt code from their M-Pesa SMS completes
  the sale. The system refuses duplicate receipt codes (each code = one
  sale), so a reused code is a real-world problem, not an app bug.
- If the amount they paid differs from the order total, the system flags a
  **discrepancy** instead of silently completing — ⚙ review the payment
  legs, void or adjust deliberately, refund any overpayment.

### Order stuck PENDING for hours ⚙
Open the order: if the M-Pesa leg shows FAILED "STK timeout", the sale is
dead — void it or take payment fresh. If a leg is still PENDING with a
BUNI checkout id, the claim sweep is still listening; if the customer paid
but nothing lands after 30+ minutes, run **Check payment status**, then
check internet on the till (offline tills catch up automatically when the
network returns).

## Offline / sync

### "Offline — sales are saved on this device" banner
Keep selling. Cash sales work fully offline; they sync automatically when
the network returns (you will see "Synced N offline sales"). M-Pesa STK
needs the internet, so offline the app steers you to manual entry or cash.

### Synced sales did not appear on the other till ⚙
Settings → System shows sync status (pending/failed events). A till that
stays red needs its internet fixed; events are queued, not lost. After any
crash mid-sync the outbox reconciliation repairs itself on next boot —
check Settings → System for outbox failures and tell the owner if they
grow.

## Printing

### Receipt did not print
1. Is the printer showing in the OS (USB/Bluetooth pairing)?
2. ⚙ Settings → Printing: run **Test print**; if it fails, check the
   printer target and width (58 vs 80 mm).
3. Auto-print off? The receipt button reprints anytime from the paid
   screen or Orders.
4. Paper Jam/garbled text: wrong width or ESC/POS vs driver printing —
   set the target to the ESC/POS device rather than the system print
   dialog.

### Receipt shows wrong store name ⚙
Branding syncs from the cloud; if one till shows an old name, that till
was offline during the change — open Settings once online and it catches
up. (Older builds had a save bug here; it is fixed — update the till.)

## Discrepancies & variances

### Z-report variance at close ⚙
Expected = opening float + completed cash sales during the shift. Compare
with the order list for that window:
- Cash sale voided after close started? The restore moves the expected.
- Payout/expense from the drawer? There is no cash-movement feature yet —
  note it on paper (see CHANGELOG known limitations) and count around it.

### "Amount discrepancy, review" pill on an order ⚙
The recorded tender did not match the order total when it completed. The
sale is NOT silently altered: open the order, read the payment legs
(each has status + result text), fix deliberately (void + resell, or
refund the difference to the customer).

## Updates & the app itself

### "Update available" toast ⚙
Let the sale finish, then restart the app when quiet — updates apply on a
quiet moment automatically; the install is signed and verified before it
runs. Never kill the app mid-payment: it waits until no payment is in
flight.

### App says version X but the site shows Y
The till updates on restart. Reboot the till app; if the banner keeps
reappearing, check internet. The download site itself only refreshes when
a release ran — that is an owner/CI matter, not a till fault.

### White screen / crash
Restart the app. If it comes back broken, restore the day's backup ⚙
(docs/OPERATIONS.md → restore) and report the last action before the
crash — the audit log and local logs pin it down.
