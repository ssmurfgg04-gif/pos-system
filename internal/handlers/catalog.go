package handlers

import (
        "database/sql"
        "fmt"
        "strconv"
        "strings"

        "github.com/gin-gonic/gin"

        "posapp/internal/models"
)

// ---- Products ----

type productBody struct {
        SKU         string `json:"sku"`
        Barcode     string `json:"barcode"`
        Name        string `json:"name" binding:"required"`
        CategoryID  int64  `json:"categoryId" binding:"required"`
        PriceCents  int64  `json:"priceCents" binding:"min=0"`
        CostCents   int64  `json:"costCents" binding:"min=0"`
        StockQty    *int   `json:"stockQty"`
        TrackStock  *bool  `json:"trackStock"`
        Active      *bool  `json:"active"`
}

func (h *H) scanProducts(rows *sql.Rows) []models.Product {
        out := []models.Product{}
        for rows.Next() {
                var pr models.Product
                var track, active int
                var catName string
                if err := rows.Scan(&pr.ID, &pr.SKU, &pr.Barcode, &pr.Name, &pr.CategoryID, &catName,
                        &pr.PriceCents, &pr.CostCents, &pr.StockQty, &track, &active, &pr.UpdatedAt); err != nil {
                        break
                }
                pr.CategoryName = catName
                pr.TrackStock = track == 1
                pr.Active = active == 1
                out = append(out, pr)
        }
        return out
}

const productSelect = `
        SELECT p.id, COALESCE(p.sku,''), COALESCE(p.barcode,''), p.name, p.category_id, COALESCE(c.name,''),
                p.price_cents, p.cost_cents, p.stock_qty, COALESCE(p.track_stock,1), COALESCE(p.is_active,1), p.updated_at
        FROM products p LEFT JOIN categories c ON c.id = p.category_id`

// ListProducts (authed — POS needs it). Filters: category, search, active.
func (h *H) ListProducts(c *gin.Context) {
        q := productSelect + ` WHERE 1=1`
        var args []any
        if cat := c.Query("categoryId"); cat != "" {
                if id, err := strconv.ParseInt(cat, 10, 64); err == nil {
                        q += ` AND p.category_id = ?`
                        args = append(args, id)
                }
        }
        if search := strings.TrimSpace(c.Query("search")); search != "" {
                like := "%" + strings.ToLower(search) + "%"
                q += ` AND (LOWER(p.name) LIKE ? OR LOWER(COALESCE(p.sku,'')) LIKE ? OR LOWER(COALESCE(p.barcode,'')) LIKE ?)`
                args = append(args, like, like, like)
        }
        if active := c.Query("active"); active == "true" {
                q += ` AND p.is_active = 1`
        } else if active == "false" {
                q += ` AND p.is_active = 0`
        }
        q += ` ORDER BY p.name COLLATE NOCASE`
        // COLLATE NOCASE is SQLite-only — strip for Postgres.
        if !h.db(c).IsSQLite() {
                q = strings.ReplaceAll(q, " COLLATE NOCASE", "")
        }
        rows, err := h.db(c).Query(h.db(c).Rebind(q), args...)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        defer rows.Close()
        h.ok(c, h.scanProducts(rows))
}

// ListLowStock (products.view) — restock list.
func (h *H) ListLowStock(c *gin.Context) {
        threshold := h.settings(c).GetInt("low_stock_threshold", 5)
        rows, err := h.db(c).Query(h.db(c).Rebind(`
                SELECT p.id, COALESCE(p.sku,''), COALESCE(p.barcode,''), p.name, p.category_id, COALESCE(c.name,''),
                        p.price_cents, p.cost_cents, p.stock_qty, COALESCE(p.track_stock,1), COALESCE(p.is_active,1), p.updated_at
                FROM products p LEFT JOIN categories c ON c.id = p.category_id
                WHERE p.track_stock = 1 AND p.is_active = 1 AND p.stock_qty <= ?
                ORDER BY p.stock_qty ASC`), threshold)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        defer rows.Close()
        h.ok(c, h.scanProducts(rows))
}

