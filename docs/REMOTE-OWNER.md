# Remote Owner Playbook — watch the shop from anywhere

The till stays in the shop. You don't have to. This playbook gives an owner
in Nairobi full visibility into a shop in Kiambu (or anywhere) with no code
changes: an encrypted tunnel plus a read-only login. It also covers the
one-time off-site backup setup so the books survive even if the shop
machine doesn't.

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
- Tailscale is end-to-end encrypted (WireGuard, peer-to-peer — no cloud
  relay in the data path). Do NOT expose the shop port directly to the
  internet — LedgerPOS serves plain HTTP by design for LAN.

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

## 4. Off-site backup without commuting (15 minutes, once)

Each shop gets its **own free Supabase project** — that is the whole
credential-exposure strategy: a leaked key opens that shop's bucket only,
and you rotate it in the dashboard in 30 seconds.

1. At supabase.com create a project (free tier: 500MB is plenty — a 20MB
   shop × 14 kept copies ≈ 300MB). Note the project URL.
2. Storage → **New bucket** named `ledgerpos`, **private** (not public).
3. Project Settings → API → copy the **`service_role` secret** (not anon).
4. In the shop: Settings → System → Off-site backup → Enabled. Fill:
   Project URL, Bucket `ledgerpos`, Service role key, keep 14.
5. Set the **backup passphrase** (any long phrase). **Write it down and keep
   it off the shop machine** — photo it, WhatsApp it to yourself. Without
   it the copies cannot be opened by anyone, including you.
6. Save, then **Back up now**. The status line should show the upload
   within a minute. From then on every nightly snapshot encrypts (AES-256)
   and uploads itself with retries — nobody has to be around.

Why Supabase instead of raw S3: plain Bearer auth (no signing keys dance,
immune to shop-PC clock drift which breaks SigV4), a dashboard owners can
understand, and Postgres under the hood if multi-store ever happens.

Disaster drill (do it once, takes 5 minutes):
1. `ledgerpos restore-backup --list --endpoint … --bucket ledgerpos` shows copies.
2. Restore the newest to a scratch file and open it (it's plain SQLite).
3. If step 2 works, the shop can burn down and you still have the books.

Restore needs the passphrase: `ledgerpos restore-backup --endpoint URL
--bucket ledgerpos --api-key KEY --passphrase '…' --out pos-restored.db`.
Keys can also come from `OFFSITE_API_KEY` / `OFFSITE_PASSPHRASE` env vars
so they never sit in shell history.

## 5. When to graduate past this

This covers one shop beautifully. When shop #3 opens, stop adding tunnels
and build the central relay (one cloud box all shops report to) — that's a
real project, not a playbook. Until then, tunnels + read-only logins win.
