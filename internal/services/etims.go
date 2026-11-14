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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
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
		WHERE rt.created_at >= ?
		  AND NOT EXISTS (SELECT 1 FROM etims_receipts r WHERE r.kind = 'R' AND r.ref_id = rt.id)`), since)
	if err != nil {
		log.Printf("[etims] enqueue returns: %v", err)
	}
}

type etimsRow struct {
	ID           int64
	Kind         string
	RefID        int64
	ClientRef    string
	OrgClientRef string
	Attempts     int
}

var etimsBusy atomic.Bool

// EtimsTick queues and sends; safe to call often (one run at a time).
func (s *Service) EtimsTick() {
	if !s.EtimsEnabled() || !etimsBusy.CompareAndSwap(false, true) {
		return
	}
	defer etimsBusy.Store(false)
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
	if r.Kind == "R" {
		sale, err = s.etimsReturnBody(r.RefID)
	} else {
		sale, err = s.etimsSaleBody(r.RefID)
	}
	if err != nil {
		s.etimsMark(r, "failed", err.Error(), false, nil)
		return false
	}
	body := map[string]any{"kind": r.Kind, "clientRef": r.ClientRef, "sale": sale}
	if r.Kind == "R" {
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
func (s *Service) etimsLine(productID int64, name, sku string, qtyMilli, unitPrice, lineTotal int64) map[string]any {
	var barcode, unit, taxTy, cls string
	trackStock := 1
	_ = s.db.QueryRow(s.db.Rebind(`SELECT COALESCE(barcode,''), COALESCE(unit,''), COALESCE(track_stock,1),
		COALESCE(etims_tax_type,''), COALESCE(etims_cls_cd,'') FROM products WHERE id = ?`), productID).
		Scan(&barcode, &unit, &trackStock, &taxTy, &cls)
	if !validTaxType(taxTy) {
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
	lines := []map[string]any{}
	for _, it := range o.Items {
		lines = append(lines, s.etimsLine(it.ProductID, it.Name, it.SKU, it.QtyMilli, it.UnitPriceCents, it.LineTotalCents))
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("sale %s has no lines", o.Number)
	}
	// Order-level discount and redeemed points both come off what the
	// customer paid; eTIMS spreads them over the lines.
	var linesTotal int64
	for _, it := range o.Items {
		linesTotal += it.LineTotalCents
	}
	gross := linesTotal
	if !o.TaxIncluded && o.TaxPercent > 0 {
		// tax-exclusive pricing: eTIMS amounts include VAT, so scale lines
		// up to what was actually charged (the total carries the VAT).
		for i, l := range lines {
			lt := l["lineTotalCents"].(int64)
			lines[i]["lineTotalCents"] = lt + (lt*int64(o.TaxPercent*100)+5000)/10000
		}
		gross = 0
		for _, l := range lines {
			gross += l["lineTotalCents"].(int64)
		}
	}
	discount := gross - o.TotalCents
	if discount < 0 {
		// rounding of exclusive VAT can leave the lines a cent short: put it on the first line
		lines[0]["lineTotalCents"] = lines[0]["lineTotalCents"].(int64) - discount
		discount = 0
	}
	return map[string]any{
		"traderInvoiceNo": o.Number, "confirmedAt": kenyaStampOf(firstNonEmpty(o.PaidAt, o.CreatedAt)),
		"customerPin": o.BuyerPIN, "customerName": o.CustomerName, "customerPhone": etimsPhoneOf(o),
		"payment": etimsPaymentOf(o), "discountCents": discount,
		"cashierId": fmt.Sprint(o.CashierID), "cashierName": o.CashierName, "lines": lines,
	}, nil
}

func (s *Service) etimsReturnBody(returnID int64) (map[string]any, error) {
	var number, createdAt, reason string
	var orderID int64
	err := s.db.QueryRow(s.db.Rebind(`SELECT number, order_id, CAST(created_at AS TEXT), COALESCE(reason,'') FROM returns WHERE id = ?`), returnID).
		Scan(&number, &orderID, &createdAt, &reason)
	if err != nil {
		return nil, fmt.Errorf("return %d not found", returnID)
	}
	rows, err := s.db.Query(s.db.Rebind(`SELECT product_id, name, COALESCE(sku,''), qty_milli, unit_price_cents, line_total_cents
		FROM return_items WHERE return_id = ? ORDER BY id`), returnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lines := []map[string]any{}
	for rows.Next() {
		var pid, qty, unit, total int64
		var name, sku string
		if rows.Scan(&pid, &name, &sku, &qty, &unit, &total) == nil && qty > 0 {
			lines = append(lines, s.etimsLine(pid, name, sku, qty, unit, total))
		}
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("return %s has no lines", number)
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
