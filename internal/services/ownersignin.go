package services

// ownersignin.go — owner sign-in on a fresh till.
//
// The reinstall problem: an owner deletes the app (or buys a new laptop),
// installs again, and lands in first-run "create your store" — the LAST
// thing they want, because their business already exists in the cloud.
// This file gives that till an owner sign-in: the owner types their normal
// username + password, the cloud verifies them against the SAME credential
// the owner web portal uses (portal_accounts — bcrypt, published from an
// approved till on every password change), approves this device into the
// team, and the shop (products, settings, customers, history) syncs down.
//
// Security mirrors sync_join_team:
//   - the cloud verifies the bcrypt credential (plaintext never stored);
//   - the cloud owns device approval (this RPC stamps it server-side);
//   - a failure-counter table dams brute force (10 tries / 15 min);
//   - failures are generic (no username enumeration; timing equalized
//     with a dummy bcrypt compare when the username is unknown).

import (
        "fmt"
        "log"
        "strings"

        "posapp/internal/hash"
        "posapp/internal/models"
)

// OwnerCloudSignin verifies the owner's portal credentials with the cloud,
// joins this till to their team, and provisions the local owner account
// (Admin role, the typed password, no rotation dance — they just proved it).
func (s *Service) OwnerCloudSignin(username, password, deviceName string) (models.TeamJoinInfo, error) {
        info := models.TeamJoinInfo{}
        username = strings.TrimSpace(username)
        if username == "" || password == "" {
                return info, fmt.Errorf("enter your username and password")
        }
        if len(password) > 128 {
                return info, fmt.Errorf("wrong username or password")
        }

        // 1. The cloud decides: are these portal credentials real?
        var res struct {
                OK        bool   `json:"ok"`
                Error     string `json:"error"`
                TeamCode  string `json:"team_code"`
                StoreName string `json:"store_name"`
        }
        if err := rpcCall(strings.TrimRight(cloudBaseURL, "/"), "owner_device_signin", map[string]any{
                "p_device_id":   s.deviceID(),
                "p_secret_hash": secretHash(s.deviceSecret()),
                "p_device_name": deviceName,
                "p_app_version": s.AppVersion(),
                "p_username":    username,
                "p_password":    password,
        }, &res); err != nil {
                return info, fmt.Errorf("could not reach the LedgerPOS cloud — check the internet and try again")
        }
        if !res.OK {
                return info, fmt.Errorf("%s", res.Error)
        }

        // 2. This till is now a fully-approved member of that team (the RPC
        // approved it server-side). Point sync at the cloud team.
        _ = s.settings.Set("sync_endpoint", strings.TrimRight(cloudProjectURL, "/"))
        _ = s.settings.Set("sync_source", "cloud")
        _ = s.settings.Set("sync_team_code", res.TeamCode)
        _ = s.settings.Set("sync_auto_approve", "false")
        _ = s.settings.Set("sync_enabled", "true")
        _ = s.settings.Set("sync_registered", "true")
        _ = s.settings.Set("sync_approved", "true")
        _ = s.settings.Set("sync_store_pending", "false")
        _ = s.settings.Set("sync_bootstrap_at", nowStamp())
        info.TeamCode, info.StoreName = res.TeamCode, res.StoreName

        // 3. Provision the local owner account. The typed password becomes the
        // local password (hash here; the plaintext never leaves this machine
        // again). An existing local user with that name (half-wiped till,
        // re-join) is claimed rather than duplicated.
        pwHash, err := hash.Password(password)
        if err != nil {
                return info, err
        }
        var roleID int64
        if err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM roles WHERE name = 'Admin' LIMIT 1`)).Scan(&roleID); err != nil {
                return info, fmt.Errorf("the Admin role is missing on this till — run LedgerPOS once more, or use a join link")
        }
        var userID int64
        err = s.db.QueryRow(s.db.Rebind(`SELECT id FROM users WHERE LOWER(username) = LOWER(?)`), username).Scan(&userID)
        switch {
        case err == nil:
                if _, err := s.db.Exec(s.db.Rebind(`
                                UPDATE users SET password_hash = ?, role_id = ?, is_active = 1, must_rotate = 0,
                                        password_changed_at = ?
                                WHERE id = ?`), pwHash, roleID, nowStamp(), userID); err != nil {
                        return info, fmt.Errorf("could not update the local account: %v", err)
                }
        default:
                res, err := s.db.Exec(s.db.Rebind(`
                                INSERT INTO users (username, full_name, password_hash, pin_hash, role_id, is_active, must_rotate, password_changed_at)
                                VALUES (?, ?, ?, '', ?, 1, 0, ?)`), username, username, pwHash, roleID, nowStamp())
                if err != nil {
                        return info, fmt.Errorf("could not create the local account: %v", err)
                }
                userID, _ = res.LastInsertId()
        }
        info.Username, info.UserID, info.RoleName = username, userID, "Admin"

        // 4. Skip first-run onboarding — the shop's real config (store name,
        // currency, receipt, printer) syncs from the team.
        _ = s.settings.Set("onboarding_done", "true")
        s.Audit(userID, username, "OWNER_CLOUD_SIGNIN", "user", username,
                "team "+res.TeamCode)
        log.Printf("[auth] owner cloud sign-in: %s into %s", username, res.TeamCode)
        return info, nil
}
