#!/usr/bin/env python3
"""Repack the LedgerPOS desktop installers for direct Netlify hosting.

Input : build/netlify-site/downloads/  (the packages as built/downloaded,
        e.g. fetched back from the GitHub release)
Output: same directory, repacked so that
        - every file is comfortably under Netlify drag-and-drop guidance
          (10 MB per file) — zips use zopfli deflation, Linux ships as
          a .tar.xz
        - archive member roots are clean (LedgerPOS/, LedgerPOS.app/,
          not stage-*/)
        - the app binaries inside are byte-identical to the originals
          (verified by sha256)
        - downloads/checksums.txt lists the SHA-256 of every package

Run from the repo root:  python3 scripts/repack.py
"""
import os
import shutil
import subprocess
import sys
import tarfile
import zipfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import pkgutil

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DL = os.path.join(ROOT, "build", "netlify-site", "downloads")
STAGE = os.path.join(ROOT, "build", "repack")

WIN_SETUP = "ledgerpos-setup-windows-x64.exe"
MAC_ARM_ZIP = "ledgerpos-macos-apple-silicon.zip"
MAC_INTEL_ZIP = "ledgerpos-macos-intel.zip"
LINUX_TAR_XZ = "ledgerpos-linux-x64.tar.xz"
LINUX_TAR_GZ = "ledgerpos-linux-x64.tar.gz"  # legacy input name

VERSION = "1.0.0"


def extract_original(name):
    """Extract a downloaded package into build/repack/<tag>/ and return its dir."""
    tag = name.split(".")[0]
    dest = os.path.join(STAGE, tag)
    shutil.rmtree(dest, ignore_errors=True)
    os.makedirs(dest)
    path = os.path.join(DL, name)
    if name.endswith(".zip"):
        with zipfile.ZipFile(path) as zf:
            zf.extractall(dest)
    else:
        with tarfile.open(path) as tf:
            tf.extractall(dest)
    return dest


def member_sha256(zip_path, suffix):
    """sha256 of the first zip member whose name ends with `suffix` (idempotent
    across the original stage-*/ layout and the clean LedgerPOS/ one)."""
    with zipfile.ZipFile(zip_path) as zf:
        member = [m for m in zf.namelist() if m.endswith(suffix)][0]
        import hashlib
        return hashlib.sha256(zf.read(member)).hexdigest()


def find_one(directory, filename):
    hits = []
    for base, _dirs, files in os.walk(directory):
        if filename in files:
            hits.append(os.path.join(base, filename))
    if len(hits) != 1:
        raise SystemExit(f"expected exactly one {filename} under {directory}, found {hits}")
    return hits[0]


