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
        "fmt"
        "log"
        "strings"

        "posapp/internal/auth"
        "posapp/internal/hash"
        "posapp/internal/models"
)

const (
        pinResetReqIDKey  = "pinreset_req_id"
        pinResetReqUserKey = "pinreset_req_user"
        pinResetReqAtKey   = "pinreset_req_at"
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
                return out, fmt.Errorf("PIN recovery needs the shop's cloud link — join the team first, or ask the owner for a fresh join link")
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
                return out, fmt.Errorf("could not reach the LedgerPOS cloud — check the internet and try again")
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

// PendingPinReset returns this till's open request (for the PIN screen).
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
        return out, true
}

func (s *Service) clearPendingPinReset() {
        _ = s.settings.Set(pinResetReqIDKey, "")
        _ = s.settings.Set(pinResetReqUserKey, "")
        _ = s.settings.Set(pinResetReqAtKey, "")
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
                return nil, fmt.Errorf("could not reach the LedgerPOS cloud — check the internet and try again")
        }
        if !res.OK {
                return nil, fmt.Errorf("%s", res.Error)
        }
        if res.Requests == nil {
                res.Requests = []PinResetRequest{}
        }
        return res.Requests, nil
}

// PinResetApproved is what ApprovePinReset returns — the code is shown to
// the owner exactly once.
type PinResetApproved struct {
        Code        string `json:"code"`
        Username    string `json:"username"`
        DeviceName  string `json:"deviceName"`
        ExpiresMins int    `json:"expiresMins"`
}

// ApprovePinReset mints the single-use code for one request (owner till).
func (s *Service) ApprovePinReset(requestID int64) (PinResetApproved, error) {
        out := PinResetApproved{}
        c, err := s.ownerClient()
        if err != nil {
                return out, err
        }
        var res struct {
                OK            bool   `json:"ok"`
                Error         string `json:"error"`
                Code          string `json:"code"`
                Username      string `json:"username"`
                DeviceName    string `json:"deviceName"`
                ExpiresMinute int    `json:"expiresMinutes"`
        }
        if err := rpcCall(c.base, "sync_approve_pin_reset", map[string]any{
                "p_device_id":   c.device,
                "p_secret_hash": c.secretHash,
                "p_request_id":  requestID,
        }, &res); err != nil {
                return out, fmt.Errorf("could not reach the LedgerPOS cloud — check the internet and try again")
        }
        if !res.OK {
                return out, fmt.Errorf("%s", res.Error)
        }
        out = PinResetApproved{
                Code:        res.Code,
                Username:    res.Username,
                DeviceName:  res.DeviceName,
                ExpiresMins: res.ExpiresMinute,
        }
        s.Audit(0, "system", "PIN_RESET_APPROVED", "user", res.Username,
                fmt.Sprintf("request %d code issued", requestID))
        return out, nil
}

// RedeemPinReset verifies the owner's code with the cloud and, on success,
// sets the new PIN on this till's local account and clears any lockout.
func (s *Service) RedeemPinReset(requestID int64, code, newPin string) (models.PinResetRedeemed, error) {
        out := models.PinResetRedeemed{}
        if !auth.ValidPIN(newPin) {
                return out, fmt.Errorf("choose a 4-digit PIN that isn't an obvious pattern like 0000 or 1234")
        }
        c, err := s.ownerClient()
        if err != nil {
                return out, fmt.Errorf("PIN recovery needs the shop's cloud link — join the team first")
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
                return out, fmt.Errorf("could not reach the LedgerPOS cloud — check the internet and try again")
        }
        if !res.OK {
                return out, fmt.Errorf("%s", res.Error)
        }
        // Cloud says yes — apply it locally. The username comes from the cloud
        // record (bound at request time), not from the client payload.
        pinHash, err := hash.Password(newPin)
        if err != nil {
                return out, err
        }
        res2, err := s.db.Exec(s.db.Rebind(`
                UPDATE users SET pin_hash = ?, failed_pin_attempts = 0, pin_locked_until = '',
                        must_rotate = 0, updated_at = CURRENT_TIMESTAMP
                WHERE LOWER(username) = LOWER(?) AND is_active = 1`), pinHash, res.Username)
        if err != nil {
                return out, fmt.Errorf("could not update the account: %v", err)
        }
        if n, _ := res2.RowsAffected(); n == 0 {
                return out, fmt.Errorf("no active account named %s on this till — check the spelling", res.Username)
        }
        s.clearPendingPinReset()
        var userID int64
        var uname string
        _ = s.db.QueryRow(s.db.Rebind(`SELECT id, username FROM users WHERE LOWER(username) = LOWER(?)`), res.Username).Scan(&userID, &uname)
        s.Audit(userID, uname, "PIN_RESET_REDEEMED", "user", uname, fmt.Sprintf("request %d", requestID))
        log.Printf("[auth] PIN reset redeemed for %s (request %d)", uname, requestID)
        out.Username = uname
        out.UserID = userID
        // Best-effort: also nudge the password so "Password sign-in instead"
        // never dead-ends for the same person. The password stays untouched —
        // only the PIN changed; the user can rotate the password after signing
        // in if they want.
        return out, nil
}
