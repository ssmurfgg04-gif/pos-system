package services

// pinreset.go — mom-friendly "I forgot my PIN" recovery.
//
// The problem this solves: user accounts (and their PINs) are LOCAL to each
// till — they never sync. So when a cashier forgets their PIN on a till
// where nobody is logged in as an admin, the admin's SetPIN endpoint (which
// needs a live admin session on THAT machine) is unreachable, and the till
// locks the account after too many tries. A real shop hit exactly this and
// had to stop selling.
//
// The fix rides the existing cloud trust fabric: the locked till files a
// reset request with the LedgerPOS cloud (device-identity verified), the
// owner's till shows it under Settings → Team, the owner taps Approve and
// reads out a single-use 8-digit code (15-minute expiry, bound to the
// requesting device, max 6 wrong tries), and the worker types it here to
// pick a new PIN. No email, no SMS, no CLI, no second admin session.
//
// Security properties:
//   - the cloud only serves request/redeem to approved, non-revoked devices
//     (device_id + secret hash on every call);
//   - only the OWNER till (settings.manage) can list/approve requests;
//   - the code is 8 digits of crypto randomness, stored hashed, shown once;
//   - redeem is bound to the requesting device — a code read to the wrong
//     till is useless;
//   - every step is audited locally.

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"posapp/internal/auth"
	"posapp/internal/hash"
	"posapp/internal/landelivery"
	"posapp/internal/models"
	"posapp/internal/settings"
)

const (
	pinResetReqIDKey   = "pinreset_req_id"
	pinResetReqUserKey = "pinreset_req_user"
	pinResetReqAtKey   = "pinreset_req_at"
	// Code pre-filled over the LAN by a sibling till (see landelivery).
	// Only a pre-fill: the cloud still verifies and burns the code on
	// redeem, so a forged or stale push can never set a PIN.
	pinResetPushedCodeKey = "pinreset_code_pushed"
)

// PinResetRequest is what the owner till sees in Settings → Team.
type PinResetRequest struct {
	ID         int64  `json:"id"`
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
	Username   string `json:"username"`
	CreatedAt  string `json:"createdAt"`
}

// RequestPinReset files a "this till forgot the PIN for <username>" request
// with the cloud. Re-filing while one is open returns the same request.
func (s *Service) RequestPinReset(username, deviceName string) (models.PinResetRequested, error) {
	out := models.PinResetRequested{}
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 60 {
		return out, fmt.Errorf("enter the username you sign in with")
	}
	c, err := s.ownerClient()
	if err != nil {
		return out, fmt.Errorf("PIN recovery needs the shop's cloud link, join the team first, or ask the owner for a fresh join link")
	}
	var res struct {
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
		RequestID int64  `json:"request_id"`
		Reused    bool   `json:"reused"`
	}
	if err := rpcCall(c.base, "sync_request_pin_reset", map[string]any{
		"p_device_id":   c.device,
		"p_secret_hash": c.secretHash,
		"p_username":    username,
		"p_device_name": deviceName,
	}, &res); err != nil {
		return out, fmt.Errorf("could not reach the LedgerPOS cloud, check the internet and try again")
	}
	if !res.OK {
		return out, fmt.Errorf("%s", res.Error)
	}
	// Remember locally so the PIN screen can show live state after a restart.
	_ = s.settings.Set(pinResetReqIDKey, fmt.Sprint(res.RequestID))
	_ = s.settings.Set(pinResetReqUserKey, username)
	_ = s.settings.Set(pinResetReqAtKey, nowStamp())
	out.RequestID = res.RequestID
	out.Username = username
	out.Reused = res.Reused
	s.Audit(0, "system", "PIN_RESET_REQUESTED", "user", username, fmt.Sprintf("request %d", res.RequestID))
	return out, nil
}

// PendingPinReset returns this till's open request (for the PIN screen),
// including a code pushed over the LAN by the owner till (empty when the
// owner has not approved yet or the till has no LAN reachability).
func (s *Service) PendingPinReset() (models.PinResetRequested, bool) {
	out := models.PinResetRequested{}
	id := s.settings.Get(pinResetReqIDKey)
	if id == "" {
		return out, false
	}
	fmt.Sscanf(id, "%d", &out.RequestID)
	if out.RequestID == 0 {
		return out, false
	}
	out.Username = s.settings.Get(pinResetReqUserKey)
	out.CreatedAt = s.settings.Get(pinResetReqAtKey)
	out.PushedCode = s.settings.Get(pinResetPushedCodeKey)
	return out, true
}

