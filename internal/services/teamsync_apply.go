// teamsync_apply.go — the pull-side event application: turns a synced
// wire event into a local transaction (orders, products, customers,
// ledgers, voids, designs, config, quotes). Idempotency is owned by
// sync_events (isSyncApplied/markSyncApplied) in teamsync.go; the
// handlers here must tolerate replay and cross-till races the same
// way they always have. Split out of teamsync.go (file-level, same
// package — no behavior change).

package services

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"posapp/internal/models"
)

// ---- Event application ----

func (s *Service) applyEvent(entity, op string, payload json.RawMessage) error {
	switch entity {
	case "category":
		if op == "delete" {
			return s.applyCategoryDelete(payload)
		}
		return s.applyCategory(payload)
	case "product":
		if op == "image" {
			return s.applyProductImage(payload)
		}
		return s.applyProduct(payload)
	case "stock":
		return s.applyStockDelta(payload)
	case "customer":
		return s.applyCustomer(payload)
	case "ledger":
		return s.applyLedger(payload)
	case "order":
		return s.applyOrder(payload)
	case "quote":
		return s.applyQuote(payload)
	case "void":
		return s.applyVoid(payload)
	case "design":
		return s.applyDesign(payload)
	case "config":
		return s.applyConfig(payload)
	case "voidreason":
		if op == "delete" {
			return s.applyVoidReasonDelete(payload)
		}
		return s.applyVoidReason(payload)
	case "shift":
		return s.applyShift(payload)
	default:
		return nil // unknown entity types are skipped (forward compatibility)
	}
}

// ---- Config sync ("updates over the air") ----

// configSyncWhitelist is the set of settings that propagate to every team
// device through the sync log — the "update via database, not reinstall"
// story: a price list change, tax tweak, loyalty rule, or branding refresh
// made on one till lands on the others within a sync tick. Secrets
// (keys, passkeys, JWT, storage) and per-device preferences (printer,
// device id) are deliberately absent — they must never leave the machine
// they were entered on.

var configSyncWhitelist = map[string]bool{
	"app_name": true, "store_name": true, "store_address": true,
	"store_phone": true, "receipt_footer": true,
	"currency_code": true, "currency_symbol": true,
	"tax_percent": true, "tax_included": true, "brand_color": true,
	"payment_mode": true, "till_number": true, "paybill_number": true,
	"loyalty_enabled": true, "loyalty_earn_per_cents": true,
	"loyalty_point_cents": true, "loyalty_max_percent": true,
	"credit_enabled": true, "low_stock_threshold": true,
	"paystack_enabled": true, "paystack_public_key": true,
	"paystack_currency": true, "paystack_callback_url": true,
	brandLogoDataKey: true, // synced logo — applyBrandLogoData materialises the file
}

// EmitConfig queues a config delta (called from the settings API with the
// keys that actually changed).

func (s *Service) EmitConfig(keys []string) {
	if len(keys) == 0 {
		return
	}
	values := map[string]string{}
	for _, k := range keys {
		if configSyncWhitelist[k] {
			values[k] = s.settings.Get(k)
		}
	}
	if len(values) == 0 {
		return
	}
	s.Emit("config", "upsert", map[string]any{"values": values, "updatedAt": nowStamp()})
}

// applyConfig merges a remote config delta. Only whitelisted keys apply;
// tax_percent is re-validated (defence in depth against a poisoned event).

func (s *Service) applyConfig(payload json.RawMessage) error {
	var cfg struct {
		Values map[string]string `json:"values"`
	}
	if err := json.Unmarshal(payload, &cfg); err != nil {
		return err
	}
	for k, v := range cfg.Values {
		if !configSyncWhitelist[k] {
			continue
		}
		if k == "tax_percent" {
			var f float64
			// Affirmative range test only: NaN slips through
			// `f < 0 || f > 100` (every NaN compare is false)
			// and then poisons tax math on every checkout.
			if _, err := fmt.Sscanf(v, "%g", &f); err != nil || !(f >= 0 && f <= 100) {
				continue
			}
		}
		if err := s.settings.Set(k, v); err != nil {
			return err
		}
		if k == brandLogoDataKey {
			s.applyBrandLogoData(v) // write file + flag so /settings/logo serves it
		}
	}
	s.broadcast(EventSettingsUpdate, s.settings.Branding())
	return nil
}

