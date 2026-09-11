// Package printer renders ESC/POS receipts and drives a persistent,
// crash-recoverable print queue.
package printer

import (
        "bytes"
        "fmt"
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
        StoreName    string
        StoreAddress string
        StorePhone   string
        Footer       string
        OrderNumber  string
        When         string
        Cashier      string
        Customer     string
        Items        []models.OrderItem
        SubtotalCents int64
        TaxCents     int64
        TotalCents   int64
        Currency     string
        PayLines     []string // e.g. "M-Pesa NLJ7RT61SV", "Cash"
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

// Render produces ESC/POS bytes. widthCols: 48 (80mm) or 32 (58mm).
func Render(d ReceiptData, widthCols int) ([]byte, error) {
        var buf bytes.Buffer
        p := escpos.New(&buf)

        p.Initialize()
        p.Justify(escpos.JustifyCenter)
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
                line := fmt.Sprintf("%d x %s", it.Qty, formatMoneyFull(it.UnitPriceCents, d.Currency))
                p.Write(padlr("  "+line, formatMoneyFull(it.LineTotalCents, ""), widthCols) + "\n")
        }

        p.Write(strings.Repeat("-", widthCols) + "\n")
        p.Write(padlr("Subtotal", formatMoneyFull(d.SubtotalCents, d.Currency), widthCols) + "\n")
        p.Write(padlr("Tax", formatMoneyFull(d.TaxCents, d.Currency), widthCols) + "\n")
        p.Bold(true)
        p.Size(1, 1)
        p.Write(padlr("TOTAL", formatMoneyFull(d.TotalCents, d.Currency), widthCols) + "\n")
        p.Bold(false)
        p.Size(0, 0)
        p.Write(strings.Repeat("-", widthCols) + "\n")
        for _, pl := range d.PayLines {
                p.Write(truncate(pl, widthCols) + "\n")
        }
        p.Write("\n")

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
                when = t.Local().Format("2006-01-02 15:04")
        }
        var payLines []string
        for _, pm := range o.Payments {
                switch pm.Method {
                case models.MethodCash:
                        payLines = append(payLines, "PAID: Cash")
                case models.MethodMpesa:
                        label := "PAID: M-Pesa"
                        if pm.MpesaReceipt != "" {
                                label += " " + pm.MpesaReceipt
                        }
                        if pm.Phone != "" {
                                label += " (" + pm.Phone + ")"
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
                Items: o.Items, SubtotalCents: o.SubtotalCents, TaxCents: o.TaxCents,
                TotalCents: o.TotalCents, Currency: currency, PayLines: payLines,
        }
}
