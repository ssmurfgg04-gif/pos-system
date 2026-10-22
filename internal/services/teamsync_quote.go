// teamsync_quote.go — quote sync: the quoteEvent wire shape, the emit
// builder and the pull-side apply handler. Split out of teamsync.go
// (file-level, same package — no behavior change).

package services

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"posapp/internal/models"
)

// ---- Quote sync ----

// quoteEvent is the wire form of a synced quote.

type quoteEvent struct {
	ClientUUID    string             `json:"clientUuid"`
	QuoteNo       string             `json:"quoteNo"`
	CustomerID    int64              `json:"customerId"`
	CustomerName  string             `json:"customerName"`
	CustomerPhone string             `json:"customerPhone"`
	Status        string             `json:"status"`
	Items         []models.QuoteItem `json:"items"`
	SubtotalCents int64              `json:"subtotalCents"`
	DiscountCents int64              `json:"discountCents"`
	DiscountLabel string             `json:"discountLabel"`
	TaxCents      int64              `json:"taxCents"`
	TotalCents    int64              `json:"totalCents"`
	Notes         string             `json:"notes"`
	ValidUntil    string             `json:"validUntil"`
	CreatedByName string             `json:"createdByName"`
	ConvertedHeld int64              `json:"convertedHeldSaleId"`
	CreatedAt     string             `json:"createdAt"`
	UpdatedAt     string             `json:"updatedAt"`
}

// EmitQuote queues the full quote document (create/update/status moves).
// synced_at emit-once bookkeeping is unnecessary here: the emit reads the
// CURRENT row, so a burst of edits collapses into the latest state and the
// apply side is idempotent by client_uuid + LWW anyway.

func (s *Service) EmitQuote(quoteID int64) {
	qy, err := s.GetQuote(quoteID)
	if err != nil {
		return
	}
	items := qy.Items
	if items == nil {
		items = []models.QuoteItem{}
	}
	s.Emit("quote", "upsert", quoteEvent{
		ClientUUID: qy.ClientUUID, QuoteNo: qy.QuoteNo,
		CustomerID: qy.CustomerID, CustomerName: qy.CustomerName, CustomerPhone: qy.CustomerPhone,
		Status: qy.Status, Items: items,
		SubtotalCents: qy.SubtotalCents, DiscountCents: qy.DiscountCents, DiscountLabel: qy.DiscountLabel,
		TaxCents: qy.TaxCents, TotalCents: qy.TotalCents,
		Notes: qy.Notes, ValidUntil: qy.ValidUntil,
		CreatedByName: qy.CreatedByName, ConvertedHeld: qy.ConvertedHeldSaleID,
		CreatedAt: qy.CreatedAt, UpdatedAt: qy.UpdatedAt,
	})
}

// applyQuote merges a remote quote. Identity is client_uuid (insert exactly
// once, like orders); a re-delivered event is an LWW merge on updated_at so
// late status moves still land. Quote numbers are display-only across
// devices: on collision the local copy is renumbered with a short suffix.

func (s *Service) applyQuote(payload json.RawMessage) error {
	var qe quoteEvent
	if err := json.Unmarshal(payload, &qe); err != nil {
		return err
	}
	if qe.ClientUUID == "" {
		return fmt.Errorf("quote event missing clientUuid")
	}
	raw, err := json.Marshal(qe.Items)
	if err != nil {
		return err
	}
	if raw == nil {
		raw = []byte("[]")
	}
	// Resolve the customer by phone (ids differ across devices).
	var custID int64
	if qe.CustomerPhone != "" {
		s.db.QueryRow(s.db.Rebind(`SELECT id FROM customers WHERE phone = ?`), qe.CustomerPhone).Scan(&custID)
	}
	var existing int64
	err = s.db.QueryRow(s.db.Rebind(`SELECT id FROM quotes WHERE client_uuid = ?`), qe.ClientUUID).Scan(&existing)
	if err == nil {
		// Known quote: apply only when the event is newer (LWW).
		var localUpdated string
		if err := s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(updated_at,'') FROM quotes WHERE id = ?`), existing).Scan(&localUpdated); err != nil {
			return err
		}
		if qe.UpdatedAt <= localUpdated {
			return nil
		}
		_, err := s.db.Exec(s.db.Rebind(`
                        UPDATE quotes SET customer_id = ?, customer_name = ?, customer_phone = ?, status = ?,
                                items_json = ?, subtotal_cents = ?, discount_cents = ?, discount_label = ?,
                                tax_cents = ?, total_cents = ?, notes = ?, valid_until = ?,
                                converted_held_sale_id = ?, updated_at = ?
                        WHERE id = ?`),
			custID, qe.CustomerName, qe.CustomerPhone, qe.Status, string(raw),
			qe.SubtotalCents, qe.DiscountCents, qe.DiscountLabel, qe.TaxCents, qe.TotalCents,
			qe.Notes, qe.ValidUntil, qe.ConvertedHeld, qe.UpdatedAt, existing)
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	// Quote number collision across tills: suffix until unique.
	qno := qe.QuoteNo
	for attempt := 0; ; attempt++ {
		var n int
		s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM quotes WHERE quote_no = ?`), qno).Scan(&n)
		if n == 0 {
			break
		}
		if attempt >= 8 {
			return fmt.Errorf("quote number %s: cannot allocate a free suffix", qe.QuoteNo)
		}
		qno = fmt.Sprintf("%s-%s", qe.QuoteNo, strings.ToUpper(randToken(2+attempt/3)))
	}
	_, err = s.db.Exec(s.db.Rebind(`
                INSERT INTO quotes (quote_no, client_uuid, customer_id, customer_name, customer_phone,
                        status, items_json, subtotal_cents, discount_cents, discount_label, tax_cents,
                        total_cents, notes, valid_until, created_by, created_by_name, converted_held_sale_id,
                        created_at, updated_at, synced_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?)`),
		qno, qe.ClientUUID, custID, qe.CustomerName, qe.CustomerPhone,
		qe.Status, string(raw), qe.SubtotalCents, qe.DiscountCents, qe.DiscountLabel,
		qe.TaxCents, qe.TotalCents, qe.Notes, qe.ValidUntil, qe.CreatedByName,
		qe.ConvertedHeld, qe.CreatedAt, qe.UpdatedAt, nowStamp())
	return err
}

// resolveLocalCashierID finds a users.id for a synced order's cashier:
// the same username when this device knows it, else any active user
// (the receiving till owns the record — attribution stays informative).

func (s *Service) resolveLocalCashierID(tx *sql.Tx, username string) int64 {
	if username != "" {
		var id int64
		if err := tx.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id); err == nil {
			return id
		}
	}
	var id int64
	if err := tx.QueryRow(`SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&id); err == nil {
		return id
	}
	return 0
}
