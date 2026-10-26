#!/usr/bin/env python3
"""Netlify build — assembles the deployable LedgerPOS download site.

This is the Netlify build command (netlify.toml):

    [build]
      base    = "frontend"
      command = "python3 ../scripts/netlify_build.py"
      publish = "dist"

and can be run locally for verification (from anywhere):

    python3 scripts/netlify_build.py [--skip-npm]

Everything Netlify serves comes out of THIS repository — the installer
packages live ONLY on GitHub Releases (the repo is not a CDN); the
landing page links straight at the release assets and the small
manifest (downloads/manifest.json: version, sizes, sha256) feeds the
page's size/version labels. Icons live under scripts/assets/icons/. No
network fetches at build time, so a deploy can never fail on a registry
hiccup.

Output layout (frontend/dist — the publish directory):

    index.html        the download landing page (Big-7, OS-detected CTA)
    icon-*.png        favicon / og image
    downloads/        checksums.txt + manifest.json (small text only)
    demo/             in-browser demo build of the full POS (VITE_BASE=/demo/)
    _redirects        SPA fallback for /demo/*
"""
import hashlib
import json
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
PORTAL_TEMPLATE = os.path.join(ROOT, "scripts", "assets", "portal-page.html")
JOIN_TEMPLATE = os.path.join(ROOT, "scripts", "assets", "join-page.html")

RELEASES_PAGE = "https://github.com/ssmurfgg04-gif/pos-system/releases"


def _resolve_version() -> str:
    """Site version = the release-train-synced downloads/manifest.json.

    This used to be a hardcoded constant, and it drifted EVERY release
    (v1.4.2 shipped a manifest the site build then refused — one more
    reason the landing page kept freezing on stale links). The manifest
    is the single source of truth; LEDGERPOS_SITE_VERSION overrides for
    rehearsals and the gate below still enforces the pair agrees.
    """
    override = os.environ.get("LEDGERPOS_SITE_VERSION", "").strip()
    if override:
        return override
    try:
        with open(os.path.join(ROOT, "downloads", "manifest.json")) as f:
            return str(json.load(f).get("version") or "")
    except Exception:
        return ""


VERSION = _resolve_version()

INSTALLERS = [
    "ledgerpos-setup-windows-x64.exe",
    "ledgerpos-macos-apple-silicon.zip",
    "ledgerpos-macos-intel.zip",
    "ledgerpos-linux-x64.tar.xz",
]
ICONS = ("icon-64.png", "icon-128.png", "icon-256.png", "icon-512.png")


def release_base() -> str:
    """GitHub Release asset base for the shipped version."""
    return f"{RELEASES_PAGE}/download/v{VERSION}"


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


def load_manifest() -> dict:
    """Fail the build if the installer manifest is missing or inconsistent.

    The binaries themselves live only on GitHub Releases; the committed
    manifest (version + per-asset size + sha256) is what the landing page
    renders. Binary blobs must NEVER come back into the repo — releases
    are the distribution channel.
    """
    mp = os.path.join(DL_SRC, "manifest.json")
    if not os.path.isfile(mp):
        die("downloads/manifest.json is missing — installers are release-only, "
            "run scripts/create_release.py <tag> to refresh it after a release")
    manifest = json.load(open(mp))
    names = {a["name"] for a in manifest["assets"]}
    missing = [n for n in INSTALLERS if n not in names]
    if missing:
        die(f"downloads/manifest.json is missing assets: {', '.join(missing)}")
    if manifest.get("version") != VERSION:
        die(f"downloads/manifest.json version {manifest.get('version')} != "
            f"site VERSION {VERSION or '<unreadable>'} — the release train "
            f"must sync the manifest (LEDGERPOS_SITE_VERSION overrides for rehearsals)")
    total = sum(a["size"] for a in manifest["assets"])
    print(f"installer manifest ok — {len(INSTALLERS)} packages, "
          f"{total/1e6:.1f} MB total, served from {release_base()}")
    return manifest


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


def render_landing(manifest: dict) -> None:
    sizes = {a["name"]: a["size"] / 1e6 for a in manifest["assets"]}
    tpl = open(TEMPLATE).read()
    html = (tpl
            .replace("{{VERSION}}", VERSION)
            .replace("{{BUILD_DATE}}", date.today().strftime("%b %Y"))
            .replace("{{RELEASES_PAGE}}", RELEASES_PAGE)
            .replace("{{RELEASE_BASE}}", release_base())
            .replace("{{WIN_SIZE_MB}}", f"{sizes['ledgerpos-setup-windows-x64.exe']:.1f}")
            .replace("{{MAC_ARM_SIZE_MB}}", f"{sizes['ledgerpos-macos-apple-silicon.zip']:.1f}")
            .replace("{{MAC_INTEL_SIZE_MB}}", f"{sizes['ledgerpos-macos-intel.zip']:.1f}")
            .replace("{{LINUX_SIZE_MB}}", f"{sizes['ledgerpos-linux-x64.tar.xz']:.1f}"))
    if "{{" in html:
        die("unsubstituted template variable left in landing page")
    with open(os.path.join(DIST, "index.html"), "w") as f:
        f.write(html)
    print("index.html rendered (installers link to GitHub Releases)")


