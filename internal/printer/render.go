// Package printer renders ESC/POS receipts and drives a persistent,
// crash-recoverable print queue.
package printer

import (
	"bytes"
	"fmt"
	"image"
	"strings"
	"time"

	"posapp/internal/escpos"

	"posapp/internal/models"
)

// FormatMoney renders integer cents as a decimal money string with symbol
// and thousands separators (e.g. "KES 1,234.50").
func FormatMoney(cents int64, symbol string) string {
	return formatMoneyFull(cents, symbol)
}

// thousands inserts commas into the integer part of "12345.67".
func thousands(intPart string) string {
	if len(intPart) <= 3 {
		return intPart
	}
	var out []string
	for len(intPart) > 3 {
		out = append([]string{intPart[len(intPart)-3:]}, out...)
		intPart = intPart[:len(intPart)-3]
	}
	out = append([]string{intPart}, out...)
	return strings.Join(out, ",")
}

// formatMoneyFull is the real formatter (thousands separators included).
func formatMoneyFull(cents int64, symbol string) string {
	neg := cents < 0
	if neg {
		cents = -cents
	}
	s := fmt.Sprintf("%s.%02d", thousands(fmt.Sprintf("%d", cents/100)), cents%100)
	if symbol != "" {
		s = symbol + " " + s
	}
	if neg {
		s = "-" + s
	}
	return s
}

// ReceiptData is everything the renderer needs; built from an order plus
// white-label settings.
type ReceiptData struct {
	StoreName     string
	StoreAddress  string
	StorePhone    string
	KraPin        string             // eTIMS: printed under the phone line when set
	BuyerPin      string             // customer KRA PIN — printed on a plain INVOICE (not eTIMS)
	Etims         *models.EtimsStamp // v1.10: KRA signature; "signed" makes this a TAX INVOICE
	GiftCards     []string           // codes minted by this sale — the customer's value
	Logo          image.Image        // nil = skip; printed centered above the name
	Footer        string
	OrderNumber   string
	When          string
	Cashier       string
	Customer      string
	Items         []models.OrderItem
	SubtotalCents int64
	DiscountCents int64  // shown as its own line so Subtotal − Discount + Tax = TOTAL reconciles
	DiscountLabel string // "staff 10%" — the WHY matters at the counter
	PointsCents   int64  // loyalty redemption as MONEY (points × point-cents) — the row must reconcile
	PointsPts     int64  // the point count, shown beside the money
	TaxCents      int64
	TaxPercent    float64 // for the "VAT (16%, incl.)" label — inclusive pricing is Kenya's default
	TaxIncluded   bool
	TotalCents    int64
	Currency      string
	PayLines      []string // e.g. "M-Pesa NLJ7RT61SV", "Cash"
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max > 1 {
		return string(r[:max-1]) + "…"
	}
	return string(r[:max])
}

func padlr(l, r string, width int) string {
	// l left-aligned, r right-aligned within width columns
	lw, rw := len([]rune(l)), len([]rune(r))
	space := width - lw - rw
	if space < 1 {
		return truncate(l, width)
	}
	return l + strings.Repeat(" ", space) + r
}

// trimPct formats 16.00 → "16", 13.5 → "13.5" for the VAT label.
func trimPct(f float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
	if s == "" {
		return "0"
	}
	return s
}

