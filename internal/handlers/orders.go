package handlers

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"posapp/internal/models"
	"posapp/internal/printer"
	"posapp/internal/services"
)

// ListOrders (orders.view) with filters.
func (h *H) ListOrders(c *gin.Context) {
	f := services.OrderFilter{
		Status: c.Query("status"),
		Search: strings.TrimSpace(c.Query("search")),
		Limit:  50,
	}
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Offset = n
		}
	}
	if v := c.Query("cashierId"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.Cashier = id
		}
	}
	f.From, f.To = c.Query("from"), c.Query("to")
	orders, err := h.svc(c).ListOrders(f, h.principal(c))
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	if orders == nil {
		orders = []models.Order{}
	}
	h.ok(c, orders)
}

// ListOrderCashiers (orders.view, orders.view.all required) — the filter
// dropdown's options for the Orders page. Staff without orders.view.all
// only ever see their own sales, so a cashier picker is meaningless (and
// an information leak) for them: 403.
func (h *H) ListOrderCashiers(c *gin.Context) {
	p := h.principal(c)
	if !p.Can("orders.view.all") {
		h.fail(c, 403, "the cashier filter needs full order access")
		return
	}
	rows, err := h.db(c).Query(h.db(c).Rebind(`
                SELECT u.id, COALESCE(NULLIF(u.full_name,''), u.username)
                FROM users u WHERE u.is_active = 1 ORDER BY 2`))
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	defer rows.Close()
	type cashier struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	out := []cashier{}
	for rows.Next() {
		var cs cashier
		if err := rows.Scan(&cs.ID, &cs.Name); err == nil {
			out = append(out, cs)
		}
	}
	h.ok(c, out)
}

// GetOrder (orders.view).
func (h *H) GetOrder(c *gin.Context) {
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	order, err := h.svc(c).GetOrderScoped(h.principal(c), id)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	// Detail view rides the connected lifecycle: fulfilment jobs for
	// this sale (scoped like the board) + the activity timeline.
	order.Jobs = h.svc(c).ListDesignJobs(h.principal(c), "", id)
	h.ok(c, order)
}

// Checkout (pos.sell) — the POS terminal's Charge button.
func (h *H) Checkout(c *gin.Context) {
	p := h.principal(c)
	var req models.CheckoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	if req.ClientUUID == "" {
		req.ClientUUID = fmt.Sprintf("srv-%d-%d", time.Now().UnixNano(), p.ID)
	}
	order, err := h.svc(c).Checkout(c.Request.Context(), p, req)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.created(c, order)
}

// VoidOrder (pos.void).
func (h *H) VoidOrder(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body models.VoidRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "reason required")
		return
	}
	order, err := h.svc(c).Void(id, body.Reason, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, order)
}

// RetrySTK (pos.sell) — re-push with (possibly corrected) phone.
func (h *H) RetrySTK(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body models.STKRetryRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "phone required")
		return
	}
	order, err := h.svc(c).RetrySTKWithPhone(c.Request.Context(), id, body.Phone, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, order)
}

// ManualConfirm (payments.manual) — the till receipt-code fallback.
func (h *H) ManualConfirm(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body models.ManualEntryRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "receiptCode required")
		return
	}
	order, err := h.svc(c).ManualConfirm(id, body.ReceiptCode, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, order)
}

// OrderRecheck (pos.sell) — re-verify an order's M-Pesa charges against
// the provider and complete the sale if the money actually arrived.
// The recovery path for late PIN entries: the customer paid after the till
// stopped watching, and the order is stuck Pending. Always returns the
// order plus a cashier-readable message; never voids, never double-completes.
func (h *H) OrderRecheck(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	order, msg, err := h.svc(c).RecheckPayment(id, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, gin.H{"order": order, "message": msg})
}

// MpesaCallback (public) — Daraja webhook. Amount mismatches are flagged
// (discrepancy) rather than trusted — fixes the client-trust flaw seen in
// the mpesa-pos fork. Throttled per IP (unguessable IDs + throttle kills
// blind guessing); routed to the owning shop by checkout request id.
func (h *H) MpesaCallback(c *gin.Context) {
	if h.CallbackRL != nil && !h.CallbackRL.Allow("cb:"+c.ClientIP()) {
		c.JSON(429, gin.H{"ResultCode": 1, "ResultDesc": "rate limited"})
		return
	}
	raw, err := c.GetRawData()
	if err != nil || len(raw) == 0 {
		c.JSON(400, gin.H{"ResultCode": 1, "ResultDesc": "invalid payload"})
		return
	}
	cb, err := mpesaParse(raw)
	if err != nil {
		c.JSON(400, gin.H{"ResultCode": 1, "ResultDesc": "unparseable callback"})
		return
	}
	svc := h.svc(c)
	if h.Shops != nil {
		if shopID, ok := h.Shops.FindCheckoutShop(cb.CheckoutRequestID); ok {
			if s, err := h.Shops.Service(shopID); err == nil {
				svc = s
			}
		}
	}
	if _, err := svc.HandleCallback(cb); err != nil {
		// Answer Daraja politely either way (retries would duplicate);
		// unknown checkout ids are logged server-side.
		c.JSON(200, gin.H{"ResultCode": 0, "ResultDesc": "accepted"})
		return
	}
	c.JSON(200, gin.H{"ResultCode": 0, "ResultDesc": "accepted"})
}

