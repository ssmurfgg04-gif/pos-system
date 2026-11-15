package services

// etims.go — v1.10.0 KRA eTIMS (OSCU) on the till.
//
// The till never talks to KRA and never holds the store's cmcKey: every
// call goes to the cloud `etims` edge function with this device's identity
// (supabase/functions/etims). The cloud numbers invoices for the whole
// store (gapless across tills), registers items, signs the sale with KRA
// and hands back the control-unit stamp.
//
// Checkout never waits on KRA. etims_receipts is an outbox:
//   - etimsEnqueue picks up every PAID sale made ON THIS TILL since eTIMS
//     was switched on (all payment paths, offline sales included) and every
//     return of a signed sale (credit note);
//   - etimsSendDue sends them, with backoff on network trouble; a KRA
//     rejection is kept as "failed" with KRA's message and re-tried every
//     few hours (or at once from Settings → eTIMS) after the fix;
//   - the stamp rides the order (GetOrder → Order.Etims) onto every receipt.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"posapp/internal/models"
)

var ErrEtimsNotLinked = errors.New("connect this till to LedgerPOS Cloud first (Settings → Team)")

// etimsCall is the cloud transport; tests swap it for a fake.
var etimsCall = func(s *Service, action string, body map[string]any) (map[string]any, int, error) {
	c, ok := s.syncConfig()
	if !ok || c.mode != "rpc" {
		return nil, 0, ErrEtimsNotLinked
	}
	if err := guardProduction(c.base); err != nil {
		return nil, 0, err
	}
	payload := map[string]any{"p_device_id": c.device, "p_secret_hash": c.secretHash, "action": action}
	for k, v := range body {
		payload[k] = v
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequest("POST", c.base+"/functions/v1/etims", bytes.NewReader(raw))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("apikey", c.key)
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("eTIMS cloud answered %d", resp.StatusCode)
	}
	return out, resp.StatusCode, nil
}

func (s *Service) EtimsEnabled() bool { return s.settings.GetBool("etims_enabled", false) }

// etimsDefaultTax: the shop's default KRA tax type for products that don't
// set one. VAT-registered shops (tax_percent > 0) default to B (16%).
func (s *Service) etimsDefaultTax() string {
	if t := strings.ToUpper(strings.TrimSpace(s.settings.Get("etims_default_tax"))); validTaxType(t) {
		return t
	}
	if s.settings.GetFloat("tax_percent", 16) > 0 {
		return "B"
	}
	return "D"
}

func validTaxType(t string) bool { return len(t) == 1 && strings.Contains("ABCDE", t) }

// vatFor (v1.11) is THE rule for what VAT a product sells at:
//   - gift cards are outside VAT (type X, 0%): the VAT is charged when the
//     card is spent on goods, not when it is sold;
//   - a shop with no VAT (tax_percent 0) charges none on anything;
//   - otherwise by KRA tax type (the product's, else the shop default):
//     B = the shop's standard rate, E = 8%, A / C / D = 0%.
func (s *Service) vatFor(productType string, isGiftCard bool, shopPct float64) (string, float64) {
	if isGiftCard {
		return "X", 0
	}
	t := strings.ToUpper(strings.TrimSpace(productType))
	if !validTaxType(t) {
		t = s.etimsDefaultTax()
	}
	if shopPct <= 0 {
		if t == "B" || t == "E" {
			t = "D"
		}
		return t, 0
	}
	switch t {
	case "B":
		return t, shopPct
	case "E":
		return t, 8
	}
	return t, 0
}

// VatRateFor is vatFor for display (product lists, the till's cart).
func (s *Service) VatRateFor(productType string, isGiftCard bool) (string, float64) {
	return s.vatFor(productType, isGiftCard, s.settings.GetFloat("tax_percent", 16))
}

func errOf(out map[string]any, status int, err error) error {
	if err != nil {
		return err
	}
	if ok, _ := out["ok"].(bool); ok {
		return nil
	}
	if msg, _ := out["error"].(string); msg != "" {
		return errors.New(msg)
	}
	return fmt.Errorf("eTIMS cloud answered %d", status)
}

// ---- setup -----------------------------------------------------------------

type EtimsConnectInput struct {
	Env        string `json:"env"` // sandbox | production
	Tin        string `json:"tin"`
	BhfID      string `json:"bhfId"`
	DvcSrlNo   string `json:"dvcSrlNo"`
	LastInvcNo int64  `json:"lastInvcNo"` // last invoice number the device already used elsewhere
}

