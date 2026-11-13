package handlers

// expenses.go — HTTP layer for the shop expense log (v1.6.3).

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"posapp/internal/models"
)

// ListExpenseTypes (expenses.view) — GET /api/v1/expenses/types
func (h *H) ListExpenseTypes(c *gin.Context) {
	types, err := h.svc(c).ListExpenseTypes()
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	h.ok(c, types)
}

// CreateExpense (expenses.manage) — POST /api/v1/expenses
func (h *H) CreateExpense(c *gin.Context) {
	p := h.principal(c)
	var body models.CreateExpenseRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	doc, err := h.svc(c).CreateExpense(body, p)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.created(c, doc)
}

// ListExpenses (expenses.view) — GET /api/v1/expenses?date=YYYY-MM-DD
func (h *H) ListExpenses(c *gin.Context) {
	limit := 200
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	docs, err := h.svc(c).ListExpenses(c.Query("date"), limit)
	if err != nil {
		h.fail(c, 422, err.Error())
		return
	}
	h.ok(c, docs)
}
