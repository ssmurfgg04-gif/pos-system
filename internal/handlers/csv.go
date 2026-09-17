package handlers

import (
        "database/sql"
        "fmt"
        "net/http"
        "strings"

        "github.com/gin-gonic/gin"

        "posapp/internal/models"
)

// csvHeader is the canonical import/export column order.
var csvHeader = []string{"sku", "barcode", "name", "category", "price", "cost", "stock", "track_stock", "active"}

// ProductsExport streams the catalog as CSV (products.manage).
func (h *H) ProductsExport(c *gin.Context) {
        rows, err := h.db(c).Query(h.db(c).Rebind(`
                SELECT COALESCE(p.sku,''), COALESCE(p.barcode,''), p.name, COALESCE(c.name,''),
                        p.price_cents, p.cost_cents, p.stock_qty, COALESCE(p.track_stock,1), COALESCE(p.is_active,1)
                FROM products p LEFT JOIN categories c ON c.id = p.category_id
                ORDER BY p.id`))
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        defer rows.Close()
        var b strings.Builder
        b.WriteString(strings.Join(csvHeader, ",") + "\n")
        for rows.Next() {
                var sku, barcode, name, cat string
                var price, cost int64
                var stock, track, active int
                if err := rows.Scan(&sku, &barcode, &name, &cat, &price, &cost, &stock, &track, &active); err != nil {
                        break
                }
                b.WriteString(fmt.Sprintf("%s,%s,%s,%s,%.2f,%.2f,%d,%s,%s\n",
                        csv(sku), csv(barcode), csv(name), csv(cat), float64(price)/100, float64(cost)/100,
                        stock, boolCSV(track == 1), boolCSV(active == 1)))
        }
        c.Header("Content-Type", "text/csv; charset=utf-8")
        c.Header("Content-Disposition", `attachment; filename="products.csv"`)
        c.String(http.StatusOK, b.String())
}

// csv quotes a field when needed and neutralizes spreadsheet formula
// injection: cells starting with = + - @ get a leading single quote so
// Excel/LibreOffice open them as text, never as formulas.
func csv(s string) string {
        if s != "" && strings.ContainsRune("=+-@", rune(s[0])) {
                s = "'" + s
        }
        if strings.ContainsAny(s, ",\"\n") {
                return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
        }
        return s
}

func boolCSV(b bool) string {
        if b {
                return "true"
        }
        return "false"
}

// ProductsTemplate streams a one-row sample for importers (products.manage).
func (h *H) ProductsTemplate(c *gin.Context) {
        c.Header("Content-Type", "text/csv; charset=utf-8")
        c.Header("Content-Disposition", `attachment; filename="products-template.csv"`)
        c.String(http.StatusOK, strings.Join(csvHeader, ",")+"\nTS-101,4001234500103,Sample T-Shirt,T-Shirts,550.00,320.00,40,true,true\n")
}

