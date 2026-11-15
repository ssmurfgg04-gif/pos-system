package handlers

// timecards.go — staff time clock and timecards (v1.12.0).

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"posapp/internal/services"
)

type punchBody struct {
	UserID int64  `json:"userId" binding:"required"`
	PIN    string `json:"pin" binding:"required,len=4"`
	ShopID string `json:"shopId"`
	Action string `json:"action" binding:"required"`
}

// PinPunch (public, PIN pad) — POST /api/v1/timeclock/punch
// Staff clock in/out with their own PIN without signing in; same rate
// limits and lockout as PIN sign-in.
func (h *H) PinPunch(c *gin.Context) {
	var body punchBody
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "userId, 4-digit pin and action required")
		return
	}
	shopID, p, ok := h.checkPin(c, pinBody{UserID: body.UserID, PIN: body.PIN, ShopID: body.ShopID})
	if !ok {
		return
	}
	svc, err := h.Shops.Service(shopID)
	if err != nil {
		h.fail(c, 500, "shop unavailable")
		return
	}
	e, err := svc.Punch(p.ID, p.Username, body.Action)
	if err != nil {
		h.punchErr(c, err)
		return
	}
	h.ok(c, gin.H{"entry": e, "name": p.User().FullName})
}

func (h *H) punchErr(c *gin.Context, err error) {
	switch err {
	case services.ErrAlreadyClockedIn, services.ErrNotClockedIn:
		h.fail(c, 409, err.Error())
	default:
		h.mapErr(c, err)
	}
}

// MyClock — GET /api/v1/timeclock/me (any signed-in user)
func (h *H) MyClock(c *gin.Context) {
	p := h.principal(c)
	e, err := h.svc(c).ClockStatus(p.ID)
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	week, err := h.svc(c).Timecards(c.Query("from"), c.Query("to"), p.ID)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, gin.H{"open": e, "report": week})
}

// MyPunch — POST /api/v1/timeclock/me {action}
func (h *H) MyPunch(c *gin.Context) {
	p := h.principal(c)
	var body struct {
		Action string `json:"action" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "action required")
		return
	}
	e, err := h.svc(c).Punch(p.ID, p.Username, body.Action)
	if err != nil {
		h.punchErr(c, err)
		return
	}
	h.ok(c, e)
}

// Timecards (timecards.manage) — GET /api/v1/timecards?from&to&userId
func (h *H) Timecards(c *gin.Context) {
	uid, _ := strconv.ParseInt(c.Query("userId"), 10, 64)
	rep, err := h.svc(c).Timecards(c.Query("from"), c.Query("to"), uid)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, rep)
}

// TimecardsCSV (timecards.manage) — GET /api/v1/timecards.csv?from&to
func (h *H) TimecardsCSV(c *gin.Context) {
	out, err := h.svc(c).TimecardsCSV(c.Query("from"), c.Query("to"))
	if err != nil {
		h.mapErr(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="timecards.csv"`)
	c.Data(200, "text/csv; charset=utf-8", []byte(out))
}

// AddTimeEntry (timecards.manage) — POST /api/v1/timecards {userId, ...}
func (h *H) AddTimeEntry(c *gin.Context) {
	var body struct {
		UserID int64 `json:"userId" binding:"required"`
		services.TimeEntryEdit
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	e, err := h.svc(c).AddTimeEntry(body.UserID, body.TimeEntryEdit, h.principal(c))
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.created(c, e)
}

// EditTimeEntry (timecards.manage) — PUT /api/v1/timecards/:id
func (h *H) EditTimeEntry(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		h.fail(c, 400, "bad id")
		return
	}
	var body services.TimeEntryEdit
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	e, err := h.svc(c).EditTimeEntry(id, body, h.principal(c))
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, e)
}

// VoidTimeEntry (timecards.manage) — POST /api/v1/timecards/:id/void {reason}
func (h *H) VoidTimeEntry(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		h.fail(c, 400, "bad id")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = c.ShouldBindJSON(&body)
	if err := h.svc(c).VoidTimeEntry(id, body.Reason, h.principal(c)); err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, gin.H{"ok": true})
}

// SetHourlyRate (timecards.manage) — PUT /api/v1/timecards/rates/:userId {hourlyRateCents}
func (h *H) SetHourlyRate(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("userId"), 10, 64)
	if err != nil {
		h.fail(c, 400, "bad id")
		return
	}
	var body struct {
		HourlyRateCents int64 `json:"hourlyRateCents"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	if err := h.svc(c).SetHourlyRate(id, body.HourlyRateCents, h.principal(c)); err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, gin.H{"ok": true})
}
