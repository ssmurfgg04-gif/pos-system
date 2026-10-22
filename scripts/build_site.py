#!/usr/bin/env python3
"""Assemble the LedgerPOS download site (offline fallback builder).

Same output as scripts/netlify_build.py: installers live ONLY on GitHub
Releases and the landing page links straight at the release assets; the
committed downloads/manifest.json (version + sizes + sha256) feeds the
page labels and downloads/checksums.txt is copied for same-origin
integrity checks.

  build/netlify-site/
    index.html            rendered landing page (release asset links)
    icon-*.png            favicons / og image
    demo/                 in-browser demo build of the full app
    downloads/            checksums.txt + manifest.json (small text only)
    _redirects            SPA fallback for /demo/*

Also writes download/ledgerpos-netlify-site.zip (drag-and-drop deploy).

Fast path: reuses the demo dist that already exists under
build/netlify-site/ — no Go or npm required. Rebuild that first if you
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
    "ledgerpos-setup-windows-x64.exe",
    "ledgerpos-macos-apple-silicon.zip",
    "ledgerpos-macos-intel.zip",
    "ledgerpos-linux-x64.tar.xz",
]
ICONS = ("icon-64.png", "icon-128.png", "icon-256.png", "icon-512.png")


def main():
    # ---- preflight: manifest + demo build, no binaries required ----
    manifest_path = os.path.join(ROOT, "downloads", "manifest.json")
    if not os.path.isfile(manifest_path):
        raise SystemExit("downloads/manifest.json missing — installers are "
                         "release-only; run scripts/create_release.py to refresh it")
    import json as _json
    manifest = _json.load(open(manifest_path))
    names = {a["name"] for a in manifest["assets"]}
    missing = [n for n in INSTALLERS if n not in names]
    if missing:
        raise SystemExit(f"downloads/manifest.json missing assets: {missing}")
    if not os.path.isfile(os.path.join(SITE, "demo", "index.html")):
        raise SystemExit("missing demo build — run the vite demo build first "
                         "(see scripts/package_desktop.py step 5)")
    for icon in ICONS:
        if not os.path.isfile(os.path.join(ROOT, "build", icon)):
            raise SystemExit(f"missing build/{icon} — run scripts/make_icon.py first")

    # ---- checksums + manifest (copied from the committed small files) ----
    os.makedirs(DL, exist_ok=True)
    for small in ("checksums.txt", "manifest.json"):
        src = os.path.join(ROOT, "downloads", small)
        if os.path.isfile(src):
            shutil.copy2(src, os.path.join(DL, small))
    print("checksums + manifest -> downloads/")

    # ---- landing page ----
    sizes = {a["name"]: f"{a['size'] / 1e6:.1f}" for a in manifest["assets"]}
    rel_base = f"{RELEASES_PAGE}/download/v{manifest['version']}"
    tpl = open(os.path.join(SCRIPTS, "assets", "download-page.html")).read()
    html = (tpl
            .replace("{{VERSION}}", manifest["version"])
            .replace("{{BUILD_DATE}}", date.today().strftime("%b %Y"))
            .replace("{{RELEASES_PAGE}}", RELEASES_PAGE)
            .replace("{{RELEASE_BASE}}", rel_base)
            .replace("{{WIN_SIZE_MB}}", sizes["ledgerpos-setup-windows-x64.exe"])
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

    # ---- sanity: every internal href in the landing page resolves ----
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

    # ---- the drag-and-drop zip (demo + pages; installers are release-only) ----
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
                z.write(full, rel)
                files += 1
    total_site = sum(os.path.getsize(os.path.join(dp, f))
                     for dp, _, fs in os.walk(SITE) for f in fs)
    print(f"site zip -> {site_zip}  {os.path.getsize(site_zip)/1e6:.1f} MB  "
          f"({files} files; ~{total_site/1e6:.1f} MB deployed)")

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