// Render produces ESC/POS bytes. widthCols: 48 (80mm) or 32 (58mm).
func Render(d ReceiptData, widthCols int) ([]byte, error) {
	var buf bytes.Buffer
	p := escpos.New(&buf)

	p.Initialize()
	p.Justify(escpos.JustifyCenter)
	if d.Logo != nil {
		if _, err := p.PrintImage(d.Logo); err != nil {
			return nil, err
		}
		p.Write("\n")
	}
	p.Size(1, 1)
	p.Bold(true)
	p.Write(truncate(strings.ToUpper(d.StoreName), widthCols) + "\n")
	p.Bold(false)
	p.Size(0, 0)
	if d.StoreAddress != "" {
		p.Write(truncate(d.StoreAddress, widthCols) + "\n")
	}
	if d.StorePhone != "" {
		p.Write(truncate(d.StorePhone, widthCols) + "\n")
	}
	if d.KraPin != "" {
		p.Write(truncate("KRA PIN: "+d.KraPin, widthCols) + "\n")
	}
	if d.Etims != nil && d.Etims.Status == "signed" {
		p.Bold(true)
		p.Write("TAX INVOICE\n")
		p.Bold(false)
		if d.BuyerPin != "" {
			p.Write(truncate("Buyer PIN: "+d.BuyerPin, widthCols) + "\n")
		}
	} else if d.BuyerPin != "" {
		// Not "TAX INVOICE": that title belongs to invoices signed by KRA
		// eTIMS (control-unit number + QR).
		p.Write(padlr("INVOICE", "Buyer PIN: "+d.BuyerPin, widthCols) + "\n")
		p.Write(truncate("Not an eTIMS tax invoice", widthCols) + "\n")
	}
	p.Write(strings.Repeat("-", widthCols) + "\n")

	p.Justify(escpos.JustifyLeft)
	p.Size(0, 0)
	p.Write(padlr("Receipt:", d.OrderNumber, widthCols) + "\n")
	p.Write(padlr("Date:", truncate(d.When, widthCols-12), widthCols) + "\n")
	if d.Cashier != "" {
		p.Write(padlr("Served by:", truncate(d.Cashier, widthCols-16), widthCols) + "\n")
	}
	if d.Customer != "" {
		p.Write(padlr("Customer:", truncate(d.Customer, widthCols-14), widthCols) + "\n")
	}
	p.Write(strings.Repeat("-", widthCols) + "\n")

	for _, it := range d.Items {
		p.Bold(true)
		p.Write(truncate(it.Name, widthCols) + "\n")
		p.Bold(false)
		line := fmt.Sprintf("%s x %s", formatQtyMilli(it.QtyMilli), formatMoneyFull(it.UnitPriceCents, d.Currency))
		p.Write(padlr("  "+line, formatMoneyFull(it.LineTotalCents, ""), widthCols) + "\n")
	}

	p.Write(strings.Repeat("-", widthCols) + "\n")
	p.Write(padlr("Subtotal", formatMoneyFull(d.SubtotalCents, d.Currency), widthCols) + "\n")
	if d.DiscountCents != 0 {
		lbl := "Discount"
		if d.DiscountLabel != "" {
			lbl = "Discount (" + d.DiscountLabel + ")"
		}
		p.Write(padlr(truncate(lbl, widthCols-14), "-"+formatMoneyFull(d.DiscountCents, ""), widthCols) + "\n")
	}
	if d.PointsCents != 0 {
		lbl := fmt.Sprintf("Points redeemed (%d pts)", d.PointsPts)
		p.Write(padlr(truncate(lbl, widthCols-14), "-"+formatMoneyFull(d.PointsCents, ""), widthCols) + "\n")
	}
	if d.TaxCents != 0 {
		lbl := fmt.Sprintf("VAT (%s%%)", trimPct(d.TaxPercent))
		if d.TaxIncluded {
			lbl = fmt.Sprintf("VAT (%s%%, incl.)", trimPct(d.TaxPercent))
		}
		p.Write(padlr(truncate(lbl, widthCols-14), formatMoneyFull(d.TaxCents, d.Currency), widthCols) + "\n")
	}
	p.Bold(true)
	p.Size(1, 1)
	p.Write(padlr("TOTAL", formatMoneyFull(d.TotalCents, d.Currency), widthCols) + "\n")
	p.Bold(false)
	p.Size(0, 0)
	p.Write(strings.Repeat("-", widthCols) + "\n")
	for _, pl := range d.PayLines {
		p.Write(truncate(pl, widthCols) + "\n")
	}
	for _, code := range d.GiftCards {
		p.Write(truncate("Gift card: "+code, widthCols) + "\n")
	}
	p.Write("\n")
	if d.Etims != nil {
		writeEtimsBlock(p, d.Etims, widthCols)
	}

	if d.Footer != "" {
		p.Justify(escpos.JustifyCenter)
		for _, line := range strings.Split(d.Footer, "\n") {
			p.Write(truncate(line, widthCols) + "\n")
		}
	}
	p.Write("\n")

	if err := p.PrintAndCut(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// BuildReceiptData assembles the render payload from an order + settings.
func BuildReceiptData(o *models.Order, storeName, storeAddr, storePhone, footer, currency string) ReceiptData {
	when := o.PaidAtOrCreated()
	if t, err := time.Parse(time.RFC3339, when); err == nil {
		when = t.Local().Format("02/01/2006 15:04") // Kenya reads day-first
	}
	// Every leg prints, with an HONEST label: a leg the provider has not
	// confirmed yet must never say "PAID" (that promise is in the docs,
	// and cashiers re-print pending orders from the order screen).
	var payLines []string
	for _, pm := range o.Payments {
		if pm.Status == "FAILED" {
			continue
		}
		done := pm.Status == "COMPLETED"
		switch pm.Method {
		case models.MethodCash:
			payLines = append(payLines, "PAID: Cash")
		case models.MethodMpesa:
			label := "M-Pesa (pending)"
			if done {
				label = "PAID: M-Pesa"
				if pm.MpesaReceipt != "" {
					label += " " + pm.MpesaReceipt
				}
			}
			if pm.Phone != "" {
				label += " (" + pm.Phone + ")"
			}
			payLines = append(payLines, label)
		case "account":
			label := "Tab charge"
			if !done {
				label += " (pending)"
			}
			payLines = append(payLines, label)
		case "credit":
			label := "Store credit"
			if !done {
				label += " (pending)"
			}
			payLines = append(payLines, label)
		case "paystack":
			label := "Card / mobile money (pending)"
			if done {
				label = "PAID: Card / mobile money"
			}
			payLines = append(payLines, label)
		}
	}
	if o.Status == models.OrderVoided {
		payLines = append(payLines, "*** VOIDED ***")
	}
	return ReceiptData{
		StoreName: storeName, StoreAddress: storeAddr, StorePhone: storePhone,
		Footer: footer, OrderNumber: o.Number, When: when,
		Cashier: o.CashierName, Customer: o.CustomerName,
		Items: o.Items, SubtotalCents: o.SubtotalCents,
		DiscountCents: o.DiscountCents, DiscountLabel: o.DiscountLabel,
		TaxCents:    o.TaxCents,
		TaxPercent:  o.TaxPercent,
		TaxIncluded: o.TaxIncluded,
		TotalCents:  o.TotalCents, Currency: currency, PayLines: payLines,
		BuyerPin: o.BuyerPIN,
	}
}

// formatQtyMilli renders a milli-unit quantity for the receipt: whole
// numbers plain ("2"), fractional with up to three decimals ("0.5").
func formatQtyMilli(m int64) string {
	if m%1000 == 0 {
		return fmt.Sprintf("%d", m/1000)
	}
	s := fmt.Sprintf("%.3f", float64(m)/1000.0)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

// writeEtimsBlock prints the KRA control-unit block of a signed sale (or a
// one-line note while it is still being sent).
func writeEtimsBlock(p *escpos.Escpos, st *models.EtimsStamp, widthCols int) {
	if st.Status != "signed" {
		p.Justify(escpos.JustifyCenter)
		p.Write(truncate("KRA eTIMS: sending to KRA", widthCols) + "\n\n")
		p.Justify(escpos.JustifyLeft)
		return
	}
	p.Write(strings.Repeat("-", widthCols) + "\n")
	p.Justify(escpos.JustifyCenter)
	p.Bold(true)
	p.Write("SCU INFORMATION\n")
	p.Bold(false)
	p.Justify(escpos.JustifyLeft)
	row := func(k, v string) { p.Write(padlr(k, truncate(v, widthCols-len(k)-1), widthCols) + "\n") }
	row("Date:", EtimsDate(st.SdcDateTime))
	row("SCU ID:", st.SdcID)
	row("CU invoice:", fmt.Sprintf("%s/%d", st.SdcID, st.CurRcptNo))
	row("Receipt no:", fmt.Sprintf("%d/%d NS", st.CurRcptNo, st.TotRcptNo))
	row("Internal data:", st.IntrlData)
	row("Signature:", st.RcptSign)
	if st.MrcNo != "" {
		row("MRC no:", st.MrcNo)
	}
	if st.QR != "" {
		p.Justify(escpos.JustifyCenter)
		size := uint8(5)
		if widthCols < 40 {
			size = 4
		}
		_, _ = p.QRCode(st.QR, true, size, 1)
		p.Write("\n")
		p.Justify(escpos.JustifyLeft)
	}
	p.Write(strings.Repeat("-", widthCols) + "\n")
}

// EtimsDate turns KRA's yyyyMMddHHmmss into 14/11/2026 14:30:05.
func EtimsDate(s string) string {
	if len(s) != 14 {
		return s
	}
	return s[6:8] + "/" + s[4:6] + "/" + s[0:4] + " " + s[8:10] + ":" + s[10:12] + ":" + s[12:14]
}