// CreateProduct (products.manage).
func (h *H) CreateProduct(c *gin.Context) {
        p := h.principal(c)
        var body productBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        stock, track := defaultStock(body)
        active := 1
        if body.Active != nil && !*body.Active {
                active = 0
        }
        if err := models.CheckProductInput(body.Name, body.SKU, body.Barcode, body.PriceCents, body.CostCents, stock); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        res, err := h.db(c).Exec(h.db(c).Rebind(`
                INSERT INTO products (sku, barcode, name, category_id, price_cents, cost_cents, stock_qty, track_stock, is_active)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
                uniqueSKU(h.db(c), body.SKU), body.Barcode, body.Name, body.CategoryID, body.PriceCents, body.CostCents, stock, track, active)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        id, _ := res.LastInsertId()
        h.svc(c).Audit(p.ID, p.Username, "PRODUCT_CREATED", "product", body.Name, "")
        h.svc(c).EmitProduct(id, true)
        h.created(c, gin.H{"id": id})
}

func defaultStock(body productBody) (int, int) {
        stock := 0
        if body.StockQty != nil {
                stock = *body.StockQty
        }
        track := 1
        if body.TrackStock != nil && !*body.TrackStock {
                track = 0
        }
        return stock, track
}

// uniqueSKU generates a non-colliding SKU when empty.
func uniqueSKU(db interface{ QueryRow(string, ...any) *sql.Row }, sku string) string {
        if strings.TrimSpace(sku) != "" {
                return strings.TrimSpace(sku)
        }
        for i := 0; i < 50; i++ {
                candidate := fmt.Sprintf("SKU-%06d", 100000+i)
                var count int
                if err := db.QueryRow(`SELECT COUNT(*) FROM products WHERE sku = ?`, candidate).Scan(&count); err == nil && count == 0 {
                        return candidate
                }
        }
        return fmt.Sprintf("SKU-%d", 999999)
}

// UpdateProduct (products.manage).
func (h *H) UpdateProduct(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var body productBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        stock, track := defaultStock(body)
        active := 1
        if body.Active != nil && !*body.Active {
                active = 0
        }
        sku := strings.TrimSpace(body.SKU)
        if sku == "" {
                var existing string
                h.db(c).QueryRow(`SELECT COALESCE(sku,'') FROM products WHERE id = ?`, id).Scan(&existing)
                sku = existing
        }
        if err := models.CheckProductInput(body.Name, sku, body.Barcode, body.PriceCents, body.CostCents, stock); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        res, err := h.db(c).Exec(h.db(c).Rebind(`
                UPDATE products SET sku = ?, barcode = ?, name = ?, category_id = ?, price_cents = ?, cost_cents = ?,
                        stock_qty = ?, track_stock = ?, is_active = ?, updated_at = CURRENT_TIMESTAMP
                WHERE id = ?`), sku, body.Barcode, body.Name, body.CategoryID, body.PriceCents, body.CostCents, stock, track, active, id)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        if n(res) != 1 {
                h.fail(c, 404, "product not found")
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "PRODUCT_UPDATED", "product", body.Name, "")
        h.svc(c).EmitProduct(id, true)
        h.ok(c, gin.H{"updated": true})
}

// AdjustStock (products.manage) — quick +/- without full edit.
func (h *H) AdjustStock(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var body struct {
                Delta int `json:"delta" binding:"required"`
        }
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "delta required")
                return
        }
        if body.Delta < -models.MaxStockDelta || body.Delta > models.MaxStockDelta {
                h.fail(c, 400, fmt.Sprintf("delta out of range (max %d)", models.MaxStockDelta))
                return
        }
        res, err := h.db(c).Exec(h.db(c).Rebind(
                `UPDATE products SET stock_qty = MAX(0, stock_qty + ?), updated_at = CURRENT_TIMESTAMP WHERE id = ?`),
                body.Delta, id)
        if err != nil || n(res) != 1 {
                h.fail(c, 404, "product not found")
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "STOCK_ADJUSTED", "product", itoa64(id), strconv.Itoa(body.Delta))
        var sku string
        h.db(c).QueryRow(`SELECT COALESCE(sku,'') FROM products WHERE id = ?`, id).Scan(&sku)
        h.svc(c).EmitStockDelta(sku, body.Delta)
        h.ok(c, gin.H{"adjusted": true})
}

// DeactivateProduct soft-deletes (order history references products).
func (h *H) DeactivateProduct(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        res, err := h.db(c).Exec(h.db(c).Rebind(`UPDATE products SET is_active = 0, updated_at = CURRENT_TIMESTAMP WHERE id = ?`), id)
        if err != nil || n(res) != 1 {
                h.fail(c, 404, "product not found")
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "PRODUCT_DEACTIVATED", "product", itoa64(id), "")
        h.svc(c).EmitProduct(id, false)
        h.ok(c, gin.H{"deactivated": true})
}

// ---- Categories ----

type categoryBody struct {
        Name string `json:"name" binding:"required"`
        Slug string `json:"slug"`
}

func slugify(s string) string {
        s = strings.ToLower(strings.TrimSpace(s))
        s = strings.ReplaceAll(s, "&", "and")
        var b strings.Builder
        lastDash := false
        for _, r := range s {
                switch {
                case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
                        b.WriteRune(r)
                        lastDash = false
                default:
                        if !lastDash {
                                b.WriteRune('-')
                                lastDash = true
                        }
                }
        }
        out := strings.Trim(b.String(), "-")
        if out == "" {
                out = fmt.Sprintf("cat-%d", 999)
        }
        return out
}

// ListCategories (authed).
func (h *H) ListCategories(c *gin.Context) {
        rows, err := h.db(c).Query(`
                SELECT c.id, c.name, c.slug, (SELECT COUNT(*) FROM products p WHERE p.category_id = c.id AND p.is_active = 1)
                FROM categories c ORDER BY c.sort_order, c.name`)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        defer rows.Close()
        out := []map[string]any{}
        for rows.Next() {
                var id, count int64
                var name, slug string
                if err := rows.Scan(&id, &name, &slug, &count); err != nil {
                        break
                }
                out = append(out, map[string]any{"id": id, "name": name, "slug": slug, "productCount": count})
        }
        h.ok(c, out)
}

// CreateCategory (products.manage).
func (h *H) CreateCategory(c *gin.Context) {
        p := h.principal(c)
        var body categoryBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "name required")
                return
        }
        slug := body.Slug
        if slug == "" {
                slug = slugify(body.Name)
        }
        res, err := h.db(c).Exec(h.db(c).Rebind(
                `INSERT INTO categories (name, slug, sort_order) VALUES (?, ?, (SELECT COALESCE(MAX(sort_order),0)+1 FROM categories))`),
                body.Name, slug)
        if err != nil {
                h.fail(c, 409, "category name/slug already exists")
                return
        }
        id, _ := res.LastInsertId()
        h.svc(c).Audit(p.ID, p.Username, "CATEGORY_CREATED", "category", body.Name, "")
        h.svc(c).EmitCategory(body.Name, slug, false)
        h.created(c, gin.H{"id": id})
}

// UpdateCategory (products.manage).
func (h *H) UpdateCategory(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var body categoryBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "name required")
                return
        }
        res, err := h.db(c).Exec(h.db(c).Rebind(`UPDATE categories SET name = ?, slug = ? WHERE id = ?`),
                body.Name, slugify(body.Name), id)
        if err != nil || n(res) != 1 {
                h.fail(c, 404, "category not found")
                return
        }
        h.svc(c).Audit(p.ID, p.Username, "CATEGORY_UPDATED", "category", body.Name, "")
        h.svc(c).EmitCategory(body.Name, slugify(body.Name), false)
        h.ok(c, gin.H{"updated": true})
}

// DeleteCategory (products.manage) — blocked while products reference it.
func (h *H) DeleteCategory(c *gin.Context) {
        p := h.principal(c)
        id, ok := h.pathID(c, "id")
        if !ok {
                return
        }
        var count int
        if err := h.db(c).QueryRow(`SELECT COUNT(*) FROM products WHERE category_id = ?`, id).Scan(&count); err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        if count > 0 {
                h.fail(c, 409, "category still has products")
                return
        }
        if _, err := h.db(c).Exec(`DELETE FROM categories WHERE id = ?`, id); err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        var slug string
        h.db(c).QueryRow(`SELECT COALESCE(slug,'') FROM categories WHERE id = ?`, id).Scan(&slug)
        h.svc(c).Audit(p.ID, p.Username, "CATEGORY_DELETED", "category", itoa64(id), "")
        h.svc(c).EmitCategory("", slug, true)
        h.ok(c, gin.H{"deleted": true})
}
