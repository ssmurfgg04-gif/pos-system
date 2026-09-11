#!/usr/bin/env python3
"""Package LedgerPOS for desktop distribution + build the Netlify site.

Repo-relative and CI-friendly. Run from anywhere:

    python3 scripts/package_desktop.py [version]

Produces, under build/netlify-site/:
  index.html + icons   — the download landing page (drag-drop to Netlify)
  demo/                — in-browser demo build of the full app (/demo/)
  downloads/           — the installer packages (for GitHub Release upload):
      ledgerpos-setup-windows-x64.zip      — self-installing exe
      ledgerpos-macos-apple-silicon.zip    — .app bundle (M-series)
      ledgerpos-macos-intel.zip            — .app bundle (Intel)
      ledgerpos-linux-x64.tar.gz           — static binary + first-run notes

The site zip (build/netlify-site.zip + copy in LEDGERPOS_DOWNLOAD_DIR)
EXCLUDES downloads/ by default — binaries are hosted on GitHub Releases
and the landing page links there. Set INCLUDE_DOWNLOADS_IN_SITE=1 to
embed them instead (fully self-contained site, ~40 MB).

Env overrides: LEDGERPOS_RELEASE_BASE (download URL prefix),
LEDGERPOS_DOWNLOAD_DIR (extra copy of site zip + installers),
INCLUDE_DOWNLOADS_IN_SITE=1.
"""
import os
import shutil
import subprocess
import sys
import zipfile
from datetime import date

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRIPTS = os.path.join(ROOT, "scripts")
SITE = os.path.join(ROOT, "build", "netlify-site")
DOWNLOAD = (os.environ.get("LEDGERPOS_DOWNLOAD_DIR")
            or ("/home/z/my-project/download" if os.path.isdir("/home/z/my-project/download")
                else os.path.join(ROOT, "build")))
RELEASES_PAGE = "https://github.com/ssmurfgg04-gif/pos-system/releases"

VERSION = sys.argv[1] if len(sys.argv) > 1 else "1.0.0"
RELEASE_BASE = (os.environ.get("LEDGERPOS_RELEASE_BASE")
                or f"{RELEASES_PAGE}/download/v{VERSION}")

GOENV = dict(os.environ, PATH="/home/z/.local/go-sdk/bin:" + os.environ["PATH"])
if not os.path.isdir("/home/z/.local/go-sdk/bin"):
    GOENV = dict(os.environ)


def run(cmd, cwd=ROOT, env=None, check=True):
    print(f"  $ {' '.join(cmd) if isinstance(cmd, list) else cmd}")
    return subprocess.run(
        cmd if isinstance(cmd, list) else cmd,
        cwd=cwd, shell=isinstance(cmd, str),
        check=check, env=env or os.environ,
        capture_output=True, text=True,
    )


def find_go_winres():
    """Locate go-winres (Windows icon/manifest/version compiler) or install it."""
    for cand in (os.path.expanduser("~/go/bin/go-winres"),
                 "/tmp/gobin/go-winres"):
        if os.path.isfile(cand):
            return cand
    found = shutil.which("go-winres")
    if found:
        return found
    gobin = "/tmp/gobin"
    print("  go-winres not found — installing github.com/tc-hib/go-winres@v1.26.0")
    run(["go", "install", "github.com/tc-hib/go-winres@v1.26.0"],
        env=dict(GOENV, GOBIN=gobin))
    return os.path.join(gobin, "go-winres")


def go_build(goos, goarch, out, extra_ldflags=""):
    ld = f"-s -w -X main.version={VERSION}"
    if extra_ldflags:
        ld += " " + extra_ldflags
    env = dict(GOENV, GOOS=goos, GOARCH=goarch, CGO_ENABLED="0")
    run(["go", "build", "-trimpath", "-ldflags", ld, "-o", out, "."], env=env)


