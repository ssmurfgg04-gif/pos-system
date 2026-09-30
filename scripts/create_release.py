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
    ("ledgerpos-setup-windows-x64.exe", "application/x-msdownload"),
    ("ledgerpos-macos-apple-silicon.zip", "application/zip"),
    ("ledgerpos-macos-intel.zip", "application/zip"),
    ("ledgerpos-linux-x64.tar.xz", "application/x-xz"),
]
LEGACY = {"ledgerpos-linux-x64.tar.gz"}

NOTES = """LedgerPOS {v} — the point of sale that installs itself.

**New in 1.1.10 — money that landed is never lost, and success is something you can SEE.** The field report that drove this release: a KES 10 sale whose M-Pesa confirmation arrived **21 minutes** after the STK push — the till had given up watching at 3 minutes ("no money moved" — which wasn't true), the background sweep gave up at 15, and the order sat Pending for good while the money was in the bank. Three layers close that hole for good: **(1)** the payment screen now watches for a full 15 minutes and auto-confirms the sale the moment the money shows up — after 3 quiet minutes it calmly says "still waiting — if the customer already paid, this screen confirms by itself" instead of a red failure; **(2)** after 15 minutes, one tap of **Check payment status** (on the payment screen, and on every Pending order in Orders) re-verifies every charge ever sent for that order with Paystack and completes the sale if the money arrived — it never double-counts: if the cashier already keyed the M-Pesa code, the late verify is ignored; **(3)** a once-a-minute background sweep quietly re-verifies recently-failed charges on pending orders, so late money completes its sale even when nobody touches anything. Second headline: the **sale-complete moment** — a big green "Sale complete" with the amount, order number and M-Pesa receipt, a two-note chime sellers can hear across the counter, and **Start a new sale** as the primary button, so nobody ever stares at a payment screen wondering if it worked. Also: the waiting screen now explains that the customer's M-Pesa prompt shows **PAYSTACK PAYMENTS KENYA LIMITED** for account *your shop* (that's Paystack, the card & M-Pesa processor — the money lands in your account), and the PIN screen tells locked-out staff exactly how to get back in (Password sign-in, or the owner's team join link). New recovery regression suite replays the 21-minute late payment against a fake Paystack: exactly-once completion, no double-counting with manual entry, superseded-push recovery. 95/95 frontend, full Go suite green.

Download the file for your machine, double-click it, start selling. The whole shop — stock, till, M-Pesa, KRA reports — runs on that machine. No servers to configure, nothing to type into a terminal, works when the internet doesn\'t.

| File | For |
|---|---|
| `ledgerpos-setup-windows-x64.exe` | Windows 10/11 (64-bit) — one-click installer |
| `ledgerpos-macos-apple-silicon.zip` | macOS 12+ on Apple Silicon (M1–M4) |
| `ledgerpos-macos-intel.zip` | macOS 12+ on Intel Macs |
| `ledgerpos-linux-x64.tar.xz` | Linux x86-64, any distro |

**First login:** `admin` / `0000` in the web demo; on a real box you create your own admin during first-run. Your data stays on your machine.

- **Windows SmartScreen** may warn (no code-signing certificate yet): *More info → Run anyway*.
- **macOS Gatekeeper**: right-click the app → *Open → Open* (once; Apple remembers).
- **Integrity:** SHA-256 of every package is published in the release's `checksums.txt` — or verify against `downloads/checksums.txt` in the repo.
- **Updates:** the app checks the cloud update manifest (SHA-256 verified) and can install or undo updates from Settings → System.""".replace("{v}", TAG.lstrip("v"))


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
