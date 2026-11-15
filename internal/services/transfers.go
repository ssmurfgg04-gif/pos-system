package services

// transfers.go — stock transfers between the shops of one business
// (v1.12.0; cloud side db/deploy-v1120-transfers.sql).
//
// The cloud holds THE record of every transfer; each till applies only its
// own shop's side, exactly once, guarded by a unique local row:
//
//   out     the sending till took the stock off its shelf (one tx with the
//           ledger rows) and queued the transfer; the sync loop sends it
//           until the cloud has it (idempotent by client_ref)
//   in      the receiving till won the cloud claim and added what arrived
//   cancel  the stock went back on the sender's shelf (cancelled before
//           anyone received it, or the cloud refused the transfer for good)
//
// Crash safety: the cloud claim/cancel comes first and remembers WHICH
// till did it (received_device / cancelled_device); if the till dies before
// its local apply, the next list on that till applies it. Only that till
// ever applies, so two tills of one shop can never both add the stock (the
// stock itself then reaches the shop's other tills as normal stock events).
//
// Products are matched on the receiving side by SKU, then barcode; one the
// shop has never stocked is created from the sender's product details.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"posapp/internal/auth"
	"posapp/internal/models"
)

var ErrTransfersOffline = errors.New("stock transfers need this till linked to the cloud (Settings → Team)")

type TransferLine struct {
	SKU           string `json:"sku"`
	Barcode       string `json:"barcode,omitempty"`
	Name          string `json:"name"`
	Unit          string `json:"unit,omitempty"`
	AllowFraction bool   `json:"allowFraction,omitempty"`
	QtyMilli      int64  `json:"qtyMilli"`
	PriceCents    int64  `json:"priceCents"`
	CostCents     int64  `json:"costCents"`
	TaxType       string `json:"taxType,omitempty"`
	ClsCd         string `json:"clsCd,omitempty"`
}

type TransferQty struct {
	SKU      string `json:"sku"`
	QtyMilli int64  `json:"qtyMilli"`
}

type Transfer struct {
	ID            string         `json:"id"`
	ClientRef     string         `json:"clientRef"`
	Direction     string         `json:"direction"` // out | in (from this shop's view)
	FromTeam      string         `json:"fromTeam"`
	FromName      string         `json:"fromName"`
	ToTeam        string         `json:"toTeam"`
	ToName        string         `json:"toName"`
	Status        string         `json:"status"` // queued | sent | received | cancelled | rejected
	Lines         []TransferLine `json:"lines"`
	ReceivedLines []TransferQty  `json:"receivedLines"`
	Note          string         `json:"note"`
	SentBy        string         `json:"sentBy"`
	SentAt        string         `json:"sentAt"`
	ReceivedBy    string         `json:"receivedBy"`
	ReceivedAt    string         `json:"receivedAt"`
	CancelledBy   string         `json:"cancelledBy"`
	CancelledAt   string         `json:"cancelledAt"`
	LastError     string         `json:"lastError"`
	// ShortMilli: sent minus received, summed over the lines (lost or
	// damaged on the way).
	ShortMilli int64 `json:"shortMilli"`
}

type TransferShop struct {
	TeamCode string `json:"teamCode"`
	Name     string `json:"name"`
}

type TransferList struct {
	Online    bool           `json:"online"`
	Error     string         `json:"error"`
	MyTeam    string         `json:"myTeam"`
	Shops     []TransferShop `json:"shops"`
	Transfers []Transfer     `json:"transfers"`
}

type SendTransferRequest struct {
	ToTeam string `json:"toTeam"`
	Note   string `json:"note"`
	Lines  []struct {
		ProductID int64 `json:"productId"`
		QtyMilli  int64 `json:"qtyMilli"`
	} `json:"lines"`
}

// ---- cloud calls -----------------------------------------------------------

type transferRPCFunc func(fn string, args map[string]any, out any) error

