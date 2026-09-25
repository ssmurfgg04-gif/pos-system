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

The Paystack **secret** key never syncs and never leaves the machine it is
entered on (that is the point of it). On each till that takes card/M-Pesa
payments: Settings → Payments → paste the secret key once (it is masked and
stored locally), or set `PAYSTACK_SECRET_KEY` as an environment variable.
The public key, currency (KES) and callback URL
(`https://awesomeposs.netlify.app/`) are part of the synced config.

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
  from sync and backups.
