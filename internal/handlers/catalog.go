package handlers

import (
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"posapp/internal/models"
	"posapp/internal/services"
)

// ---- Products ----

type productBody struct {
	SKU           string `json:"sku"`
	Barcode       string `json:"barcode"`
	Name          string `json:"name" binding:"required"`
	CategoryID    int64  `json:"categoryId"` // 0 = Uncategorised (resolved below)
	PriceCents    int64  `json:"priceCents" binding:"min=0"`
	CostCents     int64  `json:"costCents" binding:"min=0"`
	StockQtyMilli *int64 `json:"stockQtyMilli"`
	TrackStock    *bool  `json:"trackStock"`
	Active        *bool  `json:"active"`
	IsGiftCard    *bool  `json:"isGiftCard"`
	// v1.8.0: sellable unit ('pc','kg',…), whether the till accepts
	// fractional quantities, and batch/expiry tracking.
	Unit          string `json:"unit"`
	AllowFraction *bool  `json:"allowFraction"`
	TrackBatches  *bool  `json:"trackBatches"`
	// v1.6.3: wholesale tier price (0 = same as retail) + per-product
	// low-stock threshold (0 = use the global setting).
	WholesalePriceCents int64 `json:"wholesalePriceCents"`
	LowStockUnits       *int  `json:"lowStockUnits"`
	// v1.10 KRA eTIMS: tax type A-E ('' = shop default) and KRA item
	// class code ('' = shop default). nil = leave unchanged.
	EtimsTaxType *string `json:"etimsTaxType"`
	EtimsClsCd   *string `json:"etimsClsCd"`
}

// saveEtimsFields writes the optional eTIMS fields of a product body.
func (h *H) saveEtimsFields(c *gin.Context, id int64, body productBody) error {
	if body.EtimsTaxType != nil {
		t := strings.ToUpper(strings.TrimSpace(*body.EtimsTaxType))
		if t != "" && (len(t) != 1 || !strings.Contains("ABCDE", t)) {
			return fmt.Errorf("eTIMS tax type is one of A, B, C, D, E")
		}
		if _, err := h.db(c).Exec(h.db(c).Rebind(`UPDATE products SET etims_tax_type = ? WHERE id = ?`), t, id); err != nil {
			return err
		}
	}
	if body.EtimsClsCd != nil {
		cls := strings.TrimSpace(*body.EtimsClsCd)
		if cls != "" && !regexp.MustCompile(`^\d{1,10}$`).MatchString(cls) {
			return fmt.Errorf("KRA item class is up to 10 digits")
		}
		if _, err := h.db(c).Exec(h.db(c).Rebind(`UPDATE products SET etims_cls_cd = ? WHERE id = ?`), cls, id); err != nil {
			return err
		}
	}
	return nil
}

// ensureProductCategory maps a missing category to the shared "Uncategorised"
// bucket (find-or-create). A fresh shop has zero categories, and the schema is
// NOT NULL on products.category_id — without this, the very first product on a
// new till fails with a raw gin validator error.
func (h *H) ensureProductCategory(c *gin.Context, id int64) int64 {
	if id != 0 {
		return id
	}
	const name, slug = "Uncategorised", "uncategorised"
	var catID int64
	err := h.db(c).QueryRow(`SELECT id FROM categories WHERE slug = ?`, slug).Scan(&catID)
	if err == nil {
		return catID
	}
	res, err := h.db(c).Exec(h.db(c).Rebind(
		`INSERT INTO categories (name, slug, sort_order) VALUES (?, ?, (SELECT COALESCE(MAX(sort_order),0)+1 FROM categories))`),
		name, slug)
	if err != nil {
		// Lost a race with a sibling till — re-read.
		_ = h.db(c).QueryRow(`SELECT id FROM categories WHERE slug = ?`, slug).Scan(&catID)
		return catID
	}
	catID, _ = res.LastInsertId()
	h.svc(c).EmitCategory(name, slug, false)
	return catID
}