// ---- Void-reason catalog sync ----

// EmitVoidReason queues a reason catalog change so every till offers the
// same void reasons (audit consistency).

func (s *Service) EmitVoidReason(label string, active bool, sortOrder int) {
	s.Emit("voidreason", "upsert", map[string]any{
		"label": label, "active": active, "sortOrder": sortOrder, "updatedAt": nowStamp(),
	})
}

func (s *Service) applyVoidReason(payload json.RawMessage) error {
	var r struct {
		Label     string `json:"label"`
		Active    bool   `json:"active"`
		SortOrder int    `json:"sortOrder"`
	}
	if err := json.Unmarshal(payload, &r); err != nil {
		return err
	}
	if strings.TrimSpace(r.Label) == "" || len(r.Label) > 120 {
		return fmt.Errorf("void reason label invalid")
	}
	var id int64
	err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM void_reasons WHERE label = ?`), r.Label).Scan(&id)
	if err == nil {
		_, err = s.db.Exec(s.db.Rebind(`UPDATE void_reasons SET is_active = ?, sort_order = ? WHERE id = ?`),
			btoi(r.Active), r.SortOrder, id)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = s.db.Exec(s.db.Rebind(`INSERT INTO void_reasons (label, is_active, sort_order) VALUES (?, ?, ?)`),
		r.Label, btoi(r.Active), r.SortOrder)
	return err
}

func (s *Service) applyVoidReasonDelete(payload json.RawMessage) error {
	var r struct {
		Label string `json:"label"`
	}
	if err := json.Unmarshal(payload, &r); err != nil {
		return err
	}
	_, err := s.db.Exec(s.db.Rebind(`DELETE FROM void_reasons WHERE label = ?`), r.Label)
	return err
}

func (s *Service) applyCategory(payload json.RawMessage) error {
	var cat struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(payload, &cat); err != nil {
		return err
	}
	if cat.Slug == "" || cat.Name == "" {
		return fmt.Errorf("category event missing name/slug")
	}
	var id int64
	err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM categories WHERE slug = ?`), cat.Slug).Scan(&id)
	if err == nil {
		_, err = s.db.Exec(s.db.Rebind(`UPDATE categories SET name = ? WHERE id = ?`), cat.Name, id)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = s.db.Exec(s.db.Rebind(`INSERT INTO categories (name, slug, sort_order) VALUES (?, ?, (SELECT COALESCE(MAX(sort_order),0)+1 FROM categories))`), cat.Name, cat.Slug)
	return err
}

