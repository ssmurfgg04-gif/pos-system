#!/usr/bin/env python3
"""Netlify build — assembles the deployable LedgerPOS download site.

This is the Netlify build command (netlify.toml):

    [build]
      base    = "frontend"
      command = "python3 ../scripts/netlify_build.py"
      publish = "dist"

and can be run locally for verification (from anywhere):

    python3 scripts/netlify_build.py [--skip-npm]

Everything Netlify serves comes out of THIS repository — the four
installer packages are committed under downloads/ (mirrored from the
GitHub Release), the icons under scripts/assets/icons/. No network
fetches at build time, so a deploy can never fail on a registry hiccup.

Output layout (frontend/dist — the publish directory):

    index.html        the download landing page (Big-7, OS-detected CTA)
    icon-*.png        favicon / og image
    downloads/        4 installers + checksums.txt
    demo/             in-browser demo build of the full POS (VITE_BASE=/demo/)
    _redirects        SPA fallback for /demo/*
"""
import hashlib
import os
import re
import shutil
import subprocess
import sys
from datetime import date

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FRONTEND = os.path.join(ROOT, "frontend")
DIST = os.path.join(FRONTEND, "dist")
DL_SRC = os.path.join(ROOT, "downloads")
ICONS_SRC = os.path.join(ROOT, "scripts", "assets", "icons")
TEMPLATE = os.path.join(ROOT, "scripts", "assets", "download-page.html")

RELEASES_PAGE = "https://github.com/ssmurfgg04-gif/pos-system/releases"
VERSION = "1.0.0"

INSTALLERS = [
    "ledgerpos-setup-windows-x64.zip",
    "ledgerpos-macos-apple-silicon.zip",
    "ledgerpos-macos-intel.zip",
    "ledgerpos-linux-x64.tar.xz",
]
ICONS = ("icon-64.png", "icon-128.png", "icon-256.png", "icon-512.png")


def die(msg: str) -> None:
    print(f"BUILD FAILED: {msg}", file=sys.stderr)
    sys.exit(1)


