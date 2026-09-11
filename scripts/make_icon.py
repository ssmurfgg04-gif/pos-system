#!/usr/bin/env python3
"""Generate the LedgerPOS app icon set: .ico (Windows exe resource) and
.png (Linux/AppImage + landing page favicon/og image).

Ledger design language: emerald brand on slate shell, brutal 2px borders,
hard shadows. Single-letter mark for small-size legibility.
"""
from PIL import Image, ImageDraw

BRAND = (16, 185, 129)        # #10B981
BRAND_STRONG = (6, 95, 70)    # #065F46 border
BRAND_INK = (6, 26, 22)       # near-black ink on emerald
SHELL = (15, 23, 42)          # #0F172A

OUT = "build"  # run from repo root


def rounded(draw, box, radius, fill, outline, width):
    draw.rounded_rectangle(box, radius=radius, fill=fill, outline=outline, width=width)


def draw_mark(size: int, scale: float = 1.0) -> Image.Image:
    """Emerald rounded square + white slab 'L', drawn at high res."""
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    bw = max(2, round(size * 0.06))          # brutal border width
    m = bw  # outer margin so the border isn't clipped
    radius = round(size * 0.22)

    # shadow (offset hard block)
    off = round(size * 0.05)
    shadow_box = (m + off, m + off, size - m, size - m)
    rounded(d, shadow_box, radius, (2, 6, 23, 110), None, 0)

    # card
    card_box = (m, m, size - m - off, size - m - off)
    rounded(d, card_box, radius, BRAND, BRAND_STRONG, bw)

    # 'L' slab letter
    L = (255, 255, 255, 255)
    w = round(size * 0.115)                   # stroke width
    x0 = round(size * 0.32)
    x1 = round(size * 0.68)
    y0 = round(size * 0.26)
    y1 = round(size * 0.74)
    # vertical bar
    d.rounded_rectangle((x0, y0, x0 + w, y1), radius=w // 3, fill=L)
    # horizontal bar
    d.rounded_rectangle((x0, y1 - w, x1, y1), radius=w // 3, fill=L)
    return img


def main():
    import os
    os.makedirs(OUT, exist_ok=True)

    # master at 1024, downscale for crisp small sizes
    master = draw_mark(1024)

    # Windows .ico
    ico_sizes = [(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)]
    master.resize((256, 256), Image.LANCZOS).save(
        f"{OUT}/app.ico", format="ICO", sizes=ico_sizes
    )
    print("wrote app.ico")

    # PNGs
    for s in (512, 256, 128, 64):
        master.resize((s, s), Image.LANCZOS).save(f"{OUT}/icon-{s}.png")
    print("wrote icon pngs")

    # macOS .icns — modern ICNS accepts raw PNG payloads per chunk type.
    def png_bytes(size):
        from io import BytesIO
        b = BytesIO()
        master.resize((size, size), Image.LANCZOS).save(b, format="PNG")
        return b.getvalue()

    chunks = [
        (b"icp4", 16), (b"icp5", 32), (b"ic07", 128), (b"ic08", 256),
        (b"ic09", 512), (b"ic10", 1024), (b"ic11", 32), (b"ic12", 64),
        (b"ic13", 256), (b"ic14", 512),
    ]
    body = b""
    for ctype, px in chunks:
        data = png_bytes(min(px, 1024))
        body += ctype + (len(data) + 8).to_bytes(4, "big") + data
    with open(f"{OUT}/app.icns", "wb") as f:
        f.write(b"icns" + (len(body) + 8).to_bytes(4, "big") + body)
    print("wrote app.icns")


if __name__ == "__main__":
    main()
