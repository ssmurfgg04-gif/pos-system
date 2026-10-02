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
        summary, err := h.svc(c).GetDailySummary(date)
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
        shift, err := h.svc(c).OpenShift(p, body.OpeningFloatCents)
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
        shift, err := h.svc(c).CloseShift(p, body.CountedCents)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.ok(c, shift)
}

func (h *H) CurrentShift(c *gin.Context) {
        shift, err := h.svc(c).CurrentShift(h.principal(c).ID)
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
        shifts := h.svc(c).ListShifts(userID, 30)
        if shifts == nil {
                shifts = []models.Shift{}
        }
        h.ok(c, shifts)
}

// ShiftReportXZ (shifts.manage) — the X/Z cash-session report: X for the
// caller's open shift (live totals), Z for any closed shift (?shift=id).
func (h *H) ShiftReportXZ(c *gin.Context) {
        p := h.principal(c)
        var rep any
        var err error
        if v := c.Query("shift"); v != "" {
                var id int64
                if id, err = parseI64(v); err == nil {
                        rep, err = h.svc(c).GetShiftReport(id)
                }
        } else {
                rep, err = h.svc(c).CurrentShiftReport(p.ID)
        }
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.ok(c, rep)
}

// ConsolidatedDailyReport (reports.view) — ?shop=all sums the daily
// summary across every shop the user belongs to (owner's multi-store
// view on a server / till running several shops). Per-shop rows ride
// along so a branch that's off still shows. Single-shop callers get the
// plain summary, unchanged.
func (h *H) ConsolidatedDailyReport(c *gin.Context) {
        if c.Query("shop") != "all" || h.Tenants == nil || h.Shops == nil {
                h.DailyReport(c)
                return
        }
        p := h.principal(c)
        ids := h.Tenants.ShopsForUser(p.Username)
        date := c.Query("date")
        out := gin.H{"date": date, "consolidated": true, "shops": []gin.H{}}
        var totalSales, cash, mpesa, paystack, credit, avg int64
        var paid, open, voided, disc, n int
        for _, sid := range ids {
                svc, err := h.Shops.Service(sid)
                if err != nil {
                        continue
                }
                s, err := svc.GetDailySummary(date)
                if err != nil || s == nil {
                        continue
                }
                name := sid
                if shop, ok := h.Tenants.Find(sid); ok {
                        name = shop.Name
                }
                totalSales += s.SalesCents
                cash += s.CashCents
                mpesa += s.MpesaCents
                paystack += s.PaystackCents
                credit += s.CreditCents
                paid += s.OrdersPaid
                open += s.OrdersOpen
                voided += s.OrdersVoided
                disc += s.Discrepancies
                avg += s.AvgOrderCents
                n++
                out["shops"] = append(out["shops"].([]gin.H), gin.H{
                        "id": sid, "name": name,
                        "salesCents": s.SalesCents, "ordersPaid": s.OrdersPaid,
                        "cashCents": s.CashCents, "mpesaCents": s.MpesaCents,
                        "paystackCents": s.PaystackCents, "creditCents": s.CreditCents,
                        "ordersVoided": s.OrdersVoided, "discrepancies": s.Discrepancies,
                })
        }
        if n > 0 {
                avg /= int64(n)
        }
        out["salesCents"] = totalSales
        out["ordersPaid"] = paid
        out["ordersOpen"] = open
        out["ordersVoided"] = voided
        out["avgOrderCents"] = avg
        out["cashCents"] = cash
        out["mpesaCents"] = mpesa
        out["paystackCents"] = paystack
        out["creditCents"] = credit
        out["discrepancies"] = disc
        h.ok(c, out)
}

// ---- Design board ----

func (h *H) ListDesignJobs(c *gin.Context) {
        p := h.principal(c)
        orderID, _ := strconv.ParseInt(c.Query("orderId"), 10, 64)
        jobs := h.svc(c).ListDesignJobs(p, c.Query("status"), orderID)
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
        job, err := h.svc(c).CreateDesignJob(p, in)
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
        job, err := h.svc(c).UpdateDesignJob(p, id, in)
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
        job, err := h.svc(c).MoveDesignJob(p, id, body.Status)
        if err != nil {
                h.mapErr(c, err)
                return
        }
        h.ok(c, job)
}

// ---- Audit ----

func (h *H) ListAudit(c *gin.Context) {
        entries := h.svc(c).ListAudit(200)
        if entries == nil {
                entries = []models.AuditEntry{}
        }
        h.ok(c, entries)
}
