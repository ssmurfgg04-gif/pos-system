// receipt_image.go — the branded receipt PNG that rides every automatic
// WhatsApp send (v1.5.0).
//
// The customer receives a real image with the shop's logo (or a monogram
// rendered from the shop name when no logo is uploaded), the shop's brand
// color, the sale details (items, totals, payments) and the receipt footer.
// Everything renders server-side with Go's stdlib image packages plus the
// embedded Go fonts (golang.org/x/image/font/gofont) — no system fonts, no
// external services, deterministic output on every till.
//
// Layout (720px wide, dynamic height):
//
//      [ brand-colored header band: logo/monogram + shop name ]
//      RECEIPT <number>                    <date>
//      ------------------------------------------
//      item name                                amount
//      2 x KES 500.00   (gray sub-line)
//      ------------------------------------------
//      Discount            -KES 50.00
//      VAT (16%)            KES 69.00
//      TOTAL               KES 1,250.00   (big, bold)
//      Paid M-Pesa         KES 1,250.00
//      M-Pesa code: QGH7XYZ1
//      ------------------------------------------
//      We're glad you chose us!   (brand color, centered)
//      <receipt footer setting>   (centered, gray)

package services

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg" // logo uploads may be JPEG
	"image/png"
	"math"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"posapp/internal/models"
	"posapp/internal/printer"
)

// The logo file lives next to the database (the desktop process chdir's
// into the data dir) — the same file the Settings → Store upload writes.
// The constant is duplicated here because handlers imports services.
const receiptLogoFile = "brand-logo.png"

const (
	receiptW      = 720
	receiptMargin = 40
)

// Embedded Go fonts, parsed once per process.
var (
	goFontOnce sync.Once
	goRegFont  *opentype.Font
	goBoldFont *opentype.Font
	goFontErr  error
)

func loadGoFonts() (*opentype.Font, *opentype.Font, error) {
	goFontOnce.Do(func() {
		goRegFont, goFontErr = opentype.Parse(goregular.TTF)
		if goFontErr != nil {
			return
		}
		goBoldFont, goFontErr = opentype.Parse(gobold.TTF)
	})
	return goRegFont, goBoldFont, goFontErr
}

func faceFor(f *opentype.Font, size float64) font.Face {
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		panic("font face: " + err.Error()) // embedded fonts cannot fail in practice
	}
	return face
}

// hexColor parses #RRGGBB (falls back to the POS blue when unset/mangled).
func hexColor(s, fallback string) color.RGBA {
	parse := func(h string) (color.RGBA, bool) {
		h = strings.TrimPrefix(strings.TrimSpace(h), "#")
		if len(h) != 6 {
			return color.RGBA{}, false
		}
		var r, g, b int
		if _, err := fmt.Sscanf(h, "%02x%02x%02x", &r, &g, &b); err != nil {
			return color.RGBA{}, false
		}
		return color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}, true
	}
	if c, ok := parse(s); ok {
		return c
	}
	c, _ := parse(fallback)
	return c
}

func tint(c color.RGBA, alpha uint8) color.RGBA {
	return color.RGBA{R: c.R, G: c.G, B: c.B, A: alpha}
}

// measureText returns the rendered width of s in pixels.
func measureText(f font.Face, s string) int {
	return font.MeasureString(f, s).Ceil()
}

// drawText writes s at (x, baselineY) and returns the new x.
func drawText(img draw.Image, f font.Face, col color.RGBA, x, y int, s string) int {
	d := &font.Drawer{Dst: img, Src: image.NewUniform(col), Face: f, Dot: fixed.P(x, y)}
	d.DrawString(s)
	return x + measureText(f, s)
}

// drawTextRight writes s right-aligned to maxX at baselineY.
func drawTextRight(img draw.Image, f font.Face, col color.RGBA, maxX, y int, s string) {
	drawText(img, f, col, maxX-measureText(f, s), y, s)
}

// drawTextCenter writes s horizontally centered in [minX, maxX].
func drawTextCenter(img draw.Image, f font.Face, col color.RGBA, minX, maxX, y int, s string) {
	drawText(img, f, col, minX+(maxX-minX-measureText(f, s))/2, y, s)
}

// wrapText word-wraps s to maxWidth, honoring hard newlines. Words longer
// than the whole column are kept intact (they overflow rather than vanish).
func wrapText(f font.Face, s string, maxWidth int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := words[0]
		for _, w := range words[1:] {
			cand := line + " " + w
			if measureText(f, cand) > maxWidth {
				out = append(out, line)
				line = w
			} else {
				line = cand
			}
		}
		out = append(out, line)
	}
	return out
}