def sha256_of(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def human_mb(path: str) -> str:
    return f"{os.path.getsize(path) / 1e6:.1f}"


def verify_installers() -> dict:
    """Fail the build if a committed installer is missing or corrupted."""
    sizes = {}
    ck_path = os.path.join(DL_SRC, "checksums.txt")
    expected: dict[str, str] = {}
    if os.path.isfile(ck_path):
        for line in open(ck_path):
            m = re.match(r"^([0-9a-f]{64})\s+(\S+)$", line.strip())
            if m:
                expected[m.group(2)] = m.group(1)
    for name in INSTALLERS:
        p = os.path.join(DL_SRC, name)
        if not os.path.isfile(p):
            die(f"downloads/{name} is missing from the repo")
        if name in expected and sha256_of(p) != expected[name]:
            die(f"downloads/{name} does not match checksums.txt — do NOT deploy")
        sizes[name] = human_mb(p)
    total = sum(os.path.getsize(os.path.join(DL_SRC, n)) for n in INSTALLERS)
    print(f"installers verified — {len(INSTALLERS)} packages, {total/1e6:.1f} MB total")
    return sizes


def build_demo(skip_npm: bool) -> None:
    """Vite demo build with VITE_DEMO_MODE + VITE_BASE=/demo/."""
    if skip_npm and os.path.isfile(os.path.join(DIST, "index.html")):
        print("skip-npm: reusing existing dist/ demo build")
    else:
        env = dict(os.environ)
        env["VITE_DEMO_MODE"] = "true"
        env["VITE_BASE"] = "/demo/"
        subprocess.run(["npm", "ci"], cwd=FRONTEND, check=True, env=env)
        subprocess.run(["npm", "run", "build"], cwd=FRONTEND, check=True, env=env)
    # sanity: assets must reference /demo/ (base applied)
    html = open(os.path.join(DIST, "index.html")).read()
    if "/demo/" not in html:
        die("demo index.html does not reference /demo/ — VITE_BASE was not applied")
    if "src=\"/assets/" in html or "href=\"/assets/" in html:
        die("demo index.html uses root-relative assets — refusing to serve broken demo")


def restructure() -> None:
    """dist/ (demo SPA at root) -> dist/demo/ (demo SPA one level down)."""
    tmp = os.path.join(FRONTEND, "dist-demo-tmp")
    if os.path.isdir(tmp):
        shutil.rmtree(tmp)
    os.rename(DIST, tmp)
    os.makedirs(DIST)
    demo = os.path.join(DIST, "demo")
    os.makedirs(demo)
    for entry in os.listdir(tmp):
        shutil.move(os.path.join(tmp, entry), demo)
    # the site root's redirect rules are ours now, not the SPA's
    red = os.path.join(demo, "_redirects")
    if os.path.isfile(red):
        os.remove(red)
    shutil.rmtree(tmp, ignore_errors=True)
    print("demo build -> dist/demo/")


def render_landing(sizes: dict) -> None:
    tpl = open(TEMPLATE).read()
    html = (tpl
            .replace("{{VERSION}}", VERSION)
            .replace("{{BUILD_DATE}}", date.today().strftime("%b %Y"))
            .replace("{{RELEASES_PAGE}}", RELEASES_PAGE)
            .replace("{{WIN_SIZE_MB}}", sizes["ledgerpos-setup-windows-x64.zip"])
            .replace("{{MAC_ARM_SIZE_MB}}", sizes["ledgerpos-macos-apple-silicon.zip"])
            .replace("{{MAC_INTEL_SIZE_MB}}", sizes["ledgerpos-macos-intel.zip"])
            .replace("{{LINUX_SIZE_MB}}", sizes["ledgerpos-linux-x64.tar.xz"]))
    if "{{" in html:
        die("unsubstituted template variable left in landing page")
    with open(os.path.join(DIST, "index.html"), "w") as f:
        f.write(html)
    print("index.html rendered")


def copy_assets() -> None:
    dl = os.path.join(DIST, "downloads")
    os.makedirs(dl, exist_ok=True)
    for name in INSTALLERS + ["checksums.txt"]:
        shutil.copy2(os.path.join(DL_SRC, name), os.path.join(dl, name))
    for icon in ICONS:
        src = os.path.join(ICONS_SRC, icon)
        if not os.path.isfile(src):
            die(f"scripts/assets/icons/{icon} missing")
        shutil.copy2(src, os.path.join(DIST, icon))
    with open(os.path.join(DIST, "_redirects"), "w") as f:
        f.write("# SPA fallback for the in-browser demo app only\n"
                "/demo/*  /demo/index.html  200\n")
    print(f"downloads/ ({len(INSTALLERS) + 1} files), icons, _redirects -> dist/")


def link_check() -> None:
    html = open(os.path.join(DIST, "index.html")).read()
    hrefs = re.findall(r'(?:href|src|content)="((?:downloads|demo|icon)[^"]*)"', html)
    for h in hrefs:
        target = os.path.join(DIST, h.rstrip("/").replace("/", os.sep))
        if not (os.path.isfile(target) or os.path.isdir(target)):
            die(f"landing page links to missing file: {h}")
    for h in hrefs:
        if h.endswith("/"):
            if not os.path.isfile(os.path.join(DIST, h, "index.html")):
                die(f"directory link without index.html: {h}")
    # demo assets referenced by its own index.html must exist under dist/demo
    demo_html = open(os.path.join(DIST, "demo", "index.html")).read()
    for m in re.findall(r'(?:src|href)="(/demo/[^"]+)"', demo_html):
        target = os.path.join(DIST, m.lstrip("/").replace("/", os.sep))
        if not os.path.isfile(target):
            die(f"demo references missing asset: {m}")
    print(f"link check: {len(hrefs)} landing refs + demo assets resolve")


def main() -> None:
    skip_npm = "--skip-npm" in sys.argv
    sizes = verify_installers()
    build_demo(skip_npm)
    restructure()
    render_landing(sizes)
    copy_assets()
    link_check()
    n_files = sum(len(fs) for _, _, fs in os.walk(DIST))
    total = sum(os.path.getsize(os.path.join(dp, f)) for dp, _, fs in os.walk(DIST) for f in fs)
    print(f"\nDONE — {n_files} files, {total/1e6:.1f} MB in frontend/dist "
          "(landing page at /, demo at /demo/, installers at /downloads/)")


if __name__ == "__main__":
    main()