func (h *H) scanProducts(rows *sql.Rows) []models.Product {
	out := []models.Product{}
	for rows.Next() {
		var pr models.Product
		var track, active, gift, fraction, batches int
		var catName, unit string
		if err := rows.Scan(&pr.ID, &pr.SKU, &pr.Barcode, &pr.Name, &pr.CategoryID, &catName,
			&pr.PriceCents, &pr.CostCents, &pr.StockQtyMilli, &track, &active, &pr.UpdatedAt, &gift,
			&pr.WholesalePriceCents, &pr.LowStockUnits, &unit, &fraction, &batches, &pr.EtimsTaxType, &pr.EtimsClsCd); err != nil {
			break
		}
		pr.CategoryName = catName
		pr.TrackStock = track == 1
		pr.Active = active == 1
		pr.IsGiftCard = gift == 1
		pr.Unit = unit
		pr.AllowFraction = fraction == 1
		pr.TrackBatches = batches == 1
		out = append(out, pr)
	}
	return out
}

const productSelect = `
        SELECT p.id, COALESCE(p.sku,''), COALESCE(p.barcode,''), p.name, p.category_id, COALESCE(c.name,''),
                p.price_cents, p.cost_cents, p.stock_qty_milli, COALESCE(p.track_stock,1), COALESCE(p.is_active,1), p.updated_at, COALESCE(p.is_gift_card,0),
                COALESCE(p.wholesale_price_cents,0), COALESCE(p.low_stock_units,0), COALESCE(p.unit,'pc'), COALESCE(p.allow_fraction,0), COALESCE(p.track_batches,0),
                COALESCE(p.etims_tax_type,''), COALESCE(p.etims_cls_cd,'')
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

// ListLowStock (products.view) — restock list. Per-product thresholds
// (v1.6.3) win when set; the global setting is the fallback.
func (h *H) ListLowStock(c *gin.Context) {
	threshold := h.settings(c).GetInt("low_stock_threshold", 5)
	rows, err := h.db(c).Query(h.db(c).Rebind(`
                SELECT p.id, COALESCE(p.sku,''), COALESCE(p.barcode,''), p.name, p.category_id, COALESCE(c.name,''),
                        p.price_cents, p.cost_cents, p.stock_qty_milli, COALESCE(p.track_stock,1), COALESCE(p.is_active,1), p.updated_at, COALESCE(p.is_gift_card,0),
                        COALESCE(p.wholesale_price_cents,0), COALESCE(p.low_stock_units,0), COALESCE(p.unit,'pc'), COALESCE(p.allow_fraction,0), COALESCE(p.track_batches,0),
                COALESCE(p.etims_tax_type,''), COALESCE(p.etims_cls_cd,'')
                FROM products p LEFT JOIN categories c ON c.id = p.category_id
                WHERE p.track_stock = 1 AND p.is_active = 1
                  AND p.stock_qty_milli <= (CASE WHEN COALESCE(p.low_stock_units,0) > 0 THEN p.low_stock_units ELSE ? END) * 1000
                ORDER BY p.stock_qty_milli ASC`), threshold)
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
	gift := 0
	if body.IsGiftCard != nil && *body.IsGiftCard {
		gift = 1
		track = 0 // gift cards are value, not shelf stock
	}
	batches := 0
	if body.TrackBatches != nil && *body.TrackBatches {
		batches = 1
	}
	unit, fraction := normalizedUnit(body)
	sku := strings.TrimSpace(body.SKU)
	auto := sku == ""
	if auto {
		sku = services.AutoSKU()
	}
	if err := models.CheckProductInput(body.Name, sku, body.Barcode, body.PriceCents, body.CostCents, stock); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	// A SKU the user typed is checked up front so the shop gets a readable
	// 409 instead of a raw constraint dump (auto SKUs rely on the retry
	// below instead - the INSERT is the authoritative check for those).
	if !auto {
		var count int
		if err := h.db(c).QueryRow(`SELECT COUNT(*) FROM products WHERE sku = ?`, sku).Scan(&count); err == nil && count > 0 {
			h.fail(c, 409, "SKU already exists - change it, or leave it blank to auto-generate one")
			return
		}
	}
	var res sql.Result
	var err error
	for attempt := 0; ; attempt++ {
		res, err = h.db(c).Exec(h.db(c).Rebind(`
            INSERT INTO products (sku, barcode, name, category_id, price_cents, cost_cents, stock_qty_milli, stock_qty, track_stock, is_active, is_gift_card, wholesale_price_cents, low_stock_units, unit, allow_fraction, track_batches)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			sku, body.Barcode, body.Name, h.ensureProductCategory(c, body.CategoryID), body.PriceCents, body.CostCents, stock, stock/1000, track, active, gift, body.WholesalePriceCents, lowStockUnits(body), unit, fraction, batches)
		if err == nil {
			break
		}
		// A generated SKU can still lose a race (two clerks creating at the
		// same instant, or a sibling till syncing in between). Regenerate
		// and retry rather than surfacing the raw constraint error.
		if isDuplicateSKUErr(err) {
			if auto && attempt < 3 {
				sku = services.AutoSKU()
				continue
			}
			h.fail(c, 409, "SKU already exists - change it, or leave it blank to auto-generate one")
			return
		}
		h.fail(c, 500, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	if err := h.saveEtimsFields(c, id, body); err != nil {
		h.fail(c, 422, err.Error())
		return
	}
	h.svc(c).Audit(p.ID, p.Username, "PRODUCT_CREATED", "product", body.Name, "")
	if batches == 1 {
		if err := h.svc(c).EnableBatchesIfNeeded(id); err != nil {
			h.fail(c, 500, err.Error())
			return
		}
	}
	h.svc(c).EmitProduct(id, true)
	h.created(c, gin.H{"id": id, "sku": sku})
}

