package printer

import (
        "bytes"
        "context"
        "database/sql"
        "fmt"
        "image"
        _ "image/jpeg"
        _ "image/png"
        "io"
        "log"
        "os"
        "time"

        "posapp/internal/database"
        "posapp/internal/escpos"
        "posapp/internal/models"
        "posapp/internal/settings"
)

// logoFileName mirrors the upload handler's stored file (brand logo).
const logoFileName = "brand-logo.png"

// loadLogo decodes the brand logo scaled to dots wide (nil on any failure —
// receipts must print with or without a logo).
func loadLogo(dots int) image.Image {
        f, err := os.Open(logoFileName)
        if err != nil {
                return nil
        }
        defer f.Close()
        src, _, err := image.Decode(f)
        if err != nil {
                return nil
        }
        return scaleToWidth(src, dots)
}

// scaleToWidth nearest-neighbor scales src to width dots, keeping aspect
// (height capped so a tall logo can't waterfall paper).
func scaleToWidth(src image.Image, width int) image.Image {
        b := src.Bounds()
        sw, sh := b.Dx(), b.Dy()
        if sw <= 0 || sh <= 0 {
                return nil
        }
        dh := sh * width / sw
        if dh > 256 {
                dh = 256
        }
        if dh <= 0 {
                dh = 1
        }
        dst := image.NewRGBA(image.Rect(0, 0, width, dh))
        for y := 0; y < dh; y++ {
                for x := 0; x < width; x++ {
                        dst.Set(x, y, src.At(b.Min.X+x*sw/width, b.Min.Y+y*sh/dh))
                }
        }
        return dst
}

// Worker drains a persistent print_jobs queue. Jobs survive reboots: on
// startup anything stuck "printing" is re-queued (power-loss = reprint).
// Failed jobs retry up to 5 attempts with linear backoff, then park as
// "failed" for admin inspection.
type Worker struct {
        db       *database.DB
        settings *settings.Store
        interval time.Duration
}

func NewWorker(db *database.DB, s *settings.Store) *Worker {
        return &Worker{db: db, settings: s, interval: 2 * time.Second}
}

// Recover resets jobs interrupted by a crash (status=printing at boot).
func (w *Worker) Recover() error {
        _, err := w.db.Exec(`UPDATE print_jobs SET status = 'queued' WHERE status = 'printing'`)
        return err
}

// Enqueue creates a print job for the order when a printer is configured
// and auto-print is on. Returns the job id (0 = skipped).
func (w *Worker) Enqueue(order *models.Order) (int64, error) {
        target := w.settings.Get("printer_target")
        if !Target(target).Enabled() || !w.settings.GetBool("auto_print_receipts", true) {
                return 0, nil
        }
        if !Target(target).Valid() {
                return 0, fmt.Errorf("invalid printer target %q", target)
        }
        res, err := w.db.Exec(w.db.Rebind(`INSERT INTO print_jobs (order_id, target) VALUES (?, ?)`), order.ID, target)
        if err != nil {
                return 0, err
        }
        return res.LastInsertId()
}

// Run polls the queue until the context is cancelled.
func (w *Worker) Run(ctx context.Context) {
        t := time.NewTicker(w.interval)
        defer t.Stop()
        for {
                select {
                case <-ctx.Done():
                        return
                case <-t.C:
                        w.tick(ctx)
                }
        }
}

// tick claims one queued job and prints it.
func (w *Worker) tick(ctx context.Context) {
        tx, err := w.db.Begin()
        if err != nil {
                return
        }
        // Claim: oldest queued job → printing (guarded update = single consumer).
        res, err := tx.Exec(`UPDATE print_jobs SET status = 'printing'
                WHERE id = (SELECT id FROM print_jobs WHERE status = 'queued' ORDER BY id LIMIT 1) AND status = 'queued'`)
        if err != nil {
                tx.Rollback()
                return
        }
        n, _ := res.RowsAffected()
        if n == 0 {
                tx.Rollback()
                return
        }
        var jobID int64
        if err := tx.QueryRow(`SELECT id FROM print_jobs WHERE status = 'printing' ORDER BY id LIMIT 1`).Scan(&jobID); err != nil {
                tx.Rollback()
                return
        }
        if err := tx.Commit(); err != nil {
                return
        }
        w.printJob(ctx, jobID)
}

