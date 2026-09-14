# Point of Sale

A white-label, multi-terminal point-of-sale system for retail — built for
Kenyan shops (M-Pesa first) but not hardcoded to any of them. One Go binary
serves the API **and** the React frontend; SQLite runs in WAL mode so a
power cut never corrupts a till.

```
┌────────────────────────────────────────────────────────────┐
│  pos-app  (single ~38MB binary)                            │
│  ├─ Go API  : Gin, JWT, RBAC, M-Pesa, ESC/POS, mDNS, WS    │
│  ├─ SQLite  : WAL + busy_timeout (or PostgreSQL)           │
│  └─ React   : embedded SPA (Ledger design system)          │
└────────────────────────────────────────────────────────────┘
```

## Feature highlights

- **Dynamic RBAC** — an admin creates any role and edits its permissions
  from a catalog (`pos.sell`, `payments.manual`, `products.manage`, …).
  Seeded system roles: **Admin** (everything), **Cashier** (sell, void,
  manual M-Pesa entry, shifts), **Designer** (design/production board).
  Enforcement is server-side only; the UI just hides what you can't do.
- **M-Pesa, three ways** — provider is swappable behind one interface:
  `mock` (demos/training, default), Daraja `sandbox`, Daraja `production`.
  Payment modes: `auto` (STK push, manual fallback), `stk`, `manual`.
  LAN boxes can't receive webhooks, so a **sweeper polls stkpushquery**
  every 5s and completes orders itself; the public callback endpoint works
  too, and both are idempotent.
- **Manual receipt entry (first-class)** — customer pays to the
  till/paybill themselves; the cashier types the 10-character M-Pesa
  receipt code. Format-validated (`^[A-Z0-9]{10}$`), unique-indexed,
  amount-checked. Paying the wrong amount flags a **discrepancy** for
  admin review instead of silently trusting it.
- **Crash-safety everywhere** — integer cents for money, guarded stock
  deduction (`WHERE status != 'PAID'` — no double-deduct even if two
  completions race), persistent `print_jobs` (power loss = reprint on
  boot), idempotent offline sync keyed by `client_uuid`.
- **Offline-first terminals** — the SPA queues cash sales in IndexedDB
  when the network drops, shows a banner, and syncs on reconnect. The
  server replays idempotently, so double-submits are harmless.
- **ESC/POS printing** — 80mm/58mm receipts over TCP (`tcp://host:9100`)
  or USB (`file:///dev/usb/lp0`), retry queue with backoff, browser-print
  fallback at `/api/v1/orders/{id}/receipt`.
- **Shifts & reconciliation** — open with a float, close counting the
  drawer; expected cash is computed from completed cash payments, variance
  highlighted. Full audit log of who did what.
- **KRA monthly returns** — Reports → Monthly gives the accountant one
  calendar month of VAT figures (gross, taxable value, VAT collected,
  transaction count, cash/M-Pesa split, per-day chart) and downloads it
  as a CSV to attach to the iTax return.
- **Automatic backups** — a daily 02:00 `VACUUM INTO` snapshot into
  `backups/` (consistent even mid-sale) with configurable retention; a
  manual **Back up now** button lives in Settings → System. Snapshots
  are plain SQLite files — copy them to USB/cloud for off-site safety.
- **Barcode scanners (no focus needed)** — hardware USB/Bluetooth
  scanners fire straight into the cart via a global HID listener that
  only accepts scanner-speed keystroke bursts; focused typing into the
  search box works too.
- **Every screen works on phones** — the POS stacks under 1024px with a
  full cart bottom-sheet (add/remove/qty), and the charge modal's
  quick-tender buttons are 48px touch targets.
- **White-label by construction** — every visible string (app name, store
  name/address/phone, receipt footer, currency, VAT %, brand color, till/
  paybill numbers) comes from the settings table, plus an uploadable
  **brand logo** (Settings → Store) shown on login and the topbar.
  No company name is hardcoded anywhere. Secrets are masked (`__SET__`)
  in the API.