def main():
    os.makedirs(DL, exist_ok=True)
    results = []

    # ---------- Windows ----------
    # The one-click NSIS installer is already final — no re-shrink step
    # exists for it (rebuilding would invalidate the uninstaller offsets),
    # so repack only verifies it: size guard + fresh checksum entry.
    print("[windows] verifying setup.exe (no repack — NSIS output is final)…")
    out = os.path.join(DL, WIN_SETUP)
    if not os.path.isfile(out):
        raise SystemExit(f"missing {out} — run scripts/build_installer.py first")
    size = os.path.getsize(out)
    results.append(("windows-exe", out, size, None, None))

    # ---------- macOS ----------
    for src_name, member_root in ((MAC_ARM_ZIP, "LedgerPOS.app"),
                                  (MAC_INTEL_ZIP, "LedgerPOS.app")):
        print(f"[mac] extracting original {src_name}…")
        src = os.path.join(DL, src_name)
        with zipfile.ZipFile(src) as zf:
            bin_member = [m for m in zf.namelist() if m.endswith("MacOS/ledgerpos")][0]
            import hashlib
            orig_bin_sha = hashlib.sha256(zf.read(bin_member)).hexdigest()
        mac_dir = extract_original(src_name)
        bin_path = find_one(mac_dir, "ledgerpos")
        os.chmod(bin_path, 0o755)
        app_root = os.path.dirname(os.path.dirname(os.path.dirname(bin_path)))  # .../LedgerPOS.app
        entries = []
        for base, dirs, files in os.walk(app_root):
            dirs.sort()
            for f in sorted(files):
                full = os.path.join(base, f)
                rel = os.path.relpath(full, app_root)
                mode = 0o755 if full == bin_path else (os.stat(full).st_mode & 0o777)
                entries.append((os.path.join("LedgerPOS.app", rel), full, 0o100000 | mode))
        print(f"[mac] re-zipping {src_name} with zopfli (~1 min)…")
        out = os.path.join(DL, src_name)
        size = pkgutil.write_zip_zopfli(out, entries)
        results.append(("mac", out, size, orig_bin_sha, "LedgerPOS.app/Contents/MacOS/ledgerpos"))

    # ---------- Linux: tar.xz ----------
    print("[linux] extracting original tar…")
    gz = os.path.join(DL, LINUX_TAR_GZ)
    xz_existing = os.path.join(DL, LINUX_TAR_XZ)
    src_tar = gz if os.path.exists(gz) else xz_existing  # idempotent re-runs
    lin_dir = os.path.join(STAGE, "linux")
    shutil.rmtree(lin_dir, ignore_errors=True)
    os.makedirs(lin_dir)
    with tarfile.open(src_tar) as tf:
        bin_member = [m for m in tf.getnames() if m.endswith("/ledgerpos") or m == "ledgerpos"][0]
        import hashlib
        orig_lin_sha = hashlib.sha256(tf.extractfile(bin_member).read()).hexdigest()
        tf.extractall(lin_dir, filter="data")
    lin_bin = find_one(lin_dir, "ledgerpos")
    os.chmod(lin_bin, 0o755)
    readme = find_one(lin_dir, "README.txt")
    with open(readme, "w") as f:
        f.write(
            "LedgerPOS " + VERSION + "\n\n"
            "  tar xf ledgerpos-linux-x64.tar.xz\n"
            "  ./ledgerpos\n\n"
            "The app opens in your browser. First login: admin / admin123.\n"
            "Data lives in ~/.local/share/LedgerPOS.\n"
        )
    print("[linux] building tar.xz (xz -9e)…")
    xz_out = os.path.join(DL, LINUX_TAR_XZ)
    if os.path.exists(xz_out):
        os.remove(xz_out)
    subprocess.run(["tar", "-cf", "-", "-C", lin_dir, "ledgerpos", "README.txt"],
                   check=True, stdout=subprocess.PIPE)  # dry probe not needed; real run below
    with open(xz_out, "wb") as fh:
        proc = subprocess.Popen(["tar", "-cf", "-", "-C", lin_dir, "ledgerpos", "README.txt"],
                                stdout=subprocess.PIPE)
        xz = subprocess.Popen(["xz", "-9e", "-T2"], stdin=proc.stdout, stdout=fh)
        proc.stdout.close()
        rc = xz.wait()
        if rc != 0:
            raise SystemExit(f"xz failed rc={rc}")
        if proc.wait() != 0:
            raise SystemExit("tar failed")
    if os.path.exists(gz):
        os.remove(gz)
    results.append(("linux", xz_out, os.path.getsize(xz_out), orig_lin_sha, "ledgerpos"))

    # ---------- verify + checksums ----------
    print("\n[verify] size guard + binary integrity…")
    lines = []
    ok = True
    for tag, path, size, orig_sha, member in results:
        safe = pkgutil.assert_netlify_safe(path)
        import hashlib
        if member is None:
            # Final installer binary (NSIS setup.exe): no inner member to
            # byte-compare — size guard plus a fresh checksum entry.
            match = True
            print(f"  [{'OK ' if safe else 'FAIL'}] {os.path.basename(path):42s} {size/1e6:6.2f} MB  "
                  f"installer passthrough (no byte-identity check)")
            if not safe:
                ok = False
        else:
            if tag == "linux":
                with tarfile.open(path) as tf:
                    data = tf.extractfile(member).read()
            else:
                with zipfile.ZipFile(path) as zf:
                    data = zf.read(member)
            new_sha = hashlib.sha256(data).hexdigest()
            match = new_sha == orig_sha
            status = "OK " if (match and safe) else "FAIL"
            if not (match and safe):
                ok = False
            print(f"  [{status}] {os.path.basename(path):42s} {size/1e6:6.2f} MB  "
                  f"binary sha256 {'match' if match else 'MISMATCH!'}")
        lines.append(f"{pkgutil.sha256_of(path)}  {os.path.basename(path)}")

    ck = os.path.join(DL, "checksums.txt")
    with open(ck, "w") as f:
        f.write("LedgerPOS " + VERSION + " — SHA-256 of the download packages\n\n")
        f.write("\n".join(lines) + "\n")
    print(f"\nchecksums -> {ck}")
    if not ok:
        raise SystemExit("VERIFICATION FAILED")
    print("ALL PACKAGES NETLIFY-SAFE & BYTE-IDENTICAL BINARIES")


if __name__ == "__main__":
    main()
