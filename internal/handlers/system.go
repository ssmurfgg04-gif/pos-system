package handlers

import (
        "context"
        "errors"
        "fmt"
        "log"
        "os/exec"
        "runtime"
        "strings"
        "time"

        "github.com/gin-gonic/gin"

        "posapp/internal/services"
)

// ---- KRA monthly returns ----

// MonthlyReport (reports.view) — one calendar month of VAT figures for the
// accountant's KRA return. ?month=YYYY-MM (defaults to current month).
func (h *H) MonthlyReport(c *gin.Context) {
        summary, err := h.svc(c).GetMonthlySummary(c.Query("month"))
        if err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        h.ok(c, summary)
}

// MonthlyReportCSV (reports.view) — the same month as a CSV the accountant
// can attach to the iTax return.
func (h *H) MonthlyReportCSV(c *gin.Context) {
        m, err := h.svc(c).GetMonthlySummary(c.Query("month"))
        if err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        store := h.settings(c).GetString("store_name", "Store")
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

// csvField quotes a CSV field when needed (same rules as csv.go,
// including formula-injection neutralization).
func csvField(s string) string {
        if s != "" && strings.ContainsRune("=+-@", rune(s[0])) {
                s = "'" + s
        }
        if strings.ContainsAny(s, ",\"\n") {
                return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
        }
        return s
}

// ---- Backups ----

// RunBackup (settings.manage) — manual snapshot now (SQLite VACUUM INTO).
func (h *H) RunBackup(c *gin.Context) {
        p := h.principal(c)
        res, err := h.svc(c).BackupNow()
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "BACKUP_CREATED", "backup", res.File, fmt.Sprintf("%d bytes", res.Bytes))
        h.ok(c, res)
}

// ListBackups (settings.manage) — backup history, newest first.
func (h *H) ListBackups(c *gin.Context) {
        h.ok(c, h.svc(c).ListBackups())
}

// UpdateStatus (settings.manage) — cached release check state.
func (h *H) UpdateStatus(c *gin.Context) {
        h.ok(c, h.Updater.Status())
}

// UpdateRefresh (settings.manage) — force a live check now.
func (h *H) UpdateRefresh(c *gin.Context) {
        p := h.principal(c)
        h.Updater.Refresh(c.Request.Context())
        h.svc(c).Audit(p.ID, p.Username, "UPDATE_CHECKED", "system", "update", "")
        h.ok(c, h.Updater.Status())
}

// UpdateDownload (settings.manage) — stage the picked asset into temp.
func (h *H) UpdateDownload(c *gin.Context) {
        p := h.principal(c)
        staged, err := h.Updater.Download(c.Request.Context())
        if err != nil {
                h.fail(c, 502, err.Error())
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "UPDATE_DOWNLOADED", "system", "update", staged)
        h.ok(c, gin.H{"staged": staged})
}

// UpdateInstall (settings.manage) — Windows: launch the staged NSIS setup
// and quit so it can replace the install. Other platforms: 501 + URL.
//
// BEFORE the installer runs, the whole shop state is archived for rollback
// (UndoUpdate): every shop DB (VACUUM INTO snapshot), the tenants registry,
// the vault key, and on Windows the running binary. A failed/lost update
// never means lost work — "Undo update" restores data and the previous exe.
func (h *H) UpdateInstall(c *gin.Context) {
        p := h.principal(c)
        if runtime.GOOS != "windows" {
                h.fail(c, 501, "one-click install is Windows-only; download from the status URL")
                return
        }
        actor := "system"
        if p != nil {
                actor = p.Username
        }
        if err := h.ApplyStagedUpdate(actor); err != nil {
                code := 500
                if strings.Contains(err.Error(), "nothing staged") {
                        code = 409
                }
                h.fail(c, code, err.Error())
                return
        }
        h.ok(c, gin.H{"installing": true})
}

// svcDefault is the audit target for non-request work (the OTA engine).
func (h *H) svcDefault() *services.Service {
        if h.DefaultShop == "" || h.Shops == nil {
                return nil
        }
        svc, err := h.Shops.Service(h.DefaultShop)
        if err != nil {
                return nil
        }
        return svc
}