def render_portal() -> None:
    """The owner portal — a standalone page (no build step) that calls the
    cloud's portal_login RPC and renders shop performance."""
    src = PORTAL_TEMPLATE
    if not os.path.isfile(src):
        die("scripts/assets/portal-page.html missing")
    portal_dir = os.path.join(DIST, "portal")
    os.makedirs(portal_dir, exist_ok=True)
    shutil.copy2(src, os.path.join(portal_dir, "index.html"))
    print("portal/index.html rendered")


def render_join() -> None:
    """The /join landing for team join links. The app's join links point here
    (fragment carries t/r/k); a worker opening the link in a BROWSER lands on
    real instructions instead of a 404, with a copy box for the till."""
    src = JOIN_TEMPLATE
    if not os.path.isfile(src):
        die("scripts/assets/join-page.html missing")
    join_dir = os.path.join(DIST, "join")
    os.makedirs(join_dir, exist_ok=True)
    shutil.copy2(src, os.path.join(join_dir, "index.html"))
    print("join/index.html rendered")


def copy_assets() -> None:
    dl = os.path.join(DIST, "downloads")
    os.makedirs(dl, exist_ok=True)
    for name in ("checksums.txt", "manifest.json"):
        shutil.copy2(os.path.join(DL_SRC, name), os.path.join(dl, name))
    for icon in ICONS:
        src = os.path.join(ICONS_SRC, icon)
        if not os.path.isfile(src):
            die(f"scripts/assets/icons/{icon} missing")
        shutil.copy2(src, os.path.join(DIST, icon))
    with open(os.path.join(DIST, "_redirects"), "w") as f:
        f.write("# SPA fallback for the in-browser demo app only\n"
                "/demo/*  /demo/index.html  200\n")
    print("downloads/ (checksums + manifest), icons, _redirects -> dist/")


def link_check() -> None:
    html = open(os.path.join(DIST, "index.html")).read()
    hrefs = re.findall(r'(?:href|src|content)="((?:downloads|demo|icon|join|portal)[^"]*)"', html)
    for h in hrefs:
        target = os.path.join(DIST, h.rstrip("/").replace("/", os.sep))
        if not (os.path.isfile(target) or os.path.isdir(target)):
            die(f"landing page links to missing file: {h}")
    for h in hrefs:
        if h.endswith("/"):
            if not os.path.isfile(os.path.join(DIST, h, "index.html")):
                die(f"directory link without index.html: {h}")
    # Standalone pages (join/portal) reference ../icon-64.png and ../ —
    # resolve their relative refs too, so a bad icon path fails the build.
    for page in ("join", "portal"):
        page_html = open(os.path.join(DIST, page, "index.html")).read()
        for m in re.findall(r'(?:src|href)="\.\./([^"]+)"', page_html):
            target = os.path.join(DIST, m)
            if not (os.path.isfile(target) or os.path.isdir(target)):
                die(f"{page} page references missing asset: ../{m}")
    # demo assets referenced by its own index.html must exist under dist/demo
    demo_html = open(os.path.join(DIST, "demo", "index.html")).read()
    for m in re.findall(r'(?:src|href)="(/demo/[^"]+)"', demo_html):
        target = os.path.join(DIST, m.lstrip("/").replace("/", os.sep))
        if not os.path.isfile(target):
            die(f"demo references missing asset: {m}")
    print(f"link check: {len(hrefs)} landing refs + join/portal pages + demo assets resolve")


def main() -> None:
    skip_npm = "--skip-npm" in sys.argv
    manifest = load_manifest()
    build_demo(skip_npm)
    restructure()
    render_landing(manifest)
    render_portal()
    render_join()
    copy_assets()
    link_check()
    n_files = sum(len(fs) for _, _, fs in os.walk(DIST))
    total = sum(os.path.getsize(os.path.join(dp, f)) for dp, _, fs in os.walk(DIST) for f in fs)
    print(f"\nDONE — {n_files} files, {total/1e6:.1f} MB in frontend/dist "
          "(landing at /, demo at /demo/, owner portal at /portal/, join page at /join/, "
          "checksums at /downloads/; installers link to GitHub Releases)")


if __name__ == "__main__":
    main()
