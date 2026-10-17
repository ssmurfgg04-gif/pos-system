#!/usr/bin/env python3
"""Regenerate downloads/checksums.txt from the committed installers.

Run this EVERY time you rebuild the installers, before committing:

    python3 scripts/write_checksums.py

The deploy gate in netlify_build.py verifies installers against
checksums.txt and refuses to ship on any mismatch, so a rebuilt binary
without a regenerated checksum file fails CI by design.
"""
import hashlib
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DL = os.path.join(ROOT, "downloads")
INSTALLERS = [
    "ledgerpos-setup-windows-x64.exe",
    "ledgerpos-macos-apple-silicon.zip",
    "ledgerpos-macos-intel.zip",
    "ledgerpos-linux-x64.tar.xz",
]
VERSION = "1.3.6"


def sha256_of(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def main():
    lines = ["LedgerPOS %s — SHA-256 of the download packages" % VERSION, ""]
    for name in INSTALLERS:
        p = os.path.join(DL, name)
        if not os.path.isfile(p):
            print("missing downloads/%s" % name, file=sys.stderr)
            return 1
        lines.append("%s  %s" % (sha256_of(p), name))
    out = os.path.join(DL, "checksums.txt")
    with open(out, "w") as f:
        f.write("\n".join(lines) + "\n")
    print("wrote %s (%d installers)" % (out, len(INSTALLERS)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