def zip_dir(src_dir, arc_root, dest):
    """zip that preserves unix permissions (mac .app needs the exec bit)."""
    if os.path.exists(dest):
        os.remove(dest)
    with zipfile.ZipFile(dest, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
        for base, _dirs, files in os.walk(arc_root):
            for f in files:
                full = os.path.join(base, f)
                rel = os.path.relpath(full, arc_root)
                zi = zipfile.ZipInfo(os.path.join(os.path.basename(arc_root), rel) if arc_root not in (None, "") else rel)
                zi.compress_type = zipfile.ZIP_DEFLATED
                st = os.stat(full)
                zi.external_attr = (st.st_mode & 0xFFFF) << 16
                zi.date_time = (2026, 1, 1, 0, 0, 0)
                with open(full, "rb") as fh:
                    z.writestr(zi, fh.read())
    print(f"    → {os.path.basename(dest)}  {os.path.getsize(dest)/1e6:.1f} MB")


def human_mb(p):
    return f"{os.path.getsize(p)/1e6:.0f}"


ASSETS = ("ledgerpos-setup-windows-x64.zip",
          "ledgerpos-macos-apple-silicon.zip",
          "ledgerpos-macos-intel.zip",
          "ledgerpos-linux-x64.tar.gz")


def main():
    print(f"[1/7] icons + windows resources")
    run(["python3", os.path.join(SCRIPTS, "make_icon.py")], cwd=ROOT)
    winres = find_go_winres()
    run([winres, "simply", "--icon", "build/app.ico",
         "--manifest", "gui", "--out", "rsrc", "--arch", "amd64",
         "--product-name", "LedgerPOS", "--file-description", "LedgerPOS - Point of Sale",
         "--original-filename", "LedgerPOS.exe",
         "--product-version", VERSION, "--file-version", VERSION,
         "--copyright", "MIT License"])

    print(f"[2/7] frontend build (probe mode, embedded into all binaries)")
    run(["npm", "run", "build"], cwd=f"{ROOT}/frontend", env=dict(GOENV))

    print(f"[3/7] cross-compile 4 targets (CGO_ENABLED=0)")
    go_build("windows", "amd64", "build/out/LedgerPOS.exe", "-H=windowsgui")
    go_build("darwin", "arm64", "build/out/ledgerpos-arm")
    go_build("darwin", "amd64", "build/out/ledgerpos-intel")
    go_build("linux", "amd64", "build/out/ledgerpos-linux")

    dl = os.path.join(SITE, "downloads")
    os.makedirs(dl, exist_ok=True)

    print(f"[4/7] assemble installers")
    # ---- Windows: self-installing exe + first-run note ----
    win_stage = os.path.join(ROOT, "build", "stage-win")
    shutil.rmtree(win_stage, ignore_errors=True)
    os.makedirs(win_stage)
    shutil.copy2("build/out/LedgerPOS.exe", os.path.join(win_stage, "LedgerPOS.exe"))
    with open(os.path.join(win_stage, "FIRST-RUN.txt"), "w") as f:
        f.write(
            "LedgerPOS " + VERSION + " — Point of Sale\n"
            "=========================================\n\n"
            "1. Double-click LedgerPOS.exe.\n"
            "   It installs itself (desktop + Start menu shortcuts, appears in\n"
            "   Add/Remove Programs) and opens in your browser.\n"
            "2. Sign in:  admin / admin123   (PIN 1234)\n"
            "3. Change that password in Settings, set your store name — done.\n\n"
            "Windows may show 'Windows protected your PC' (no code-signing\n"
            "certificate yet): More info -> Run anyway.\n\n"
            "Everything stays on this machine; data lives in\n"
            "%APPDATA%\\LedgerPOS. Uninstall from Windows Settings, or:\n"
            "  LedgerPOS.exe --uninstall\n"
        )
    zip_dir(win_stage, win_stage, os.path.join(dl, "ledgerpos-setup-windows-x64.zip"))

    # ---- macOS: .app bundles ----
    def mac_app(binary, stage_name):
        stage = os.path.join(ROOT, "build", stage_name)
        shutil.rmtree(stage, ignore_errors=True)
        app = os.path.join(stage, "LedgerPOS.app", "Contents")
        os.makedirs(os.path.join(app, "MacOS"))
        os.makedirs(os.path.join(app, "Resources"))
        exe = os.path.join(app, "MacOS", "ledgerpos")
        shutil.copy2(binary, exe)
        os.chmod(exe, 0o755)
        shutil.copy2("build/app.icns", os.path.join(app, "Resources", "app.icns"))
        with open(os.path.join(app, "PkgInfo"), "w") as f:
            f.write("APPL????")
        with open(os.path.join(app, "Info.plist"), "w") as f:
            f.write(f"""<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key><string>LedgerPOS</string>
    <key>CFBundleDisplayName</key><string>LedgerPOS</string>
    <key>CFBundleIdentifier</key><string>app.ledgerpos.desktop</string>
    <key>CFBundleVersion</key><string>{VERSION}</string>
    <key>CFBundleShortVersionString</key><string>{VERSION}</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleExecutable</key><string>ledgerpos</string>
    <key>CFBundleIconFile</key><string>app.icns</string>
    <key>LSUIElement</key><true/>
    <key>LSMinimumSystemVersion</key><string>12.0</string>
    <key>NSHighResolutionCapable</key><true/>
    <key>CFBundleDevelopmentRegion</key><string>en</string>
</dict>
</plist>
""")
        return stage

    arm_stage = mac_app("build/out/ledgerpos-arm", "stage-mac-arm")
    zip_dir(arm_stage, arm_stage, os.path.join(dl, "ledgerpos-macos-apple-silicon.zip"))
    intel_stage = mac_app("build/out/ledgerpos-intel", "stage-mac-intel")
    zip_dir(intel_stage, intel_stage, os.path.join(dl, "ledgerpos-macos-intel.zip"))

    # ---- Linux: tar.gz ----
    lin_stage = os.path.join(ROOT, "build", "stage-linux")
    shutil.rmtree(lin_stage, ignore_errors=True)
    os.makedirs(lin_stage)
    shutil.copy2("build/out/ledgerpos-linux", os.path.join(lin_stage, "ledgerpos"))
    os.chmod(os.path.join(lin_stage, "ledgerpos"), 0o755)
    with open(os.path.join(lin_stage, "README.txt"), "w") as f:
        f.write(
            "LedgerPOS " + VERSION + "\n\n"
            "  tar xf ledgerpos-linux-x64.tar.gz\n"
            "  ./ledgerpos\n\n"
            "The app opens in your browser. First login: admin / admin123.\n"
            "Data lives in ~/.local/share/LedgerPOS.\n"
        )
    tar_dest = os.path.join(dl, "ledgerpos-linux-x64.tar.gz")
    if os.path.exists(tar_dest):
        os.remove(tar_dest)
    run(["tar", "-czf", tar_dest, "-C", lin_stage, "ledgerpos", "README.txt"])
    print(f"    → ledgerpos-linux-x64.tar.gz  {os.path.getsize(tar_dest)/1e6:.1f} MB")

    print(f"[5/7] demo build for /demo/ (in-browser backend, base=/demo/)")
    demo_out = os.path.join(SITE, "demo")
    run(["npx", "vite", "build", "--base=/demo/", "--outDir", demo_out,
         "--emptyOutDir"], cwd=f"{ROOT}/frontend",
        env=dict(GOENV, VITE_DEMO_MODE="true"))
    # Netlify only honours _redirects at the publish root; drop the copy
    # the frontend template injected into the demo dist.
    for junk in (os.path.join(demo_out, "_redirects"),):
        if os.path.exists(junk):
            os.remove(junk)

    print(f"[6/7] landing page")
    for icon in ("icon-64.png", "icon-128.png", "icon-256.png", "icon-512.png"):
        shutil.copy2(f"build/{icon}", os.path.join(SITE, icon))
    tpl = open(os.path.join(SCRIPTS, "assets", "download-page.html")).read()
    tpl = (tpl
           .replace("{{VERSION}}", VERSION)
           .replace("{{BUILD_DATE}}", date.today().strftime("%b %Y"))
           .replace("{{RELEASE_BASE}}", RELEASE_BASE)
           .replace("{{RELEASES_PAGE}}", RELEASES_PAGE)
           .replace("{{WIN_SIZE_MB}}", human_mb(os.path.join(dl, "ledgerpos-setup-windows-x64.zip")))
           .replace("{{MAC_ARM_SIZE_MB}}", human_mb(os.path.join(dl, "ledgerpos-macos-apple-silicon.zip")))
           .replace("{{MAC_INTEL_SIZE_MB}}", human_mb(os.path.join(dl, "ledgerpos-macos-intel.zip")))
           .replace("{{LINUX_SIZE_MB}}", human_mb(tar_dest)))
    with open(os.path.join(SITE, "index.html"), "w") as f:
        f.write(tpl)
    with open(os.path.join(SITE, "_redirects"), "w") as f:
        f.write("# SPA fallback for the in-browser demo app only\n"
                "/demo/*  /demo/index.html  200\n")

    print(f"[7/7] site zip for drag-and-drop deploy")
    embed_downloads = os.environ.get("INCLUDE_DOWNLOADS_IN_SITE") == "1"
    os.makedirs(DOWNLOAD, exist_ok=True)
    site_zip = os.path.join(DOWNLOAD, "ledgerpos-netlify-site.zip")
    if os.path.exists(site_zip):
        os.remove(site_zip)
    skipped = 0
    with zipfile.ZipFile(site_zip, "w", zipfile.ZIP_DEFLATED, compresslevel=6) as z:
        for base, dirs, files in os.walk(SITE):
            dirs.sort()
            for f in sorted(files):
                full = os.path.join(base, f)
                rel = os.path.relpath(full, SITE)
                if not embed_downloads and rel.startswith("downloads" + os.sep):
                    skipped += 1
                    continue
                z.write(full, rel)
    print(f"    → {site_zip}  {os.path.getsize(site_zip)/1e6:.1f} MB"
          + (f"  ({skipped} installer files skipped — host them on GitHub Releases)"
             if skipped else ""))
    # Standalone copies of the installers for direct upload to a release.
    for a in ASSETS:
        shutil.copy2(os.path.join(dl, a), os.path.join(DOWNLOAD, a))
        print(f"    → {os.path.join(DOWNLOAD, a)}  {os.path.getsize(os.path.join(dl, a))/1e6:.1f} MB")

    print(f"\nDONE — upload {SITE}/downloads/* to a GitHub release, then drag-drop")
    print(f"the site zip onto Netlify. Installers also copied to {DOWNLOAD}.")


if __name__ == "__main__":
    main()
