// receipt_watermark.go — the invisible watermark inside every branded
// receipt image (v1.6.0).
//
// WHY: a receipt image can be forwarded, edited in a photo editor, or
// outright fabricated (AI image generators included) and then presented
// as proof of payment. The watermark gives the shop a way to settle that
// argument in seconds: every image the POS renders carries the order's
// identity (order id, issue time, total) plus an HMAC-SHA256 tag keyed
// with a shop secret. The verifier reads the payload back and the tag
// only verifies for images THIS shop actually issued, unmodified. An
// edited amount, an AI-generated fake, or a foreign shop's image fails.
//
// TWO LAYERS, because lossy copies exist:
//
//   - Layer "lsb" (exact): one bit in the red channel's least
//     significant bit of consecutive pixels. Survives every lossless
//     copy (PNG re-save, download, forward as document). Dead after any
//     lossy re-encode (JPEG, WhatsApp's media pipeline, screenshots).
//   - Layer "block" (best-effort): the payload, every bit repeated 3×,
//     encoded as near-white cells darkened ~3% inside a dedicated pure
//     white strip added under the receipt footer. Invisible at receipt
//     contrast; the ~7-level shift in flat white is large enough that a
//     majority of cells survive moderate JPEG re-encoding (WhatsApp is
//     ≈ q70-80). The extractor majority-votes each triple, so scattered
//     cell losses do not corrupt the payload.
//
// The verifier tries "block" first (survives more) and falls back to
// "lsb". Verification requires the shop's watermark key — images from
// another shop, or plain photos, fail the HMAC tag.
//
// The key lives in settings (`receipt_watermark_key`, generated on first
// use) and rides team sync, so any till can verify a receipt any other
// till issued. It never leaves the shop.

package services

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"time"

	"posapp/internal/models"
)

const (
	watermarkMagic   = "CDCW1" // v1 of the payload format
	watermarkKeyKind = "receipt_watermark_key"
	// The dedicated white strip added under the receipt footer: 720px
	// wide → 120 cells/row × 10 rows = 1200 cells, comfortably more than
	// the 37-byte payload × 8 bits × 3 repeats = 888 cells needed.
	watermarkStripH = 60
	watermarkCell   = 6
)

// watermarkPayloadLen = magic(5) + orderID(8) + issued(8) + total(8) = 29
// body bytes, + 8 bytes of truncated HMAC tag = 37 total.
const watermarkBodyLen = 5 + 8 + 8 + 8

type ReceiptWatermark struct {
	OrderID    int64  `json:"orderId"`
	IssuedUnix int64  `json:"issuedAt"`
	TotalCents int64  `json:"totalCents"`
	Layer      string `json:"layer"` // "block" or "lsb"
}

// watermarkPayload builds the exact bytes that travel inside the image:
// magic + order id + issue time + total + truncated HMAC tag.
func watermarkPayload(key []byte, orderID, issuedUnix, totalCents int64) []byte {
	body := make([]byte, 0, watermarkBodyLen)
	body = append(body, watermarkMagic...)
	var num [8]byte
	binary.BigEndian.PutUint64(num[:], uint64(orderID))
	body = append(body, num[:]...)
	binary.BigEndian.PutUint64(num[:], uint64(issuedUnix))
	body = append(body, num[:]...)
	binary.BigEndian.PutUint64(num[:], uint64(totalCents))
	body = append(body, num[:]...)
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	return append(body, mac.Sum(nil)[:8]...)
}