func (s *Service) EtimsConnect(in EtimsConnectInput) (map[string]any, error) {
	body := map[string]any{
		"env": in.Env, "tin": strings.ToUpper(strings.TrimSpace(in.Tin)), "bhfId": strings.TrimSpace(in.BhfID),
		"dvcSrlNo": strings.TrimSpace(in.DvcSrlNo), "lastInvcNo": in.LastInvcNo,
	}
	if c, ok := s.syncConfig(); ok {
		if tok := ownerCapabilityTokenFor(c); tok != "" {
			body["ownerCapability"] = tok
		}
	}
	out, st, err := etimsCall(s, "connect", body)
	if err := errOf(out, st, err); err != nil {
		return nil, err
	}
	if s.settings.Get("etims_enabled_at") == "" || !s.EtimsEnabled() {
		_ = s.settings.Set("etims_enabled_at", nowStamp())
	}
	_ = s.settings.Set("etims_enabled", "true")
	_ = s.settings.Set("kra_pin", fmt.Sprint(body["tin"])) // receipts print the seller PIN
	dev, _ := out["device"].(map[string]any)
	s.cacheEtimsDevice(dev)
	return dev, nil
}

func (s *Service) cacheEtimsDevice(dev map[string]any) {
	if dev == nil {
		return
	}
	if b, err := json.Marshal(dev); err == nil {
		_ = s.settings.Set("etims_device_cache", string(b))
	}
}

// EtimsDisable stops queueing new sales (already signed receipts keep their stamp).
func (s *Service) EtimsDisable() {
	_ = s.settings.Set("etims_enabled", "false")
}

type EtimsStatus struct {
	Enabled    bool           `json:"enabled"`
	EnabledAt  string         `json:"enabledAt"`
	DefaultTax string         `json:"defaultTax"`
	Device     map[string]any `json:"device"`
	Signed     int            `json:"signed"`
	Pending    int            `json:"pending"`
	Failed     int            `json:"failed"`
	LastError  string         `json:"lastError"`
	CloudError string         `json:"cloudError,omitempty"`
}