func (s *Service) clearPendingPinReset() {
	_ = s.settings.Set(pinResetReqIDKey, "")
	_ = s.settings.Set(pinResetReqUserKey, "")
	_ = s.settings.Set(pinResetReqAtKey, "")
	_ = s.settings.Set(pinResetPushedCodeKey, "")
}

// StorePushedPinResetCode accepts a LAN-delivered code pre-fill. It only
// lands when this till has an ACTIVE reset request with the exact id sent
// — a code for anything else is refused, so the LAN listener can never
// plant a code without a real, matching request.
func (s *Service) StorePushedPinResetCode(requestID int64, code string) error {
	id := s.settings.Get(pinResetReqIDKey)
	var pending int64
	fmt.Sscanf(id, "%d", &pending)
	if pending <= 0 || pending != requestID {
		return fmt.Errorf("no matching reset request on this till")
	}
	return s.settings.Set(pinResetPushedCodeKey, strings.TrimSpace(code))
}

// PendingPinResetRequestID returns the id of this till's open reset
// request (0 when none) — the LAN delivery endpoint's acceptance test.
func (s *Service) PendingPinResetRequestID() int64 {
	req, ok := s.PendingPinReset()
	if !ok {
		return 0
	}
	return req.RequestID
}

// ListPinResetRequests shows the team's open requests (owner till).
func (s *Service) ListPinResetRequests() ([]PinResetRequest, error) {
	c, err := s.ownerClient()
	if err != nil {
		return nil, err
	}
	var res struct {
		OK       bool              `json:"ok"`
		Error    string            `json:"error"`
		Requests []PinResetRequest `json:"requests"`
	}
	if err := rpcCall(c.base, "sync_list_pin_resets", map[string]any{
		"p_device_id":   c.device,
		"p_secret_hash": c.secretHash,
	}, &res); err != nil {
		return nil, fmt.Errorf("could not reach the LedgerPOS cloud, check the internet and try again")
	}
	if !res.OK {
		return nil, fmt.Errorf("%s", res.Error)
	}
	if res.Requests == nil {
		res.Requests = []PinResetRequest{}
	}
	return res.Requests, nil
}

// PinResetApproved is what ApprovePinReset returns. When the owner typed a
// new PIN (Granted), there is no code at all — the cloud holds a hash the
// locked till collects by itself, and the worker just types the PIN they
// were given. Otherwise Code is the single-use 8-digit fallback (shown to
// the owner exactly once), and Delivered reports the bonus path: the code
// also flew straight to the locked till over the shop WiFi.
type PinResetApproved struct {
	Granted     bool   `json:"granted"`
	Code        string `json:"code"`
	Username    string `json:"username"`
	DeviceName  string `json:"deviceName"`
	ExpiresMins int    `json:"expiresMins"`
	DeviceID    string `json:"deviceId"`
	LANAddr     string `json:"lanAddr"`
	Delivered   bool   `json:"delivered"`
}

// ApprovePinReset closes one request (owner till). With newPin (exactly 4
// digits) the cloud stores a grant: the locked till collects the PIN hash
// by itself and the worker only types the PIN they were told. Without one,
// the legacy single-use 8-digit code is minted for read-aloud delivery.
func (s *Service) ApprovePinReset(requestID int64, newPin string) (PinResetApproved, error) {
	out := PinResetApproved{}
	c, err := s.ownerClient()
	if err != nil {
		return out, err
	}
	newPin = strings.TrimSpace(newPin)
	var res struct {
		OK            bool   `json:"ok"`
		Error         string `json:"error"`
		Granted       bool   `json:"granted"`
		Code          string `json:"code"`
		Username      string `json:"username"`
		DeviceName    string `json:"deviceName"`
		DeviceID      string `json:"deviceId"`
		LANAddr       string `json:"lanAddr"`
		ExpiresMinute int    `json:"expiresMinutes"`
	}
	if err := rpcCall(c.base, "sync_approve_pin_reset", map[string]any{
		"p_device_id":   c.device,
		"p_secret_hash": c.secretHash,
		"p_request_id":  requestID,
		"p_new_pin":     newPin,
	}, &res); err != nil {
		return out, fmt.Errorf("could not reach the LedgerPOS cloud, check the internet and try again")
	}
	if !res.OK {
		return out, fmt.Errorf("%s", res.Error)
	}
	out = PinResetApproved{
		Granted:     res.Granted,
		Code:        res.Code,
		Username:    res.Username,
		DeviceName:  res.DeviceName,
		DeviceID:    res.DeviceID,
		LANAddr:     res.LANAddr,
		ExpiresMins: res.ExpiresMinute,
	}
	if out.Granted {
		s.Audit(0, "system", "PIN_RESET_GRANTED", "user", res.Username,
			fmt.Sprintf("request %d: PIN set by the owner, till collects it by itself", requestID))
		return out, nil
	}
	s.Audit(0, "system", "PIN_RESET_APPROVED", "user", res.Username,
		fmt.Sprintf("request %d code issued", requestID))
	// WiFi hand-off: if the locked till is on this shop's network, the
	// code flies straight to it and the worker only types their new
	// PIN. The cloud-registered LAN address is tried first (works on
	// WiFi that blocks multicast), then mDNS. Fails silently — the code
	// on screen stays the fallback.
	if res.DeviceID != "" && res.DeviceID != c.device {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		out.Delivered = landelivery.PushPinResetCode(ctx, res.DeviceID, requestID, res.Code, res.LANAddr)
		if out.Delivered {
			s.Audit(0, "system", "PIN_RESET_CODE_DELIVERED", "user", res.Username,
				fmt.Sprintf("request %d delivered over the shop WiFi", requestID))
		}
	}
	return out, nil
}