// watermarkKey returns the shop's watermark secret, generating one on
// first use and storing it (team-synced — see the allowlist in
// teamsync_apply.go).
func (s *Service) watermarkKey() []byte {
	if k := s.settings.Get(watermarkKeyKind); len(k) >= 64 {
		if raw, err := hex.DecodeString(k[:64]); err == nil {
			return raw
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// Never fail a receipt over entropy: fall back to a derived key
		// (stored from then on, so verification stays deterministic).
		derived := sha256.Sum256([]byte(fmt.Sprintf("cdc|%s|%d", s.settings.Get("store_name"), time.Now().UnixNano())))
		copy(raw, derived[:])
	}
	_, _ = s.settings.Update(map[string]string{watermarkKeyKind: hex.EncodeToString(raw)})
	// Announce the new key so every team till can verify this shop's
	// receipts (EmitConfig honours the sync whitelist).
	s.EmitConfig([]string{watermarkKeyKind})
	return raw
}

var watermarkWhite = color.RGBA{R: 255, G: 255, B: 255, A: 255}

// embedWatermark writes the payload into a COPY of the receipt image:
// the block layer into a new pure-white strip under the content, the
// lsb layer into the first pixels. Returns the extended image.
func embedWatermark(o *models.Order, img image.Image, now time.Time, key []byte) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewRGBA(image.Rect(0, 0, w, h+watermarkStripH))
	draw.Draw(out, image.Rect(0, 0, w, h), img, b.Min, draw.Src)
	draw.Draw(out, image.Rect(0, h, w, h+watermarkStripH), image.NewUniform(watermarkWhite), image.Point{}, draw.Src)

	payload := watermarkPayload(key, o.ID, now.Unix(), o.TotalCents)

	// ---- block layer: every bit repeated 3×, one darkened cell each ----
	cellsPerRow := w / watermarkCell
	rows := watermarkStripH / watermarkCell
	capacity := cellsPerRow * rows
	full := payloadBitsTripled(payload)
	if capacity < len(full) { // guard: never write outside the strip
		full = full[:capacity-capacity%3]
	}
	for i, on := range full {
		if !on {
			continue
		}
		cx := (i % cellsPerRow) * watermarkCell
		cy := h + (i/cellsPerRow)*watermarkCell
		darkenCell(out, cx, cy)
	}

	// ---- lsb layer: exact copy for lossless verification ----
	setLSB(out, payload)
	return out
}

// payloadBitsTripled flattens the payload to bits, each repeated 3×
// (b0 b0 b0 b1 b1 b1 …) so the extractor can majority-vote through
// lossy re-encoding.
func payloadBitsTripled(payload []byte) []bool {
	bits := make([]bool, 0, len(payload)*24)
	for _, b := range payload {
		for k := 7; k >= 0; k-- {
			on := (b>>uint(k))&1 == 1
			bits = append(bits, on, on, on)
		}
	}
	return bits
}

// darkenCell pulls one cell ~3% toward gray — invisible on white, but a
// stable luminance signature for the extractor.
func darkenCell(img *image.RGBA, cx, cy int) {
	for y := cy; y < cy+watermarkCell; y++ {
		for x := cx; x < cx+watermarkCell; x++ {
			c := img.RGBAAt(x, y)
			img.SetRGBA(x, y, color.RGBA{
				R: uint8(int(c.R) * 248 / 255),
				G: uint8(int(c.G) * 248 / 255),
				B: uint8(int(c.B) * 248 / 255),
				A: c.A,
			})
		}
	}
}

func setLSB(img *image.RGBA, payload []byte) {
	bit := 0
	for y := 0; y < img.Bounds().Dy() && bit < len(payload)*8; y++ {
		for x := 0; x < img.Bounds().Dx() && bit < len(payload)*8; x++ {
			on := (payload[bit/8]>>uint(7-bit%8))&1 == 1
			c := img.RGBAAt(x, y)
			if on {
				c.R |= 1
			} else {
				c.R &^= 1
			}
			img.SetRGBA(x, y, c)
			bit++
		}
	}
}

// VerifyReceiptImage decodes a PNG (or JPEG) receipt image and checks
// its watermark against the shop key. Returns the payload and which
// layer carried it; ok=false when nothing verifiable is found.
func (s *Service) VerifyReceiptImage(data []byte) (*ReceiptWatermark, bool, string) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, false, "not a decodable image"
	}
	key := s.watermarkKey()
	if wm, ok := extractBlockLayer(src, key); ok {
		wm.Layer = "block"
		return wm, true, ""
	}
	if wm, ok := extractLSBLayer(src, key); ok {
		wm.Layer = "lsb"
		return wm, true, ""
	}
	return nil, false, "no valid watermark — image was not issued by this shop, or was modified/re-encoded past recovery"
}