func (s *Service) EtimsStatusNow() EtimsStatus {
	st := EtimsStatus{Enabled: s.EtimsEnabled(), EnabledAt: s.settings.Get("etims_enabled_at"), DefaultTax: s.etimsDefaultTax()}
	_ = s.db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN status = 'signed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END),0)
		FROM etims_receipts`).Scan(&st.Signed, &st.Pending, &st.Failed)
	_ = s.db.QueryRow(`SELECT last_error FROM etims_receipts WHERE status = 'failed' ORDER BY updated_at DESC LIMIT 1`).Scan(&st.LastError)
	out, code, err := etimsCall(s, "status", nil)
	if e := errOf(out, code, err); e != nil {
		st.CloudError = e.Error()
		_ = json.Unmarshal([]byte(s.settings.Get("etims_device_cache")), &st.Device)
	} else {
		st.Device, _ = out["device"].(map[string]any)
		s.cacheEtimsDevice(st.Device)
	}
	return st
}

func (s *Service) EtimsClasses(q string) ([]any, error) {
	out, st, err := etimsCall(s, "classes", map[string]any{"q": q})
	if err := errOf(out, st, err); err != nil {
		return nil, err
	}
	list, _ := out["classes"].([]any)
	return list, nil
}

// EtimsSetDefaults: the store's default KRA item class (cloud) and this
// shop's default tax type (local).
func (s *Service) EtimsSetDefaults(clsCd, tax string) error {
	if tax = strings.ToUpper(strings.TrimSpace(tax)); tax != "" {
		if !validTaxType(tax) {
			return fmt.Errorf("tax type is one of A, B, C, D, E")
		}
		_ = s.settings.Set("etims_default_tax", tax)
	}
	if clsCd = strings.TrimSpace(clsCd); clsCd != "" {
		out, st, err := etimsCall(s, "defaults", map[string]any{"defaultClsCd": clsCd})
		if err := errOf(out, st, err); err != nil {
			return err
		}
		dev, _ := out["device"].(map[string]any)
		s.cacheEtimsDevice(dev)
	}
	// a fix may unblock failed sends: try them on the next tick
	s.EtimsRetryFailed()
	return nil
}

// EtimsRetryFailed re-arms every failed send for the next worker tick.
func (s *Service) EtimsRetryFailed() int {
	res, err := s.db.Exec(`UPDATE etims_receipts SET status = 'pending', next_try_at = '' WHERE status = 'failed'`)
	if err != nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return int(n)
}

// ---- outbox ----------------------------------------------------------------

// etimsEnqueue queues sales and credit notes that still need KRA.
func (s *Service) etimsEnqueue() {
	if !s.EtimsEnabled() {
		return
	}
	since := s.settings.Get("etims_enabled_at")
	if since == "" {
		return
	}
	// Sales made on THIS till (each till sends its own; the cloud numbers
	// them for the whole store). Gift-card-only and zero sales still go:
	// KRA wants every receipt.
	_, err := s.db.Exec(s.db.Rebind(`
		INSERT INTO etims_receipts (kind, ref_id, client_ref, status)
		SELECT 'S', o.id, CASE WHEN COALESCE(o.client_uuid,'') != '' THEN o.client_uuid ELSE o.number END, 'pending'
		FROM orders o
		WHERE o.status = 'PAID' AND COALESCE(o.device_id,'') = ? AND o.created_at >= ?
		  AND NOT EXISTS (SELECT 1 FROM etims_receipts r WHERE r.kind = 'S' AND r.ref_id = o.id)`),
		s.deviceID(), since)
	if err != nil {
		log.Printf("[etims] enqueue sales: %v", err)
	}
	// Returns of a sale this till got signed → credit note.
	_, err = s.db.Exec(s.db.Rebind(`
		INSERT INTO etims_receipts (kind, ref_id, client_ref, org_client_ref, status)
		SELECT 'R', rt.id, CASE WHEN COALESCE(rt.client_uuid,'') != '' THEN rt.client_uuid ELSE rt.number END, org.client_ref, 'pending'
		FROM returns rt JOIN etims_receipts org ON org.kind = 'S' AND org.ref_id = rt.order_id AND org.status = 'signed'
		WHERE NOT EXISTS (SELECT 1 FROM etims_receipts r WHERE r.kind = 'R' AND r.ref_id = rt.id)`))
	if err != nil {
		log.Printf("[etims] enqueue returns: %v", err)
	}
	// A VOIDED sale KRA already signed → credit note for what is left of
	// it (kind 'V' locally; a credit note on the wire). Without this the
	// shop would owe VAT on a sale that never happened.
	_, err = s.db.Exec(`
		INSERT INTO etims_receipts (kind, ref_id, client_ref, org_client_ref, status)
		SELECT 'V', o.id, 'void:' || org.client_ref, org.client_ref, 'pending'
		FROM orders o JOIN etims_receipts org ON org.kind = 'S' AND org.ref_id = o.id AND org.status = 'signed'
		WHERE o.status = 'VOIDED'
		  AND NOT EXISTS (SELECT 1 FROM etims_receipts r WHERE r.kind = 'V' AND r.ref_id = o.id)`)
	if err != nil {
		log.Printf("[etims] enqueue voids: %v", err)
	}
	s.etimsEnqueueStock()
	// A sale voided BEFORE KRA signed it is never sent.
	_, _ = s.db.Exec(`UPDATE etims_receipts SET status = 'skipped', last_error = 'voided before sending', next_try_at = ''
		WHERE kind = 'S' AND status IN ('pending','failed')
		  AND ref_id IN (SELECT id FROM orders WHERE status = 'VOIDED')`)
}

type etimsRow struct {
	ID           int64
	Kind         string
	RefID        int64
	ClientRef    string
	OrgClientRef string
	Attempts     int
}

// one run at a time PER SHOP (a multi-shop server has many Services)
var etimsBusy sync.Map // *Service -> *atomic.Bool

// EtimsTick queues and sends; safe to call often (one run at a time).
func (s *Service) EtimsTick() {
	if !s.EtimsEnabled() {
		return
	}
	v, _ := etimsBusy.LoadOrStore(s, new(atomic.Bool))
	busy := v.(*atomic.Bool)
	if !busy.CompareAndSwap(false, true) {
		return
	}
	defer busy.Store(false)
	s.etimsEnqueue()
	s.etimsSendDue(8)
}

func (s *Service) etimsSendDue(limit int) int {
	now := nowStamp()
	rows, err := s.db.Query(s.db.Rebind(`SELECT id, kind, ref_id, client_ref, org_client_ref, attempts FROM etims_receipts
		WHERE status = 'pending' AND (next_try_at = '' OR next_try_at <= ?) ORDER BY id LIMIT ?`), now, limit)
	if err != nil {
		return 0
	}
	var due []etimsRow
	for rows.Next() {
		var r etimsRow
		if rows.Scan(&r.ID, &r.Kind, &r.RefID, &r.ClientRef, &r.OrgClientRef, &r.Attempts) == nil {
			due = append(due, r)
		}
	}
	rows.Close()
	sent := 0
	for _, r := range due {
		if s.etimsSendOne(r) {
			sent++
		}
	}
	return sent
}

func (s *Service) etimsSendOne(r etimsRow) bool {
	var sale map[string]any
	var err error
	switch r.Kind {
	case "R":
		sale, err = s.etimsReturnBody(r.RefID)
	case "V":
		sale, err = s.etimsVoidBody(r.RefID)
	case "M":
		return s.etimsSendStock(r)
	default:
		sale, err = s.etimsSaleBody(r.RefID)
	}
	if errors.Is(err, errEtimsNothingLeft) {
		// gift cards only, or already fully credited: not a supply for KRA
		_, _ = s.db.Exec(s.db.Rebind(`UPDATE etims_receipts SET status = 'skipped', last_error = 'nothing for KRA (gift cards / already credited)', updated_at = ? WHERE id = ?`), nowStamp(), r.ID)
		return false
	}
	if err != nil {
		s.etimsMark(r, "failed", err.Error(), false, nil)
		return false
	}
	wire := r.Kind
	if wire == "V" {
		wire = "R"
	}
	body := map[string]any{"kind": wire, "clientRef": r.ClientRef, "sale": sale}
	if wire == "R" {
		body["orgClientRef"] = r.OrgClientRef
	}
	out, code, err := etimsCall(s, "sale", body)
	if e := errOf(out, code, err); e != nil {
		retry := err != nil || code >= 500 || code == 0
		if v, ok := out["retry"].(bool); ok {
			retry = v
		}
		s.etimsMark(r, "", e.Error(), retry, nil)
		return false
	}
	stamp, _ := out["stamp"].(map[string]any)
	s.etimsMark(r, "signed", "", false, stamp)
	return true
}

func (s *Service) etimsMark(r etimsRow, status, msg string, retry bool, stamp map[string]any) {
	attempts := r.Attempts + 1
	if status == "signed" {
		num := func(k string) int64 { f, _ := stamp[k].(float64); return int64(f) }
		str := func(k string) string { v, _ := stamp[k].(string); return v }
		_, _ = s.db.Exec(s.db.Rebind(`UPDATE etims_receipts SET status = 'signed', invc_no = ?, cur_rcpt_no = ?, tot_rcpt_no = ?,
			intrl_data = ?, rcpt_sign = ?, sdc_date_time = ?, sdc_id = ?, mrc_no = ?, qr = ?, last_error = '',
			attempts = ?, next_try_at = '', updated_at = ? WHERE id = ?`),
			num("invcNo"), num("curRcptNo"), num("totRcptNo"), str("intrlData"), str("rcptSign"), str("sdcDateTime"),
			str("sdcId"), str("mrcNo"), str("qr"), attempts, nowStamp(), r.ID)
		if r.Kind == "S" && num("invcNo") > 0 {
			// the KRA invoice number is the document number from now on
			_, _ = s.db.Exec(s.db.Rebind(`UPDATE orders SET invoice_number = ? WHERE id = ?`),
				fmt.Sprintf("%s/%d", str("sdcId"), num("curRcptNo")), r.RefID)
		}
		return
	}
	if status == "" {
		status = "failed"
		if retry {
			status = "pending"
		}
	}
	// pending: back off 1, 2, 4 ... 60 minutes; failed: look again in 6h
	wait := 6 * time.Hour
	if status == "pending" {
		m := 1 << min(attempts-1, 6)
		wait = time.Duration(min(m, 60)) * time.Minute
	}
	next := time.Now().UTC().Add(wait).Format(nowStampLayout)
	_, _ = s.db.Exec(s.db.Rebind(`UPDATE etims_receipts SET status = ?, last_error = ?, attempts = ?, next_try_at = ?, updated_at = ? WHERE id = ?`),
		status, truncStr(msg, 300), attempts, next, nowStamp(), r.ID)
	if status == "failed" {
		log.Printf("[etims] %s %d not signed: %s", r.Kind, r.RefID, msg)
	}
}

// etimsLine builds one wire line from an order/return line + its product.
// frozenType is the tax type frozen on the sale line (v1.11); ” = old
// line, use the product's current type.
func (s *Service) etimsLine(productID int64, name, sku, frozenType string, qtyMilli, unitPrice, lineTotal int64) map[string]any {
	var barcode, unit, taxTy, cls string
	trackStock := 1
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(barcode,''), COALESCE(unit,''), COALESCE(track_stock,1),
		COALESCE(etims_tax_type,''), COALESCE(etims_cls_cd,'') FROM products WHERE id = ?`), productID).
		Scan(&barcode, &unit, &trackStock, &taxTy, &cls)
	if validTaxType(frozenType) {
		taxTy = frozenType
	} else if !validTaxType(taxTy) {
		taxTy = s.etimsDefaultTax()
	}
	key := "name:" + strings.ToLower(strings.TrimSpace(name))
	if sku = strings.TrimSpace(sku); sku != "" {
		key = "sku:" + sku
	}
	return map[string]any{
		"itemKey": key, "name": name, "barcode": barcode, "unit": unit, "service": trackStock == 0,
		"taxTy": taxTy, "clsCd": cls, "qtyMilli": qtyMilli, "unitPriceCents": unitPrice, "lineTotalCents": lineTotal,
	}
}