func (w *Worker) printJob(ctx context.Context, jobID int64) {
        job, order, ok := w.loadJob(jobID)
        if !ok {
                return
        }
        data := BuildReceiptData(order,
                w.settings.GetString("store_name", "My Store"),
                w.settings.Get("store_address"),
                w.settings.Get("store_phone"),
                w.settings.Get("receipt_footer"),
                w.settings.GetString("currency_symbol", "KES"))
        width := w.settings.GetInt("printer_width", 80)
        cols := 48
        if width <= 58 {
                cols = 32
        }
        if w.settings.GetBool("receipt_logo", true) {
                dots := 576
                if cols <= 32 {
                        dots = 384
                }
                data.Logo = loadLogo(dots)
        }
        raw, err := Render(data, cols)
        if err == nil {
                var wc io.WriteCloser
                wc, err = Target(job.target).Open()
                if err == nil {
                        _, err = wc.Write(raw)
                        if cerr := wc.Close(); err == nil {
                                err = cerr
                        }
                }
        }
        if err == nil {
                w.db.Exec(w.db.Rebind(`UPDATE print_jobs SET status = 'printed', printed_at = ?, last_error = '' WHERE id = ?`),
                        time.Now().UTC().Format(time.RFC3339), jobID)
                return
        }
        // Failure path: retry with backoff, park after 5 attempts.
        attempts := job.attempts + 1
        status := models.PrintQueued
        if attempts >= 5 {
                status = models.PrintFailed
        }
        w.db.Exec(w.db.Rebind(`UPDATE print_jobs SET status = ?, attempts = ?, last_error = ? WHERE id = ?`),
                status, attempts, truncateErr(err.Error()), jobID)
        log.Printf("[printer] job %d attempt %d failed: %v", jobID, attempts, err)
}

type jobRow struct {
        id       int64
        target   string
        attempts int
}

func (w *Worker) loadJob(jobID int64) (jobRow, *models.Order, bool) {
        var j jobRow
        err := w.db.QueryRow(`SELECT id, COALESCE(target,''), attempts FROM print_jobs WHERE id = ?`, jobID).
                Scan(&j.id, &j.target, &j.attempts)
        if err != nil {
                return j, nil, false
        }
        // Reuse the services loader without an import cycle: minimal inline scan.
        var orderID int64
        if err := w.db.QueryRow(`SELECT order_id FROM print_jobs WHERE id = ?`, jobID).Scan(&orderID); err != nil {
                return j, nil, false
        }
        order, err := w.loadOrder(orderID)
        if err != nil {
                return j, nil, false
        }
        return j, order, true
}

// loadOrder is a self-contained order loader (order + items + payments)
// using strictly sequential queries — never nested while rows are open.
func (w *Worker) loadOrder(orderID int64) (*models.Order, error) {
        var o models.Order
        var paidAt, voidedAt sql.NullString
        err := w.db.QueryRow(w.db.Rebind(`
                SELECT o.id, o.number, o.status, o.subtotal_cents, o.tax_cents, o.total_cents,
                        COALESCE(u.full_name, u.username, ''), COALESCE(o.customer_name,''), COALESCE(o.note,''),
                        COALESCE(o.discrepancy,0), o.created_at, o.paid_at, o.voided_at, COALESCE(o.void_reason,'')
                FROM orders o LEFT JOIN users u ON u.id = o.cashier_id
                WHERE o.id = ?`), orderID).
                Scan(&o.ID, &o.Number, &o.Status, &o.SubtotalCents, &o.TaxCents, &o.TotalCents,
                        &o.CashierName, &o.CustomerName, &o.Note, &o.Discrepancy, &o.CreatedAt, &paidAt, &voidedAt, &o.VoidReason)
        if err != nil {
                return nil, err
        }
        o.PaidAt, o.VoidedAt = paidAt.String, voidedAt.String

        itemRows, err := w.db.Query(w.db.Rebind(`SELECT id, product_id, name, COALESCE(sku,''), qty, unit_price_cents, line_total_cents
                FROM order_items WHERE order_id = ? ORDER BY id`), orderID)
        if err != nil {
                return nil, err
        }
        for itemRows.Next() {
                var it models.OrderItem
                if err := itemRows.Scan(&it.ID, &it.ProductID, &it.Name, &it.SKU, &it.Qty, &it.UnitPriceCents, &it.LineTotalCents); err != nil {
                        itemRows.Close()
                        return nil, err
                }
                o.Items = append(o.Items, it)
        }
        itemRows.Close()
        if err := itemRows.Err(); err != nil {
                return nil, err
        }

        payRows, err := w.db.Query(w.db.Rebind(`SELECT id, order_id, method, COALESCE(mode,''), amount_cents, status,
                COALESCE(phone,''), COALESCE(mpesa_receipt,''), COALESCE(result_desc,''), COALESCE(discrepancy,0),
                created_at, COALESCE(completed_at,'')
                FROM payments WHERE order_id = ? ORDER BY id`), orderID)
        if err != nil {
                return nil, err
        }
        for payRows.Next() {
                var pm models.Payment
                if err := payRows.Scan(&pm.ID, &pm.OrderID, &pm.Method, &pm.Mode, &pm.AmountCents, &pm.Status,
                        &pm.Phone, &pm.MpesaReceipt, &pm.ResultDesc, &pm.Discrepancy, &pm.CreatedAt, &pm.CompletedAt); err != nil {
                        payRows.Close()
                        return nil, err
                }
                o.Payments = append(o.Payments, pm)
        }
        payRows.Close()
        if err := payRows.Err(); err != nil {
                return nil, err
        }
        return &o, nil
}

