package handlers

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"posapp/internal/mpesa"
	"posapp/internal/models"
	"posapp/internal/services"
)

func mpesaParse(raw []byte) (mpesa.CallbackResult, error) {
	return mpesa.ParseCallback(raw)
}

func parseI64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// ---- Reports ----

// DailyReport (reports.view).
func (h *H) DailyReport(c *gin.Context) {
	date := c.Query("date")
	summary, err := h.Svc.GetDailySummary(date)
	if err != nil {
		h.fail(c, 500, err.Error())
		return
	}
	h.ok(c, summary)
}

// ---- Shifts ----

type shiftOpenBody struct {
	OpeningFloatCents int64 `json:"openingFloatCents" binding:"min=0"`
}

func (h *H) OpenShift(c *gin.Context) {
	p := h.principal(c)
	var body shiftOpenBody
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	shift, err := h.Svc.OpenShift(p, body.OpeningFloatCents)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.created(c, shift)
}

type shiftCloseBody struct {
	CountedCents int64 `json:"countedCents" binding:"min=0"`
}

func (h *H) CloseShift(c *gin.Context) {
	p := h.principal(c)
	var body shiftCloseBody
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	shift, err := h.Svc.CloseShift(p, body.CountedCents)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, shift)
}

func (h *H) CurrentShift(c *gin.Context) {
	shift, err := h.Svc.CurrentShift(h.principal(c).ID)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	if shift == nil {
		h.ok(c, nil)
		return
	}
	h.ok(c, shift)
}

func (h *H) ListShifts(c *gin.Context) {
	var userID int64
	if v := c.Query("userId"); v != "" {
		if id, err := parseI64(v); err == nil {
			userID = id
		}
	}
	shifts := h.Svc.ListShifts(userID, 30)
	if shifts == nil {
		shifts = []models.Shift{}
	}
	h.ok(c, shifts)
}

// ---- Design board ----

func (h *H) ListDesignJobs(c *gin.Context) {
	jobs := h.Svc.ListDesignJobs(c.Query("status"))
	if jobs == nil {
		jobs = []models.DesignJob{}
	}
	h.ok(c, jobs)
}

func (h *H) CreateDesignJob(c *gin.Context) {
	p := h.principal(c)
	var in services.DesignJobInput
	if err := c.ShouldBindJSON(&in); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	job, err := h.Svc.CreateDesignJob(p, in)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.created(c, job)
}

func (h *H) UpdateDesignJob(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var in services.DesignJobInput
	if err := c.ShouldBindJSON(&in); err != nil {
		h.fail(c, 400, err.Error())
		return
	}
	job, err := h.Svc.UpdateDesignJob(p, id, in)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, job)
}

func (h *H) MoveDesignJob(c *gin.Context) {
	p := h.principal(c)
	id, ok := h.pathID(c, "id")
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "status required")
		return
	}
	job, err := h.Svc.MoveDesignJob(p, id, body.Status)
	if err != nil {
		h.mapErr(c, err)
		return
	}
	h.ok(c, job)
}

// ---- Audit ----

func (h *H) ListAudit(c *gin.Context) {
	entries := h.Svc.ListAudit(200)
	if entries == nil {
		entries = []models.AuditEntry{}
	}
	h.ok(c, entries)
}