func (s *Service) etimsSaleBody(orderID int64) (map[string]any, error) {
	o, err := s.GetOrder(orderID)
	if err != nil {
		return nil, fmt.Errorf("sale %d not found", orderID)
	}
	if len(o.Items) == 0 {
		return nil, fmt.Errorf("sale %s has no lines", o.Number)
	}
	body := map[string]any{
		"traderInvoiceNo": o.Number, "confirmedAt": kenyaStampOf(firstNonEmpty(o.PaidAt, o.CreatedAt)),
		"customerPin": o.BuyerPIN, "customerName": o.CustomerName, "customerPhone": etimsPhoneOf(o),
		"payment": etimsPaymentOf(o), "cashierId": fmt.Sprint(o.CashierID), "cashierName": o.CashierName,
	}
	lines := []map[string]any{}
	if o.Items[0].TaxType != "" {
		// v1.11 lines carry their frozen VAT: send exactly what the receipt
		// shows. Per line: gross before discount (VAT incl.), its discount
		// share and its VAT. Gift cards (X) are not a supply: left out.
		for _, it := range o.Items {
			if it.TaxType == "X" {
				continue
			}
			l := s.etimsLine(it.ProductID, it.Name, it.SKU, it.TaxType, it.QtyMilli, it.UnitPriceCents, it.PaidCents)
			if o.TaxIncluded && it.LineTotalCents >= it.PaidCents {
				l["lineTotalCents"] = it.LineTotalCents
				l["discountCents"] = it.LineTotalCents - it.PaidCents
			} else {
				l["discountCents"] = int64(0)
			}
			l["taxCents"] = it.TaxCents
			lines = append(lines, l)
		}
		if len(lines) == 0 {
			return nil, errEtimsNothingLeft // gift cards only: nothing for KRA
		}
		body["lines"] = lines
		body["discountCents"] = int64(0)
		return body, nil
	}
	// Pre-v1.11 sale (no per-line VAT): one rate, discount and points
	// spread by the builder.
	for _, it := range o.Items {
		lines = append(lines, s.etimsLine(it.ProductID, it.Name, it.SKU, "", it.QtyMilli, it.UnitPriceCents, it.LineTotalCents))
	}
	var gross int64
	for i, l := range lines {
		lt := l["lineTotalCents"].(int64)
		if !o.TaxIncluded && o.TaxPercent > 0 {
			lt = lt + (lt*int64(o.TaxPercent*100)+5000)/10000
			lines[i]["lineTotalCents"] = lt
		}
		gross += lt
	}
	discount := gross - o.TotalCents
	if discount < 0 {
		lines[0]["lineTotalCents"] = lines[0]["lineTotalCents"].(int64) - discount
		discount = 0
	}
	body["lines"] = lines
	body["discountCents"] = discount
	return body, nil
}

