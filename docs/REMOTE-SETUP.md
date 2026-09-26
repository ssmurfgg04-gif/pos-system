# Remote setup — link a till without leaving your chair

Every till links itself to the team **automatically**: it finds its team in
the LedgerPOS cloud database (Supabase), registers its own identity, and
starts syncing. No join codes, no keys to type, nothing to copy-paste.

## One-time cloud facts (already deployed)

- Project: `https://ixxiqrobcwkvyjtxdkvh.supabase.co` (ledgerpos-backups)
- Team code: `KIAMBU-MAIN`
- Bootstrap row `sync_bootstrap#1` is public-read: project URL + team code +
  auto-approve. That is ALL a new till needs.
- Devices identify themselves in `sync_devices` (id + secret hash). The
  database decides who is who: `approved` / `revoked` columns gate every
  push and pull.
- Canonical cloud schema (tables + RPCs + RLS): **`db/cloud_schema.sql`**
  in the repo.

## Multiple stores under one owner (v1.1.1+)

One cloud project can carry any number of stores — your first store behaves
exactly as before, and every new store grows under the same umbrella:

- `sync_stores` is the registry: one row per store (slug, name, team code).
  The **team code is minted by the database** — the app never invents one.
- While a project has **exactly one store**, a new till auto-joins it
  (zero-config, unchanged).
- The moment a **second store** exists, a new till registers as *pending*
  and waits. The owner assigns it from any approved till:
  Settings → Team → *New tills waiting for a store* → pick the store →
  **Assign**. The till joins within ~20 seconds, on its next heartbeat.
- **Isolation is enforced in the database, not the app**: events are
  partitioned by `team_code`, every RPC verifies the caller's device
  identity and stamps the team server-side, RLS denies anon direct table
  access, and a till can only ever read/write its own store's rows.
- **Add a store**: Settings → Team → *Stores in the cloud* → name it →
  **Add store**.
- **Revoke a till** (stolen, sold, retired): Settings → Team → roster →
  **Revoke** — or `update sync_devices set revoked = true where device_id = 'dev-…';`

## Bring a new till online (remote, 2 steps)

1. **Update the app** on the till: Settings → System → Updates → Check →
   Download → Install (v1.1.0 or newer carries zero-config sync). If the
   till is on the old version, install the latest from
   https://awesomeposs.netlify.app/ (download page) — same app, double-click.
2. **Nothing else.** On the next start the till reads the bootstrap row,
   registers itself, and appears in Settings → Team on every device with a
   green "Approved" chip. Products, prices, customers, orders, void reasons
   and store settings flow over WiFi every ~20 seconds.

To verify from anywhere: open Settings → Team on YOUR machine — the Kiambu
devices show up in the roster with version + last-seen.

## Approving / revoking a device (SQL, only when auto-approve is off)

```sql
update sync_devices set approved = true  where device_id = 'dev-...';  -- approve
update sync_devices set revoked  = true  where device_id = 'dev-...';  -- kill switch
update sync_bootstrap set auto_approve = false where id = 1;           -- tighten later
```

Run via the Supabase SQL editor or the Management API.

## Payments on a new till (per machine, by design)

M-Pesa and card payments both run through **Paystack**: with Paystack
connected, the checkout's M-Pesa button opens the Paystack popup (the
customer picks M-Pesa / mobile money / card inside it) — no Safaricom
Daraja keys needed. A direct-Daraja path remains under Settings → Payments
→ Advanced for shops that prefer it.

The Paystack **secret** key never syncs and never leaves the machine it is
entered on (that is the point of it). It is also **encrypted at rest**:
AES-256-GCM with a key file (`secret.key`) that lives in the app data
folder NEXT TO the database — a copied `pos.db` file alone reveals no
payment secrets. On each till that takes card/M-Pesa payments:
Settings → Payments → paste the secret key once (it is masked and stored
encrypted locally), or set `PAYSTACK_SECRET_KEY` as an environment
variable. The public key, currency (KES) and callback URL
(`https://awesomeposs.netlify.app/`) are part of the synced config.

## Manual key entry was removed (security)

Older builds let an admin paste a Supabase `service_role` key into
Settings → Team on any till. That key bypasses all row-level security —
pasting it on a till everyone touches gives the whole team god access to
the cloud database. Manual key entry is gone from the UI and the API now
ignores those fields entirely: tills link via the cloud bootstrap row.
Tills configured by hand before this change keep working; switch them to
cloud identity with Settings → Team → Switch to LedgerPOS Cloud.

## What syncs (WiFi, every ~20s, offline-safe)

Products (incl. photos, gift-card flag), categories, prices, stock
movements + stocktake applications, customers, ledger (tabs, store credit,
loyalty), orders + voids with reasons, void-reason catalog, store settings,
device roster. Binary app updates still arrive via Settings → Updates.

## Security notes

- The app ships only the Supabase project URL + anon key (public by design).
- Sync access is device-scoped: a till can only read events from approved
  devices and only push under its own registered identity (secret hash,
  SHA-256; the secret itself never leaves the till).
- RLS: anon can read exactly one row — the bootstrap config. Sync tables
  deny anon entirely.
- Paystack/M-Pesa secrets: local per machine, masked in the API, excluded
  from sync and backups, and **encrypted at rest** (AES-256-GCM, key file
  outside the database).
- No manual service-key entry anywhere in the product (see above).

## Updates keep every byte of shop data

Updating (Settings → Updates, or re-running a newer installer) swaps only
the app file in `Programs\LedgerPOS`. The shop database lives in
`%APPDATA%\LedgerPOS\pos.db` (macOS: `~/Library/Application
Support/LedgerPOS`, Linux: `~/.local/share/LedgerPOS`) and is never
touched by an install or update — no re-uploading store info, no data
re-entry. Schema upgrades apply automatically on the next start.
