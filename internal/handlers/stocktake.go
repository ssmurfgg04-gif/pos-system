package handlers

// stocktake.go — HTTP surface for stock counts and gift cards.

import (
        "strconv"

        "github.com/gin-gonic/gin"
        "posapp/internal/models"
)

type saveCountLineBody struct {
        ProductID  int64 `json:"productId" binding:"required"`
        CountedQty *int  `json:"countedQty"`
}

type completeCountBody struct {
        Apply bool `json:"apply"`
}

// StartStockCount (inventory.manage) opens a counting session.
func (h *H) StartStockCount(c *gin.Context) {
        p := h.principal(c)
        var body struct {
                Note string `json:"note"`
        }
        _ = c.ShouldBindJSON(&body) // note optional
        cnt, err := h.svc(c).StartStockCount(p, body.Note)
        if err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        h.created(c, cnt)
}

// ListStockCounts (inventory.manage) — recent sessions.
func (h *H) ListStockCounts(c *gin.Context) {
        limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
        list, err := h.svc(c).ListStockCounts(limit)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.ok(c, gin.H{"counts": list})
}

// GetStockCount (inventory.manage) — header + lines.
func (h *H) GetStockCount(c *gin.Context) {
        id, err := strconv.ParseInt(c.Param("id"), 10, 64)
        if err != nil {
                h.fail(c, 400, "invalid id")
                return
        }
        cnt, err := h.svc(c).GetStockCount(id)
        if err != nil {
                h.fail(c, 404, "count session not found")
                return
        }
        lines, err := h.svc(c).GetStockCountLines(id)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.ok(c, gin.H{"count": cnt, "lines": lines})
}

// SaveCountLine (inventory.manage) — record one counted quantity.
func (h *H) SaveCountLine(c *gin.Context) {
        p := h.principal(c)
        id, err := strconv.ParseInt(c.Param("id"), 10, 64)
        if err != nil {
                h.fail(c, 400, "invalid id")
                return
        }
        var body saveCountLineBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        if err := h.svc(c).SaveCountLine(p, id, body.ProductID, body.CountedQty); err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        h.ok(c, gin.H{"saved": true})
}

// CompleteStockCount (inventory.manage) — close; apply=true sets stock.
func (h *H) CompleteStockCount(c *gin.Context) {
        p := h.principal(c)
        id, err := strconv.ParseInt(c.Param("id"), 10, 64)
        if err != nil {
                h.fail(c, 400, "invalid id")
                return
        }
        var body completeCountBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        cnt, err := h.svc(c).CompleteStockCount(p, id, body.Apply)
        if err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        h.ok(c, cnt)
}

// ListGiftCards (credit.manage) — minted codes, optional orderId filter.
func (h *H) ListGiftCards(c *gin.Context) {
        orderID, _ := strconv.ParseInt(c.DefaultQuery("orderId", "0"), 10, 64)
        cards, err := h.svc(c).ListGiftCards(orderID, 200)
        if err != nil {
                h.fail(c, 500, err.Error())
                return
        }
        h.ok(c, gin.H{"cards": cards})
}

// RedeemGiftCard (credit.manage) — convert a code to store credit.
func (h *H) RedeemGiftCard(c *gin.Context) {
        p := h.principal(c)
        var body struct {
                Code       string `json:"code" binding:"required"`
                CustomerID int64  `json:"customerId" binding:"required"`
        }
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, err.Error())
                return
        }
        cust, err := h.svc(c).RedeemGiftCard(p, body.Code, body.CustomerID)
        if err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        h.ok(c, gin.H{"customer": cust})
}

var _ = models.OrderPending