func (s *Service) etimsReturnBody(returnID int64) (map[string]any, error) {
	var number, createdAt, reason string
	var orderID int64
	err := s.db.QueryRow(s.db.Rebind(`SELECT number, order_id, CAST(created_at AS TEXT), COALESCE(reason,'') FROM returns WHERE id = ?`), returnID).
		Scan(&number, &orderID, &createdAt, &reason)
	if err != nil {
		return nil, fmt.Errorf("return %d not found", returnID)
	}
	// The refund per line, with the tax type frozen on the original sale line.
	rows, err := s.db.Query(s.db.Rebind(`SELECT ri.product_id, ri.name, COALESCE(ri.sku,''), ri.qty_milli, ri.unit_price_cents, ri.line_total_cents,
		COALESCE(oi.tax_type,'')
		FROM return_items ri LEFT JOIN order_items oi ON oi.id = ri.order_item_id
		WHERE ri.return_id = ? ORDER BY ri.id`), returnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lines := []map[string]any{}
	for rows.Next() {
		var pid, qty, unit, total int64
		var name, sku, tt string
		if rows.Scan(&pid, &name, &sku, &qty, &unit, &total, &tt) == nil && qty > 0 && tt != "X" {
			lines = append(lines, s.etimsLine(pid, name, sku, tt, qty, unit, total))
		}
	}
	if len(lines) == 0 {
		return nil, errEtimsNothingLeft
	}
	o, _ := s.GetOrder(orderID)
	body := map[string]any{
		"traderInvoiceNo": number, "confirmedAt": kenyaStampOf(createdAt), "payment": "cash",
		"refundReason": "06", "cashierId": "pos", "cashierName": "POS", "lines": lines,
	}
	if o != nil {
		body["customerPin"] = o.BuyerPIN
		body["customerName"] = o.CustomerName
		body["payment"] = etimsPaymentOf(o)
	}
	return body, nil
}

var errEtimsNothingLeft = errors.New("nothing to send to KRA")

// etimsVoidBody: credit note for a voided, KRA-signed sale: every line,
// less what earlier returns already credited.
func (s *Service) etimsVoidBody(orderID int64) (map[string]any, error) {
	body, err := s.etimsSaleBody(orderID)
	if err != nil {
		return nil, err
	}
	o, _ := s.GetOrder(orderID)
	returned := map[int64]int64{} // order_item_id -> milli already returned
	rows, err := s.db.Query(s.db.Rebind(`SELECT ri.order_item_id, COALESCE(SUM(ri.qty_milli),0) FROM return_items ri
		JOIN returns rt ON rt.id = ri.return_id WHERE rt.order_id = ? GROUP BY ri.order_item_id`), orderID)
	if err == nil {
		for rows.Next() {
			var id, q int64
			if rows.Scan(&id, &q) == nil {
				returned[id] = q
			}
		}
		rows.Close()
	}
	lines := body["lines"].([]map[string]any)
	var left []map[string]any
	j := 0 // lines skip gift cards (X): walk them in step with the items
	for _, it := range o.Items {
		if it.TaxType == "X" {
			continue
		}
		if j >= len(lines) {
			break
		}
		l := lines[j]
		j++
		rest := it.QtyMilli - returned[it.ID]
		if rest <= 0 {
			continue
		}
		if rest < it.QtyMilli {
			for _, k := range []string{"lineTotalCents", "discountCents", "taxCents"} {
				if v, ok := l[k].(int64); ok {
					l[k] = v * rest / it.QtyMilli
				}
			}
			l["qtyMilli"] = rest
		}
		left = append(left, l)
	}
	if len(left) == 0 {
		return nil, errEtimsNothingLeft
	}
	body["lines"] = left
	if len(returned) > 0 {
		body["discountCents"] = int64(0) // partial: credit the remaining lines at face value
	}
	body["traderInvoiceNo"] = o.Number + "-VOID"
	body["confirmedAt"] = kenyaStampOf(firstNonEmpty(o.VoidedAt, nowStamp()))
	body["refundReason"] = "06"
	return body, nil
}

// EtimsLoop keeps signing close to real time (the paper receipt waits a
// few seconds for KRA): a cheap local check every 3s.
func (s *Service) EtimsLoop(ctx context.Context) {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.EtimsTick()
		}
	}
}

