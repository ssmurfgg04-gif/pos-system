# Remote Owner Playbook — watch the shop from anywhere

The till stays in the shop. You don't have to. This playbook gives an owner
in Nairobi full visibility into a shop in Kiambu (or anywhere) with no code
changes: an encrypted tunnel plus a read-only login.

## 1. Put the shop on Tailscale (10 minutes, once)

1. On the shop machine, install Tailscale (Windows: `tailscale-setup.exe`,
   enable "run as service" so the tunnel survives reboots and nobody has to
   be logged in).
2. On your phone/laptop, install Tailscale and join the SAME tailnet
   (same account).
3. On the shop machine, run `tailscale ip` (or check the admin console) and
   note its `100.x.y.z` address.
4. Open `http://100.x.y.z:8765` from your device (port 8765 is the desktop
   default; server mode uses whatever `PORT` is set to). The shop's server
   binds all interfaces in server mode, and Tailscale looks like just
   another interface — nothing to configure in LedgerPOS.

Notes:
- The printer and cash drawer stay local-only (USB/LAN at the shop) — remote
  only *views*; it never prints.
- Tailscale is end-to-end encrypted (WireGuard). Do NOT expose the shop port
  directly to the internet — LedgerPOS serves plain HTTP by design for LAN.

## 2. Create the read-only Owner role (5 minutes, once)

Settings (as admin) → People → Roles → New role:

- Name: `Owner`
- Permissions (read-only recipe): `reports.view`, `orders.view`,
  `products.view`, `audit.view`
- Optional: add `shifts.manage` if the owner closes shifts remotely.
  Never add `pos.sell`, `pos.void`, `users.manage`, or `products.manage`.

Then People → Users → create the owner account on that role with a strong
password + PIN. The first login forces rotation like everyone else.

## 3. The daily 2-minute check (from anywhere)

1. **Reports → Daily**: sales total, cash vs M-Pesa split, order count.
2. **Reports → Monthly**: VAT running total for the accountant.
3. **Shifts**: today's float vs counted vs variance (variance ≠ 0 gets a call).
4. **Orders**: spot-check voids (every void carries a reason + audit row).
5. **Inventory**: low-stock list before supplier day.

## 4. Backups without commuting

Settings → System → Off-site backup: enter the S3-compatible bucket once
(R2 free tier works), set the passphrase, **write the passphrase down and
keep it off the shop machine** (photo it, WhatsApp it to yourself). From
then on every nightly snapshot encrypts and uploads itself with retries —
the status line shows last upload, queued retries, and errors.

Disaster drill (do it once, takes 5 minutes):
1. `ledgerpos restore-backup --list --endpoint … --bucket …` shows copies.
2. Restore the newest to a scratch file and open it (it's plain SQLite).
3. If step 2 works, the shop can burn down and you still have the books.

## 5. When to graduate past this

This covers one shop beautifully. When shop #3 opens, stop adding tunnels
and build the central relay (one cloud box all shops report to) — that's a
real project, not a playbook. Until then, tunnels + read-only logins win.