// Sync (pos.sell) — replay of offline-queued checkouts.
func (h *H) Sync(c *gin.Context) {
	p := h.principal(c)
	var req models.SyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	results := h.svc(c).Sync(c.Request.Context(), p, req.Transactions)
	if results == nil {
		results = []models.SyncResult{}
	}
	h.ok(c, results)
}

// trimPercentLabel formats 16.00 → "16", 13.5 → "13.5" for VAT labels
// (mirrors services.trimPercent).
func trimPercentLabel(f float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
	if s == "" {
		return "0"
	}
	return s
}

// ReceiptHTML renders a printable receipt page (browser print fallback for
// shops without a thermal printer).
func (h *H) ReceiptHTML(c *gin.Context) {
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	order, err := h.svc(c).GetOrderScoped(h.principal(c), id)
	if err != nil {
		h.fail(c, 404, "order not found")
		return
	}
	symbol := h.settings(c).GetString("currency_symbol", "KES")
	fm := func(cents int64) string { return printer.FormatMoney(cents, symbol) }
	// Day-first — Kenya reads 02/01/2006, not the American 01/02/2006.
	when := order.PaidAtOrCreated()
	if t, err := time.Parse(time.RFC3339, when); err == nil {
		when = t.Local().Format("02/01/2006 15:04")
	}
	// Honest legs (same rules as the paper + branded-image renderers):
	// a leg the provider has not confirmed must never say "PAID".
	var payLines []string
	tabCharge := false
	for _, pm := range order.Payments {
		if pm.Status == "FAILED" {
			continue
		}
		done := pm.Status == "COMPLETED"
		switch pm.Method {
		case models.MethodCash:
			payLines = append(payLines, "PAID: Cash")
		case models.MethodMpesa:
			s := "M-Pesa (pending)"
			if done {
				s = "PAID: M-Pesa"
				if pm.MpesaReceipt != "" {
					s += " " + pm.MpesaReceipt
				}
			}
			if pm.Phone != "" {
				s += " (" + pm.Phone + ")"
			}
			payLines = append(payLines, s)
		case "account":
			tabCharge = true
			s := "Tab charge"
			if !done {
				s += " (pending)"
			}
			payLines = append(payLines, s)
		case "credit":
			s := "Store credit"
			if !done {
				s += " (pending)"
			}
			payLines = append(payLines, s)
		case "paystack":
			s := "Card / mobile money (pending)"
			if done {
				s = "PAID: Card / mobile money"
			}
			payLines = append(payLines, s)
		}
	}
	if order.Status == models.OrderVoided {
		payLines = append(payLines, "*** VOIDED ***")
	}
	// Gift-card codes minted by this sale — the printed page is the
	// customer's copy of the value, same as paper and the branded image.
	var giftCards []string
	if gcRows, err := h.db(c).Query(`SELECT code FROM gift_cards WHERE order_id = ? ORDER BY id`, order.ID); err == nil {
		for gcRows.Next() {
			var code string
			if gcRows.Scan(&code) == nil {
				giftCards = append(giftCards, code)
			}
		}
		gcRows.Close()
	}
	// What the customer owes NOW on their tab (this charge already moved
	// the balance) — mirrors the branded image's "Tab balance now".
	tabBalance := ""
	if tabCharge && order.CustomerID > 0 {
		var bal int64
		if err := h.db(c).QueryRow(`SELECT balance_cents FROM customers WHERE id = ?`, order.CustomerID).Scan(&bal); err == nil && bal > 0 {
			tabBalance = fm(bal)
		}
	}
	taxIncluded := order.TaxIncluded
	pc := h.settings(c).GetInt("loyalty_point_cents", 100)
	if pc < 0 {
		pc = 100
	}
	data := map[string]any{
		"Store":          h.settings(c).GetString("store_name", "My Store"),
		"Address":        h.settings(c).Get("store_address"),
		"Phone":          h.settings(c).Get("store_phone"),
		"KraPin":         h.settings(c).Get("kra_pin"),
		"Footer":         h.settings(c).Get("receipt_footer"),
		"Number":         order.Number,
		"When":           when,
		"Cashier":        order.CashierName,
		"Customer":       order.CustomerName,
		"BuyerPin":       order.BuyerPIN,
		"Items":          order.Items,
		"Subtotal":       fm(order.SubtotalCents),
		"PointsRedeemed": order.PointsRedeemed,
		"Tax":            fm(order.TaxCents),
		"TaxCents":       order.TaxCents, // int64 gate — the formatted "KES 0.00" string is truthy
		"PointsCents":    order.PointsRedeemed * int64(pc),
		"PointsPts":      order.PointsRedeemed,
		"TaxLabel":       fmt.Sprintf("VAT (%s%%)%s", trimPercentLabel(order.TaxPercent), map[bool]string{true: " — included", false: ""}[taxIncluded]),
		"Total":          fm(order.TotalCents),
		"PayLines":       payLines,
		"GiftCards":      giftCards,
		"TabBalance":     tabBalance,
		"Status":         order.Status,
		"FM":             fm,
	}
	if order.DiscountCents != 0 { // a zero "KES 0.00" string is truthy in templates — gate here
		data["Discount"] = fm(order.DiscountCents)
		if order.DiscountLabel != "" { // bare label — the template wraps it in parens
			data["DiscountLabel"] = order.DiscountLabel
		}
	}
	tpl := template.Must(template.New("receipt").Parse(receiptTemplate))
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := tpl.Execute(c.Writer, data); err != nil {
		h.fail(c, 500, err.Error())
	}
}

