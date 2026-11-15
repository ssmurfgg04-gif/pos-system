package handlers

// transfers.go — stock transfers between shops (v1.12.0).

import (
	"errors"

	"github.com/gin-gonic/gin"

	"posapp/internal/services"
)

// ListTransfers (transfers.manage) — GET /api/v1/transfers
func (h *H) ListTransfers(c *gin.Context) {
	out, err := h.svc(c).ListTransfers()
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	h.ok(c, out)
}

// SendTransfer (transfers.manage) — POST /api/v1/transfers
func (h *H) SendTransfer(c *gin.Context) {
	var body services.SendTransferRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	t, err := h.svc(c).SendTransfer(body, h.principal(c))
	if err != nil {
		h.transferErr(c, err)
		return
	}
	h.created(c, t)
}

// ReceiveTransfer (transfers.manage) — POST /api/v1/transfers/:id/receive {lines:[{sku,qtyMilli}]}
func (h *H) ReceiveTransfer(c *gin.Context) {
	var body struct {
		Lines []services.TransferQty `json:"lines"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	t, err := h.svc(c).ReceiveTransfer(c.Param("id"), body.Lines, h.principal(c))
	if err != nil {
		h.transferErr(c, err)
		return
	}
	h.ok(c, t)
}

// CancelTransfer (transfers.manage) — POST /api/v1/transfers/cancel {clientRef, toTeam}
func (h *H) CancelTransfer(c *gin.Context) {
	var body struct {
		ClientRef string `json:"clientRef" binding:"required"`
		ToTeam    string `json:"toTeam"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	t, err := h.svc(c).CancelTransfer(body.ClientRef, body.ToTeam, h.principal(c))
	if err != nil {
		h.transferErr(c, err)
		return
	}
	h.ok(c, t)
}

func (h *H) transferErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, services.ErrTransfersOffline):
		h.fail(c, 422, err.Error())
	case errors.Is(err, services.ErrNotFound), errors.Is(err, services.ErrInvalidInput),
		errors.Is(err, services.ErrInvalidState), errors.Is(err, services.ErrInsufficientStock):
		h.mapErr(c, err)
	default:
		h.fail(c, 503, err.Error()) // cloud unreachable: try again
	}
}
