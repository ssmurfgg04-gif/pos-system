# Vendored: github.com/skip2/go-qrcode

Upstream commit da1b6568686e89143e94f980a98bc2dbd5537f13, MIT licensed (see LICENSE, copyright Tom Harwood).
Pure Go, standard library only. Copied in (import paths rewritten to
posapp/internal/qrcode) so receipts can carry the KRA eTIMS QR code:

- the WhatsApp receipt image draws it;
- thermal printers print it as a raster image, which every ESC/POS printer
  that prints the logo supports (the native GS ( k QR command is not on
  every cheap printer).

Not vendored: the decode tests (they need the zbarimg binary).
