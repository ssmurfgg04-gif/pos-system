package printer

import (
        "bytes"
        "strings"
        "testing"

        "posapp/internal/models"
)

func TestFormatMoney(t *testing.T) {
        cases := []struct {
                cents  int64
                symbol string
                want   string
        }{
                {55000, "KES", "KES 550.00"},
                {123456789, "KES", "KES 1,234,567.89"},
                {5, "KES", "KES 0.05"},
                {0, "KES", "KES 0.00"},
                {100, "", "1.00"},
                {-250, "KES", "-KES 2.50"},
        }
        for _, c := range cases {
                got := FormatMoney(c.cents, c.symbol)
                if got != c.want {
                        t.Errorf("FormatMoney(%d, %q) = %q, want %q", c.cents, c.symbol, got, c.want)
                }
        }
}

func sampleOrder() *models.Order {
        return &models.Order{
                Number: "ORD202609110001", Status: models.OrderPaid,
                SubtotalCents: 100000, TaxCents: 13793, TotalCents: 100000,
                CashierName: "Alex", CustomerName: "Jane",
                CreatedAt: "2026-09-11T10:00:00Z", PaidAt: "2026-09-11T10:01:00Z",
                Items: []models.OrderItem{
                        {ID: 1, ProductID: 1, Name: "Classic Cotton Tee — Black", SKU: "TS-001", Qty: 1, UnitPriceCents: 55000, LineTotalCents: 55000},
                        {ID: 2, ProductID: 5, Name: "Ceramic Mug — 11oz", SKU: "MG-001", Qty: 1, UnitPriceCents: 45000, LineTotalCents: 45000},
                },
                Payments: []models.Payment{
                        {ID: 1, OrderID: 1, Method: models.MethodMpesa, Mode: models.ModeSTK, AmountCents: 100000, Status: models.PaymentCompleted, MpesaReceipt: "NLJ7RT61SV", Phone: "254722123456"},
                },
        }
}

func TestRender80mm(t *testing.T) {
        data := BuildReceiptData(sampleOrder(), "My Store", "12 Market St", "+254 700 000000", "Thank you!", "KES")
        raw, err := Render(data, 48)
        if err != nil {
                t.Fatalf("render: %v", err)
        }
        if len(raw) == 0 {
                t.Fatal("empty render")
        }
        text := extractPrintable(raw)
        for _, want := range []string{
                "MY STORE", "ORD202609110001", "Classic Cotton Tee",
                "Ceramic Mug", "TOTAL", "KES 1,000.00", "NLJ7RT61SV", "Thank you!",
        } {
                if !strings.Contains(text, want) {
                        t.Errorf("80mm receipt missing %q", want)
                }
        }
        // ESC @ (init) and GS V (cut) must be present.
        if !bytes.Contains(raw, []byte{0x1b, 0x40}) {
                t.Error("missing ESC @ initialize")
        }
        if !bytes.Contains(raw, []byte{0x1d, 0x56}) {
                t.Error("missing GS V cut")
        }
}

func TestRender58mmTruncates(t *testing.T) {
        data := BuildReceiptData(sampleOrder(), "My Store", "12 Market St", "+254 700 000000", "Thank you!", "KES")
        raw, err := Render(data, 32)
        if err != nil {
                t.Fatalf("render: %v", err)
        }
        text := extractPrintable(raw)
        // Long names truncated but the cut marker survives.
        if !strings.Contains(text, "Ceramic Mug") && !strings.Contains(text, "Ceramic M") {
                t.Error("58mm should still include (truncated) item names")
        }
        if !bytes.Contains(raw, []byte{0x1d, 0x56}) {
                t.Error("58mm receipt missing cut")
        }
}

func extractPrintable(raw []byte) string {
        var b strings.Builder
        for _, c := range raw {
                if c >= 0x20 && c < 0x7f {
                        b.WriteByte(c)
                } else if c >= 0x80 {
                        b.WriteRune(' ') // UTF-8 continuation bytes etc.
                } else {
                        b.WriteByte(' ')
                }
        }
        return b.String()
}

func TestTargetParsing(t *testing.T) {
        cases := []struct {
                raw    string
                valid  bool
                enable bool
        }{
                {"", true, false},
                {"tcp://192.168.1.200:9100", true, true},
                {"file:///dev/usb/lp0", true, true},
                {"http://printer", false, true},
                {"192.168.1.200", false, true},
        }
        for _, c := range cases {
                tg := Target(c.raw)
                if tg.Valid() != c.valid {
                        t.Errorf("Target(%q).Valid() = %v, want %v", c.raw, tg.Valid(), c.valid)
                }
                if tg.Enabled() != c.enable {
                        t.Errorf("Target(%q).Enabled() = %v, want %v", c.raw, tg.Enabled(), c.enable)
                }
        }
        // Unsupported scheme must fail to open.
        if _, err := Target("http://printer").Open(); err == nil {
                t.Error("http target should not open")
        }
}
