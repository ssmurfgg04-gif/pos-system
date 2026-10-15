package services

// quotes.go — quotations: a priced offer document sent to a customer
// BEFORE a sale exists. Creative Divine quotes branding/design/cyber
// packages all day; until now those lived in WhatsApp threads. A quote
// here is a first-class document that:
//
//   - follows draft → sent → accepted/declined, with converted stamped
//     automatically when its lines move to a till as a parked sale
//     (held sale) — the actual sale remains a normal checkout, so quotes
//     NEVER touch money or stock;
//   - team-syncs like orders (insert-by-client_uuid exactly once, quote
//     numbers remapped on collision) so the whole team sees the same pipe;
//   - prints as a clean A4 offer (the frontend renders + window.print()).
//
// Money fields are informational copies of the line math (validated
// server-side) — they describe the offer, they do not move anything.

import (
        "database/sql"
        "encoding/json"
        "fmt"
        "math"
        "strings"

        "posapp/internal/auth"
        "posapp/internal/models"
)

// quoteTotals validates line items and computes subtotal/discount/tax/total
// with the SAME configurable tax math checkout uses (integer cents), so a
// converted quote rings up to the same numbers on the till.
func (s *Service) quoteTotals(items []models.QuoteItem, discountCents int64) (sub, discount, tax, total int64, err error) {
        if len(items) == 0 {
                return 0, 0, 0, 0, fmt.Errorf("add at least one line item")
        }
        if len(items) > models.MaxOrderLines {
                return 0, 0, 0, 0, fmt.Errorf("too many line items (max %d)", models.MaxOrderLines)
        }
        for i := range items {
                it := &items[i]
                if it.Qty <= 0 || it.Qty > models.MaxOrderQty {
                        return 0, 0, 0, 0, fmt.Errorf("invalid quantity on %q", lineName(*it))
                }
                if it.UnitPriceCents < 0 || it.UnitPriceCents > models.MaxPriceCents {
                        return 0, 0, 0, 0, fmt.Errorf("invalid price on %q", lineName(*it))
                }
                line := it.UnitPriceCents * int64(it.Qty)
                if it.Qty > 0 && line/int64(it.Qty) != it.UnitPriceCents {
                        return 0, 0, 0, 0, fmt.Errorf("line total overflow on %q", lineName(*it))
                }
                it.LineTotalCents = line
                sub += line
                if sub < 0 {
                        return 0, 0, 0, 0, fmt.Errorf("quote total overflow")
                }
        }
        discount = discountCents
        if discount < 0 || (sub > 0 && discount >= sub) || (sub == 0 && discount != 0) {
                return 0, 0, 0, 0, fmt.Errorf("discount out of range (0 to %d)", max64(sub-1, 0))
        }
        discounted := sub - discount
        pct := s.settings.GetFloat("tax_percent", 16)
        if !(pct >= 0 && pct <= models.MaxTaxPercent) { // NaN-proof: NaN fails every compare
                return 0, 0, 0, 0, fmt.Errorf("tax_percent misconfigured (0-%d)", models.MaxTaxPercent)
        }
        if s.settings.GetBool("tax_included", true) {
                total = discounted
                tax = int64(math.Round(float64(discounted) * pct / (100 + pct)))
        } else {
                tax = int64(math.Round(float64(discounted) * pct / 100))
                total = discounted + tax
        }
        return sub, discount, tax, total, nil
}

func lineName(it models.QuoteItem) string {
        if strings.TrimSpace(it.Name) != "" {
                return truncStr(strings.TrimSpace(it.Name), 40)
        }
        return fmt.Sprintf("line %d", it.ProductID)
}