- **Fast shift handoff** — 4-digit PIN quick-switch (bcrypt-hashed,
  escalating lockout, rate-limited). JWT 12h, per-request permission
  reload so role edits apply immediately.
- **mDNS discovery** — broadcasts `_pos-server._tcp.local` so terminals
  find the server on the LAN (best-effort; never fatal).

## The desktop app: download, double-click, sell

The recommended way to run this in a real shop is the **standalone
desktop build** — the same single binary, shipped as an app for Windows,
macOS and Linux. No terminal, no localhost URL to remember, no server
to configure:

1. Download the package for your machine from the
   [releases page](https://github.com/ssmurfgg04-gif/pos-system/releases)
   — or from the LedgerPOS download page (see the Netlify section below,
   which serves the installers directly).
2. Double-click it. On Windows run the installer once (desktop +
   Start-menu shortcuts, Add/Remove Programs entry, no extraction
   needed); on macOS unzip and open `LedgerPOS.app`; on Linux
   `tar xf ledgerpos-linux-x64.tar.xz` then `./ledgerpos`.
3. The app opens in its own window — no browser tabs. First login
   `admin / admin123` (PIN `1234`) — change it in Settings on first run.

First launch sets up everything by itself: it creates the SQLite
database, runs migrations, seeds the catalog and users, picks a free
local port (8765–7914), binds to 127.0.0.1 **only**, and opens its own
app window (a Chromium app-mode window; falls back to your default
browser if none is installed). Launching it again while it's running
just opens a new window.
An admin can stop it from the UI (sidebar → Quit). All data lives in a
per-OS app directory — `%APPDATA%\LedgerPOS`,
`~/Library/Application Support/LedgerPOS`, `~/.local/share/LedgerPOS` —
so it survives reinstalls and never leaves the machine.

Build the installers yourself (cross-compiles all four targets,
CGO-free, from any OS):

```bash
python3 scripts/package_desktop.py 1.1.0   # full pipeline (needs Go + npm + NSIS for the Windows setup.exe)
python3 scripts/build_installer.py 1.1.0   # Windows setup.exe only
python3 scripts/repack.py                  # re-shrink existing installers
python3 scripts/build_site.py 1.1.0        # re-assemble the site only
```
(On Windows use `python` instead of `python3`, and `winget install NSIS.NSIS` for the installer step.)

Packages are zopfli-deflated and kept **under 10 MB each** so the whole
site (landing page + demo + installers, ~37 MB) deploys via Netlify
drag-and-drop; `downloads/checksums.txt` carries the SHA-256 of every
package.

The old behaviour is still there: `./ledgerpos serve` runs the
LAN-appliance mode (env-driven, mDNS discovery, 0.0.0.0 bind) for
multi-terminal setups, and `./ledgerpos --uninstall` removes the
Windows desktop install.

## The download site on Netlify (landing page + demo + installers)

The site is **wired to GitHub for continuous deployment**: every push to
`main` auto-deploys on Netlify via `netlify.toml` →
`scripts/netlify_build.py`, which assembles:

| Path          | What the visitor gets                                            |
|---------------|------------------------------------------------------------------|
| `/`           | the landing page — Big-7 value props, OS-detected primary CTA     |
| `/downloads/` | the four installer packages + `checksums.txt`, served from the deploy |
| `/demo/`      | the full POS as an in-browser demo (seeded data, mock M-Pesa)     |

The installers are **committed under `downloads/`** (mirrored from the
GitHub Release, SHA-256-verified at build time), so deploys never depend
on network fetches — the build is deterministic and offline-safe. After
`scripts/package_desktop.py` produces new packages, sync them with:

```bash
# copy fresh build/netlify-site/downloads/* into downloads/, then commit+push
python3 scripts/create_release.py   # keeps the GitHub Release in sync too
```

To verify a deploy locally before pushing:

```bash
python3 scripts/netlify_build.py            # full build, exactly as Netlify runs it
python3 scripts/netlify_build.py --skip-npm # re-assemble using the existing dist
```

The SPA ships with an **in-browser demo backend**: when no server answers
`/api/v1/health`, the app runs on a seeded, fully interactive dataset
(localStorage) — same login accounts, same M-Pesa STK simulation, same
RBAC. That means the static Netlify build is a working demo you can show
anyone.

If download traffic grows heavy, host the installers on GitHub Releases
instead (they're mirrored there) and change the landing-page links — the
page footer links to the releases either way.

To point the deployed app at a **real** server later: remove
`VITE_DEMO_MODE` and set `VITE_API_URL=https://your-server.example` — the
app then talks to that backend (and shows connection errors instead of
falling back to demo). Demo data resets anytime via **Settings → System →
Reset demo data**.

## Quick start

```bash
# build the single binary (frontend must be built first)
cd frontend && npm install && npm run build && cd ..
go build -o pos-app .

# run it (SQLite, demo seed, M-Pesa in mock mode)
SEED_DEMO=true ./pos-app              # → http://localhost:3000
```

Demo accounts (seeded once, safe to delete):

| Username | Password   | PIN  | Role    |
|----------|------------|------|---------|
| admin    | admin123   | 1234 | Admin   |
| cashier  | cashier123 | 2222 | Cashier |
| designer | designer123| 3333 | Designer |

M-Pesa starts in **mock** mode: STK pushes auto-succeed after ~4s so you
can demo/training the whole flow. Switch the provider in
**Settings → Payments** when you're ready for Daraja sandbox/production.

### Environment variables

| Var           | Default    | Purpose                              |
|---------------|------------|--------------------------------------|
| `PORT`        | `3000`     | HTTP port                            |
| `DB_DRIVER`   | `sqlite`   | `sqlite` or `postgres`               |
| `DB_PATH`     | `pos.db`   | SQLite file                          |
| `POSTGRES_DSN`| —          | e.g. `postgres://user:pw@host/db`    |
| `SEED_DEMO`   | `true`     | Seed demo catalog + users            |
| `MDNS_ENABLED`| `true`     | LAN discovery broadcast              |

### Go real with M-Pesa (Daraja)

1. Settings → Payments → environment: `sandbox` (test credentials) or
   `production`.
2. Fill shortcode, passkey, consumer key/secret. The callback URL is
   optional — polling covers LAN deployments.
3. Leave the mode on `auto`: customers get an STK push; if they pay at
   the till instead, the cashier enters the receipt code. Both complete
   the same order.

### Production notes

- Put the binary on a disk that survives reboots; the SQLite file *is*
  the till. WAL mode + `synchronous=NORMAL` is the right trade for a POS.
- Run it behind Caddy/nginx for TLS if terminals connect over Wi-Fi you
  don't fully trust. The API allows all origins by design (LAN appliance)
  — scope that down if you expose it publicly.
- `GET /api/v1/health` for uptime checks; the audit log is under
  Settings → Audit log.

## Development

```bash
# backend
go vet ./... && go test ./...

# frontend (Vite dev server proxies /api → :3000)
cd frontend && npm run dev

# tests
cd frontend && npm test

# end-to-end smoke (server must be running on :3000)
python3 scripts/smoke_test.py
```

Project layout:

```
internal/
  config/     env config
  database/   dual-driver open, migrations, seeds
  models/     DTOs + permission catalog
  settings/   key/value store with in-memory cache
  auth/       JWT, bcrypt, PIN lockout, rate limiter, middleware
  hash/       bcrypt (dependency-free to avoid import cycles)
  mpesa/      Provider interface, Daraja, Manual validation, Mock
  printer/    ESC/POS renderer + persistent print worker
  mdns/       LAN discovery
  ws/         websocket hub (token auth on first message)
  services/   checkout, payments, void, sync, sweeper, shifts, design, reports
  handlers/   HTTP layer
  router/     routes + permission gates + embedded SPA
frontend/     React 18 + Vite + TS + Tailwind v4 (Ledger design system)
scripts/      E2E smoke test
```

## License

MIT — see [LICENSE](LICENSE).
