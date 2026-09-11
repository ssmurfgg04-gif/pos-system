#!/usr/bin/env python3
"""Sync GitHub Release assets with the current build/netlify-site/downloads/.

- uploads the four installer packages (deleting stale same-name assets)
- removes legacy assets that are no longer shipped (e.g. the old .tar.gz)
- patches the release notes to match the current distribution story

The GitHub release is the *mirror*; the Netlify site serves the same
files directly. Token comes from the GITHUB_TOKEN env var.
"""
import json
import os
import sys
import urllib.error
import urllib.request

REPO = "ssmurfgg04-gif/pos-system"
TAG = sys.argv[1] if len(sys.argv) > 1 else "v1.0.0"
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DL = os.path.join(ROOT, "build", "netlify-site", "downloads")
TOKEN = os.environ.get("GITHUB_TOKEN", "")
if not TOKEN:
    sys.exit("GITHUB_TOKEN env var not set")

ASSETS = [
    ("ledgerpos-setup-windows-x64.zip", "application/zip"),
    ("ledgerpos-macos-apple-silicon.zip", "application/zip"),
    ("ledgerpos-macos-intel.zip", "application/zip"),
    ("ledgerpos-linux-x64.tar.xz", "application/x-xz"),
]
LEGACY = {"ledgerpos-linux-x64.tar.gz"}

NOTES = """LedgerPOS {v} — the point of sale that installs itself.

Download the file for your machine, double-click it, start selling. The whole shop — stock, till, M-Pesa, KRA reports — runs on that machine. No servers to configure, nothing to type into a terminal, works when the internet doesn't.

| File | For |
|---|---|
| `ledgerpos-setup-windows-x64.zip` | Windows 10/11 (64-bit) — self-installing exe |
| `ledgerpos-macos-apple-silicon.zip` | macOS 12+ on Apple Silicon (M1–M4) |
| `ledgerpos-macos-intel.zip` | macOS 12+ on Intel Macs |
| `ledgerpos-linux-x64.tar.xz` | Linux x86-64, any distro |

**First login:** `admin` / `admin123` (PIN `1234`) — change it in Settings on first run. Your data stays on your machine.

- **Windows SmartScreen** may warn (no code-signing certificate yet): *More info → Run anyway*.
- **macOS Gatekeeper**: right-click the app → *Open → Open* (once; Apple remembers).
- **Integrity:** SHA-256 of every package is published in the release's `checksums.txt` — or verify against `downloads/checksums.txt` in the repo.

What's inside: offline-first till (IndexedDB queue + SQLite WAL), M-Pesa STK push with manual receipt-code fallback (mock provider for demos; Daraja sandbox/production ready), KRA monthly VAT reports + CSV export, shifts & cash reconciliation, ESC/POS receipt printing, USB barcode scanning, white-label everything (store name, brand colour, currency, VAT % in Settings), automatic daily backups, full audit log.

The same binary also runs as a LAN appliance (`ledgerpos serve`, mDNS discovery) for multi-terminal shops. The project README has the download landing page + in-browser demo details.
""".replace("{v}", TAG.lstrip("v"))


def api(method, url, data=None, ctype="application/json", raw=False):
    req = urllib.request.Request(url, method=method)
    req.add_header("Authorization", f"token {TOKEN}")
    if data is not None:
        req.add_header("Content-Type", ctype)
        req.data = data if isinstance(data, bytes) else json.dumps(data).encode()
    try:
        with urllib.request.urlopen(req, timeout=600) as r:
            return r.status, r.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def main():
    # create or fetch the release
    status, body = api("POST", f"https://api.github.com/repos/{REPO}/releases",
                       {"tag_name": TAG, "target_commitish": "main",
                        "name": f"LedgerPOS {TAG.lstrip('v')} — desktop app",
                        "body": NOTES})
    if status == 422:
        status, body = api("GET", f"https://api.github.com/repos/{REPO}/releases/tags/{TAG}")
    if status not in (200, 201):
        sys.exit(f"release create failed: {status}\n{body[:400]}")
    rel = json.loads(body)
    rid = rel["id"]
    print(f"release id={rid}  {rel['html_url']}")

    # refresh the notes (idempotent PATCH)
    s, _ = api("PATCH", f"https://api.github.com/repos/{REPO}/releases/{rid}",
               {"tag_name": TAG, "name": f"LedgerPOS {TAG.lstrip('v')} — desktop app",
                "body": NOTES})
    print(f"notes patched ({s})")

    existing = {a["name"]: a for a in rel.get("assets", [])}

    # drop legacy assets no longer shipped
    for name in LEGACY:
        if name in existing:
            s, _ = api("DELETE", existing[name]["url"])
            print(f"  removed legacy {name} ({s})")
            del existing[name]

    # upload checksums.txt too
    jobs = [(n, t) for n, t in ASSETS] + [("checksums.txt", "text/plain")]
    for name, ctype in jobs:
        path = os.path.join(DL, name)
        if not os.path.isfile(path):
            sys.exit(f"missing asset: {path}")
        if name in existing:
            s, _ = api("DELETE", existing[name]["url"])
            print(f"  removed stale {name} ({s})")
        with open(path, "rb") as f:
            payload = f.read()
        s, b = api("POST",
                   f"https://uploads.github.com/repos/{REPO}/releases/{rid}/assets?name={name}",
                   payload, ctype=ctype)
        if s not in (200, 201):
            sys.exit(f"upload failed for {name}: {s}\n{b[:400]}")
        info = json.loads(b)
        print(f"  uploaded {name}  {len(payload)/1e6:.1f} MB -> {info['browser_download_url']}")

    print("\nRelease synced with the Netlify site packages.")


if __name__ == "__main__":
    main()