// hline draws a full-width hairline divider.
func hline(img draw.Image, col color.RGBA, y int) {
	for x := receiptMargin; x < receiptW-receiptMargin; x++ {
		img.Set(x, y, col)
	}
}

// scaleLogo fits src into a hPx-tall box preserving aspect ratio.
func scaleLogo(src image.Image, hPx int) image.Image {
	b := src.Bounds()
	if b.Dy() == 0 {
		return nil
	}
	wPx := int(math.Round(float64(b.Dx()) * float64(hPx) / float64(b.Dy())))
	if wPx < 1 {
		wPx = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, wPx, hPx))
	// Nearest-neighbour is fine at receipt scale; smooth enough for logos.
	for y := 0; y < hPx; y++ {
		sy := b.Min.Y + y*b.Dy()/hPx
		for x := 0; x < wPx; x++ {
			sx := b.Min.X + x*b.Dx()/wPx
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

// shopMonogram renders the fallback logo: a white disc with the shop
// name's initials (up to two letters) in the brand color.
func shopMonogram(store string, brand color.RGBA, radius int) image.Image {
	d := radius * 2
	img := image.NewRGBA(image.Rect(0, 0, d, d))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{R: 255, G: 255, B: 255, A: 255}), image.Point{}, draw.Src)
	words := strings.Fields(store)
	initials := ""
	for _, w := range words {
		if r := []rune(w); len(r) > 0 && len(initials) < 2 {
			initials += strings.ToUpper(string(r[0]))
		}
	}
	if initials == "" {
		initials = "S"
	}
	f := faceFor(mustBold(), float64(radius))
	drawTextCenter(img, f, brand, 0, d, d/2+radius/2, initials)
	return img
}

func mustBold() *opentype.Font {
	_, b, err := loadGoFonts()
	if err != nil || b == nil {
		panic("embedded gofonts unavailable: " + fmt.Sprint(err))
	}
	return b
}