func etimsPaymentOf(o *models.Order) string {
	best, amt := "cash", int64(-1)
	for _, p := range o.Payments {
		if p.Status == "COMPLETED" && p.AmountCents > amt {
			best, amt = p.Method, p.AmountCents
		}
	}
	return best
}

func etimsPhoneOf(o *models.Order) string {
	for _, p := range o.Payments {
		if p.Phone != "" {
			return p.Phone
		}
	}
	return ""
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// kenyaStampOf turns a stored timestamp into KRA's yyyyMMddHHmmss, Kenya time.
func kenyaStampOf(ts string) string {
	ts = strings.TrimSpace(ts)
	for _, layout := range []string{time.RFC3339Nano, nowStampLayout, "2006-01-02 15:04:05", "2006-01-02 15:04:05-07", "2006-01-02 15:04:05.999999-07"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.UTC().Add(3 * time.Hour).Format("20060102150405")
		}
	}
	return time.Now().UTC().Add(3 * time.Hour).Format("20060102150405")
}

// etimsStampFor loads the KRA stamp for a sale (nil when eTIMS never saw it).
func (s *Service) etimsStampFor(kind string, refID int64) *models.EtimsStamp {
	var st models.EtimsStamp
	err := s.db.QueryRow(s.db.Rebind(`SELECT status, invc_no, cur_rcpt_no, tot_rcpt_no, intrl_data, rcpt_sign, sdc_date_time,
		sdc_id, mrc_no, qr, last_error, attempts FROM etims_receipts WHERE kind = ? AND ref_id = ?`), kind, refID).
		Scan(&st.Status, &st.InvcNo, &st.CurRcptNo, &st.TotRcptNo, &st.IntrlData, &st.RcptSign, &st.SdcDateTime,
			&st.SdcID, &st.MrcNo, &st.QR, &st.LastError, &st.Attempts)
	if err != nil {
		return nil
	}
	return &st
}

