package handlers

// returns.go — HTTP layer for customer returns (v1.6.3). The Orders page
// "Return" modal posts here; the service guarantees one-transaction
// restock + refund.

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"posapp/internal/models"
)

// CreateReturn (returns.manage) — POST /api/v1/returns
func (h *H) CreateReturn(c *gin.Context) {
	p := h.principal(c)
	var body models.CreateReturnRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	doc, err := h.svc(c).CreateReturn(body, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.created(c, doc)
}

// ListReturns (returns.manage or orders.view) — GET /api/v1/returns
func (h *H) ListReturns(c *gin.Context) {
	orderID := int64(0)
	if v := c.Query("orderId"); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			orderID = id
		}
	}
	limit := 100
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	docs, err := h.svc(c).ListReturns(orderID, limit)
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	h.ok(c, docs)
}

// GetReturn (orders.view) — GET /api/v1/returns/:id
func (h *H) GetReturn(c *gin.Context) {
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	doc, err := h.svc(c).GetReturn(id)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, doc)
}

// ListProductMovements (products.view) — GET /api/v1/products/:id/movements
func (h *H) ListProductMovements(c *gin.Context) {
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	limit := 100
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	movements, err := h.svc(c).ListProductMovements(id, limit)
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	h.ok(c, movements)
}

// ListStockMovements (products.view) — GET /api/v1/stock-movements
func (h *H) ListStockMovements(c *gin.Context) {
	limit := 200
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	movements, err := h.svc(c).ListStockMovements(c.Query("kind"), c.Query("date"), limit)
	if err != nil {
		h.fail(c, http.StatusBadRequest, err.Error())
		return
	}
	h.ok(c, movements)
}
