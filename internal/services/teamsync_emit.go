// teamsync_emit.go — the push-side payload builders: EmitOrder,
// EmitProduct, EmitStockDelta, EmitCustomer, EmitLedger, EmitCategory,
// EmitDesign, EmitConfig, EmitVoidReason, EmitQuote. Split out of
// teamsync.go (file-level, same package — no behavior change).

package services

// ---- Emit payload builders ----

// EmitOrder builds and queues the full order event (checkout + completions).

func (s *Service) EmitOrder(orderID int64) {
	order, err := s.GetOrder(orderID)
	if err != nil {
		return
	}
	phone := ""
	if order.CustomerID != 0 {
		if c, err := s.GetCustomer(order.CustomerID); err == nil {
			phone = c.Phone
		}
	}
	oe := orderEvent{
		DeviceID:   s.deviceID(),
		ClientUUID: order.ClientUUID, Number: order.Number, Status: order.Status,
		SubtotalCents: order.SubtotalCents, DiscountCents: order.DiscountCents,
		DiscountLabel: order.DiscountLabel, PointsRedeemed: order.PointsRedeemed,
		TaxCents: order.TaxCents, TotalCents: order.TotalCents,
		TaxPercent: order.TaxPercent, TaxIncluded: order.TaxIncluded,
		CashierName: order.CashierName, CustomerName: order.CustomerName,
		CustomerPhone: phone, Note: order.Note, BuyerPin: order.BuyerPIN,
		InvoiceNumber: order.InvoiceNumber,
		CreatedAt:     order.CreatedAt, PaidAt: order.PaidAt,
		Items: []struct {
			SKU       string `json:"sku"`
			Name      string `json:"name"`
			Qty       int    `json:"qty"`
			UnitPrice int64  `json:"unitPriceCents"`
			LineTotal int64  `json:"lineTotalCents"`
		}{},
		Payments: []struct {
			Method      string `json:"method"`
			Amount      int64  `json:"amountCents"`
			Status      string `json:"status"`
			Receipt     string `json:"mpesaReceipt"`
			Phone       string `json:"phone"`
			CreatedAt   string `json:"createdAt"`
			CompletedAt string `json:"completedAt"`
		}{},
	}
	for _, it := range order.Items {
		oe.Items = append(oe.Items, struct {
			SKU       string `json:"sku"`
			Name      string `json:"name"`
			Qty       int    `json:"qty"`
			UnitPrice int64  `json:"unitPriceCents"`
			LineTotal int64  `json:"lineTotalCents"`
		}{it.SKU, it.Name, it.Qty, it.UnitPriceCents, it.LineTotalCents})
	}
	for _, pm := range order.Payments {
		oe.Payments = append(oe.Payments, struct {
			Method      string `json:"method"`
			Amount      int64  `json:"amountCents"`
			Status      string `json:"status"`
			Receipt     string `json:"mpesaReceipt"`
			Phone       string `json:"phone"`
			CreatedAt   string `json:"createdAt"`
			CompletedAt string `json:"completedAt"`
		}{pm.Method, pm.AmountCents, pm.Status, pm.MpesaReceipt, pm.Phone, pm.CreatedAt, pm.CompletedAt})
	}
	s.Emit("order", "upsert", oe)
}

// EmitVoid queues a void event for cross-device cancellation.