const receiptTemplate = `<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Number}}</title>
<style>
        body{font-family:'Courier New',monospace;background:#e5e7eb;margin:0;padding:24px;display:flex;justify-content:center}
        .receipt{background:#fff;width:320px;padding:16px 20px;box-shadow:0 2px 8px rgba(0,0,0,.15);font-size:13px;color:#000}
        .center{text-align:center}.bold{font-weight:700}.large{font-size:16px}
        hr{border:none;border-top:1px dashed #000;margin:8px 0}
        .row{display:flex;justify-content:space-between;gap:8px}
        .line{display:flex;justify-content:space-between;gap:8px}
        .muted{color:#444}
        .voided{color:#b91c1c;font-weight:700;letter-spacing:.15em;text-align:center;font-size:15px}
        @media print{body{background:#fff;padding:0}.receipt{box-shadow:none;width:72mm}.noprint{display:none}}
</style></head><body>
<div class="receipt">
        <div class="center bold large">{{.Store}}</div>
        {{if .Address}}<div class="center muted">{{.Address}}</div>{{end}}
        {{if .Phone}}<div class="center muted">{{.Phone}}</div>{{end}}
        {{if .KraPin}}<div class="center muted">KRA PIN: {{.KraPin}}</div>{{end}}
        <hr>
        <div class="row"><span>{{if .BuyerPin}}Tax Invoice{{else}}Receipt{{end}}</span><span class="bold">{{.Number}}</span></div>
        <div class="row"><span>Date</span><span>{{.When}}</span></div>
        {{if .Cashier}}<div class="row"><span>Served by</span><span>{{.Cashier}}</span></div>{{end}}
        {{if .Customer}}<div class="row"><span>Customer</span><span>{{.Customer}}</span></div>{{end}}
        {{if .BuyerPin}}<div class="row"><span>Buyer PIN</span><span>{{.BuyerPin}}</span></div>{{end}}
        <hr>
        {{range .Items}}
        <div class="bold">{{.Name}}</div>
        <div class="line"><span class="muted">{{.QtyDisplay}} x {{call $.FM .UnitPriceCents}}</span><span>{{call $.FM .LineTotalCents}}</span></div>
        {{end}}
        <hr>
        <div class="row"><span>Subtotal</span><span>{{.Subtotal}}</span></div>
        {{if .Discount}}<div class="row"><span class="muted">Discount{{if .DiscountLabel}} ({{.DiscountLabel}}){{end}}</span><span class="muted">-{{.Discount}}</span></div>{{end}}
        {{if .PointsCents}}<div class="row"><span class="muted">Points redeemed ({{.PointsPts}} pts)</span><span class="muted">-{{call $.FM .PointsCents}}</span></div>{{end}}
        {{if .TaxCents}}<div class="row"><span>{{.TaxLabel}}</span><span>{{.Tax}}</span></div>{{end}}
        <div class="row large bold"><span>TOTAL</span><span>{{.Total}}</span></div>
        <hr>
        {{range .PayLines}}<div class="row"><span>{{.}}</span><span></span></div>{{end}}
        {{range .GiftCards}}<div class="row"><span>Gift card</span><span class="bold">{{.}}</span></div>{{end}}
        {{if .TabBalance}}<div class="row"><span>Tab balance now</span><span>{{.TabBalance}}</span></div>{{end}}
        {{if eq .Status "VOIDED"}}<div class="voided">* * *  V O I D E D  * * *</div>{{end}}
        {{if .Footer}}<hr><div class="center muted">{{.Footer}}</div>{{end}}
        <div class="center noprint" style="margin-top:16px">
                <button onclick="window.print()" style="padding:8px 20px;font-size:14px">Print</button>
        </div>
</div>
</body></html>`

// ReceiptImage (orders.view) — the branded receipt PNG (watermarked, see
// internal/services/receipt_watermark.go) for the Share-via-WhatsApp
// button. ?download=1 sets an attachment filename; default is inline so
// the browser/Web Share path can treat it as a plain image.
func (h *H) ReceiptImage(c *gin.Context) {
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	order, err := h.svc(c).GetOrderScoped(h.principal(c), id)
	if err != nil {
		h.fail(c, 404, "order not found")
		return
	}
	png, err := h.svc(c).ReceiptPNG(order)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	if c.Query("download") == "1" {
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="receipt-%s.png"`, order.Number))
	}
	c.Data(http.StatusOK, "image/png", png)
}