func (s *Service) applyCategoryDelete(payload json.RawMessage) error {
	var cat struct {
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(payload, &cat); err != nil {
		return err
	}
	var count int
	if err := s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM products WHERE category_id = (SELECT id FROM categories WHERE slug = ?)`), cat.Slug).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil // local products reference it — keep the category
	}
	_, err := s.db.Exec(s.db.Rebind(`DELETE FROM categories WHERE slug = ?`), cat.Slug)
	return err
}

func (s *Service) applyProduct(payload json.RawMessage) error {
	var pr struct {
		SKU          string `json:"sku"`
		Barcode      string `json:"barcode"`
		Name         string `json:"name"`
		CategorySlug string `json:"categorySlug"`
		PriceCents   int64  `json:"priceCents"`
		CostCents    int64  `json:"costCents"`
		StockQty     int    `json:"stockQty"`
		StockSet     bool   `json:"stockSet"`
		TrackStock   bool   `json:"trackStock"`
		Active       bool   `json:"active"`
		IsGiftCard   bool   `json:"isGiftCard"`
		UpdatedAt    string `json:"updatedAt"`
	}
	if err := json.Unmarshal(payload, &pr); err != nil {
		return err
	}
	if pr.SKU == "" && pr.Barcode == "" {
		return fmt.Errorf("product event has no sku/barcode")
	}
	// Resolve category by slug (create on demand — categories sync first).
	var catID int64
	if pr.CategorySlug != "" {
		err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM categories WHERE slug = ?`), pr.CategorySlug).Scan(&catID)
		if err != nil {
			s.db.Exec(s.db.Rebind(`INSERT INTO categories (name, slug) VALUES (?, ?)`), pr.CategorySlug, pr.CategorySlug)
			s.db.QueryRow(s.db.Rebind(`SELECT id FROM categories WHERE slug = ?`), pr.CategorySlug).Scan(&catID)
		}
	}
	if catID == 0 {
		s.db.QueryRow(`SELECT id FROM categories ORDER BY sort_order, id LIMIT 1`).Scan(&catID)
	}
	var localID int64
	var localUpdated string
	q := `SELECT id, COALESCE(updated_at,'') FROM products WHERE sku = ?`
	args := []any{pr.SKU}
	if pr.SKU == "" {
		q = `SELECT id, COALESCE(updated_at,'') FROM products WHERE barcode = ?`
		args = []any{pr.Barcode}
	}
	err := s.db.QueryRow(s.db.Rebind(q), args...).Scan(&localID, &localUpdated)
	if err == nil {
		// LWW on catalog fields; stock only when explicitly set (admin edit).
		if pr.UpdatedAt != "" && localUpdated != "" && pr.UpdatedAt < localUpdated {
			return nil // stale
		}
		if _, err := s.db.Exec(s.db.Rebind(`
                        UPDATE products SET barcode = ?, name = ?, category_id = ?, price_cents = ?, cost_cents = ?,
                                track_stock = ?, is_active = ?, is_gift_card = ?, updated_at = ?
                        WHERE id = ?`),
			pr.Barcode, pr.Name, catID, pr.PriceCents, pr.CostCents, btoi(pr.TrackStock), btoi(pr.Active), btoi(pr.IsGiftCard), pr.UpdatedAt, localID); err != nil {
			return err
		}
		if pr.StockSet {
			_, err = s.db.Exec(s.db.Rebind(`UPDATE products SET stock_qty = ? WHERE id = ?`), pr.StockQty, localID)
		}
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	sku := pr.SKU
	if sku == "" {
		sku = "SKU-SYNC-" + randToken(6) // 48-bit token: a collision here would dead-letter the event
	}
	_, err = s.db.Exec(s.db.Rebind(`
                INSERT INTO products (sku, barcode, name, category_id, price_cents, cost_cents, stock_qty, track_stock, is_active, is_gift_card, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		sku, pr.Barcode, pr.Name, catID, pr.PriceCents, pr.CostCents, pr.StockQty, btoi(pr.TrackStock), btoi(pr.Active), btoi(pr.IsGiftCard), pr.UpdatedAt)
	return err
}

func (s *Service) applyStockDelta(payload json.RawMessage) error {
	var d struct {
		SKU   string `json:"sku"`
		Delta int    `json:"delta"`
	}
	if err := json.Unmarshal(payload, &d); err != nil {
		return err
	}
	if d.SKU == "" || d.Delta == 0 {
		return nil
	}
	// Zero rows MUST be an error, not a silent success: a delta that
	// matches nothing (the SKU was renamed on another till while this one
	// was offline) has to land in dead-letter and retry until the rename
	// arrives — consuming it here would silently lose the movement.
	// updated_at is deliberately NOT stamped: the LWW clock belongs to
	// catalog edits only (an apply-time stamp made every remote sale
	// beat later local price edits on this device).
	res, err := s.db.Exec(s.db.Rebind(`
                UPDATE products SET stock_qty = MAX(0, stock_qty + ?) WHERE sku = ?`),
		d.Delta, d.SKU)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("stock delta for unknown product %q (catalog not synced yet?)", d.SKU)
	}
	return nil
}

func (s *Service) applyCustomer(payload json.RawMessage) error {
	var cu struct {
		Phone       string `json:"phone"`
		Name        string `json:"name"`
		Email       string `json:"email"`
		Notes       string `json:"notes"`
		CreditLimit int64  `json:"creditLimitCents"`
		Active      bool   `json:"active"`
		UpdatedAt   string `json:"updatedAt"`
	}
	if err := json.Unmarshal(payload, &cu); err != nil {
		return err
	}
	if cu.Name == "" {
		return fmt.Errorf("customer event has no name")
	}
	var id int64
	var updated string
	var err error
	if cu.Phone != "" {
		err = s.db.QueryRow(s.db.Rebind(`SELECT id, COALESCE(updated_at,'') FROM customers WHERE phone = ?`), cu.Phone).Scan(&id, &updated)
	} else {
		err = s.db.QueryRow(s.db.Rebind(`SELECT id, COALESCE(updated_at,'') FROM customers WHERE name = ? AND (phone = '' OR phone IS NULL)`), cu.Name).Scan(&id, &updated)
	}
	if err == nil {
		if cu.UpdatedAt != "" && updated != "" && cu.UpdatedAt < updated {
			return nil // stale
		}
		_, err = s.db.Exec(s.db.Rebind(`
                        UPDATE customers SET name = ?, email = ?, notes = ?, credit_limit_cents = ?, is_active = ?, updated_at = ? WHERE id = ?`),
			cu.Name, cu.Email, cu.Notes, cu.CreditLimit, btoi(cu.Active), cu.UpdatedAt, id)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = s.db.Exec(s.db.Rebind(`
                INSERT INTO customers (name, phone, email, notes, credit_limit_cents, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`),
		cu.Name, cu.Phone, cu.Email, cu.Notes, cu.CreditLimit, btoi(cu.Active), cu.UpdatedAt)
	return err
}

func (s *Service) applyLedger(payload json.RawMessage) error {
	var le struct {
		Phone     string `json:"phone"`
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		Amount    int64  `json:"amountCents"`
		Points    int64  `json:"pointsDelta"`
		Note      string `json:"note"`
		CreatedAt string `json:"createdAt"`
	}
	if err := json.Unmarshal(payload, &le); err != nil {
		return err
	}
	var custID int64
	var err error
	if le.Phone != "" {
		err = s.db.QueryRow(s.db.Rebind(`SELECT id FROM customers WHERE phone = ?`), le.Phone).Scan(&custID)
	} else if le.Name != "" {
		err = s.db.QueryRow(s.db.Rebind(`SELECT id FROM customers WHERE name = ?`), le.Name).Scan(&custID)
	} else {
		return fmt.Errorf("ledger event has no customer key")
	}
	if err == sql.ErrNoRows {
		// Customer row hasn't landed yet (or was captured on a device whose
		// customer event is still behind this one). Create the identity now —
		// an unknown customer must NOT dead-letter the money movement; the
		// customer upsert event arrives behind it and fills in the rest.
		name := le.Name
		if name == "" {
			name = "Customer " + le.Phone
		}
		ins, ierr := s.db.Exec(s.db.Rebind(`INSERT INTO customers (name, phone, balance_cents, loyalty_points, updated_at) VALUES (?, ?, 0, 0, ?)`),
			name, le.Phone, nowStamp())
		if ierr != nil {
			return ierr
		}
		custID, _ = ins.LastInsertId()
	} else if err != nil {
		return err
	}
	_, err = s.db.Exec(s.db.Rebind(`
                INSERT INTO customer_ledger (customer_id, order_id, kind, amount_cents, points_delta, note, created_by, created_at)
                VALUES (?, 0, ?, ?, ?, ?, 0, ?)`),
		custID, le.Kind, le.Amount, le.Points, le.Note, le.CreatedAt)
	if err != nil {
		return err
	}
	if le.Kind == models.LedgerCreditTopup || le.Kind == models.LedgerCreditRedeem {
		_, err = s.db.Exec(s.db.Rebind(`UPDATE customers SET store_credit_cents = store_credit_cents + ?, updated_at = ? WHERE id = ?`),
			le.Amount, nowStamp(), custID)
		return err
	}
	_, err = s.db.Exec(s.db.Rebind(`UPDATE customers SET balance_cents = balance_cents + ?, loyalty_points = loyalty_points + ?, updated_at = ? WHERE id = ?`),
		le.Amount, le.Points, nowStamp(), custID)
	return err
}

// orderEvent is the wire form of a synced order.

type orderEvent struct {
	ClientUUID     string  `json:"clientUuid"`
	DeviceID       string  `json:"deviceId"`
	Number         string  `json:"number"`
	Status         string  `json:"status"`
	SubtotalCents  int64   `json:"subtotalCents"`
	DiscountCents  int64   `json:"discountCents"`
	DiscountLabel  string  `json:"discountLabel"`
	PointsRedeemed int64   `json:"pointsRedeemed"`
	TaxCents       int64   `json:"taxCents"`
	TotalCents     int64   `json:"totalCents"`
	TaxPercent     float64 `json:"taxPercent"`
	TaxIncluded    bool    `json:"taxIncluded"`
	CashierName    string  `json:"cashierName"`
	CustomerName   string  `json:"customerName"`
	CustomerPhone  string  `json:"customerPhone"`
	Note           string  `json:"note"`
	BuyerPin       string  `json:"buyerPin"`
	InvoiceNumber  string  `json:"invoiceNumber"`
	CreatedAt      string  `json:"createdAt"`
	PaidAt         string  `json:"paidAt"`
	Items          []struct {
		SKU       string `json:"sku"`
		Name      string `json:"name"`
		Qty       int    `json:"qty"`
		UnitPrice int64  `json:"unitPriceCents"`
		LineTotal int64  `json:"lineTotalCents"`
	} `json:"items"`
	Payments []struct {
		Method      string `json:"method"`
		Amount      int64  `json:"amountCents"`
		Status      string `json:"status"`
		Receipt     string `json:"mpesaReceipt"`
		Phone       string `json:"phone"`
		CreatedAt   string `json:"createdAt"`
		CompletedAt string `json:"completedAt"`
	} `json:"payments"`
}

func (s *Service) applyOrder(payload json.RawMessage) error {
	var oe orderEvent
	if err := json.Unmarshal(payload, &oe); err != nil {
		return err
	}
	if oe.ClientUUID == "" {
		return fmt.Errorf("order event missing clientUuid")
	}
	var existing int64
	err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM orders WHERE client_uuid = ?`), oe.ClientUUID).Scan(&existing)
	if err == nil {
		// Known order: the only meaningful transition to replay is a
		// remote completion (PENDING → PAID, e.g. a tab settled or an
		// M-Pesa push finished on another till).
		var status string
		if err := s.db.QueryRow(`SELECT status FROM orders WHERE id = ?`, existing).Scan(&status); err != nil {
			return err
		}
		if status == models.OrderPending && oe.Status == models.OrderPaid {
			return s.remoteComplete(existing, oe)
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}

	// Resolve the customer by phone (ids differ across devices).
	var custID int64
	if oe.CustomerPhone != "" {
		s.db.QueryRow(s.db.Rebind(`SELECT id FROM customers WHERE phone = ?`), oe.CustomerPhone).Scan(&custID)
	}

	// Order number collision (two tills allocated the same sequence):
	// suffix with a short device tag and retry until unique — a single
	// failed insert must never eat the order on this device.
	number := oe.Number
	for attempt := 0; ; attempt++ {
		var n int
		s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM orders WHERE number = ?`), number).Scan(&n)
		if n == 0 {
			break
		}
		if attempt >= 8 {
			return fmt.Errorf("order number %s: cannot allocate a free suffix", oe.Number)
		}
		number = fmt.Sprintf("%s-%s", oe.Number, strings.ToUpper(randToken(2+attempt/3)))
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	taxInc := 0
	if oe.TaxIncluded {
		taxInc = 1
	}
	paidAt := oe.PaidAt
	status := oe.Status
	if status == models.OrderVoided {
		return nil // void events carry their own payload; skip dead orders
	}
	// cashier_id is NOT NULL + FK to users(id) — the old hardwired 0 made
	// EVERY cross-device order insert fail (and the cursor still advanced,
	// silently dropping synced orders). Resolve a local user: the same
	// username when it exists here, else any local staff account.
	cashierID := s.resolveLocalCashierID(tx, oe.CashierName)
	res, err := tx.Exec(s.db.Rebind(`
                INSERT INTO orders (number, status, subtotal_cents, tax_cents, total_cents, tax_percent, tax_included,
                        discount_cents, discount_label, points_redeemed, cashier_id, customer_name, note, client_uuid, created_at, paid_at, customer_id, buyer_pin, invoice_number, device_id)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		number, status, oe.SubtotalCents, oe.TaxCents, oe.TotalCents, oe.TaxPercent, taxInc,
		oe.DiscountCents, oe.DiscountLabel, oe.PointsRedeemed, cashierID, oe.CustomerName, oe.Note, oe.ClientUUID, oe.CreatedAt, paidAt, custID, oe.BuyerPin, oe.InvoiceNumber, oe.DeviceID)
	if err != nil {
		return err
	}
	orderID, _ := res.LastInsertId()
	// Stock does NOT move here: the checkout already emitted per-line
	// stock-delta events (the canonical movers — they are applied exactly
	// once via sync_applied). Deducting inline as well would move stock
	// twice on every receiving device.
	for _, it := range oe.Items {
		var pid int64
		if it.SKU != "" {
			s.db.QueryRow(s.db.Rebind(`SELECT id FROM products WHERE sku = ?`), it.SKU).Scan(&pid)
		}
		if _, err := tx.Exec(s.db.Rebind(`
                        INSERT INTO order_items (order_id, product_id, name, sku, qty, unit_price_cents, line_total_cents)
                        VALUES (?, ?, ?, ?, ?, ?, ?)`),
			orderID, pid, it.Name, it.SKU, it.Qty, it.UnitPrice, it.LineTotal); err != nil {
			return err
		}
	}
	for _, pm := range oe.Payments {
		if _, err := tx.Exec(s.db.Rebind(`
                        INSERT INTO payments (order_id, method, mode, amount_cents, status, phone, mpesa_receipt, created_at, completed_at)
                        VALUES (?, ?, '', ?, ?, ?, ?, ?, ?)`),
			orderID, pm.Method, pm.Amount, pm.Status, pm.Phone, pm.Receipt, pm.CreatedAt, pm.CompletedAt); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.broadcast(EventOrderCreated, map[string]any{"number": number, "synced": true})
	return nil
}

// remoteComplete replays a remote PENDING→PAID transition: adopt the
// completed payments exactly once. Stock is NOT deducted here — the
// checkout emitted per-line stock deltas (applied exactly once), so an
// inline deduction would double-move stock on this device.

func (s *Service) remoteComplete(orderID int64, oe orderEvent) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE orders SET status = 'PAID', paid_at = ? WHERE id = ? AND status = 'PENDING'`,
		oe.PaidAt, orderID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil // someone else completed it first
	}
	for _, pm := range oe.Payments {
		tx.Exec(s.db.Rebind(`UPDATE payments SET status = ?, completed_at = COALESCE(NULLIF(?,''), completed_at) WHERE order_id = ? AND method = ? AND status = 'PENDING'`),
			pm.Status, pm.CompletedAt, orderID, pm.Method)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if order, err := s.GetOrder(orderID); err == nil {
		s.broadcast(EventOrderPaid, orderBroadcastPayload(order))
	}
	return nil
}

func (s *Service) applyVoid(payload json.RawMessage) error {
	var v struct {
		OrderUUID   string `json:"orderClientUuid"`
		OrderNumber string `json:"orderNumber"`
		VoidedAt    string `json:"voidedAt"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal(payload, &v); err != nil {
		return err
	}
	var orderID int64
	var err error
	if v.OrderUUID != "" {
		err = s.db.QueryRow(s.db.Rebind(`SELECT id FROM orders WHERE client_uuid = ?`), v.OrderUUID).Scan(&orderID)
	} else if v.OrderNumber != "" {
		err = s.db.QueryRow(s.db.Rebind(`SELECT id FROM orders WHERE number = ?`), v.OrderNumber).Scan(&orderID)
	} else {
		return fmt.Errorf("void event has no order reference")
	}
	if err != nil {
		return nil // order unknown here (never synced) — nothing to void
	}
	var status string
	if err := s.db.QueryRow(`SELECT status FROM orders WHERE id = ?`, orderID).Scan(&status); err != nil {
		return err
	}
	if status == models.OrderVoided {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE orders SET status = 'VOIDED', voided_at = ?, void_reason = ? WHERE id = ? AND status != 'VOIDED'`,
		v.VoidedAt, v.Reason, orderID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil
	}
	items, err := s.loadItemsTx(tx, orderID)
	if err != nil {
		return err
	}
	// Restore stock for EVERY non-voided status, PENDING included: this
	// device (a receiver) applied the checkout's per-line stock deltas
	// regardless of when the money landed — the ORIGIN, which deducts
	// async orders only at completion, keeps its own PAID-only restore.
	// Restoring only PAID here would strand the deltas of a voided
	// pending tab/M-Pesa order (stock vanishing fleet-wide).
	for _, it := range items {
		var track int
		tx.QueryRow(`SELECT COALESCE(track_stock,1) FROM products WHERE id = ?`, it.ProductID).Scan(&track)
		if track != 1 {
			continue
		}
		if _, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty = stock_qty + ? WHERE id = ?`), it.Qty, it.ProductID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE payments SET status = 'VOIDED', result_desc = ? WHERE order_id = ? AND status IN ('PENDING','COMPLETED')`,
		"synced void: "+truncStr(v.Reason, 160), orderID); err != nil {
		return err
	}
	// Gift cards minted by this sale die with it (same rule as the local
	// Void — an ACTIVE code behind a vanished sale is free money).
	if status == models.OrderPaid {
		if _, err := tx.Exec(`UPDATE gift_cards SET status = 'VOID', remaining_cents = 0, redeemed_at = ? WHERE order_id = ? AND status = 'ACTIVE'`,
			nowStamp(), orderID); err != nil {
			return err
		}
	}
	// Reverse tab charges + refund points/credit (mirrors local Void).
	var custID, tabTotal int64
	var tabCount int
	if err := tx.QueryRow(`SELECT COALESCE(customer_id,0), total_cents FROM orders WHERE id = ?`, orderID).
		Scan(&custID, &tabTotal); err != nil {
		return err
	}
	if custID != 0 {
		tx.QueryRow(`SELECT COUNT(*) FROM payments WHERE order_id = ? AND method = 'account'`, orderID).Scan(&tabCount)
		if tabCount > 0 {
			if err := recordLedgerTx(tx, s.db.Rebind, custID, orderID, models.LedgerAdjust, -tabTotal, 0, "void reversal (synced)", 0); err != nil {
				return err
			}
		}
		var pts int64
		tx.QueryRow(`SELECT COALESCE(points_redeemed,0) FROM orders WHERE id = ?`, orderID).Scan(&pts)
		if pts > 0 {
			if err := recordLedgerTx(tx, s.db.Rebind, custID, orderID, models.LedgerLoyalty, 0, pts, "points refund (synced void)", 0); err != nil {
				return err
			}
		}
		// Refund the store credit this sale spent. The flip above set
		// every PENDING/COMPLETED leg to VOIDED but left amount_cents
		// intact — sum the VOIDED credit legs (the old PENDING/COMPLETED
		// filter always read 0 after the flip, silently confiscating
		// the customer's prepaid credit on every synced void).
		var creditUsed int64
		tx.QueryRow(`SELECT COALESCE(SUM(amount_cents),0) FROM payments WHERE order_id = ? AND method = 'credit' AND status = 'VOIDED'`, orderID).Scan(&creditUsed)
		if creditUsed > 0 {
			if err := recordLedgerTx(tx, s.db.Rebind, custID, orderID, models.LedgerCreditTopup, creditUsed, 0, "store credit refund (synced void)", 0); err != nil {
				return err
			}
		}
		// Reverse the loyalty points EARNED on this PAID order — the
		// local Void() does this (orders.go) but applyVoid never did,
		// so every synced void of a paid sale minted phantom points
		// fleet-wide (the earn row arrives via emitLedgerForOrder,
		// the reversal was simply missing here). Only positive earn
		// entries are reversed; redemptions are refunded above.
		if status == models.OrderPaid {
			var ptsEarned int64
			tx.QueryRow(s.db.Rebind(`SELECT COALESCE(SUM(points_delta),0) FROM customer_ledger
                                WHERE order_id = ? AND kind = ? AND points_delta > 0`), orderID, models.LedgerLoyalty).Scan(&ptsEarned)
			if ptsEarned > 0 {
				if err := recordLedgerTx(tx, s.db.Rebind, custID, orderID, models.LedgerLoyalty,
					0, -ptsEarned, "loyalty reversal (synced void)", 0); err != nil {
					return err
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if order, err := s.GetOrder(orderID); err == nil {
		s.broadcast(EventOrderVoided, orderBroadcastPayload(order))
	}
	return nil
}

func (s *Service) applyDesign(payload json.RawMessage) error {
	var dj struct {
		Title        string `json:"title"`
		ProductName  string `json:"productName"`
		CustomerName string `json:"customerName"`
		Notes        string `json:"notes"`
		Status       string `json:"status"`
		UpdatedAt    string `json:"updatedAt"`
	}
	if err := json.Unmarshal(payload, &dj); err != nil {
		return err
	}
	// Design jobs sync best-effort by (title, customer) — enough for the
	// production board to stay aligned without a global id.
	var id int64
	err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM design_jobs WHERE title = ? AND customer_name = ?`),
		dj.Title, dj.CustomerName).Scan(&id)
	if err == nil {
		_, err = s.db.Exec(s.db.Rebind(`UPDATE design_jobs SET status = ?, notes = ?, updated_at = ? WHERE id = ?`),
			dj.Status, dj.Notes, dj.UpdatedAt, id)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	_, err = s.db.Exec(s.db.Rebind(`
                INSERT INTO design_jobs (title, product_name, customer_name, notes, status, created_by, created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, 'synced', ?, ?)`),
		dj.Title, dj.ProductName, dj.CustomerName, dj.Notes, dj.Status, dj.UpdatedAt, dj.UpdatedAt)
	return err
}

// applyShift replays a remote cash-drawer session: the shift appears in
// this device's history (admin visibility across tills), closes here when
// the owner device closes it, and never double-applies (client_uuid is
// the replay key, exactly like orders). The username resolves to a local
// user with the SAME fallback applyOrder uses for cashiers, so the X/Z
// report window lands on the same user the synced orders are attributed
// to. Replay of a close on an already-closed local copy is a no-op.

func (s *Service) applyShift(payload json.RawMessage) error {
	var ev struct {
		ClientUUID   string `json:"clientUuid"`
		UserName     string `json:"userName"`
		OpeningFloat int64  `json:"openingFloatCents"`
		Expected     int64  `json:"expectedCents"`
		Counted      int64  `json:"countedCents"`
		Variance     int64  `json:"varianceCents"`
		OpenedAt     string `json:"openedAt"`
		ClosedAt     string `json:"closedAt"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return err
	}
	if ev.ClientUUID == "" {
		return fmt.Errorf("shift event missing clientUuid")
	}
	var existing int64
	var localClosed string
	err := s.db.QueryRow(s.db.Rebind(`SELECT id, COALESCE(closed_at,'') FROM shifts WHERE client_uuid = ?`),
		ev.ClientUUID).Scan(&existing, &localClosed)
	if err == nil {
		// Known shift: the only meaningful replay is the remote close.
		if localClosed == "" && ev.ClosedAt != "" {
			_, err = s.db.Exec(s.db.Rebind(`UPDATE shifts SET expected_cents = ?, counted_cents = ?, variance_cents = ?, closed_at = ?
                                WHERE id = ? AND closed_at = ''`),
				ev.Expected, ev.Counted, ev.Variance, ev.ClosedAt, existing)
			if err != nil {
				return err
			}
			if sh, err := s.GetShift(existing); err == nil {
				s.broadcast(EventShiftUpdate, sh)
			}
		}
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	uid := s.resolveLocalCashierID(tx, ev.UserName)
	if uid == 0 {
		// A shift with no user to attribute to is still worth keeping
		// (the drawer math is real money) — but users.id is NOT NULL,
		// so mirror applyOrder's fallback contract: at least one user
		// always exists on a booted box.
		return fmt.Errorf("shift %s: no local user to attribute to", ev.ClientUUID)
	}
	if _, err := tx.Exec(s.db.Rebind(`
                INSERT INTO shifts (user_id, opening_float_cents, expected_cents, counted_cents, variance_cents, opened_at, closed_at, client_uuid)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
		uid, ev.OpeningFloat, ev.Expected, ev.Counted, ev.Variance, ev.OpenedAt, ev.ClosedAt, ev.ClientUUID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.broadcast(EventShiftUpdate, map[string]any{"synced": true, "userName": ev.UserName})
	return nil
}