func (s *Service) EmitVoid(orderID int64, reason string) {
	var uuid, number string
	s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(client_uuid,''), number FROM orders WHERE id = ?`), orderID).Scan(&uuid, &number)
	s.Emit("void", "upsert", map[string]any{
		"orderClientUuid": uuid,
		"orderNumber":     number,
		"voidedAt":        nowStamp(),
		"reason":          reason,
	})
}

// EmitProduct queues the full catalog row (admin edits; stock_set marks
// absolute stock changes so other devices follow the last admin write).

func (s *Service) EmitProduct(productID int64, stockSet bool) {
	var pr struct {
		SKU     string
		Barcode string
		Name    string
		Slug    string
		Price   int64
		Cost    int64
		Stock   int
		Track   int
		Active  int
		Gift    int
	}
	err := s.db.QueryRow(s.db.Rebind(`
                SELECT COALESCE(p.sku,''), COALESCE(p.barcode,''), p.name, COALESCE(c.slug,''),
                        p.price_cents, p.cost_cents, p.stock_qty, COALESCE(p.track_stock,1), COALESCE(p.is_active,1), COALESCE(p.is_gift_card,0)
                FROM products p LEFT JOIN categories c ON c.id = p.category_id WHERE p.id = ?`), productID).
		Scan(&pr.SKU, &pr.Barcode, &pr.Name, &pr.Slug, &pr.Price, &pr.Cost, &pr.Stock, &pr.Track, &pr.Active, &pr.Gift)
	if err != nil {
		return
	}
	s.Emit("product", "upsert", map[string]any{
		"sku": pr.SKU, "barcode": pr.Barcode, "name": pr.Name, "categorySlug": pr.Slug,
		"priceCents": pr.Price, "costCents": pr.Cost, "stockQty": pr.Stock,
		"stockSet": stockSet, "trackStock": pr.Track == 1, "active": pr.Active == 1,
		"isGiftCard": pr.Gift == 1, "updatedAt": nowStamp(),
	})
}

// EmitStockDelta queues a signed stock movement (sales use per-line deltas
// inside the order event; this covers adjusts, PO receives, stock takes).

func (s *Service) EmitStockDelta(sku string, delta int) {
	if sku == "" || delta == 0 {
		return
	}
	s.Emit("stock", "upsert", map[string]any{"sku": sku, "delta": delta})
}

// EmitCustomer queues the identity row (balances ride on ledger events).

func (s *Service) EmitCustomer(customerID int64) {
	var name, phone, email, notes string
	var limit int64
	var active int
	err := s.db.QueryRow(s.db.Rebind(`SELECT name, COALESCE(phone,''), COALESCE(email,''), COALESCE(notes,''), credit_limit_cents, COALESCE(is_active,1) FROM customers WHERE id = ?`), customerID).
		Scan(&name, &phone, &email, &notes, &limit, &active)
	if err != nil {
		return
	}
	s.Emit("customer", "upsert", map[string]any{
		"name": name, "phone": phone, "email": email, "notes": notes,
		"creditLimitCents": limit, "active": active == 1, "updatedAt": nowStamp(),
	})
}

// emitLedgerForOrder pushes every not-yet-synced ledger movement recorded
// for an order (checkout tab charges, credit/loyalty redemptions, the
// completion earn). customer_ledger.synced_at makes each row emit exactly
// once — a duplicate would double-apply money on every receiving till.
// Void reversals are deliberately NOT emitted: applyVoid recomputes them
// on the receiver (emitting them too would double-reverse).

func (s *Service) emitLedgerForOrder(orderID int64) {
	rows, err := s.db.Query(s.db.Rebind(`SELECT id, customer_id, kind, amount_cents, points_delta, COALESCE(note,'')
                FROM customer_ledger WHERE order_id = ? AND COALESCE(synced_at,'') = ''`), orderID)
	if err != nil {
		return
	}
	type lr struct {
		custID         int64
		kind, note     string
		amount, points int64
	}
	var pending []lr
	var ids []int64
	for rows.Next() {
		var r lr
		var id int64
		if rows.Scan(&id, &r.custID, &r.kind, &r.amount, &r.points, &r.note) == nil {
			pending = append(pending, r)
			ids = append(ids, id)
		}
	}
	rows.Close()
	for i, r := range pending {
		s.EmitLedger(r.custID, r.kind, r.amount, r.points, r.note)
		s.db.Exec(s.db.Rebind(`UPDATE customer_ledger SET synced_at = ? WHERE id = ?`), nowStamp(), ids[i])
	}
}

// EmitLedger queues a balance-moving ledger row.

func (s *Service) EmitLedger(customerID int64, kind string, amount, points int64, note string) {
	var phone, name string
	s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(phone,''), name FROM customers WHERE id = ?`), customerID).Scan(&phone, &name)
	s.Emit("ledger", "upsert", map[string]any{
		"phone": phone, "name": name, "kind": kind,
		"amountCents": amount, "pointsDelta": points, "note": note, "createdAt": nowStamp(),
	})
}

// EmitCategory queues a category upsert/delete.

func (s *Service) EmitCategory(name, slug string, delete bool) {
	if delete {
		s.Emit("category", "delete", map[string]any{"slug": slug, "name": name})
		return
	}
	s.Emit("category", "upsert", map[string]any{"slug": slug, "name": name})
}

// EmitDesign queues a design-board change.

func (s *Service) EmitDesign(title, productName, customerName, notes, status string) {
	s.Emit("design", "upsert", map[string]any{
		"title": title, "productName": productName, "customerName": customerName,
		"notes": notes, "status": status, "updatedAt": nowStamp(),
	})
}
