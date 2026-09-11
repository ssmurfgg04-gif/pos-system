"""Shared packaging helpers for LedgerPOS desktop distribution.

Provides a ZIP writer whose members are deflated with Google's zopfli
(~3% smaller than zlib -9) — enough to keep every installer under
Netlify drag-and-drop's recommended 10 MB per file — plus the usual
small utilities. Zopfli emits a *zlib* container; raw deflate for ZIP
members is obtained by stripping the 2-byte zlib header and the 4-byte
Adler-32 trailer (guarded: the FDICT flag must be clear).
"""
import os
import struct
import time
import zlib

import zopfli

try:
    from zopfli.zlib import compress as _zlib_compress
except ImportError:  # pragma: no cover
    _zlib_compress = None

# Fixed timestamp for reproducible archives.
ZIP_DATE = (2026, 1, 1, 0, 0, 0)

# Netlify drag-and-drop guidance: keep every file below this.
MAX_ASSET_BYTES = 10_000_000


def zopfli_raw_deflate(data, numiterations=5):
    """Raw deflate stream (ZIP method 8) compressed with zopfli."""
    if _zlib_compress is None:
        raise RuntimeError("zopfli python module not available")
    z = _zlib_compress(data, numiterations=numiterations,
                       blocksplitting=1, blocksplittinglast=0,
                       blocksplittingmax=15)
    if len(z) < 7 or z[0] != 0x78 or (z[1] & 0x20):
        raise RuntimeError("unexpected zlib container from zopfli")
    return z[2:-4]


def _dos_datetime(dt):
    year, month, day, hour, minute, second = dt
    if year < 1980:
        raise ValueError("zip timestamps must be >= 1980")
    ddate = ((year - 1980) << 9) | (month << 5) | day
    dtime = (hour << 11) | (minute << 5) | (second // 2)
    return dtime, ddate


def write_zip_zopfli(out_path, entries, numiterations=5):
    """Write a ZIP archive with zopfli-deflated members.

    entries: list of (arcname, disk_path, unix_mode). unix_mode is stored
    in the external attributes so macOS .app bundles keep their exec bits.
    Returns the archive size in bytes.
    """
    local_chunks = []
    central = []
    offset = 0
    dtime, ddate = _dos_datetime(ZIP_DATE)
    for arcname, disk_path, mode in entries:
        with open(disk_path, "rb") as fh:
            data = fh.read()
        comp = zopfli_raw_deflate(data, numiterations)
        crc = zlib.crc32(data) & 0xFFFFFFFF
        name = arcname.encode("utf-8")
        lh = struct.pack("<IHHHHHIIIHH", 0x04034B50, 20, 0, 8,
                         dtime, ddate, crc, len(comp), len(data),
                         len(name), 0) + name
        local_chunks.append(lh)
        local_chunks.append(comp)
        central.append(struct.pack(
            "<IHHHHHHIIIHHHHHII",
            0x02014B50,          # central directory signature
            20, 20,              # version made by / needed
            0,                   # flags
            8,                   # method: deflate
            dtime, ddate,
            crc, len(comp), len(data),
            len(name), 0, 0,     # name/extra/comment lengths
            0, 0,                # disk start / internal attrs
            (mode & 0xFFFF) << 16,
            offset) + name)
        offset += len(lh) + len(comp)

    central_blob = b"".join(central)
    eocd = struct.pack("<IHHHHIIH", 0x06054B50, 0, 0,
                       len(central), len(central),
                       len(central_blob), offset, 0)
    with open(out_path, "wb") as fh:
        fh.write(b"".join(local_chunks))
        fh.write(central_blob)
        fh.write(eocd)
    return os.path.getsize(out_path)


def sha256_of(path):
    import hashlib
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def human_mb(path):
    return f"{os.path.getsize(path)/1e6:.1f}"


def assert_netlify_safe(path):
    """Hard-fail if a file exceeds the Netlify drag-and-drop guidance."""
    size = os.path.getsize(path)
    if size >= MAX_ASSET_BYTES:
        raise SystemExit(
            f"{os.path.basename(path)} is {size:,} bytes "
            f"(>= {MAX_ASSET_BYTES:,}) — too big for Netlify drag-and-drop. "
            "Compress harder or host this file elsewhere.")
    return size