type transferCloud struct {
	team, device string
	call         transferRPCFunc
}

// transferCloudClient resolves the cloud link (tests inject transferRPCForTest).
func (s *Service) transferCloudClient() (*transferCloud, error) {
	if s.transferRPCForTest != nil {
		return &transferCloud{team: s.settings.Get("sync_team_code"), device: s.deviceID(), call: s.transferRPCForTest}, nil
	}
	c, ok := s.syncConfig()
	if !ok || c.mode != "rpc" || c.team == "" {
		return nil, ErrTransfersOffline
	}
	return &transferCloud{team: c.team, device: c.device, call: func(fn string, args map[string]any, out any) error {
		args["p_device_id"] = c.device
		args["p_secret_hash"] = c.secretHash
		return rpcCall(c.base, fn, args, out)
	}}, nil
}

// cloudTransfer is the RPC's row shape.
type cloudTransfer struct {
	ID              string          `json:"id"`
	FromTeam        string          `json:"from_team"`
	ToTeam          string          `json:"to_team"`
	FromName        string          `json:"from_name"`
	ToName          string          `json:"to_name"`
	ClientRef       string          `json:"client_ref"`
	Lines           []TransferLine  `json:"lines"`
	Note            string          `json:"note"`
	Status          string          `json:"status"`
	SentBy          string          `json:"sent_by"`
	SentAt          string          `json:"sent_at"`
	ReceivedBy      string          `json:"received_by"`
	ReceivedDevice  string          `json:"received_device"`
	ReceivedLines   []TransferQty   `json:"received_lines"`
	ReceivedAt      string          `json:"received_at"`
	CancelledBy     string          `json:"cancelled_by"`
	CancelledDevice string          `json:"cancelled_device"`
	CancelledAt     string          `json:"cancelled_at"`
	Extra           json.RawMessage `json:"-"`
}

type transferAnswer struct {
	OK       bool          `json:"ok"`
	Final    bool          `json:"final"`
	Error    string        `json:"error"`
	Existing bool          `json:"existing"`
	Transfer cloudTransfer `json:"transfer"`
}

func (ct cloudTransfer) view(myTeam string) Transfer {
	t := Transfer{
		ID: ct.ID, ClientRef: ct.ClientRef, FromTeam: ct.FromTeam, FromName: ct.FromName, ToTeam: ct.ToTeam, ToName: ct.ToName,
		Status: ct.Status, Lines: ct.Lines, ReceivedLines: ct.ReceivedLines, Note: ct.Note, SentBy: ct.SentBy, SentAt: ct.SentAt,
		ReceivedBy: ct.ReceivedBy, ReceivedAt: ct.ReceivedAt, CancelledBy: ct.CancelledBy, CancelledAt: ct.CancelledAt,
	}
	t.Direction = "in"
	if ct.FromTeam == myTeam {
		t.Direction = "out"
	}
	if t.Lines == nil {
		t.Lines = []TransferLine{}
	}
	if t.ReceivedLines == nil {
		t.ReceivedLines = []TransferQty{}
	} else {
		for i, l := range t.Lines {
			if i < len(t.ReceivedLines) {
				t.ShortMilli += l.QtyMilli - t.ReceivedLines[i].QtyMilli
			}
		}
	}
	return t
}

// ---- stock moves -----------------------------------------------------------

