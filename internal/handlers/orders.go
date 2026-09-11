package handlers

import (
	"fmt"
	"html/template"
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
	orders, err := h.Svc.ListOrders(f)
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	if orders == nil {
		orders = []models.Order{}
	}
	h.ok(c, orders)
}

// GetOrder (orders.view).
func (h *H) GetOrder(c *gin.Context) {
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	order, err := h.Svc.GetOrder(id)
	if err != nil {
		h.mapErr(c, err)
		return
	}
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
	order, err := h.Svc.Checkout(c.Request.Context(), p, req)
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
	order, err := h.Svc.Void(id, body.Reason, p)
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
	order, err := h.Svc.RetrySTKWithPhone(c.Request.Context(), id, body.Phone, p)
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
	order, err := h.Svc.ManualConfirm(id, body.ReceiptCode, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, order)
}

// MpesaCallback (public) — Daraja webhook. Amount mismatches are flagged
// (discrepancy) rather than trusted — fixes the client-trust flaw seen in
// the mpesa-pos fork.
func (h *H) MpesaCallback(c *gin.Context) {
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
	if _, err := h.Svc.HandleCallback(cb); err != nil {
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
	results := h.Svc.Sync(c.Request.Context(), p, req.Transactions)
	if results == nil {
		results = []models.SyncResult{}
	}
	h.ok(c, results)
}

// ReceiptHTML renders a printable receipt page (browser print fallback for
// shops without a thermal printer).
func (h *H) ReceiptHTML(c *gin.Context) {
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	order, err := h.Svc.GetOrder(id)
	if err != nil {
		h.fail(c, 404, "order not found")
		return
	}
	symbol := h.Settings.GetString("currency_symbol", "KES")
	fm := func(cents int64) string { return printer.FormatMoney(cents, symbol) }
	when := order.PaidAtOrCreated()
	if t, err := time.Parse(time.RFC3339, when); err == nil {
		when = t.Local().Format("2006-01-02 15:04")
	}
	var payLines []string
	for _, pm := range order.Payments {
		switch pm.Method {
		case models.MethodCash:
			payLines = append(payLines, "PAID: Cash")
		case models.MethodMpesa:
			s := "PAID: M-Pesa"
			if pm.MpesaReceipt != "" {
				s += " " + pm.MpesaReceipt
			}
			payLines = append(payLines, s)
		}
	}
	if order.Status == models.OrderVoided {
		payLines = append(payLines, "*** VOIDED ***")
	}
	data := map[string]any{
		"Store":     h.Settings.GetString("store_name", "My Store"),
		"Address":   h.Settings.Get("store_address"),
		"Phone":     h.Settings.Get("store_phone"),
		"Footer":    h.Settings.Get("receipt_footer"),
		"Number":    order.Number,
		"When":      when,
		"Cashier":   order.CashierName,
		"Customer":  order.CustomerName,
		"Items":     order.Items,
		"Subtotal":  fm(order.SubtotalCents),
		"Tax":       fm(order.TaxCents),
		"Total":     fm(order.TotalCents),
		"PayLines":  payLines,
		"Status":    order.Status,
		"FM":        fm,
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
	@media print{body{background:#fff;padding:0}.receipt{box-shadow:none;width:72mm}.noprint{display:none}}
</style></head><body>
<div class="receipt">
	<div class="center bold large">{{.Store}}</div>
	{{if .Address}}<div class="center muted">{{.Address}}</div>{{end}}
	{{if .Phone}}<div class="center muted">{{.Phone}}</div>{{end}}
	<hr>
	<div class="row"><span>Receipt</span><span class="bold">{{.Number}}</span></div>
	<div class="row"><span>Date</span><span>{{.When}}</span></div>
	{{if .Cashier}}<div class="row"><span>Served by</span><span>{{.Cashier}}</span></div>{{end}}
	{{if .Customer}}<div class="row"><span>Customer</span><span>{{.Customer}}</span></div>{{end}}
	<hr>
	{{range .Items}}
	<div class="bold">{{.Name}}</div>
	<div class="line"><span class="muted">{{.Qty}} x {{printf "%d" .UnitPriceCents}}</span><span>{{printf "%d" .LineTotalCents}}</span></div>
	{{end}}
	<hr>
	<div class="row"><span>Subtotal</span><span>{{.Subtotal}}</span></div>
	<div class="row"><span>Tax</span><span>{{.Tax}}</span></div>
	<div class="row large bold"><span>TOTAL</span><span>{{.Total}}</span></div>
	<hr>
	{{range .PayLines}}<div class="row"><span>{{.}}</span><span></span></div>{{end}}
	{{if .Footer}}<hr><div class="center muted">{{.Footer}}</div>{{end}}
	<div class="center noprint" style="margin-top:16px">
		<button onclick="window.print()" style="padding:8px 20px;font-size:14px">Print</button>
	</div>
</div>
</body></html>`