// PollPinGrant asks the cloud whether an approved PIN grant is waiting for
// THIS till (the worker is sitting locked out; the owner already typed the
// new PIN on their own till). Best-effort: any error means "no grant yet".
type PinGrant struct {
	RequestID int64
	Username  string
}

func (s *Service) PollPinGrant() (PinGrant, bool) {
	out := PinGrant{}
	c, err := s.ownerClient()
	if err != nil {
		return out, false
	}
	var res struct {
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
		Grant     bool   `json:"grant"`
		RequestID int64  `json:"requestId"`
		Username  string `json:"username"`
	}
	if err := rpcCall(c.base, "sync_poll_pin_grant", map[string]any{
		"p_device_id":   c.device,
		"p_secret_hash": c.secretHash,
	}, &res); err != nil || !res.OK || !res.Grant {
		return out, false
	}
	return PinGrant{RequestID: res.RequestID, Username: res.Username}, true
}

// ClaimAndApplyPinGrant collects the waiting grant (single-use, bound to
// this device) and applies the PIN locally. The PIN itself never travels —
// the cloud hands over the bcrypt hash it stored at approve time.
// Already-applied grant ids are remembered locally: without this, the
// stale-claim re-serve would re-apply an old owner PIN every 30 minutes
// and silently revert the worker's own rotation (2nd-round review).
func (s *Service) ClaimAndApplyPinGrant(requestID int64) (models.PinResetRedeemed, error) {
	out := models.PinResetRedeemed{}
	if grantApplied(s.settings, requestID) {
		return out, fmt.Errorf("this PIN was already set on this till")
	}
	c, err := s.ownerClient()
	if err != nil {
		return out, fmt.Errorf("PIN recovery needs the shop's cloud link, join the team first")
	}
	var res struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Username string `json:"username"`
		PinHash  string `json:"pinHash"`
	}
	if err := rpcCall(c.base, "sync_claim_pin_grant", map[string]any{
		"p_device_id":   c.device,
		"p_secret_hash": c.secretHash,
		"p_request_id":  requestID,
	}, &res); err != nil {
		return out, fmt.Errorf("could not reach the LedgerPOS cloud, check the internet and try again")
	}
	if !res.OK {
		return out, fmt.Errorf("%s", res.Error)
	}
	if !strings.HasPrefix(res.PinHash, "$2") {
		return out, fmt.Errorf("the cloud sent an unreadable PIN, ask the owner to approve again")
	}
	applied, err := s.applyPinHash(res.Username, res.PinHash)
	if err != nil {
		return out, err
	}
	s.clearPendingPinReset()
	rememberGrantApplied(s.settings, requestID)
	s.Audit(applied.UserID, applied.Username, "PIN_RESET_GRANTED_APPLIED", "user", applied.Username,
		fmt.Sprintf("request %d: owner-set PIN applied on this till", requestID))
	log.Printf("[auth] owner-set PIN applied for %s (request %d)", applied.Username, requestID)
	return applied, nil
}

const pinGrantsAppliedKey = "pinreset_applied_grants"