// normalizeQuoteInput resolves the customer (id wins; name/phone are
// denormalized copies for display + sync) and validates the rest.
func (s *Service) normalizeQuoteInput(in *models.QuoteInput) error {
        in.CustomerName = strings.TrimSpace(in.CustomerName)
        in.CustomerPhone = strings.TrimSpace(in.CustomerPhone)
        // CustomerID 0 is a walk-in quote — nothing to resolve.
        if in.CustomerID != 0 {
                c, err := s.GetCustomer(in.CustomerID)
                if err != nil {
                        return fmt.Errorf("customer not found")
                }
                in.CustomerName = c.Name
                in.CustomerPhone = c.Phone
        }
        if in.CustomerID == 0 && in.CustomerName == "" {
                return fmt.Errorf("pick a customer or type a name for the quote")
        }
        if len(in.CustomerName) > 80 {
                return fmt.Errorf("customer name too long")
        }
        if len(in.Notes) > 2000 {
                return fmt.Errorf("notes too long (max 2000)")
        }
        in.DiscountLabel = truncStr(strings.TrimSpace(in.DiscountLabel), 120)
        in.ValidUntil = strings.TrimSpace(in.ValidUntil)
        if in.ValidUntil != "" && len(in.ValidUntil) > 10 {
                return fmt.Errorf("valid-until must be a date (YYYY-MM-DD)")
        }
        return nil
}

// ListQuotes returns recent quotes, newest first. status filters
// (comma-separated allowed, e.g. "draft,sent"); q searches number, customer
// name and phone. limit caps the page (default 100).
func (s *Service) ListQuotes(status, q string, limit int) ([]models.Quote, error) {
        if limit <= 0 || limit > 500 {
                limit = 100
        }
        where := []string{"1=1"}
        args := []any{}
        if status != "" && status != "all" {
                statues := strings.Split(status, ",")
                marks := make([]string, 0, len(statues))
                for _, st := range statues {
                        st = strings.TrimSpace(st)
                        if st == "" {
                                continue
                        }
                        marks = append(marks, "?")
                        args = append(args, st)
                }
                if len(marks) > 0 {
                        where = append(where, "status IN ("+strings.Join(marks, ",")+")")
                }
        }
        if qq := strings.TrimSpace(q); qq != "" {
                where = append(where, "(quote_no LIKE ? OR customer_name LIKE ? OR customer_phone LIKE ?)")
                like := "%" + qq + "%"
                args = append(args, like, like, like)
        }
        args = append(args, limit)
        rows, err := s.db.Query(s.db.Rebind(`
                SELECT id, COALESCE(quote_no,''), client_uuid, customer_id, COALESCE(customer_name,''),
                        COALESCE(customer_phone,''), status, items_json,
                        subtotal_cents, COALESCE(discount_cents,0), COALESCE(discount_label,''),
                        tax_cents, total_cents, COALESCE(notes,''), COALESCE(valid_until,''),
                        created_by, COALESCE(created_by_name,''), converted_held_sale_id,
                        COALESCE(created_at,''), COALESCE(updated_at,'')
                FROM quotes WHERE `+strings.Join(where, " AND ")+`
                ORDER BY id DESC LIMIT ?`), args...)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        out := []models.Quote{}
        for rows.Next() {
                qy, err := scanQuote(rows)
                if err != nil {
                        return nil, err
                }
                out = append(out, *qy)
        }
        return out, rows.Err()
}

type rowScanner interface{ Scan(dest ...any) error }

func scanQuote(r rowScanner) (*models.Quote, error) {
        var qy models.Quote
        var raw string
        if err := r.Scan(&qy.ID, &qy.QuoteNo, &qy.ClientUUID, &qy.CustomerID, &qy.CustomerName,
                &qy.CustomerPhone, &qy.Status, &raw,
                &qy.SubtotalCents, &qy.DiscountCents, &qy.DiscountLabel,
                &qy.TaxCents, &qy.TotalCents, &qy.Notes, &qy.ValidUntil,
                &qy.CreatedBy, &qy.CreatedByName, &qy.ConvertedHeldSaleID,
                &qy.CreatedAt, &qy.UpdatedAt); err != nil {
                return nil, err
        }
        qy.Items = []models.QuoteItem{}
        if raw != "" {
                _ = json.Unmarshal([]byte(raw), &qy.Items)
        }
        return &qy, nil
}

