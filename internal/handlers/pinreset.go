package handlers

// pinreset.go — HTTP surface for the mom-friendly forgot-PIN flow.
//
// Request/status/redeem are PUBLIC endpoints: the person who needs them is
// locked out by definition — no session can exist. Safety comes from the
// cloud (device-identity-gated RPCs, owner-only approval, single-use
// expiring codes) plus local rate limiting here. The owner side (list +
// approve) rides settings.manage like the rest of the Team panel.

import (
        "github.com/gin-gonic/gin"

        "posapp/internal/services"
)

type forgotPinBody struct {
        Username   string `json:"username" binding:"required"`
        DeviceName string `json:"deviceName"`
}

// ForgotPinRequest files a PIN reset request with the cloud (locked till).
func (h *H) ForgotPinRequest(c *gin.Context) {
        var body forgotPinBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "enter the username you sign in with")
                return
        }
        key := "forgotpin:" + h.shopID(c)
        if !h.LoginRL.Allow(key) {
                h.fail(c, 429, "too many attempts — wait a minute")
                return
        }
        info, err := h.svc(c).RequestPinReset(body.Username, body.DeviceName)
        if err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        h.ok(c, info)
}

// ForgotPinStatus reports this till's open request so the PIN screen can
// survive a restart mid-recovery.
func (h *H) ForgotPinStatus(c *gin.Context) {
        req, ok := h.svc(c).PendingPinReset()
        if !ok {
                h.ok(c, gin.H{"active": false})
                return
        }
        h.ok(c, gin.H{"active": true, "request": req})
}

type forgotPinRedeemBody struct {
        RequestID int64  `json:"requestId" binding:"required"`
        Code      string `json:"code" binding:"required"`
        PIN       string `json:"pin" binding:"required"`
}

// ForgotPinRedeem swaps the owner's one-time code for a new local PIN and
// signs the user in (no reason to make them type the PIN twice).
func (h *H) ForgotPinRedeem(c *gin.Context) {
        var body forgotPinRedeemBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "enter the 8-digit code and your new 4-digit PIN")
                return
        }
        key := "forgotpinredeem:" + h.shopID(c)
        if !h.LoginRL.Allow(key) {
                h.fail(c, 429, "too many attempts — wait a minute")
                return
        }
        out, err := h.svc(c).RedeemPinReset(body.RequestID, body.Code, body.PIN)
        if err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        h.LoginRL.Forget(key)
        h.ok(c, out)
}

type pinResetApproveBody struct {
        RequestID int64 `json:"requestId" binding:"required"`
}

// PinResetApprove (owner till): mint + reveal the one-time code.
func (h *H) PinResetApprove(c *gin.Context) {
        var body pinResetApproveBody
        if err := c.ShouldBindJSON(&body); err != nil {
                h.fail(c, 400, "requestId required")
                return
        }
        out, err := h.svc(c).ApprovePinReset(body.RequestID)
        if err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        h.ok(c, out)
}

// PinResetList (owner till): open requests across the team.
func (h *H) PinResetList(c *gin.Context) {
        reqs, err := h.svc(c).ListPinResetRequests()
        if err != nil {
                h.fail(c, 422, err.Error())
                return
        }
        if reqs == nil {
                reqs = []services.PinResetRequest{}
        }
        h.ok(c, reqs)
}