// isDuplicateSKUErr reports whether err is the storage engine's UNIQUE
// violation on products.sku (SQLite 2067 / Postgres 23505).
func isDuplicateSKUErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if !strings.Contains(msg, "products.sku") {
		return false
	}
	return strings.Contains(strings.ToLower(msg), "unique") ||
		strings.Contains(strings.ToLower(msg), "duplicate") ||
		strings.Contains(msg, "23505")
}

func defaultStock(body productBody) (int64, int) {
	stock := int64(0)
	if body.StockQtyMilli != nil {
		stock = *body.StockQtyMilli
	}
	track := 1
	if body.TrackStock != nil && !*body.TrackStock {
		track = 0
	}
	return stock, track
}

// normalizedUnit validates + normalises the unit / fraction pair: an empty
// unit means 'pc'; a weigh unit defaults allow_fraction on unless the
// client explicitly turned it off.
func normalizedUnit(body productBody) (string, int) {
	unit := strings.TrimSpace(body.Unit)
	if !models.ValidUnit(unit) {
		unit = "pc"
	}
	if unit == "" {
		unit = "pc"
	}
	fraction := 0
	if body.AllowFraction != nil {
		if *body.AllowFraction {
			fraction = 1
		}
	} else if models.UnitFractionDefault(unit) {
		fraction = 1
	}
	return unit, fraction
}