// GetQuote fetches one quote by local id.
func (s *Service) GetQuote(id int64) (*models.Quote, error) {
        row := s.db.QueryRow(s.db.Rebind(`
                SELECT id, COALESCE(quote_no,''), client_uuid, customer_id, COALESCE(customer_name,''),
                        COALESCE(customer_phone,''), status, items_json,
                        subtotal_cents, COALESCE(discount_cents,0), COALESCE(discount_label,''),
                        tax_cents, total_cents, COALESCE(notes,''), COALESCE(valid_until,''),
                        created_by, COALESCE(created_by_name,''), converted_held_sale_id,
                        COALESCE(created_at,''), COALESCE(updated_at,'')
                FROM quotes WHERE id = ?`), id)
        qy, err := scanQuote(row)
        if err == sql.ErrNoRows {
                return nil, ErrNotFound
        }
        return qy, err
}

// nextQuoteNumber mints the next QT-000123 display number. Per-store
// monotonic via MAX(id) — collisions across tills are expected and fixed
// at apply time (renumber), exactly like order numbers.
func (s *Service) nextQuoteNumber(tx *sql.Tx) (string, int64, error) {
        var n int64
        if err := tx.QueryRow(`SELECT COALESCE(MAX(id),0)+1 FROM quotes`).Scan(&n); err != nil {
                return "", 0, err
        }
        return fmt.Sprintf("QT-%06d", n), n, nil
}

