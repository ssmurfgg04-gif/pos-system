package handlers

// jobs.go — HTTP layer for the connected job lifecycle (P3) and WhatsApp
// customer contact (P4). Permission notes: creating jobs from a sale and
// the timeline ride the order's permissions; the notify endpoint is gated
// by orders.notify (Front Desk, Branding, Cyber, Owner).

import (

	"github.com/gin-gonic/gin"

	"posapp/internal/models"
	"posapp/internal/services"
)

// ListAssignees (orders.assign) — active staff for the assign dropdown.
func (h *H) ListAssignees(c *gin.Context) {
	h.ok(c, h.svc(c).ListAssignees())
}

// GetOrderEvents (orders.view) — the per-order activity timeline.
func (h *H) GetOrderEvents(c *gin.Context) {
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	h.ok(c, h.svc(c).GetOrderEvents(id))
}

// AddOrderNote (orders.view) — staff notes on the order timeline.
func (h *H) AddOrderNote(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body struct {
		Message string `json:"message" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "message required")
		return
	}
	if _, err := h.svc(c).GetOrder(id); err != nil {
		h.mapErr(c, err)
		return
	}
	h.svc(c).AddOrderEvent(id, models.OrderEventNote, body.Message, p)
	h.created(c, gin.H{"added": true})
}

// CreateJobFromOrder (orders.assign) — turn a sale into a tracked job.
func (h *H) CreateJobFromOrder(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var in services.DesignJobInput
	// Title optional here (defaults from the order), so bind leniently.
	_ = c.ShouldBindJSON(&in)
	job, err := h.svc(c).CreateJobFromOrder(p, id, in)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.created(c, job)
}

// NotifyOrderReady (orders.notify) — prepare the WhatsApp deep link for a
// sale's customer. Returns the link + prefilled message; logs the attempt.
func (h *H) NotifyOrderReady(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	contact, err := h.svc(c).NotifyReady(p, 0, id)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, contact)
}

// NotifyJobReady (orders.notify) — same, straight from a job card.
func (h *H) NotifyJobReady(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	contact, err := h.svc(c).NotifyReady(p, id, 0)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, contact)
}