// ---- stock movements (v1.11) ------------------------------------------------
// Every stock movement made on this till after eTIMS was switched on goes
// to KRA as a "stored and released" record (insertStockIO) plus the
// item's remaining stock (saveStockMaster). A movement tied to an invoice
// (sale, void, return) waits until that invoice is signed; if the invoice
// was never sent (voided first, gift cards), the movement is skipped too.

var errEtimsWait = errors.New("waiting for the invoice")

// sarTypeFor maps a ledger move to KRA's stock movement type (spec 4.15).
func sarTypeFor(kind string, delta int64) string {
	switch kind {
	case models.MoveSale:
		return "11"
	case models.MoveReturnRestock, models.MoveVoidRestore:
		return "03"
	case models.MovePOReceive, models.MoveBatchIn:
		return "02"
	case models.MoveDisposal, models.MoveBatchRecall:
		return "15"
	case models.MoveAdjustment, models.MoveCount, models.MoveOpening:
		if delta < 0 {
			return "16"
		}
		return "06"
	}
	return ""
}

func (s *Service) etimsEnqueueStock() {
	since := s.settings.Get("etims_stock_since_id")
	if since == "" {
		// first run after switching on: only movements from now on
		var maxID int64
		_ = s.db.QueryRow(`SELECT COALESCE(MAX(id),0) FROM stock_movements`).Scan(&maxID)
		_ = s.settings.Set("etims_stock_since_id", fmt.Sprint(maxID))
		return
	}
	dev := s.deviceID()
	_, err := s.db.Exec(s.db.Rebind(`
		INSERT INTO etims_receipts (kind, ref_id, client_ref, status)
		SELECT 'M', m.id, 'mv:' || ? || ':' || CAST(m.id AS TEXT), 'pending'
		FROM stock_movements m
		WHERE m.id > ? AND COALESCE(m.device_id,'') = ? AND COALESCE(m.delta_milli,0) != 0
		  AND m.kind IN ('SALE','RETURN_RESTOCK','VOID_RESTORE','PO_RECEIVE','BATCH_IN','DISPOSAL','BATCH_RECALL','ADJUSTMENT','COUNT','OPENING')
		  AND NOT EXISTS (SELECT 1 FROM etims_receipts r WHERE r.kind = 'M' AND r.ref_id = m.id)`),
		dev, since, dev)
	if err != nil {
		log.Printf("[etims] enqueue stock: %v", err)
		return
	}
}