// CreateQuote validates, stores and emits a new quote. Money-shaping
// permissions mirror checkout exactly: a non-zero discount needs
// payments.apply_discount and an off-catalog line price needs
// payments.override_price — otherwise a custom role could quote prices its
// bearer could never ring up at checkout (the convert would dead-end).
func (s *Service) CreateQuote(in models.QuoteInput, p *auth.Principal) (*models.Quote, error) {
        if err := s.checkQuoteMoneyPerms(in, p); err != nil {
                return nil, err
        }
        if err := s.normalizeQuoteInput(&in); err != nil {
                return nil, err
        }
        sub, disc, tax, total, err := s.quoteTotals(in.Items, in.DiscountCents)
        if err != nil {
                return nil, err
        }
        raw, err := json.Marshal(in.Items)
        if err != nil {
                return nil, err
        }
        tx, err := s.db.Begin()
        if err != nil {
                return nil, err
        }
        defer tx.Rollback()
        qno, _, err := s.nextQuoteNumber(tx)
        if err != nil {
                return nil, err
        }
        now := nowStamp()
        createdBy, createdByName := int64(0), "system"
        if p != nil {
                createdBy, createdByName = p.ID, p.Username
        }
        res, err := tx.Exec(s.db.Rebind(`
                INSERT INTO quotes (quote_no, client_uuid, customer_id, customer_name, customer_phone,
                        status, items_json, subtotal_cents, discount_cents, discount_label, tax_cents, total_cents,
                        notes, valid_until, created_by, created_by_name, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
                qno, randToken(12), in.CustomerID, in.CustomerName, in.CustomerPhone,
                models.QuoteDraft, string(raw), sub, disc, in.DiscountLabel, tax, total,
                truncStr(in.Notes, 2000), in.ValidUntil, createdBy, createdByName, now, now)
        if err != nil {
                return nil, err
        }
        id, _ := res.LastInsertId()
        if err := tx.Commit(); err != nil {
                return nil, err
        }
        qy, err := s.GetQuote(id)
        if err != nil {
                return nil, err
        }
        s.EmitQuote(id)
        s.Audit(createdBy, createdByName, "QUOTE_CREATED", "quote", qy.QuoteNo,
                fmt.Sprintf("%s, %s", qy.CustomerName, moneyStr(qy.TotalCents)))
        return qy, nil
}

// UpdateQuote edits a draft or sent quote (status unchanged). Accepted,
// declined and converted quotes are historical records — not editable.
func (s *Service) UpdateQuote(id int64, in models.QuoteInput, p *auth.Principal) (*models.Quote, error) {
        if err := s.checkQuoteMoneyPerms(in, p); err != nil {
                return nil, err
        }
        cur, err := s.GetQuote(id)
        if err != nil {
                return nil, err
        }
        if cur.Status != models.QuoteDraft && cur.Status != models.QuoteSent {
                return nil, fmt.Errorf("a %s quote can no longer be edited", cur.Status)
        }
        if err := s.normalizeQuoteInput(&in); err != nil {
                return nil, err
        }
        sub, disc, tax, total, err := s.quoteTotals(in.Items, in.DiscountCents)
        if err != nil {
                return nil, err
        }
        raw, err := json.Marshal(in.Items)
        if err != nil {
                return nil, err
        }
        now := nowStamp()
        if _, err := s.db.Exec(s.db.Rebind(`
                UPDATE quotes SET customer_id = ?, customer_name = ?, customer_phone = ?,
                        items_json = ?, subtotal_cents = ?, discount_cents = ?, discount_label = ?,
                        tax_cents = ?, total_cents = ?, notes = ?, valid_until = ?, updated_at = ?
                WHERE id = ?`),
                in.CustomerID, in.CustomerName, in.CustomerPhone, string(raw),
                sub, disc, in.DiscountLabel, tax, total,
                truncStr(in.Notes, 2000), in.ValidUntil, now, id); err != nil {
                return nil, err
        }
        qy, err := s.GetQuote(id)
        if err != nil {
                return nil, err
        }
        s.EmitQuote(id)
        who, uid := "system", int64(0)
        if p != nil {
                who, uid = p.Username, p.ID
        }
        s.Audit(uid, who, "QUOTE_UPDATED", "quote", qy.QuoteNo, "")
        return qy, nil
}

// checkQuoteMoneyPerms enforces the same permission gates checkout does,
// so a quote is always ring-up-able by its creator's role.
func (s *Service) checkQuoteMoneyPerms(in models.QuoteInput, p *auth.Principal) error {
        if p == nil {
                return nil // system path (tests); handlers always pass a principal
        }
        if in.DiscountCents > 0 && !p.Can("payments.apply_discount") {
                return fmt.Errorf("quoting a discount requires payments.apply_discount permission")
        }
        if !p.Can("payments.override_price") {
                for _, it := range in.Items {
                        if it.ProductID != 0 {
                                var catalog int64
                                if err := s.db.QueryRow(s.db.Rebind(`SELECT price_cents FROM products WHERE id = ?`), it.ProductID).Scan(&catalog); err == nil {
                                        if it.UnitPriceCents != catalog {
                                                return fmt.Errorf("a line priced off the catalog requires payments.override_price permission")
                                        }
                                }
                        }
                }
        }
        return nil
}

// SetQuoteStatus moves a quote through its pipeline. Legal moves:
// draft→sent; draft/sent→accepted/declined; declined→accepted (the
// "customer changed their mind back" path). Anything else is refused —
// in particular an accepted quote cannot quietly become declined after
// money started moving.
func (s *Service) SetQuoteStatus(id int64, status string, p *auth.Principal) (*models.Quote, error) {
        cur, err := s.GetQuote(id)
        if err != nil {
                return nil, err
        }
        switch status {
        case models.QuoteSent:
                if cur.Status != models.QuoteDraft {
                        return nil, fmt.Errorf("only a draft can be marked sent")
                }
        case models.QuoteAccepted:
                if cur.Status != models.QuoteDraft && cur.Status != models.QuoteSent && cur.Status != models.QuoteDeclined {
                        return nil, fmt.Errorf("this quote is already %s", cur.Status)
                }
        case models.QuoteDeclined:
                if cur.Status != models.QuoteDraft && cur.Status != models.QuoteSent {
                        return nil, fmt.Errorf("this quote is already %s", cur.Status)
                }
        case models.QuoteConverted:
                return nil, fmt.Errorf("conversion happens when the sale is parked on a till")
        default:
                return nil, fmt.Errorf("unknown status %q", status)
        }
        if cur.Status == status {
                return cur, nil // same-state no-op: do not re-emit
        }
        if _, err := s.db.Exec(s.db.Rebind(`UPDATE quotes SET status = ?, updated_at = ? WHERE id = ?`),
                status, nowStamp(), id); err != nil {
                return nil, err
        }
        qy, err := s.GetQuote(id)
        if err != nil {
                return nil, err
        }
        s.EmitQuote(id)
        who := "system"
        var uid int64
        if p != nil {
                who, uid = p.Username, p.ID
        }
        s.Audit(uid, who, "QUOTE_"+strings.ToUpper(status), "quote", qy.QuoteNo, "")
        return qy, nil
}

// ConvertQuote parks the quote's lines on THIS till as a held sale (the
// proven resume-in-POS path: items, customer and note land in the POS's
// Held sales panel; checkout does the money). The quote is stamped
// converted and cross-device sync carries both the stamp and — via the
// normal order flow, once the sale completes — the outcome.
func (s *Service) ConvertQuote(id int64, p *auth.Principal) (*models.HeldSale, error) {
        qy, err := s.GetQuote(id)
        if err != nil {
                return nil, err
        }
        if qy.Status == models.QuoteConverted {
                return nil, fmt.Errorf("this quote was already moved to a till")
        }
        if qy.Status == models.QuoteDeclined {
                return nil, fmt.Errorf("this quote was declined, set it back or create a new one")
        }
        // Catalog lines only: held sales freeze productId/qty/price triples.
        // Lines are resolved by SKU FIRST (stable across tills), with the
        // product id as fallback — a synced quote carries the ORIGIN till's
        // local ids, and AUTOINCREMENT ids mean id 42 can be "Banner" on the
        // till that made the quote and "A4 paper" on the till converting it.
        // Resolving by id alone could park the WRONG product (and the wrong
        // price) into the cart.
        req := models.CheckoutRequest{
                Items:         make([]models.CheckoutItem, 0, len(qy.Items)),
                CustomerID:    qy.CustomerID,
                CustomerName:  qy.CustomerName,
                Note:          fmt.Sprintf("From quote %s", qy.QuoteNo),
                DiscountCents: qy.DiscountCents, // the quoted discount survives the move
                DiscountLabel: qy.DiscountLabel,
        }
        var skipped []string
        for _, it := range qy.Items {
                var pid int64
                if it.SKU != "" {
                        s.db.QueryRow(s.db.Rebind(`SELECT id FROM products WHERE sku = ? AND is_active = 1`), it.SKU).Scan(&pid)
                }
                if pid == 0 && it.ProductID != 0 {
                        // id fallback guarded by SKU match where possible: accept the
                        // id only when the local product's SKU is unknown-or-equal.
                        var localSKU string
                        s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(sku,'') FROM products WHERE id = ? AND is_active = 1`), it.ProductID).Scan(&localSKU)
                        if it.SKU == "" || localSKU == it.SKU {
                                pid = it.ProductID
                        }
                }
                if pid == 0 {
                        skipped = append(skipped, lineName(it))
                        continue
                }
                req.Items = append(req.Items, models.CheckoutItem{
                        ProductID: pid, Qty: it.Qty, UnitPriceCents: it.UnitPriceCents,
                })
        }
        if len(req.Items) == 0 {
                return nil, fmt.Errorf("this quote has no catalog products to move to the till, add products to the catalog first")
        }
        if len(skipped) > 0 {
                req.Note += fmt.Sprintf(" (custom lines not in catalog: %s)", truncStr(strings.Join(skipped, ", "), 200))
        }
        held, err := s.HoldSale(fmt.Sprintf("Quote %s, %s", qy.QuoteNo, qy.CustomerName), req, p)
        if err != nil {
                return nil, err
        }
        // Compare-and-set the converted stamp: a double-click (or two clerks)
        // must not park TWO held sales for one quote — the loser deletes the
        // held sale it just created.
        res, err := s.db.Exec(s.db.Rebind(`
                UPDATE quotes SET status = ?, converted_held_sale_id = ?, updated_at = ?
                WHERE id = ? AND status <> ?`),
                models.QuoteConverted, held.ID, nowStamp(), id, models.QuoteConverted)
        if err != nil {
                return nil, err
        }
        if n, _ := res.RowsAffected(); n == 0 {
                _, _ = s.db.Exec(s.db.Rebind(`DELETE FROM held_sales WHERE id = ?`), held.ID)
                return nil, fmt.Errorf("this quote was already moved to a till")
        }
        s.EmitQuote(id)
        who := "system"
        var uid int64
        if p != nil {
                who, uid = p.Username, p.ID
        }
        s.Audit(uid, who, "QUOTE_CONVERTED", "quote", qy.QuoteNo, fmt.Sprintf("held sale %d", held.ID))
        return held, nil
}

// moneyStr renders cents as a plain decimal (audit lines).
func moneyStr(cents int64) string {
        sign := ""
        if cents < 0 {
                sign, cents = "-", -cents
        }
        return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}
