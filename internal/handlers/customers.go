package handlers

import (
	"github.com/gin-gonic/gin"
)

// ListCustomers (customers.view) — ?search=name-or-phone.
func (h *H) ListCustomers(c *gin.Context) {
	list, err := h.Svc.ListCustomers(c.Query("search"))
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
		LimitCents int64  `json:"creditLimitCents"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	customer, err := h.Svc.CreateCustomer(body.Name, body.Phone, body.LimitCents, p)
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
		LimitCents int64  `json:"creditLimitCents"`
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
	customer, err := h.Svc.UpdateCustomer(id, body.Name, body.Phone, body.LimitCents, active, p)
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
	entries, err := h.Svc.CustomerLedger(id)
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
	customer, err := h.Svc.RecordCustomerPayment(id, body.AmountCents, body.Note, p)
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
	customer, err := h.Svc.RecordCustomerAdjustment(id, body.AmountCents, body.Note, p)
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
	order, err := h.Svc.SettleTab(id, body.Method, body.ReceiptCode, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, order)
}