// extractBlockLayer reads the bottom strip, majority-votes each bit
// triple, and verifies the HMAC tag.
func extractBlockLayer(src image.Image, key []byte) (*ReceiptWatermark, bool) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if h < watermarkStripH {
		return nil, false
	}
	cellsPerRow := w / watermarkCell
	// The full stream (body+tag) is len(payload)*8 bits; cells needed ×3.
	full := len(watermarkPayload(key, 0, 0, 0)) * 8
	if cellsPerRow*(watermarkStripH/watermarkCell) < full {
		return nil, false
	}
	raw := make([]byte, 0, len(watermarkPayload(key, 0, 0, 0)))
	byteAcc, nbits := uint8(0), 0
	for i := 0; i < full; i++ {
		// bit=1 was EMBEDDED as a darkened triple (255→248); count dark
		// cells. Majority over the triple rides out JPEG ringing.
		darks := 0
		for v := 0; v < 3; v++ {
			j := i*3 + v
			jcx := (j % cellsPerRow) * watermarkCell
			jcy := b.Min.Y + (h - watermarkStripH) + (j/cellsPerRow)*watermarkCell
			if cellLuma(src, jcx, jcy) < 251 {
				darks++
			}
		}
		bit := darks >= 2
		byteAcc = byteAcc<<1 | b2u8(bit)
		nbits++
		if nbits == 8 {
			raw = append(raw, byteAcc)
			byteAcc, nbits = 0, 0
		}
	}
	return decodeWatermark(raw, key)
}

// cellLuma averages one cell's luminance (0-255 scale).
func cellLuma(img image.Image, cx, cy int) int {
	sum, n := 0, 0
	for y := cy; y < cy+watermarkCell; y++ {
		for x := cx; x < cx+watermarkCell; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			sum += int(uint8(r>>8))*299 + int(uint8(g>>8))*587 + int(uint8(bl>>8))*114
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / n / 1000
}

func b2u8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// extractLSBLayer reads the exact LSB copy.
func extractLSBLayer(src image.Image, key []byte) (*ReceiptWatermark, bool) {
	b := src.Bounds()
	full := watermarkBodyLen + 8
	raw := make([]byte, 0, full)
	byteAcc, nbits := uint8(0), 0
	for y := b.Min.Y; y < b.Max.Y && len(raw) < full; y++ {
		for x := b.Min.X; x < b.Max.X && len(raw) < full; x++ {
			r, _, _, _ := src.At(x, y).RGBA()
			byteAcc = byteAcc<<1 | uint8(r>>8)&1
			nbits++
			if nbits == 8 {
				raw = append(raw, byteAcc)
				byteAcc, nbits = 0, 0
			}
		}
	}
	return decodeWatermark(raw, key)
}

// decodeWatermark splits body|tag and verifies the HMAC.
func decodeWatermark(raw []byte, key []byte) (*ReceiptWatermark, bool) {
	if len(raw) < watermarkBodyLen+8 || !bytes.HasPrefix(raw, []byte(watermarkMagic)) {
		return nil, false
	}
	body, tag := raw[:watermarkBodyLen], raw[watermarkBodyLen:watermarkBodyLen+8]
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	if !hmac.Equal(tag, mac.Sum(nil)[:8]) {
		return nil, false
	}
	return &ReceiptWatermark{
		OrderID:    int64(binary.BigEndian.Uint64(body[5:13])),
		IssuedUnix: int64(binary.BigEndian.Uint64(body[13:21])),
		TotalCents: int64(binary.BigEndian.Uint64(body[21:29])),
	}, true
}

// watermarkCapacity documents/locks the strip capacity math (test seam).
func watermarkCapacity(w int) int {
	return (w / watermarkCell) * (watermarkStripH / watermarkCell)
}