// shopLogo returns the uploaded brand logo (decoded) or nil.
func (s *Service) shopLogo() image.Image {
	if s.settings.Get("brand_logo") != "1" {
		return nil
	}
	data, err := os.ReadFile(receiptLogoFile)
	if err != nil || len(data) == 0 {
		return nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	return src
}

// receiptPNG renders the branded receipt image for one order.
func (s *Service) receiptPNG(o *models.Order) ([]byte, error) {
	reg, bold, err := loadGoFonts()
	if err != nil {
		return nil, fmt.Errorf("fonts: %w", err)
	}

	store := s.settings.Get("store_name")
	if store == "" {
		store = s.settings.Get("app_name")
	}
	if store == "" {
		store = "Store"
	}
	symbol := s.settings.Get("currency_symbol")
	if symbol == "" {
		symbol = "KES"
	}
	brand := hexColor(s.settings.Get("brand_color"), "#0047AB")

	ink := color.RGBA{R: 0x1F, G: 0x29, B: 0x37, A: 255}
	muted := color.RGBA{R: 0x6B, G: 0x72, B: 0x80, A: 255}
	faint := color.RGBA{R: 0x9C, G: 0xA3, B: 0xAF, A: 255}
	line := color.RGBA{R: 0xE5, G: 0xE7, B: 0xEB, A: 255}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}

	fName := faceFor(bold, 21)
	fSub := faceFor(reg, 15)
	fMeta := faceFor(reg, 16)
	fItem := faceFor(bold, 19)
	fTotalLbl := faceFor(reg, 17)
	fTotal := faceFor(bold, 26)
	fThanks := faceFor(bold, 19)
	fFooter := faceFor(reg, 14)
	fStore := faceFor(bold, 30)

	// Generous canvas; cropped to the drawn height at the end.
	canvas := image.NewRGBA(image.Rect(0, 0, receiptW, 3600))
	right := receiptW - receiptMargin

	y := 0
	// ---- Header band ----
	bandH := 132
	draw.Draw(canvas, image.Rect(0, 0, receiptW, bandH), image.NewUniform(brand), image.Point{}, draw.Src)
	logoX := receiptMargin
	logoH := 76
	if lg := s.shopLogo(); lg != nil {
		if scaled := scaleLogo(lg, logoH); scaled != nil {
			sb := scaled.Bounds()
			if sb.Dx() > receiptW-receiptMargin-360 { // never crowd the name out
				scaled = scaleLogo(lg, logoH*(receiptW-receiptMargin-360)/sb.Dx())
				sb = scaled.Bounds()
			}
			draw.Draw(canvas, image.Rect(logoX, (bandH-sb.Dy())/2, logoX+sb.Dx(), (bandH-sb.Dy())/2+sb.Dy()), scaled, image.Point{}, draw.Over)
			logoX += sb.Dx() + 18
		}
	} else {
		mg := shopMonogram(store, brand, logoH/2)
		mb := mg.Bounds()
		draw.Draw(canvas, image.Rect(logoX, (bandH-mb.Dy())/2, logoX+mb.Dx(), (bandH-mb.Dy())/2+mb.Dy()), mg, image.Point{}, draw.Over)
		logoX += mb.Dx() + 18
	}
	nameY := bandH/2 + 11
	for i, l := range wrapText(fStore, store, right-logoX) {
		if i == 1 {
			fStore = faceFor(bold, 22) // long names shrink on the wrap
		}
		drawText(canvas, fStore, white, logoX, nameY, l)
		nameY += 32
		if nameY > bandH { // safety: never paint outside the band
			break
		}
	}

	y = bandH + 34
	// ---- Meta ----
	drawText(canvas, faceFor(reg, 13), faint, receiptMargin, y, "RECEIPT")
	drawText(canvas, fName, ink, receiptMargin, y+30, o.Number)
	if t, err := time.Parse(time.RFC3339, o.PaidAtOrCreated()); err == nil {
		drawTextRight(canvas, fMeta, muted, right, y+30, t.Local().Format("02/01/2006 15:04"))
	}
	if o.CashierName != "" {
		y += 30
		drawTextRight(canvas, fSub, muted, right, y, "Served by "+o.CashierName)
	}
	y += 46
	hline(canvas, line, y)

	// ---- Items ----
	y += 26
	for _, it := range o.Items {
		nameX := receiptMargin
		maxName := right - 150
		lines := wrapText(fItem, it.Name, maxName-nameX)
		lineY := y
		for _, l := range lines {
			drawText(canvas, fItem, ink, nameX, lineY, l)
			lineY += 26
		}
		drawTextRight(canvas, fItem, ink, right, y, printer.FormatMoney(it.LineTotalCents, symbol))
		sub := fmt.Sprintf("%d x %s", it.Qty, printer.FormatMoney(it.UnitPriceCents, symbol))
		if it.LineTotalCents != it.UnitPriceCents*int64(it.Qty) {
			sub += " · discount applied"
		}
		drawText(canvas, fSub, muted, nameX, lineY-8, sub)
		y = lineY + 18
	}
	y += 8
	hline(canvas, line, y)

	// ---- Totals ----
	row := func(lbl string, cents int64, neg bool, f font.Face, col color.RGBA) {
		drawText(canvas, f, col, receiptMargin, y, lbl)
		v := printer.FormatMoney(cents, symbol)
		if neg {
			v = "-" + v
		}
		drawTextRight(canvas, f, col, right, y, v)
		y += 32
	}
	y += 26
	if o.DiscountCents != 0 {
		row("Discount", o.DiscountCents, true, fTotalLbl, muted)
	}
	if o.TaxCents != 0 {
		row(fmt.Sprintf("VAT (%s%%)", trimPercent(o.TaxPercent)), o.TaxCents, false, fTotalLbl, muted)
	}
	drawText(canvas, fTotal, ink, receiptMargin, y+6, "TOTAL")
	drawTextRight(canvas, fTotal, ink, right, y+6, printer.FormatMoney(o.TotalCents, symbol))
	y += 52

	for _, pm := range o.Payments {
		if pm.Status == "FAILED" {
			continue
		}
		row(paymentWord(pm), pm.AmountCents, false, fTotalLbl, ink)
		if pm.MpesaReceipt != "" {
			drawText(canvas, fSub, muted, receiptMargin, y-22, "M-Pesa code: "+pm.MpesaReceipt)
		}
	}
	y += 6
	hline(canvas, line, y)

	// ---- Footer ----
	y += 38
	drawTextCenter(canvas, fThanks, brand, receiptMargin, right, y, "We're glad you chose us!")
	y += 34
	if note := s.settings.Get("receipt_footer"); note != "" {
		for _, l := range wrapText(fFooter, note, receiptW-2*receiptMargin) {
			drawTextCenter(canvas, fFooter, muted, receiptMargin, right, y, l)
			y += 20
		}
	}
	y += 26

	crop := image.Rect(0, 0, receiptW, y)
	var out bytes.Buffer
	if err := png.Encode(&out, canvas.SubImage(crop)); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
