package handlers

// quotes.go — HTTP surface for the quotation pipeline.

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"posapp/internal/models"
)

type quoteBody struct {
	CustomerID    int64              `json:"customerId"`
	CustomerName  string             `json:"customerName"`
	CustomerPhone string             `json:"customerPhone"`
	Items         []models.QuoteItem `json:"items"`
	DiscountCents int64              `json:"discountCents"`
	DiscountLabel string             `json:"discountLabel"`
	Notes         string             `json:"notes"`
	ValidUntil    string             `json:"validUntil"`
}

// ListQuotes GET /quotes?status=&q=&limit=
func (h *H) ListQuotes(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	quotes, err := h.svc(c).ListQuotes(c.Query("status"), c.Query("q"), limit)
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	if quotes == nil {
		quotes = []models.Quote{}
	}
	h.ok(c, quotes)
}

// GetQuote GET /quotes/:id
func (h *H) GetQuote(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		h.fail(c, 400, "invalid quote id")
		return
	}
	qy, err := h.svc(c).GetQuote(id)
	if err != nil {
		h.fail(c, 404, err.Error())
		return
	}
	h.ok(c, qy)
}

// CreateQuote POST /quotes
func (h *H) CreateQuote(c *gin.Context) {
	var body quoteBody
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "quote needs a customer and at least one line item")
		return
	}
	p := h.principal(c)
	qy, err := h.svc(c).CreateQuote(models.QuoteInput{
		CustomerID:    body.CustomerID,
		CustomerName:  body.CustomerName,
		CustomerPhone: body.CustomerPhone,
		Items:         body.Items,
		DiscountCents: body.DiscountCents,
		DiscountLabel: body.DiscountLabel,
		Notes:         body.Notes,
		ValidUntil:    body.ValidUntil,
	}, p)
	if err != nil {
		h.fail(c, 422, err.Error())
		return
	}
	h.created(c, qy)
}

// UpdateQuote PUT /quotes/:id
func (h *H) UpdateQuote(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		h.fail(c, 400, "invalid quote id")
		return
	}
	var body quoteBody
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "quote needs a customer and at least one line item")
		return
	}
	qy, err := h.svc(c).UpdateQuote(id, models.QuoteInput{
		CustomerID:    body.CustomerID,
		CustomerName:  body.CustomerName,
		CustomerPhone: body.CustomerPhone,
		Items:         body.Items,
		DiscountCents: body.DiscountCents,
		DiscountLabel: body.DiscountLabel,
		Notes:         body.Notes,
		ValidUntil:    body.ValidUntil,
	}, h.principal(c))
	if err != nil {
		h.fail(c, 422, err.Error())
		return
	}
	h.ok(c, qy)
}

type quoteStatusBody struct {
	Status string `json:"status" binding:"required"`
}

// SetQuoteStatus POST /quotes/:id/status
func (h *H) SetQuoteStatus(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		h.fail(c, 400, "invalid quote id")
		return
	}
	var body quoteStatusBody
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "status required")
		return
	}
	qy, err := h.svc(c).SetQuoteStatus(id, strings.TrimSpace(body.Status), h.principal(c))
	if err != nil {
		h.fail(c, 422, err.Error())
		return
	}
	h.ok(c, qy)
}

// ConvertQuote POST /quotes/:id/convert — parks the lines as a held sale
// on this till (resume from the POS Held-sales panel).
func (h *H) ConvertQuote(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		h.fail(c, 400, "invalid quote id")
		return
	}
	held, err := h.svc(c).ConvertQuote(id, h.principal(c))
	if err != nil {
		h.fail(c, 422, err.Error())
		return
	}
	h.ok(c, held)
}