// ApplyStagedUpdate runs the full one-click install: pre-update backup,
// launch the staged installer, quit the app so it can replace the build.
// Shared by the manual endpoint and the auto-OTA tick (actor labels the
// audit rows).
//
// The installer runs SILENT (/S): the auto path fires with nobody at the
// keyboard, and an interactive wizard would leave the till dead. The NSIS
// script closes the RUNNING build first (graceful taskkill, bounded wait,
// force-kill backstop), replaces the exe, and relaunches the new build by
// itself in silent mode. When there is no quit hook (serve mode on a
// Windows appliance), auto-apply refuses above - the exe could never be
// replaced while this process holds it.
func (h *H) ApplyStagedUpdate(actor string) error {
        staged := h.Updater.Staged()
        if staged == "" {
                return errors.New("nothing staged, download first")
        }
        archive, err := h.PreUpdateSnapshot(h.Version)
        if err != nil {
                return errors.New("pre-update backup failed, update aborted so no work can be lost: " + err.Error())
        }
        if svc := h.svcDefault(); svc != nil {
                svc.Audit(0, actor, "UPDATE_PRE_BACKUP", "system", "update", archive)
        }
        if svc := h.svcDefault(); svc != nil {
                // Audit BEFORE launching: the installer's first job is closing
                // this running build (taskkill in the NSIS script) — anything
                // written after Start() may never land.
                svc.Audit(0, actor, "UPDATE_INSTALLED", "system", "update", staged)
        }
        if err := exec.Command(staged, "/S").Start(); err != nil {
                return err
        }
        if h.OnQuit != nil {
                go func() {
                        time.Sleep(300 * time.Millisecond) // let the response flush
                        h.OnQuit()
                }()
        }
        return nil
}

// AutoApplyTick is the OTA engine. Runs on a timer from main: when an
// update is available it downloads and stages the asset in the
// background, then installs at the first quiet moment — no payment in
// flight and nobody selling in the last 15 minutes, across every shop on
// this till. Windows-only (the one-click installer is the apply path);
// other platforms keep the manual flow. Errors are logged and retried on
// the next tick — a till that misses tonight catches the next window.
func (h *H) AutoApplyTick(ctx context.Context) {
        if runtime.GOOS != "windows" || h.Version == "dev" {
                return
        }
        // No quit hook (serve appliance): the running exe can never be replaced,
        // so unattended install is impossible - keep the manual flow only.
        if h.OnQuit == nil {
                return
        }
        // Unattended installs require the SHA-256-verified cloud manifest; the
        // GitHub fallback has no digest, so a tampered download must never be
        // applied by itself (the owner can still install it manually).
        if !h.Updater.HasVerifiedAsset() {
                return
        }
        st := h.Updater.Status()
        if !h.Updater.Available() && st["staged"] != true {
                return
        }
        if h.Updater.Staged() == "" {
                dlCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
                defer cancel()
                if _, err := h.Updater.Download(dlCtx); err != nil {
                        log.Printf("[ota] download failed, retried next tick: %v", err)
                        return
                }
                log.Printf("[ota] update staged, waiting for a quiet moment to install")
        }
        for _, shopID := range h.Shops.ShopIDs() {
                svc, err := h.Shops.Service(shopID)
                if err != nil {
                        return
                }
                if svc.HasUnfinishedWork() {
                        log.Printf("[ota] %s is busy, install postponed", shopID)
                        return
                }
        }
        log.Printf("[ota] till idle, applying staged update %s", h.Version)
        if err := h.ApplyStagedUpdate("auto-ota"); err != nil {
                log.Printf("[ota] apply failed, retried next tick: %v", err)
        }
}

// OffsiteStatus (settings.manage) — encrypted-upload worker state.
func (h *H) OffsiteStatus(c *gin.Context) {
        if h.Offsite == nil {
                h.ok(c, gin.H{"enabled": false, "pending": 0, "lastOk": "", "lastError": "", "lastAt": ""})
                return
        }
        enabled, pending, lastOk, lastErr, lastAt := h.Offsite.Status()
        h.ok(c, gin.H{"enabled": enabled, "pending": pending, "lastOk": lastOk, "lastError": lastErr, "lastAt": lastAt})
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
        h.svc(c).Audit(p.ID, p.Username, "APP_QUIT", "system", "desktop", "")
        h.ok(c, gin.H{"quitting": true})
        if h.OnQuit != nil {
                go func() {
                        time.Sleep(300 * time.Millisecond) // let the response flush
                        h.OnQuit()
                }()
        }
}