// moveStockTx applies a signed delta to one product inside tx (strict: a
// removal larger than the shelf is refused, never clamped), with the
// ledger row. Batch products move through their lots (FEFO out).
func (s *Service) moveStockTx(tx *sql.Tx, productID, delta int64, kind, ref, note string, userID int64, username string) error {
	if delta == 0 {
		return nil
	}
	var batches int
	var stock int64
	if err := tx.QueryRow(s.db.Rebind(`SELECT COALESCE(track_batches,0), stock_qty_milli FROM products WHERE id = ?`), productID).Scan(&batches, &stock); err != nil {
		return ErrNotFound
	}
	if delta > 0 && stock+delta > models.MaxStockQtyMilli {
		return fmt.Errorf("%w: stock would exceed the maximum", ErrInvalidInput)
	}
	if batches == 1 {
		if err := adjustBatchStockTx(s, tx, productID, delta); err != nil {
			return err
		}
	}
	res, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty_milli = stock_qty_milli + ?, stock_qty = (stock_qty_milli + ?) / 1000, updated_at = ?
		WHERE id = ? AND stock_qty_milli + ? >= 0`), delta, delta, nowStamp(), productID, delta)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: not enough stock", ErrInsufficientStock)
	}
	return recordStockMoveTx(s, tx, productID, delta, kind, ref, note, userID, username)
}

type appliedMove struct {
	productID int64
	sku       string
	delta     int64
	created   bool
}

func (s *Service) afterMoves(moves []appliedMove) {
	for _, m := range moves {
		s.EmitProduct(m.productID, false)
		s.EmitStockDelta(m.sku, m.delta)
	}
}

// ---- send ------------------------------------------------------------------

// SendTransfer takes the goods off this shop's shelf now and queues the
// transfer for the cloud (it goes as soon as the till is online).
func (s *Service) SendTransfer(req SendTransferRequest, p *auth.Principal) (*Transfer, error) {
	cloud, err := s.transferCloudClient()
	if err != nil {
		return nil, err
	}
	req.ToTeam = strings.TrimSpace(req.ToTeam)
	if req.ToTeam == "" || req.ToTeam == cloud.team {
		return nil, fmt.Errorf("%w: pick the shop to send to", ErrInvalidInput)
	}
	if len(req.Lines) == 0 || len(req.Lines) > 500 {
		return nil, fmt.Errorf("%w: a transfer needs 1 to 500 lines", ErrInvalidInput)
	}
	toName := ""
	if shops, err := s.transferShops(cloud); err == nil {
		found := false
		for _, sh := range shops {
			if sh.TeamCode == req.ToTeam {
				found, toName = true, sh.Name
			}
		}
		if !found {
			return nil, fmt.Errorf("%w: that shop is not part of your business", ErrInvalidInput)
		}
	} // offline: the cloud checks the shop when the transfer goes out

	s.transferMu.Lock()
	defer s.transferMu.Unlock()
	ref := "TR-" + time.Now().In(eastAfrica).Format("20060102") + "-" + randToken(8)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	seen := map[int64]bool{}
	lines := make([]TransferLine, 0, len(req.Lines))
	moves := []appliedMove{}
	for _, rl := range req.Lines {
		if rl.QtyMilli <= 0 {
			return nil, fmt.Errorf("%w: quantity must be positive", ErrInvalidInput)
		}
		if seen[rl.ProductID] {
			return nil, fmt.Errorf("%w: a product is on the transfer twice", ErrInvalidInput)
		}
		seen[rl.ProductID] = true
		var l TransferLine
		var track, gift, frac int
		var stock int64
		err := tx.QueryRow(s.db.Rebind(`SELECT COALESCE(sku,''), COALESCE(barcode,''), name, COALESCE(unit,'pc'), COALESCE(allow_fraction,0),
			price_cents, cost_cents, COALESCE(etims_tax_type,''), COALESCE(etims_cls_cd,''), COALESCE(track_stock,1), COALESCE(is_gift_card,0), stock_qty_milli
			FROM products WHERE id = ?`), rl.ProductID).
			Scan(&l.SKU, &l.Barcode, &l.Name, &l.Unit, &frac, &l.PriceCents, &l.CostCents, &l.TaxType, &l.ClsCd, &track, &gift, &stock)
		if err != nil {
			return nil, fmt.Errorf("%w: product %d", ErrNotFound, rl.ProductID)
		}
		if track != 1 || gift == 1 || l.SKU == "" {
			return nil, fmt.Errorf("%w: %s does not track stock", ErrInvalidInput, l.Name)
		}
		l.AllowFraction = frac == 1
		if !l.AllowFraction && rl.QtyMilli%1000 != 0 {
			return nil, fmt.Errorf("%w: %s is sold in whole units", ErrInvalidInput, l.Name)
		}
		if rl.QtyMilli > stock {
			return nil, fmt.Errorf("%w: %s has %s, cannot send %s", ErrInsufficientStock, l.Name, qtyStr(stock), qtyStr(rl.QtyMilli))
		}
		l.QtyMilli = rl.QtyMilli
		if err := s.moveStockTx(tx, rl.ProductID, -rl.QtyMilli, models.MoveTransferOut, ref, truncStr("to "+firstNonEmpty(toName, req.ToTeam), 200), p.ID, p.Username); err != nil {
			return nil, err
		}
		moves = append(moves, appliedMove{productID: rl.ProductID, sku: l.SKU, delta: -rl.QtyMilli})
		lines = append(lines, l)
	}
	raw, _ := json.Marshal(lines)
	now := nowStamp()
	if _, err := tx.Exec(s.db.Rebind(`INSERT INTO stock_transfers (client_ref, direction, other_team, other_name, status, lines_json, note, created_by_name, applied, created_at, updated_at)
		VALUES (?, 'out', ?, ?, 'queued', ?, ?, ?, 1, ?, ?)`), ref, req.ToTeam, toName, string(raw), truncStr(strings.TrimSpace(req.Note), 300), p.Username, now, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.afterMoves(moves)
	s.Audit(p.ID, p.Username, "TRANSFER_SENT", "transfer", ref, fmt.Sprintf("to %s, %d lines", req.ToTeam, len(lines)))
	s.pushTransfersLocked(cloud)
	t := s.localTransfer(ref)
	if t == nil {
		return nil, ErrNotFound
	}
	return t, nil
}

func qtyStr(milli int64) string {
	if milli%1000 == 0 {
		return fmt.Sprint(milli / 1000)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%d.%03d", milli/1000, milli%1000), "0"), ".")
}

func (s *Service) localTransfer(ref string) *Transfer {
	var t Transfer
	var linesRaw string
	err := s.db.QueryRow(s.db.Rebind(`SELECT client_ref, other_team, other_name, status, lines_json, note, created_by_name, created_at, last_error
		FROM stock_transfers WHERE direction = 'out' AND client_ref = ?`), ref).
		Scan(&t.ClientRef, &t.ToTeam, &t.ToName, &t.Status, &linesRaw, &t.Note, &t.SentBy, &t.SentAt, &t.LastError)
	if err != nil {
		return nil
	}
	t.Direction = "out"
	_ = json.Unmarshal([]byte(linesRaw), &t.Lines)
	t.ReceivedLines = []TransferQty{}
	return &t
}

// PushTransfers sends queued transfers (called by the sync loop).
func (s *Service) PushTransfers() {
	cloud, err := s.transferCloudClient()
	if err != nil {
		return
	}
	s.transferMu.Lock()
	defer s.transferMu.Unlock()
	s.pushTransfersLocked(cloud)
}

func (s *Service) pushTransfersLocked(cloud *transferCloud) {
	rows, err := s.db.Query(`SELECT client_ref, other_team, lines_json, note, created_by_name FROM stock_transfers WHERE direction = 'out' AND status = 'queued' ORDER BY id LIMIT 20`)
	if err != nil {
		return
	}
	type q struct{ ref, to, lines, note, by string }
	var queue []q
	for rows.Next() {
		var r q
		if rows.Scan(&r.ref, &r.to, &r.lines, &r.note, &r.by) == nil {
			queue = append(queue, r)
		}
	}
	rows.Close()
	for _, r := range queue {
		var lines []TransferLine
		_ = json.Unmarshal([]byte(r.lines), &lines)
		var ans transferAnswer
		err := cloud.call("transfer_send", map[string]any{
			"p_client_ref": r.ref, "p_to_team": r.to, "p_lines": lines, "p_note": r.note, "p_user": r.by,
		}, &ans)
		now := nowStamp()
		switch {
		case err != nil || (!ans.OK && !ans.Final):
			msg := ans.Error
			if err != nil {
				msg = err.Error()
			}
			s.db.Exec(s.db.Rebind(`UPDATE stock_transfers SET last_error = ?, updated_at = ? WHERE direction = 'out' AND client_ref = ?`), truncStr(msg, 200), now, r.ref)
		case ans.OK && ans.Transfer.Status == "cancelled":
			// a cancel got there first (tombstone): the stock comes back
			s.restoreTransferLocked(r.ref, lines, "cancelled", "")
		case ans.OK:
			s.db.Exec(s.db.Rebind(`UPDATE stock_transfers SET status = 'sent', other_name = ?, sent_at = ?, last_error = '', updated_at = ?
				WHERE direction = 'out' AND client_ref = ? AND status = 'queued'`), ans.Transfer.ToName, firstNonEmpty(ans.Transfer.SentAt, now), now, r.ref)
		default: // refused for good: the goods go back on the shelf
			s.restoreTransferLocked(r.ref, lines, "rejected", ans.Error)
		}
	}
}

// restoreTransferLocked puts a transfer's stock back on this shelf, once.
func (s *Service) restoreTransferLocked(ref string, lines []TransferLine, status, why string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowStamp()
	if _, err := tx.Exec(s.db.Rebind(`INSERT INTO stock_transfers (client_ref, direction, status, lines_json, applied, created_at, updated_at)
		VALUES (?, 'cancel', ?, '[]', 1, ?, ?)`), ref, status, now, now); err != nil {
		if isUniqueViolation(err) {
			return nil // already restored
		}
		return err
	}
	moves := []appliedMove{}
	for _, l := range lines {
		pid, err := s.productForLineTx(tx, l, false)
		if err != nil || pid == 0 {
			continue // product deleted since: nothing to put back on
		}
		if err := s.moveStockTx(tx, pid, l.QtyMilli, models.MoveTransferReturn, ref, truncStr("transfer "+status, 200), 0, "system"); err != nil {
			return err
		}
		moves = append(moves, appliedMove{productID: pid, sku: l.SKU, delta: l.QtyMilli})
	}
	if _, err := tx.Exec(s.db.Rebind(`UPDATE stock_transfers SET status = ?, last_error = ?, updated_at = ? WHERE direction = 'out' AND client_ref = ?`),
		status, truncStr(why, 200), now, ref); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.afterMoves(moves)
	s.Audit(0, "system", "TRANSFER_RESTORED", "transfer", ref, status+" "+why)
	return nil
}

// productForLineTx finds the shop's product for a transfer line (SKU, then
// barcode); with create it makes one from the line's details.
func (s *Service) productForLineTx(tx *sql.Tx, l TransferLine, create bool) (int64, error) {
	var id int64
	err := tx.QueryRow(s.db.Rebind(`SELECT id FROM products WHERE sku = ?`), l.SKU).Scan(&id)
	if err == nil {
		return id, nil
	}
	if l.Barcode != "" {
		if err := tx.QueryRow(s.db.Rebind(`SELECT id FROM products WHERE barcode = ? ORDER BY id LIMIT 1`), l.Barcode).Scan(&id); err == nil {
			return id, nil
		}
	}
	if !create {
		return 0, nil
	}
	var cat int64
	if err := tx.QueryRow(s.db.Rebind(`SELECT id FROM categories WHERE slug = ?`), "transfers").Scan(&cat); err != nil {
		if _, err := tx.Exec(s.db.Rebind(`INSERT INTO categories (name, slug) VALUES (?, ?)`), "Received from other shops", "transfers"); err != nil {
			return 0, err
		}
		if err := tx.QueryRow(s.db.Rebind(`SELECT id FROM categories WHERE slug = ?`), "transfers").Scan(&cat); err != nil {
			return 0, err
		}
	}
	frac := 0
	if l.AllowFraction {
		frac = 1
	}
	taxType := l.TaxType
	if !strings.Contains("ABCDE", taxType) || len(taxType) > 1 {
		taxType = ""
	}
	unit := l.Unit
	if unit == "" {
		unit = "pc"
	}
	now := nowStamp()
	if _, err := tx.Exec(s.db.Rebind(`INSERT INTO products (sku, barcode, name, category_id, price_cents, cost_cents, stock_qty, stock_qty_milli, track_stock,
		unit, allow_fraction, etims_tax_type, etims_cls_cd, is_active, updated_at) VALUES (?, ?, ?, ?, ?, ?, 0, 0, 1, ?, ?, ?, ?, 1, ?)`),
		truncStr(l.SKU, 64), truncStr(l.Barcode, 64), truncStr(firstNonEmpty(l.Name, l.SKU), 200), cat, max64(0, l.PriceCents), max64(0, l.CostCents),
		truncStr(unit, 12), frac, taxType, truncStr(l.ClsCd, 10), now); err != nil {
		return 0, err
	}
	if err := tx.QueryRow(s.db.Rebind(`SELECT id FROM products WHERE sku = ?`), l.SKU).Scan(&id); err != nil {
		return 0, err
	}
	if err := recordStockMoveTx(s, tx, id, 0, models.MoveOpening, "", "created by a stock transfer", 0, "system"); err != nil {
		return 0, err
	}
	return id, nil
}

// ---- list / receive / cancel -----------------------------------------------

func (s *Service) transferShops(cloud *transferCloud) ([]TransferShop, error) {
	var out struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Stores []struct {
			Name     string `json:"name"`
			TeamCode string `json:"team_code"`
		} `json:"stores"`
	}
	if err := cloud.call("sync_list_stores", map[string]any{}, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, errors.New(out.Error)
	}
	shops := []TransferShop{}
	for _, st := range out.Stores {
		if st.TeamCode != cloud.team {
			shops = append(shops, TransferShop{TeamCode: st.TeamCode, Name: st.Name})
		}
	}
	return shops, nil
}

func (s *Service) cloudTransfers(cloud *transferCloud) ([]cloudTransfer, error) {
	var out struct {
		OK        bool            `json:"ok"`
		Error     string          `json:"error"`
		Transfers []cloudTransfer `json:"transfers"`
	}
	if err := cloud.call("transfer_list", map[string]any{"p_days": 90}, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, errors.New(out.Error)
	}
	return out.Transfers, nil
}

// ListTransfers shows this shop's transfers (cloud + anything still
// queued here) and finishes any apply a crash interrupted.
func (s *Service) ListTransfers() (*TransferList, error) {
	res := &TransferList{Shops: []TransferShop{}, Transfers: []Transfer{}}
	cloud, err := s.transferCloudClient()
	if err != nil {
		res.Error = err.Error()
		return res, nil
	}
	res.MyTeam = cloud.team
	s.transferMu.Lock()
	defer s.transferMu.Unlock()
	s.pushTransfersLocked(cloud)
	inCloud := map[string]bool{}
	if shops, err := s.transferShops(cloud); err == nil {
		res.Shops = shops
	}
	list, err := s.cloudTransfers(cloud)
	if err != nil {
		res.Error = "cloud unreachable: " + err.Error()
	} else {
		res.Online = true
		for _, ct := range list {
			s.reconcileTransferLocked(cloud, ct)
			if ct.FromTeam == cloud.team {
				inCloud[ct.ClientRef] = true
			}
			t := ct.view(cloud.team)
			if t.Direction == "out" {
				if lt := s.localTransfer(ct.ClientRef); lt != nil && lt.Status == "rejected" {
					t.Status, t.LastError = "rejected", lt.LastError
				}
			}
			res.Transfers = append(res.Transfers, t)
		}
	}
	// queued (or refused) here and not yet in the cloud list
	rows, err := s.db.Query(`SELECT client_ref FROM stock_transfers WHERE direction = 'out' AND status IN ('queued', 'rejected') ORDER BY id DESC LIMIT 100`)
	if err == nil {
		var refs []string
		for rows.Next() {
			var r string
			if rows.Scan(&r) == nil && !inCloud[r] {
				refs = append(refs, r)
			}
		}
		rows.Close()
		for _, r := range refs {
			if t := s.localTransfer(r); t != nil {
				t.FromTeam = cloud.team
				res.Transfers = append([]Transfer{*t}, res.Transfers...)
			}
		}
	}
	return res, nil
}

// reconcileTransferLocked applies what this till won in the cloud but has
// not applied yet (crash between the cloud answer and the local commit).
func (s *Service) reconcileTransferLocked(cloud *transferCloud, ct cloudTransfer) {
	switch {
	case ct.ToTeam == cloud.team && ct.Status == "received" && ct.ReceivedDevice == cloud.device:
		_ = s.applyIncomingLocked(ct)
	case ct.FromTeam == cloud.team && ct.Status == "cancelled" && ct.CancelledDevice == cloud.device:
		lines := ct.Lines
		if len(lines) == 0 { // tombstone: the lines are only here
			if lt := s.localTransfer(ct.ClientRef); lt != nil {
				lines = lt.Lines
			}
		}
		_ = s.restoreTransferLocked(ct.ClientRef, lines, "cancelled", "")
	case ct.FromTeam == cloud.team && ct.Status != "cancelled":
		s.db.Exec(s.db.Rebind(`UPDATE stock_transfers SET status = ?, updated_at = ? WHERE direction = 'out' AND client_ref = ? AND status IN ('queued', 'sent')`),
			ct.Status, nowStamp(), ct.ClientRef)
	}
}

// ReceiveTransfer claims an incoming transfer and adds what arrived.
// received: per SKU what actually arrived (missing = all of it).
func (s *Service) ReceiveTransfer(id string, received []TransferQty, p *auth.Principal) (*Transfer, error) {
	cloud, err := s.transferCloudClient()
	if err != nil {
		return nil, err
	}
	s.transferMu.Lock()
	defer s.transferMu.Unlock()
	list, err := s.cloudTransfers(cloud)
	if err != nil {
		return nil, fmt.Errorf("cloud unreachable, try again when online: %w", err)
	}
	var ct *cloudTransfer
	for i := range list {
		if list[i].ID == id && list[i].ToTeam == cloud.team {
			ct = &list[i]
		}
	}
	if ct == nil {
		return nil, ErrNotFound
	}
	got := map[string]int64{}
	given := map[string]bool{}
	for _, r := range received {
		if r.QtyMilli < 0 {
			return nil, fmt.Errorf("%w: received quantity cannot be negative", ErrInvalidInput)
		}
		got[r.SKU] = r.QtyMilli
		given[r.SKU] = true
	}
	arr := make([]TransferQty, len(ct.Lines))
	for i, l := range ct.Lines {
		q := l.QtyMilli
		if given[l.SKU] {
			q = got[l.SKU]
		}
		if q > l.QtyMilli {
			return nil, fmt.Errorf("%w: %s: received more than was sent", ErrInvalidInput, l.Name)
		}
		arr[i] = TransferQty{SKU: l.SKU, QtyMilli: q}
	}
	var ans transferAnswer
	if err := cloud.call("transfer_receive", map[string]any{"p_id": id, "p_received": arr, "p_user": p.Username}, &ans); err != nil {
		return nil, fmt.Errorf("cloud unreachable, try again: %w", err)
	}
	if !ans.OK {
		return nil, fmt.Errorf("%w: %s", ErrInvalidState, ans.Error)
	}
	if err := s.applyIncomingLocked(ans.Transfer); err != nil {
		// claimed in the cloud: the next list on this till finishes it
		return nil, fmt.Errorf("received, but adding the stock failed (it will retry): %w", err)
	}
	s.Audit(p.ID, p.Username, "TRANSFER_RECEIVED", "transfer", ans.Transfer.ClientRef, "from "+ans.Transfer.FromTeam)
	t := ans.Transfer.view(cloud.team)
	return &t, nil
}

func (s *Service) applyIncomingLocked(ct cloudTransfer) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowStamp()
	raw, _ := json.Marshal(ct.Lines)
	rec, _ := json.Marshal(ct.ReceivedLines)
	if _, err := tx.Exec(s.db.Rebind(`INSERT INTO stock_transfers (client_ref, direction, other_team, other_name, status, lines_json, received_json, note,
		received_by_name, applied, created_at, received_at, updated_at) VALUES (?, 'in', ?, ?, 'received', ?, ?, ?, ?, 1, ?, ?, ?)`),
		ct.ID, ct.FromTeam, ct.FromName, string(raw), string(rec), ct.Note, ct.ReceivedBy, now, firstNonEmpty(ct.ReceivedAt, now), now); err != nil {
		if isUniqueViolation(err) {
			return nil // already on the shelf
		}
		return err
	}
	moves := []appliedMove{}
	for i, l := range ct.Lines {
		q := l.QtyMilli
		if i < len(ct.ReceivedLines) {
			q = ct.ReceivedLines[i].QtyMilli
		}
		if q <= 0 {
			continue
		}
		pid, err := s.productForLineTx(tx, l, true)
		if err != nil {
			return err
		}
		if err := s.moveStockTx(tx, pid, q, models.MoveTransferIn, ct.ClientRef, truncStr("from "+firstNonEmpty(ct.FromName, ct.FromTeam), 200), 0, firstNonEmpty(ct.ReceivedBy, "system")); err != nil {
			return err
		}
		moves = append(moves, appliedMove{productID: pid, sku: l.SKU, delta: q})
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.afterMoves(moves)
	return nil
}

// CancelTransfer takes back a transfer nobody has received yet.
func (s *Service) CancelTransfer(clientRef, toTeam string, p *auth.Principal) (*Transfer, error) {
	cloud, err := s.transferCloudClient()
	if err != nil {
		return nil, err
	}
	s.transferMu.Lock()
	defer s.transferMu.Unlock()
	var ans transferAnswer
	if err := cloud.call("transfer_cancel", map[string]any{"p_client_ref": clientRef, "p_to_team": toTeam, "p_user": p.Username}, &ans); err != nil {
		return nil, fmt.Errorf("cloud unreachable, a transfer can only be cancelled online: %w", err)
	}
	if !ans.OK {
		return nil, fmt.Errorf("%w: %s", ErrInvalidState, ans.Error)
	}
	if ans.Transfer.Status == "cancelled" && ans.Transfer.CancelledDevice == cloud.device {
		lines := ans.Transfer.Lines
		if len(lines) == 0 {
			if lt := s.localTransfer(clientRef); lt != nil {
				lines = lt.Lines
			}
		}
		if err := s.restoreTransferLocked(clientRef, lines, "cancelled", ""); err != nil {
			return nil, fmt.Errorf("cancelled, but putting the stock back failed (it will retry): %w", err)
		}
	}
	s.Audit(p.ID, p.Username, "TRANSFER_CANCELLED", "transfer", clientRef, "")
	t := ans.Transfer.view(cloud.team)
	if len(t.Lines) == 0 {
		if lt := s.localTransfer(clientRef); lt != nil {
			t.Lines = lt.Lines
		}
	}
	return &t, nil
}
