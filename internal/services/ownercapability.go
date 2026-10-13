package services

// ownercapability.go — the till-side client for cloud OWNER authority.
//
// Cloud owner RPCs (store create/assign, device approve/remove, invites,
// portal credentials, PIN-reset approval) verify a HUMAN, not just a device:
// the RPC needs a short-lived signed capability minted by proving the
// owner's portal credential (username + password) on top of the till's
// device identity. See db/cloud_schema_v132.sql (owner_mint_capability).
//
// UX contract ("mama-simple"): the owner types their portal password once
// in the Owner-approval dialog; the token is cached in memory for its
// lifetime (≤60 min) and EVERY owner action on this till reuses it
// silently. Restart or expiry → one more prompt. Nothing lands on disk.

import (
        "encoding/json"
        "fmt"
        "strings"
        "sync"
        "time"
)

// ownerApprovalRequiredMsg mirrors the SQL error text of every gated RPC —
// the frontend keys its dialog on this exact phrase.
const ownerApprovalRequiredMsg = "owner approval required"

// OwnerApprovalRequired reports whether an RPC error is the capability gate
// (as opposed to a genuine failure the UI should surface as-is).
func OwnerApprovalRequired(err error) bool {
        if err == nil {
                return false
        }
        return strings.Contains(err.Error(), ownerApprovalRequiredMsg)
}

// IsOwnerAuthError classifies mint failures the OWNER can fix by retrying
// (wrong credentials, dam, identity) — those are 401s, not transport bugs.
func IsOwnerAuthError(err error) bool {
        if err == nil {
                return false
        }
        msg := err.Error()
        for _, probe := range []string{
                "not recognized",
                "not recognized for this store",
                "too many attempts",
                "required",
                "refused",
        } {
                if strings.Contains(msg, probe) {
                        return true
                }
        }
        return false
}

type ownerCapability struct {
        token  string
        team   string
        owner  string
        expiry time.Time
}

var (
        ocMu sync.Mutex
        oc   ownerCapability
)

func (c ownerCapability) valid() bool {
        return c.token != "" && time.Now().Before(c.expiry)
}

// MintOwnerCapability proves the owner's portal credential to the cloud and
// caches the returned capability. Returns the owner name + validity window
// for the UI confirmation.
func (s *Service) MintOwnerCapability(username, password string) (string, time.Duration, error) {
        c, ok := s.syncConfig()
        if !ok || c.mode != "rpc" {
                return "", 0, fmt.Errorf("owner approval needs the LedgerPOS cloud link (Settings → Team)")
        }
        if strings.TrimSpace(username) == "" || password == "" {
                return "", 0, fmt.Errorf("owner username and password are required")
        }
        var out struct {
                OK         bool   `json:"ok"`
                Error      string `json:"error"`
                Capability string `json:"capability"`
                Owner      string `json:"owner"`
                TeamCode   string `json:"teamCode"`
                ExpiresAt  string `json:"expiresAt"`
        }
        err := rpcCall(c.base, "owner_mint_capability", map[string]any{
                "p_device_id":   c.device,
                "p_secret_hash": c.secretHash,
                "p_username":    username,
                "p_password":    password,
                "p_ttl_minutes": 30,
        }, &out)
        if err != nil {
                return "", 0, err
        }
        if !out.OK {
                if out.Error != "" {
                        return "", 0, fmt.Errorf("%s", out.Error)
                }
                return "", 0, fmt.Errorf("owner approval was refused")
        }
        expiry := time.Now().Add(30 * time.Minute)
        if t, perr := time.Parse(time.RFC3339, out.ExpiresAt); perr == nil {
                expiry = t
        }
        // Refresh 60s early so an action started at the last second still passes.
        ocMu.Lock()
        oc = ownerCapability{token: out.Capability, team: out.TeamCode, owner: out.Owner, expiry: expiry.Add(-time.Minute)}
        ocMu.Unlock()
        d := time.Until(expiry)
        if d < 0 {
                d = 0
        }
        return out.Owner, d, nil
}

// ownerCapabilityToken returns the cached capability for THIS till's team,
// or "" (the RPC then answers 'owner approval required' and the UI prompts).
func (s *Service) ownerCapabilityToken() string {
        ocMu.Lock()
        defer ocMu.Unlock()
        if !oc.valid() {
                return ""
        }
        if c, ok := s.syncConfig(); ok && c.team != "" && oc.team != "" && oc.team != c.team {
                return "" // minted for a different team — never reuse
        }
        return oc.token
}

// ClearOwnerCapability drops the cached capability (logout / team change).
func ClearOwnerCapability() {
        ocMu.Lock()
        oc = ownerCapability{}
        ocMu.Unlock()
}

// MarshalOwnerCapabilityState is used by tests to observe the cache.
func MarshalOwnerCapabilityState() map[string]any {
        ocMu.Lock()
        defer ocMu.Unlock()
        if !oc.valid() {
                return nil
        }
        return map[string]any{"owner": oc.owner, "team": oc.team, "expiresIn": time.Until(oc.expiry).Round(time.Second).String()}
}

var _ = json.Marshal // keep import parity with build tags in tests
