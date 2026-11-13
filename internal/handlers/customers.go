package handlers

import (
	"github.com/gin-gonic/gin"
)

// ListCustomers (customers.view) — ?search=name-or-phone.
func (h *H) ListCustomers(c *gin.Context) {
	list, err := h.svc(c).ListCustomers(c.Query("search"))
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	h.ok(c, list)
}

// CreateCustomer (customers.manage).
func (h *H) CreateCustomer(c *gin.Context) {
	p := h.principal(c)
	var body struct {
		Name       string `json:"name" binding:"required"`
		Phone      string `json:"phone"`
		Email      string `json:"email"`
		Notes      string `json:"notes"`
		LimitCents int64  `json:"creditLimitCents"`
		PriceTier  string `json:"priceTier"` // 'retail' | 'wholesale' (v1.6.3)
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	customer, err := h.svc(c).CreateCustomer(body.Name, body.Phone, body.Email, body.Notes, body.LimitCents, body.PriceTier, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.created(c, customer)
}

// UpdateCustomer (customers.manage).
func (h *H) UpdateCustomer(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body struct {
		Name       string `json:"name" binding:"required"`
		Phone      string `json:"phone"`
		Email      string `json:"email"`
		Notes      string `json:"notes"`
		LimitCents int64  `json:"creditLimitCents"`
		PriceTier  string `json:"priceTier"` // 'retail' | 'wholesale' (v1.6.3)
		Active     *bool  `json:"active"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	active := true
	if body.Active != nil {
		active = *body.Active
	}
	customer, err := h.svc(c).UpdateCustomer(id, body.Name, body.Phone, body.Email, body.Notes, body.LimitCents, body.PriceTier, active, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, customer)
}

// CustomerLedger (customers.view) — newest-first entries.
func (h *H) CustomerLedger(c *gin.Context) {
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	entries, err := h.svc(c).CustomerLedger(id)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, entries)
}

// RecordCustomerPayment (pos.sell) — walk-in cash against a tab.
func (h *H) RecordCustomerPayment(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body struct {
		AmountCents int64  `json:"amountCents" binding:"required"`
		Note        string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	customer, err := h.svc(c).RecordCustomerPayment(id, body.AmountCents, body.Note, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, customer)
}

// RecordCustomerAdjustment (customers.manage) — signed correction.
func (h *H) RecordCustomerAdjustment(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body struct {
		AmountCents int64  `json:"amountCents"`
		Note        string `json:"note" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	customer, err := h.svc(c).RecordCustomerAdjustment(id, body.AmountCents, body.Note, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, customer)
}

// AddOpeningBalance (customers.manage, v1.7.0) — record the debt a
// customer carried in when the shop joined, as a first-class ledger row
// (the SmartPOS opening-balance lesson: bare numbers can't be receipted).
func (h *H) AddOpeningBalance(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body struct {
		AmountCents int64  `json:"amountCents"`
		Note        string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	customer, err := h.svc(c).AddOpeningBalance(id, body.AmountCents, body.Note, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, customer)
}

// ReverseLedgerEntry (customers.manage, v1.7.0) — soft-reverse one manual
// ledger entry with a mirrored row; the original is never touched.
func (h *H) ReverseLedgerEntry(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	entryID, ok := h.pathID(c, "entryId")
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "reason is required")
		return
	}
	customer, err := h.svc(c).ReverseLedgerEntry(id, entryID, body.Reason, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, customer)
}

// SettleTab (pos.sell) — complete a tab order by cash or M-Pesa receipt.
func (h *H) SettleTab(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body struct {
		Method      string `json:"method" binding:"required,oneof=cash mpesa"`
		ReceiptCode string `json:"receiptCode"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	order, err := h.svc(c).SettleTab(id, body.Method, body.ReceiptCode, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, order)
}

// CustomerOrders (customers.view) — the shared purchase history: every
// sale linked to this CRM record, newest first.
func (h *H) CustomerOrders(c *gin.Context) {
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	orders, err := h.svc(c).GetCustomerOrders(id, h.principal(c))
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, orders)
}
