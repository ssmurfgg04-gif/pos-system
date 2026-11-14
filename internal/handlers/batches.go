package handlers

// batches.go — v1.8.0 batch/expiry endpoints: list a product's lots,
// goods-in without a PO (the milk crate with the expiry printed on it),
// recall a bad lot, and the expiry report that tells the shopkeeper what
// dies this week and what it is worth.

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

// ListProductBatches (products.view) — a product's lots, sellable first.
func (h *H) ListProductBatches(c *gin.Context) {
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	batches, err := h.svc(c).ListProductBatches(id)
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	h.ok(c, batches)
}

// CreateBatchGoodsIn (products.manage) — record stock straight onto a lot
// (no purchase order needed). One tx: batch row + projection + ledger.
func (h *H) CreateBatchGoodsIn(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body struct {
		BatchNo    string `json:"batchNo"`
		ExpiryDate string `json:"expiryDate"`
		QtyMilli   int64  `json:"qtyMilli" binding:"required"`
		CostCents  int64  `json:"costCents"`
		SupplierID int64  `json:"supplierId"`
		Note       string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "qtyMilli is required")
		return
	}
	b, err := h.svc(c).CreateBatchGoodsIn(id, body.BatchNo, body.ExpiryDate, body.QtyMilli, body.CostCents, body.SupplierID, body.Note, p)
	if err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	h.created(c, b)
}

// RecallBatch (products.manage) — pull a lot off the shelf: stops selling,
// remaining quantity leaves the projection, ledger records the write-off.
func (h *H) RecallBatch(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
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
	b, err := h.svc(c).RecallBatch(id, body.Reason, p)
	if err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	h.ok(c, b)
}

// ExpiryReport (reports.view) — every dated lot holding stock inside the
// window (days query param, default 30, -1 = no window), split into
// already-expired and expiring-soon with the money at risk.
func (h *H) ExpiryReport(c *gin.Context) {
	days := 30
	if raw := c.Query("days"); raw != "" {
		d, err := strconv.Atoi(raw)
		if err != nil || d < -1 || d > 3650 {
			h.fail(c, 400, "days must be an integer between -1 and 3650")
			return
		}
		days = d
	}
	expired, soon, valueCents, err := h.svc(c).ListExpiryBatches(days)
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	h.ok(c, gin.H{
		"days":       days,
		"expired":    expired,
		"soon":       soon,
		"valueCents": valueCents,
	})
}