// TestPrint renders a minimal receipt to the configured target.
func (w *Worker) TestPrint() error {
        target := w.settings.Get("printer_target")
        t := Target(target)
        if !t.Enabled() {
                return fmt.Errorf("no printer target configured")
        }
        wc, err := t.Open()
        if err != nil {
                return err
        }
        defer wc.Close()
        items := []models.OrderItem{{ID: 1, ProductID: 0, Name: "Test item", Qty: 1, UnitPriceCents: 100, LineTotalCents: 100}}
        var logo image.Image
        if w.settings.GetBool("receipt_logo", true) {
                logo = loadLogo(576)
        }
        data := ReceiptData{
                StoreName: w.settings.GetString("store_name", "My Store"),
                Logo:      logo,
                Footer:    w.settings.Get("receipt_footer"),
                OrderNumber: "TEST",
                When:       time.Now().Format("2006-01-02 15:04"),
                Items:      items, SubtotalCents: 100, TaxCents: 0, TotalCents: 100,
                Currency:  w.settings.GetString("currency_symbol", "KES"),
                PayLines:  []string{"Printer test page"},
        }
        cols := 48
        if w.settings.GetInt("printer_width", 80) <= 58 {
                cols = 32
        }
        raw, err := Render(data, cols)
        if err != nil {
                return err
        }
        _, err = wc.Write(raw)
        return err
}

// Kick pulses the cash drawer on the configured target (pin 0, 50ms on,
// 500ms off). No job row — fire-and-forget like a test page.
func (w *Worker) Kick() error {
        target := w.settings.Get("printer_target")
        t := Target(target)
        if !t.Enabled() {
                return fmt.Errorf("no printer target configured")
        }
        wc, err := t.Open()
        if err != nil {
                return err
        }
        defer wc.Close()
        var buf bytes.Buffer
        e := escpos.New(&buf)
        if _, err := e.KickDrawer(0, 25, 250); err != nil {
                return err
        }
        if err := e.Print(); err != nil {
                return err
        }
        _, err = wc.Write(buf.Bytes())
        return err
}

// ListJobs returns recent print jobs (admin diagnostics).
func (w *Worker) ListJobs(limit int) []models.PrintJob {
        rows, err := w.db.Query(w.db.Rebind(`
                SELECT p.id, p.order_id, COALESCE(o.number,''), p.status, COALESCE(p.target,''), p.attempts,
                        COALESCE(p.last_error,''), p.created_at, COALESCE(p.printed_at,'')
                FROM print_jobs p LEFT JOIN orders o ON o.id = p.order_id
                ORDER BY p.id DESC LIMIT ?`), limit)
        if err != nil {
                return nil
        }
        defer rows.Close()
        var out []models.PrintJob
        for rows.Next() {
                var j models.PrintJob
                if err := rows.Scan(&j.ID, &j.OrderID, &j.OrderNumber, &j.Status, &j.Target, &j.Attempts, &j.LastError, &j.CreatedAt, &j.PrintedAt); err != nil {
                        return out
                }
                out = append(out, j)
        }
        return out
}

func truncateErr(s string) string {
        if len(s) > 300 {
                return s[:300]
        }
        return s
}
