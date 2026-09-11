package handlers

import (
        "fmt"
        "strings"
        "time"

        "github.com/gin-gonic/gin"
)

// ---- KRA monthly returns ----

// MonthlyReport (reports.view) — one calendar month of VAT figures for the
// accountant's KRA return. ?month=YYYY-MM (defaults to current month).
func (h *H) MonthlyReport(c *gin.Context) {
        summary, err := h.Svc.GetMonthlySummary(c.Query("month"))
        if err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        h.ok(c, summary)
}

// MonthlyReportCSV (reports.view) — the same month as a CSV the accountant
// can attach to the iTax return.
func (h *H) MonthlyReportCSV(c *gin.Context) {
        m, err := h.Svc.GetMonthlySummary(c.Query("month"))
        if err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        store := h.Settings.GetString("store_name", "Store")
        var b strings.Builder
        b.WriteString("KRA MONTHLY VAT RETURN SUMMARY\n")
        b.WriteString(fmt.Sprintf("Business,%s\n", csvField(store)))
        b.WriteString(fmt.Sprintf("Period,%s\n", m.Month))
        b.WriteString("Figure,Amount\n")
        b.WriteString(fmt.Sprintf("Gross sales (KES),%.2f\n", float64(m.GrossCents)/100))
        b.WriteString(fmt.Sprintf("Taxable value / nett (KES),%.2f\n", float64(m.NettCents)/100))
        b.WriteString(fmt.Sprintf("VAT at %.0f%% (KES),%.2f\n", m.TaxPercent, float64(m.VatCents)/100))
        b.WriteString(fmt.Sprintf("Transactions,%d\n", m.OrdersPaid))
        b.WriteString(fmt.Sprintf("Average transaction (KES),%.2f\n", float64(m.AvgOrderCents)/100))
        b.WriteString(fmt.Sprintf("Cash takings (KES),%.2f\n", float64(m.CashCents)/100))
        b.WriteString(fmt.Sprintf("M-Pesa takings (KES),%.2f\n", float64(m.MpesaCents)/100))
        b.WriteString(fmt.Sprintf("Voided orders,%d\n", m.OrdersVoided))
        b.WriteString(fmt.Sprintf("Amount discrepancies,%d\n", m.Discrepancies))
        if len(m.TopProducts) > 0 {
                b.WriteString("\nTop products\nProduct,Qty,Sales (KES)\n")
                for _, p := range m.TopProducts {
                        b.WriteString(fmt.Sprintf("%s,%d,%.2f\n", csvField(p.Name), p.Qty, float64(p.SalesCents)/100))
                }
        }
        c.Header("Content-Type", "text/csv; charset=utf-8")
        c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="kra-return-%s.csv"`, m.Month))
        c.String(200, b.String())
}

// csvField quotes a CSV field when needed (same rules as csv.go).
func csvField(s string) string {
        if strings.ContainsAny(s, ",\"\n") {
                return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
        }
        return s
}

// ---- Backups ----

// RunBackup (settings.manage) — manual snapshot now (SQLite VACUUM INTO).
func (h *H) RunBackup(c *gin.Context) {
        p := h.principal(c)
        res, err := h.Svc.BackupNow()
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.Svc.Audit(p.ID, p.Username, "BACKUP_CREATED", "backup", res.File, fmt.Sprintf("%d bytes", res.Bytes))
        h.ok(c, res)
}

// ListBackups (settings.manage) — backup history, newest first.
func (h *H) ListBackups(c *gin.Context) {
        h.ok(c, h.Svc.ListBackups())
}

// ---- Desktop (single-machine) mode ----

// DesktopInfo (public) — lets the SPA know it is running inside the
// desktop app so it can offer first-run hints and the Quit action.
// Zero risk: only booleans and the version string are exposed.
func (h *H) DesktopInfo(c *gin.Context) {
        h.ok(c, h.Desktop)
}

// QuitApp (settings.manage) — admin-only graceful shutdown for desktop
// mode. The response is flushed before the server stops so the browser
// can render the "safe to close" state.
func (h *H) QuitApp(c *gin.Context) {
        p := h.principal(c)
        h.Svc.Audit(p.ID, p.Username, "APP_QUIT", "system", "desktop", "")
        h.ok(c, gin.H{"quitting": true})
        if h.OnQuit != nil {
                go func() {
                        time.Sleep(300 * time.Millisecond) // let the response flush
                        h.OnQuit()
                }()
        }
}