// lowStockUnits normalises the optional per-product threshold.
func lowStockUnits(body productBody) int {
	if body.LowStockUnits == nil || *body.LowStockUnits < 0 {
		return 0
	}
	return *body.LowStockUnits
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
	gift := 0
	if body.IsGiftCard != nil && *body.IsGiftCard {
		gift = 1
		track = 0
	}
	batches := 0
	if body.TrackBatches != nil && *body.TrackBatches {
		batches = 1
	}
	unit, fraction := normalizedUnit(body)
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
	// Renaming onto another product's SKU would corrupt the cross-till
	// identity (team sync keys catalog rows on sku) - block it readably.
	var dupID int64
	if err := h.db(c).QueryRow(`SELECT id FROM products WHERE sku = ? AND id <> ?`, sku, id).Scan(&dupID); err == nil {
		h.fail(c, 409, "SKU already exists on another product")
		return
	}
	res, err := h.db(c).Exec(h.db(c).Rebind(`
                UPDATE products SET sku = ?, barcode = ?, name = ?, category_id = ?, price_cents = ?, cost_cents = ?,
                        stock_qty_milli = ?, stock_qty = ? / 1000, track_stock = ?, is_active = ?, is_gift_card = ?, wholesale_price_cents = ?, low_stock_units = ?, updated_at = ?,
                        unit = ?, allow_fraction = ?, track_batches = ?
                WHERE id = ?`), sku, body.Barcode, body.Name, h.ensureProductCategory(c, body.CategoryID), body.PriceCents, body.CostCents, stock, stock/1000, track, active, gift, body.WholesalePriceCents, lowStockUnits(body), services.NowStamp(), unit, fraction, batches, id)
	if err != nil {
		if isDuplicateSKUErr(err) {
			h.fail(c, 409, "SKU already exists on another product")
		} else {
			h.fail(c, 500, err.Error())
		}
		return
	}
	if n(res) != 1 {
		h.fail(c, 404, "product not found")
		return
	}
	if err := h.saveEtimsFields(c, id, body); err != nil {
		h.fail(c, 422, err.Error())
		return
	}
	h.svc(c).Audit(p.ID, p.Username, "PRODUCT_UPDATED", "product", body.Name, "")
	// v1.8.0: flipping batch tracking on wraps existing shelf stock into
	// an OPENING lot so SUM(lots) can reconcile with the projection.
	if batches == 1 {
		if err := h.svc(c).EnableBatchesIfNeeded(int64(id)); err != nil {
			h.fail(c, 500, err.Error())
			return
		}
	}
	h.svc(c).EmitProduct(id, true)
	h.ok(c, gin.H{"updated": true})
}

// AdjustStock (products.manage) — quick +/- without full edit. The change
// lands in the stock ledger (ADJUSTMENT) with the real applied delta; the
// service path keeps batch-tracked lots in lockstep (FEFO on negative).
func (h *H) AdjustStock(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body struct {
		DeltaMilli int64  `json:"deltaMilli" binding:"required"`
		Note       string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "deltaMilli required")
		return
	}
	if body.DeltaMilli < -models.MaxStockDeltaMilli || body.DeltaMilli > models.MaxStockDeltaMilli {
		h.fail(c, 400, fmt.Sprintf("delta out of range (max %d milli)", models.MaxStockDeltaMilli))
		return
	}
	// The service path keeps every side in lockstep: the projection, the
	// ledger row, batch lots (FEFO on negative deltas), the audit trail
	// and the cross-till stock event.
	applied, err := h.svc(c).AdjustProductStock(id, body.DeltaMilli, body.Note, p)
	if err != nil {
		if err == services.ErrNotFound {
			h.fail(c, 404, "product not found")
		} else {
			h.fail(c, 400, err.Error())
		}
		return
	}
	h.ok(c, gin.H{"adjusted": true, "applied": applied})
}

// DisposeStock (products.manage, v1.7.0) — structured loss recording: the
// friend's SmartPOS disposal module (expired / spoilt / lost / damaged /
// other) with the loss value computable from product cost at report time.
// The deduction is guarded and lands in the stock ledger as DISPOSAL.
func (h *H) DisposeStock(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body struct {
		QtyMilli int64  `json:"qtyMilli" binding:"required"`
		Reason   string `json:"reason" binding:"required"`
		Note     string `json:"note"`
		BatchID  int64  `json:"batchId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "qtyMilli and reason are required")
		return
	}
	res, err := h.svc(c).DisposeProduct(int64(id), body.QtyMilli, body.Reason, body.Note, body.BatchID, p)
	if err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	var sku string
	h.db(c).QueryRow(`SELECT COALESCE(sku,'') FROM products WHERE id = ?`, id).Scan(&sku)
	h.svc(c).EmitStockDelta(sku, -body.QtyMilli)
	h.ok(c, res)
}

// DeactivateProduct soft-deletes (order history references products).
func (h *H) DeactivateProduct(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	res, err := h.db(c).Exec(h.db(c).Rebind(`UPDATE products SET is_active = 0, updated_at = ? WHERE id = ?`), services.NowStamp(), id)
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