// ProductsImport upserts by SKU inside ONE transaction (all-or-nothing —
// pattern taken from OSPOS's importer, minus its flaws). Columns:
// sku,barcode,name,category,price,cost,stock,track_stock,active
func (h *H) ProductsImport(c *gin.Context) {
        p := h.principal(c)
        file, _, err := c.Request.FormFile("file")
        if err != nil {
                h.fail(c, 400, "CSV file required (multipart field 'file')")
                return
        }
        defer file.Close()
        records, err := readCSV(file, 2000)
        if err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        if len(records) == 0 {
                h.fail(c, 400, "empty CSV")
                return
        }
        // Validate the header.
        header := records[0]
        col := map[string]int{}
        for i, name := range header {
                col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "\ufeff")))] = i
        }
        for _, want := range csvHeader {
                if _, ok := col[want]; !ok {
                        h.fail(c, 400, "missing column "+want+" (use the template endpoint)")
                        return
                }
        }

        type row struct {
                sku, barcode, name, category string
                price, cost                  int64
                stock                        int
                track, active                int
        }
        var rows []row
        for i, r := range records[1:] {
                get := func(key string) string {
                        idx := col[key]
                        if idx < len(r) {
                                return strings.TrimSpace(r[idx])
                        }
                        return ""
                }
                name := get("name")
                if name == "" {
                        h.fail(c, 400, fmt.Sprintf("row %d: name is required", i+2))
                        return
                }
                price, err := parseMoneyToCents(get("price"))
                if err != nil {
                        h.fail(c, 400, fmt.Sprintf("row %d: bad price %q", i+2, get("price")))
                        return
                }
                cost, err := parseMoneyToCents(get("cost"))
                if err != nil {
                        h.fail(c, 400, fmt.Sprintf("row %d: bad cost %q", i+2, get("cost")))
                        return
                }
                stock := 0
                fmt.Sscanf(get("stock"), "%d", &stock)
                if err := models.CheckProductInput(name, get("sku"), get("barcode"), price, cost, stock); err != nil {
                        h.fail(c, 400, fmt.Sprintf("row %d: %s", i+2, err.Error()))
                        return
                }
                rows = append(rows, row{
                        sku: get("sku"), barcode: get("barcode"), name: name, category: get("category"),
                        price: price, cost: cost, stock: stock,
                        track: parseBoolCSV(get("track_stock")), active: parseBoolCSV(get("active")),
                })
        }

        // Resolve categories once (name → id), creating missing ones.
        tx, err := h.db(c).Begin()
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        defer tx.Rollback()
        catIDs := map[string]int64{}
        for _, r := range rows {
                if r.category == "" {
                        continue
                }
                if _, done := catIDs[strings.ToLower(r.category)]; done {
                        continue
                }
                var id int64
                err := tx.QueryRow(h.db(c).Rebind(`SELECT id FROM categories WHERE LOWER(name) = LOWER(?)`), r.category).Scan(&id)
                if err != nil {
                        res, err := tx.Exec(h.db(c).Rebind(
                                `INSERT INTO categories (name, slug, sort_order) VALUES (?, ?, (SELECT COALESCE(MAX(sort_order),0)+1 FROM categories))`),
                                r.category, slugify(r.category))
                        if err != nil {
                                h.fail(c, 500, err.Error())
                                return
                        }
                        id, _ = res.LastInsertId()
                }
                catIDs[strings.ToLower(r.category)] = id
        }
        var catID int64
        _ = tx.QueryRow(`SELECT id FROM categories ORDER BY id LIMIT 1`).Scan(&catID) // fallback bucket

        created, updated := 0, 0
        for _, r := range rows {
                cid := catID
                if id, ok := catIDs[strings.ToLower(r.category)]; ok && r.category != "" {
                        cid = id
                }
                if r.sku != "" {
                        var exists int
                        if err := tx.QueryRow(`SELECT COUNT(*) FROM products WHERE sku = ?`, r.sku).Scan(&exists); err == nil && exists > 0 {
                                if _, err := tx.Exec(h.db(c).Rebind(`
                                        UPDATE products SET barcode = ?, name = ?, category_id = ?, price_cents = ?, cost_cents = ?,
                                                stock_qty = ?, track_stock = ?, is_active = ?, updated_at = CURRENT_TIMESTAMP
                                        WHERE sku = ?`), r.barcode, r.name, cid, r.price, r.cost, r.stock, r.track, r.active, r.sku); err != nil {
                                        h.fail(c, 500, fmt.Sprintf("row sku %s: %v", r.sku, err))
                                        return
                                }
                                updated++
                                continue
                        }
                }
                sku := r.sku
                if sku == "" {
                        sku = uniqueSKUTx(tx, r.name)
                }
                if _, err := tx.Exec(h.db(c).Rebind(`
                        INSERT INTO products (sku, barcode, name, category_id, price_cents, cost_cents, stock_qty, track_stock, is_active)
                        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`), sku, r.barcode, r.name, cid, r.price, r.cost, r.stock, r.track, r.active); err != nil {
                        h.fail(c, 500, fmt.Sprintf("row %s: %v", r.name, err))
                        return
                }
                created++
        }
        if err := tx.Commit(); err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "PRODUCTS_IMPORTED", "product", "", fmt.Sprintf("created %d, updated %d", created, updated))
        h.ok(c, gin.H{"created": created, "updated": updated})
}

