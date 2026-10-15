package handlers

// team_owner.go — the Owner-approval dialog endpoint (v1.3.2).
//
// Cloud owner RPCs (store create/assign, device approve/remove, invites,
// portal credentials, PIN-reset approval) verify a HUMAN via a short-lived
// capability minted from the owner's portal credential. The Settings → Team
// panel collects the username+password ONCE, this endpoint mints + caches
// the capability, and every subsequent owner action reuses it silently.

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"posapp/internal/services"
)

type ownerApprovalBody struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// TeamOwnerApproval (settings.manage) proves the owner's portal credential
// to the cloud and caches the capability for this till's session.
func (h *H) TeamOwnerApproval(c *gin.Context) {
	p := h.principal(c)
	var body ownerApprovalBody
	if err := c.ShouldBindJSON(&body); err != nil {
		h.fail(c, 400, "owner username and password are required")
		return
	}
	owner, expiresIn, err := h.svc(c).MintOwnerCapability(body.Username, body.Password)
	if err != nil {
		if services.OwnerApprovalRequired(err) || services.IsOwnerAuthError(err) {
			// 422, never 401: a wrong portal password is THIS dialog's
			// problem, not a dead session. 401 makes the frontend's
			// session-expiry interceptor wipe the token and dump the
			// owner on the login page mid-task (the kick-out bug).
			h.fail(c, 422, err.Error())
			return
		}
		h.fail(c, 502, err.Error())
		return
	}
	h.svc(c).Audit(p.ID, p.Username, "OWNER_APPROVAL", "settings", "",
		"cloud owner capability minted for "+owner)
	h.ok(c, gin.H{
		"owner":      owner,
		"expiresAt":  time.Now().Add(expiresIn).UTC().Format(time.RFC3339),
		"expiresInS": int(expiresIn.Seconds()),
	})
}

// OwnerApprovalStatus tells the frontend whether an approval is already
// cached (so the Team panel can show a quiet "approved as <owner>" chip).
func (h *H) TeamOwnerApprovalStatus(c *gin.Context) {
	h.ok(c, gin.H{"state": services.MarshalOwnerCapabilityState()})
}

var _ = http.StatusOK