// grantApplied reports whether this till already applied a grant id.
func grantApplied(st *settings.Store, requestID int64) bool {
	for _, id := range strings.Split(st.Get(pinGrantsAppliedKey), ",") {
		if id == fmt.Sprint(requestID) {
			return true
		}
	}
	return false
}

// rememberGrantApplied records a grant id (newest first, capped).
func rememberGrantApplied(st *settings.Store, requestID int64) {
	id := fmt.Sprint(requestID)
	ids := []string{id}
	for _, prev := range strings.Split(st.Get(pinGrantsAppliedKey), ",") {
		if prev != "" && prev != id {
			ids = append(ids, prev)
		}
		if len(ids) >= 20 {
			break
		}
	}
	_ = st.Set(pinGrantsAppliedKey, strings.Join(ids, ","))
}

// applyPinHash sets a bcrypt PIN hash on the named active account and
// clears any lockout. Shared by the redeem and grant paths.
func (s *Service) applyPinHash(username, pinHash string) (models.PinResetRedeemed, error) {
	out := models.PinResetRedeemed{}
	var userID int64
	var uname string
	err := s.db.QueryRow(s.db.Rebind(`SELECT id, username FROM users WHERE LOWER(username) = LOWER(?) AND is_active = 1`), username).Scan(&userID, &uname)
	if err == sql.ErrNoRows {
		s.clearPendingPinReset()
		return out, fmt.Errorf("no active account named %s on this till anymore, send a fresh request or ask the owner to add you back", username)
	}
	if err != nil {
		return out, fmt.Errorf("could not look up the account: %v", err)
	}
	res2, err := s.db.Exec(s.db.Rebind(`
                UPDATE users SET pin_hash = ?, failed_pin_attempts = 0, pin_locked_until = '',
                        must_rotate = 0, updated_at = CURRENT_TIMESTAMP
                WHERE id = ? AND is_active = 1`), pinHash, userID)
	if err != nil {
		return out, fmt.Errorf("could not update the account: %v", err)
	}
	if n, _ := res2.RowsAffected(); n == 0 {
		// Lost a race with a deactivation between check and update.
		s.clearPendingPinReset()
		return out, fmt.Errorf("no active account named %s on this till, ask the owner", username)
	}
	out.Username = uname
	out.UserID = userID
	return out, nil
}

// RedeemPinReset verifies the owner's code with the cloud and, on success,
// sets the new PIN on this till's local account and clears any lockout.
// (The legacy read-aloud path; new approvals carry a direct grant instead.)
func (s *Service) RedeemPinReset(requestID int64, code, newPin string) (models.PinResetRedeemed, error) {
	out := models.PinResetRedeemed{}
	if !auth.ValidPIN(newPin) {
		return out, fmt.Errorf("choose a 4-digit PIN that isn't an obvious pattern like 0000 or 1234")
	}
	c, err := s.ownerClient()
	if err != nil {
		return out, fmt.Errorf("PIN recovery needs the shop's cloud link, join the team first")
	}
	var res struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Username string `json:"username"`
	}
	if err := rpcCall(c.base, "sync_redeem_pin_reset", map[string]any{
		"p_device_id":   c.device,
		"p_secret_hash": c.secretHash,
		"p_request_id":  requestID,
		"p_code":        strings.TrimSpace(code),
	}, &res); err != nil {
		return out, fmt.Errorf("could not reach the LedgerPOS cloud, check the internet and try again")
	}
	if !res.OK {
		return out, fmt.Errorf("%s", res.Error)
	}
	// Cloud says yes — apply it locally. The username comes from the cloud
	// record (bound at request time), not from the client payload.
	// Pre-check BEFORE trusting anything: the cloud burns the code the
	// moment we redeem it, so a till that can't actually apply the reset
	// (account renamed/deactivated meanwhile) must find out FIRST —
	// otherwise the code is consumed and the flow dead-ends.
	pinHash, err := hash.Password(newPin)
	if err != nil {
		return out, err
	}
	out, err = s.applyPinHash(res.Username, pinHash)
	if err != nil {
		return out, err
	}
	s.clearPendingPinReset()
	s.Audit(out.UserID, out.Username, "PIN_RESET_REDEEMED", "user", out.Username, fmt.Sprintf("request %d", requestID))
	log.Printf("[auth] PIN reset redeemed for %s (request %d)", out.Username, requestID)
	// Best-effort: also nudge the password so "Password sign-in instead"
	// never dead-ends for the same person. The password stays untouched —
	// only the PIN changed; the user can rotate the password after signing
	// in if they want.
	return out, nil
}
