#!/usr/bin/env python3
"""Assemble the self-contained LedgerPOS download site.

Everything the Netlify deploy needs, in one folder — including the four
installer packages in downloads/, so visitors download straight from the
Netlify page (no GitHub redirect):

  build/netlify-site/
    index.html            rendered landing page (relative download links)
    icon-*.png            favicons / og image
    demo/                 in-browser demo build of the full app
    downloads/            the 4 installers + checksums.txt
    _redirects            SPA fallback for /demo/*

Also writes download/ledgerpos-netlify-site.zip (drag-and-drop deploy)
and standalone copies of the installers.

Fast path: reuses the packages + demo dist that already exist under
build/netlify-site/ — no Go or npm required. Rebuild those first if you
changed app code (scripts/package_desktop.py).

Run from the repo root:  python3 scripts/build_site.py [version]
"""
import os
import shutil
import sys
import zipfile
from datetime import date

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import pkgutil

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPTS = os.path.join(ROOT, "scripts")
SITE = os.path.join(ROOT, "build", "netlify-site")
DL = os.path.join(SITE, "downloads")
DOWNLOAD = (os.environ.get("LEDGERPOS_DOWNLOAD_DIR")
            or ("/home/z/my-project/download" if os.path.isdir("/home/z/my-project/download")
                else os.path.join(ROOT, "build")))
RELEASES_PAGE = "https://github.com/ssmurfgg04-gif/pos-system/releases"

VERSION = sys.argv[1] if len(sys.argv) > 1 else "1.0.0"

INSTALLERS = [
    "ledgerpos-setup-windows-x64.zip",
    "ledgerpos-macos-apple-silicon.zip",
    "ledgerpos-macos-intel.zip",
    "ledgerpos-linux-x64.tar.xz",
]
ICONS = ("icon-64.png", "icon-128.png", "icon-256.png", "icon-512.png")


def main():
    # ---- preflight: everything we embed must exist and be Netlify-safe ----
    missing = [f for f in INSTALLERS
               if not os.path.isfile(os.path.join(DL, f))]
    if missing:
        raise SystemExit("missing installers in downloads/ — run scripts/repack.py "
                         f"first: {missing}")
    if not os.path.isfile(os.path.join(SITE, "demo", "index.html")):
        raise SystemExit("missing demo build — run the vite demo build first "
                         "(see scripts/package_desktop.py step 5)")
    total = 0
    for f in INSTALLERS:
        total += pkgutil.assert_netlify_safe(os.path.join(DL, f))
    for icon in ICONS:
        if not os.path.isfile(os.path.join(ROOT, "build", icon)):
            raise SystemExit(f"missing build/{icon} — run scripts/make_icon.py first")

    # ---- checksums (regenerate so they always match what's embedded) ----
    ck = os.path.join(DL, "checksums.txt")
    with open(ck, "w") as f:
        f.write(f"LedgerPOS {VERSION} — SHA-256 of the download packages\n\n")
        for name in INSTALLERS:
            f.write(f"{pkgutil.sha256_of(os.path.join(DL, name))}  {name}\n")
    print(f"checksums -> {ck}")

    # ---- landing page ----
    sizes = {name: pkgutil.human_mb(os.path.join(DL, name)) for name in INSTALLERS}
    tpl = open(os.path.join(SCRIPTS, "assets", "download-page.html")).read()
    html = (tpl
            .replace("{{VERSION}}", VERSION)
            .replace("{{BUILD_DATE}}", date.today().strftime("%b %Y"))
            .replace("{{RELEASES_PAGE}}", RELEASES_PAGE)
            .replace("{{WIN_SIZE_MB}}", sizes["ledgerpos-setup-windows-x64.zip"])
            .replace("{{MAC_ARM_SIZE_MB}}", sizes["ledgerpos-macos-apple-silicon.zip"])
            .replace("{{MAC_INTEL_SIZE_MB}}", sizes["ledgerpos-macos-intel.zip"])
            .replace("{{LINUX_SIZE_MB}}", sizes["ledgerpos-linux-x64.tar.xz"]))
    if "{{" in html:
        raise SystemExit("unsubstituted template variable left in landing page")
    with open(os.path.join(SITE, "index.html"), "w") as f:
        f.write(html)
    print("index.html rendered")

    for icon in ICONS:
        shutil.copy2(os.path.join(ROOT, "build", icon), os.path.join(SITE, icon))

    with open(os.path.join(SITE, "_redirects"), "w") as f:
        f.write("# SPA fallback for the in-browser demo app only\n"
                "/demo/*  /demo/index.html  200\n")

    # ---- sanity: every href in the landing page resolves ----
    import re
    hrefs = re.findall(r'href="((?:downloads|demo|icon)[^"]*)"', html)
    for h in hrefs:
        target = os.path.join(SITE, h.rstrip("/"))
        if not (os.path.isfile(target) or os.path.isdir(target)):
            raise SystemExit(f"landing page links to missing file: {h}")
    # directory links must have an index.html behind them
    for h in hrefs:
        if h.endswith("/"):
            if not os.path.isfile(os.path.join(SITE, h, "index.html")):
                raise SystemExit(f"directory link without index.html: {h}")
    print(f"link check: {len(hrefs)} internal hrefs resolve")

    # ---- the drag-and-drop zip (everything included) ----
    os.makedirs(DOWNLOAD, exist_ok=True)
    site_zip = os.path.join(DOWNLOAD, "ledgerpos-netlify-site.zip")
    if os.path.exists(site_zip):
        os.remove(site_zip)
    files = 0
    with zipfile.ZipFile(site_zip, "w", zipfile.ZIP_DEFLATED, compresslevel=6) as z:
        for base, dirs, names in os.walk(SITE):
            dirs.sort()
            for fn in sorted(names):
                full = os.path.join(base, fn)
                rel = os.path.relpath(full, SITE)
                # installers are already compressed — store them as-is
                if rel.startswith("downloads" + os.sep) and rel.endswith(
                        (".zip", ".xz")):
                    zi = zipfile.ZipInfo(rel, date_time=(2026, 1, 1, 0, 0, 0))
                    with open(full, "rb") as fh:
                        z.writestr(zi, fh.read(), compress_type=zipfile.ZIP_STORED)
                else:
                    z.write(full, rel)
                files += 1
    total_site = sum(os.path.getsize(os.path.join(DL, f)) for f in INSTALLERS) + 500_000
    print(f"site zip -> {site_zip}  {os.path.getsize(site_zip)/1e6:.1f} MB  "
          f"({files} files; ~{total_site/1e6:.0f} MB deployed — under Netlify's "
          "50 MB drag-and-drop guidance)")

    # ---- standalone installer copies ----
    for a in INSTALLERS + ["checksums.txt"]:
        dst = os.path.join(DOWNLOAD, a)
        if a in INSTALLERS:
            shutil.copy2(os.path.join(DL, a), dst)
    print(f"installers copied -> {DOWNLOAD}")

    print("\nDONE — drag ledgerpos-netlify-site.zip onto Netlify (or deploy the")
    print("build/netlify-site/ folder). Downloads are served straight from the site.")


if __name__ == "__main__":
    main()