func uniqueSKUTx(tx *sql.Tx, name string) string {
        // Fallback SKU from name slug + counter.
        base := slugify(name)
        if len(base) > 12 {
                base = base[:12]
        }
        for i := 1; i < 100; i++ {
                candidate := fmt.Sprintf("%s-%02d", base, i)
                var count int
                if err := tx.QueryRow(`SELECT COUNT(*) FROM products WHERE sku = ?`, candidate).Scan(&count); err == nil && count == 0 {
                        return candidate
                }
        }
        return fmt.Sprintf("sku-%d", 99999)
}

func parseBoolCSV(s string) int {
        switch strings.ToLower(strings.TrimSpace(s)) {
        case "true", "1", "yes", "y":
                return 1
        }
        return 0
}

// parseMoneyToCents accepts "550", "550.00", "1,550.50".
func parseMoneyToCents(s string) (int64, error) {
        s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
        if s == "" {
                return 0, nil
        }
        negative := strings.HasPrefix(s, "-")
        if negative {
                s = s[1:]
        }
        var whole, frac string
        if idx := strings.Index(s, "."); idx >= 0 {
                whole, frac = s[:idx], s[idx+1:]
                if len(frac) > 2 {
                        frac = frac[:2]
                }
                for len(frac) < 2 {
                        frac += "0"
                }
        } else {
                whole, frac = s, "00"
        }
        var w, f int64
        if whole != "" {
                if _, err := fmt.Sscanf(whole, "%d", &w); err != nil {
                        return 0, fmt.Errorf("bad number %q", s)
                }
        }
        if frac != "" {
                fmt.Sscanf(frac, "%d", &f)
        }
        cents := w*100 + f
        if negative {
                return 0, fmt.Errorf("negative amounts not supported")
        }
        return cents, nil
}

// readCSV parses comma-separated CSV with quote support (stdlib-lean).
func readCSV(r interface{ Read([]byte) (int, error) }, maxRows int) ([][]string, error) {
        data := make([]byte, 0, 64*1024)
        buf := make([]byte, 8192)
        for {
                n, err := r.Read(buf)
                if n > 0 {
                        data = append(data, buf[:n]...)
                }
                if err != nil {
                        break
                }
                if len(data) > 4*1024*1024 {
                        return nil, fmt.Errorf("file too large")
                }
        }
        var records [][]string
        var record []string
        var field strings.Builder
        inQuotes := false
        lines := 0
        flushField := func() {
                record = append(record, field.String())
                field.Reset()
        }
        flushRecord := func() {
                flushField()
                if len(record) > 1 || record[0] != "" {
                        records = append(records, record)
                        lines++
                }
                record = nil
        }
        for i := 0; i < len(data); i++ {
                ch := data[i]
                switch {
                case inQuotes:
                        if ch == '"' {
                                if i+1 < len(data) && data[i+1] == '"' {
                                        field.WriteByte('"')
                                        i++
                                } else {
                                        inQuotes = false
                                }
                        } else {
                                field.WriteByte(ch)
                        }
                case ch == '"':
                        inQuotes = true
                case ch == ',':
                        flushField()
                case ch == '\r':
                        // skip
                case ch == '\n':
                        flushRecord()
                        if lines > maxRows {
                                return nil, fmt.Errorf("too many rows (max %d)", maxRows)
                        }
                default:
                        field.WriteByte(ch)
                }
        }
        if field.Len() > 0 || len(record) > 0 {
                flushRecord()
        }
        return records, nil
}