// etimsInvoiceGate: the state of the invoice a movement depends on.
// "go" = signed (send the movement), "skip" = never sent, "wait" = not yet.
func (s *Service) etimsInvoiceGate(kind, reference string) string {
	var status string
	var err error
	switch kind {
	case models.MoveSale:
		err = s.db.QueryRow(s.db.Rebind(`SELECT r.status FROM etims_receipts r JOIN orders o ON o.id = r.ref_id
			WHERE r.kind = 'S' AND o.number = ?`), reference).Scan(&status)
	case models.MoveVoidRestore:
		err = s.db.QueryRow(s.db.Rebind(`SELECT r.status FROM etims_receipts r JOIN orders o ON o.id = r.ref_id
			WHERE r.kind = 'V' AND o.number = ?`), reference).Scan(&status)
		if err != nil {
			// voided before it was ever signed: the sale was skipped, so is this
			var st string
			if s.db.QueryRow(s.db.Rebind(`SELECT r.status FROM etims_receipts r JOIN orders o ON o.id = r.ref_id
				WHERE r.kind = 'S' AND o.number = ?`), reference).Scan(&st) == nil && st == "skipped" {
				return "skip"
			}
		}
	case models.MoveReturnRestock:
		err = s.db.QueryRow(s.db.Rebind(`SELECT r.status FROM etims_receipts r JOIN returns rt ON rt.id = r.ref_id
			WHERE r.kind = 'R' AND rt.number = ?`), reference).Scan(&status)
	default:
		return "go"
	}
	if err != nil {
		// sale from before eTIMS (never queued) → nothing to tie it to: skip
		if kind == models.MoveSale {
			var n int
			_ = s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM orders WHERE number = ?`), reference).Scan(&n)
			if n > 0 {
				var queued int
				_ = s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM etims_receipts r JOIN orders o ON o.id = r.ref_id WHERE o.number = ?`), reference).Scan(&queued)
				if queued == 0 {
					return "wait" // the sale is about to be queued (same tick)
				}
			}
		}
		return "wait"
	}
	switch status {
	case "signed":
		return "go"
	case "skipped":
		return "skip"
	}
	return "wait"
}

func (s *Service) etimsSendStock(r etimsRow) bool {
	var productID, delta, balance, userID int64
	var kind, reference, note, username, createdAt string
	err := s.db.QueryRow(s.db.Rebind(`SELECT product_id, COALESCE(delta_milli,0), COALESCE(balance_after_milli,0), kind,
		COALESCE(reference,''), COALESCE(note,''), COALESCE(user_id,0), COALESCE(username,''), CAST(created_at AS TEXT)
		FROM stock_movements WHERE id = ?`), r.RefID).
		Scan(&productID, &delta, &balance, &kind, &reference, &note, &userID, &username, &createdAt)
	if err != nil {
		s.etimsMark(r, "failed", "stock movement not found", false, nil)
		return false
	}
	sar := sarTypeFor(kind, delta)
	if sar == "" {
		s.etimsSkip(r, "not a stock movement KRA tracks")
		return false
	}
	switch s.etimsInvoiceGate(kind, reference) {
	case "skip":
		s.etimsSkip(r, "its invoice was never sent to KRA")
		return false
	case "wait":
		// not a failure: look again shortly, without burning attempts
		_, _ = s.db.Exec(s.db.Rebind(`UPDATE etims_receipts SET next_try_at = ?, last_error = 'waiting for its invoice' WHERE id = ?`),
			time.Now().UTC().Add(15*time.Second).Format(nowStampLayout), r.ID)
		return false
	}
	var name, sku string
	var price, cost int64
	var gift, track int
	_ = s.db.QueryRow(s.db.Rebind(`SELECT name, COALESCE(sku,''), price_cents, COALESCE(cost_cents,0), COALESCE(is_gift_card,0), COALESCE(track_stock,1)
		FROM products WHERE id = ?`), productID).Scan(&name, &sku, &price, &cost, &gift, &track)
	if name == "" || gift == 1 || track == 0 {
		s.etimsSkip(r, "not a stocked good")
		return false
	}
	unit := cost
	if sar == "11" || unit <= 0 {
		unit = price
	}
	line := s.etimsLine(productID, name, sku, "", absI64(delta), unit, 0)
	body := map[string]any{
		"clientRef": r.ClientRef, "sarTyCd": sar, "occurredAt": kenyaStampOf(createdAt), "line": line,
		"remainQtyMilli": balance, "remark": truncStr(strings.TrimSpace(kind+" "+reference+" "+note), 400),
		"userId": fmt.Sprint(userID), "userName": username,
	}
	out, code, err := etimsCall(s, "stock", body)
	if e := errOf(out, code, err); e != nil {
		retry := err != nil || code >= 500 || code == 0
		if v, ok := out["retry"].(bool); ok {
			retry = v
		}
		s.etimsMark(r, "", e.Error(), retry, nil)
		return false
	}
	_, _ = s.db.Exec(s.db.Rebind(`UPDATE etims_receipts SET status = 'signed', last_error = '', attempts = attempts + 1, next_try_at = '', updated_at = ? WHERE id = ?`), nowStamp(), r.ID)
	return true
}

func (s *Service) etimsSkip(r etimsRow, why string) {
	_, _ = s.db.Exec(s.db.Rebind(`UPDATE etims_receipts SET status = 'skipped', last_error = ?, next_try_at = '', updated_at = ? WHERE id = ?`), why, nowStamp(), r.ID)
}

func absI64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
