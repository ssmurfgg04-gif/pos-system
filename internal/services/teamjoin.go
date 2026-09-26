package services

// teamjoin.go — team join links + owner invites + the owner portal.
//
// Join links: the owner mints an invite from any approved till (Settings →
// Team). The link carries a DB-minted, single-use, expiring token plus the
// role for display; the TOKEN is the authority. A worker pastes the link
// during onboarding on a new machine: this till redeems it (sync_join_team
// approves the device into the team), creates a local user with the
// invite's role, and skips the full admin onboarding — the shop config
// (store name, currency, receipt) arrives through normal team sync.
//
// Portal: an owner-level password saved on an approved till publishes its
// bcrypt hash to the cloud (plaintext never leaves the machine), letting
// the owner log into the public website and see shop performance replayed
// from the synced order stream.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"posapp/internal/hash"
	"posapp/internal/models"
)

// joinTokenRe pulls the token out of a pasted join link (k=… parameter,
// any of ? &# separators — the token lives in the URL fragment so it never
// reaches web-server logs).
var joinTokenRe = regexp.MustCompile(`[?&#]k=([A-Za-z0-9-]{12,64})`)

// bareTokenRe accepts a bare token with no link around it.
var bareTokenRe = regexp.MustCompile(`^[A-Za-z0-9-]{12,64}$`)

func extractJoinToken(link string) string {
	link = strings.TrimSpace(link)
	if m := joinTokenRe.FindStringSubmatch(link); m != nil {
		return m[1]
	}
	if bareTokenRe.MatchString(link) {
		return link
	}
	return ""
}

// ---- Owner-side invite management (Settings → Team) ----

// TeamCreateInvite mints a join link token. The cloud invents the token and
// stores only its SHA-256 — this till shows it once and forgets it.
func (s *Service) TeamCreateInvite(roleName string, perms []string, note string) (models.TeamInviteCreated, error) {
	out := models.TeamInviteCreated{RoleName: roleName}
	c, err := s.ownerClient()
	if err != nil {
		return out, err
	}
	permsJSON, _ := json.Marshal(perms)
	var res struct {
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
		Token     string `json:"token"`
		TeamCode  string `json:"team_code"`
		RoleName  string `json:"role_name"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := rpcCall(c.base, "sync_create_invite", map[string]any{
		"p_device_id":   c.device,
		"p_secret_hash": c.secretHash,
		"p_role_name":   roleName,
		"p_permissions": json.RawMessage(permsJSON),
		"p_note":        note,
		"p_days":        7,
	}, &res); err != nil {
		return out, err
	}
	if !res.OK {
		return out, fmt.Errorf("%s", res.Error)
	}
	out.TeamCode, out.Token, out.RoleName, out.ExpiresAt = res.TeamCode, res.Token, res.RoleName, res.ExpiresAt
	s.Audit(0, "system", "TEAM_INVITE_CREATED", "settings", "",
		"role " + truncStr(roleName, 40))
	return out, nil
}

// TeamListInvites shows recent invites for the owner's team.
func (s *Service) TeamListInvites() ([]models.TeamInvite, error) {
	c, err := s.ownerClient()
	if err != nil {
		return nil, err
	}
	var res struct {
		OK      bool               `json:"ok"`
		Error   string             `json:"error"`
		Invites []models.TeamInvite `json:"invites"`
	}
	if err := rpcCall(c.base, "sync_list_invites", map[string]any{
		"p_device_id": c.device, "p_secret_hash": c.secretHash,
	}, &res); err != nil {
		return nil, err
	}
	if !res.OK {
		return nil, fmt.Errorf("%s", res.Error)
	}
	if res.Invites == nil {
		res.Invites = []models.TeamInvite{}
	}
	return res.Invites, nil
}

// TeamRevokeInvite kills an unused invite.
func (s *Service) TeamRevokeInvite(id int64) error {
	c, err := s.ownerClient()
	if err != nil {
		return err
	}
	ok, errMsg, err := c.rpcOwnerCall("sync_revoke_invite", map[string]any{
		"p_invite_id": id,
	})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s", errMsg)
	}
	s.Audit(0, "system", "TEAM_INVITE_REVOKED", "settings", "", fmt.Sprint(id))
	return nil
}

// TeamApproveDevice approves a device that is on this team but waiting
// (auto-approve is off — this is the roster's Approve button).
func (s *Service) TeamApproveDevice(deviceID string) error {
	c, err := s.ownerClient()
	if err != nil {
		return err
	}
	ok, errMsg, err := c.rpcOwnerCall("sync_approve_device", map[string]any{
		"p_target_device_id": deviceID,
	})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s", errMsg)
	}
	s.Audit(0, "system", "TEAM_DEVICE_APPROVED", "settings", "",
		truncStr(deviceID, 80))
	return nil
}

// ---- Worker side: redeem a join link on a new machine ----

func slugifyUsername(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '_':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 24 {
		out = out[:24]
	}
	return strings.Trim(out, "-")
}

// TeamJoinWithLink redeems a join link on this till: joins the team in the
// cloud, creates the local user with the invite's role, and marks
// onboarding done — a worker is selling within a minute of installing.
func (s *Service) TeamJoinWithLink(link, name, pin string) (models.TeamJoinInfo, error) {
	info := models.TeamJoinInfo{}
	token := extractJoinToken(link)
	if token == "" {
		return info, fmt.Errorf("that doesn't look like a join link — paste the whole link the owner sent")
	}
	name = strings.TrimSpace(name)
	if len(name) < 2 || len(name) > 40 {
		return info, fmt.Errorf("enter the worker's name (2-40 characters)")
	}
	if !regexp.MustCompile(`^\d{4}$`).MatchString(pin) || authIsWeakPIN(pin) {
		return info, fmt.Errorf("choose a 4-digit PIN that isn't 0000 or 1234")
	}

	// 1. The cloud decides: is this token real, alive, and unused?
	var res struct {
		OK          bool      `json:"ok"`
		Error       string    `json:"error"`
		TeamCode    string    `json:"team_code"`
		StoreName   string    `json:"store_name"`
		RoleName    string    `json:"role_name"`
		Permissions []string  `json:"permissions"`
	}
	if err := rpcCall(strings.TrimRight(cloudBaseURL, "/"), "sync_join_team", map[string]any{
		"p_device_id":   s.deviceID(),
		"p_secret_hash": secretHash(s.deviceSecret()),
		"p_token":       token,
	}, &res); err != nil {
		return info, fmt.Errorf("could not reach the LedgerPOS cloud — check the internet and try again")
	}
	if !res.OK {
		return info, fmt.Errorf("%s", res.Error)
	}

	// 2. This till is now a fully-approved member of that team.
	_ = s.settings.Set("sync_endpoint", strings.TrimRight(cloudProjectURL, "/"))
	_ = s.settings.Set("sync_source", "cloud")
	_ = s.settings.Set("sync_team_code", res.TeamCode)
	_ = s.settings.Set("sync_auto_approve", "false")
	_ = s.settings.Set("sync_enabled", "true")
	_ = s.settings.Set("sync_registered", "true")
	_ = s.settings.Set("sync_approved", "true")
	_ = s.settings.Set("sync_store_pending", "false")
	_ = s.settings.Set("sync_bootstrap_at", nowStamp())
	info.TeamCode, info.StoreName, info.RoleName = res.TeamCode, res.StoreName, res.RoleName

	// 3. Local role: use the shop's role if it exists; otherwise create it
	// from the permissions the invite carried (empty → Cashier's set).
	perms := res.Permissions
	if len(perms) == 0 {
		perms = models.SeededRolePermissions["Cashier"]
	}
	var roleID int64
	err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM roles WHERE LOWER(name) = LOWER(?)`), res.RoleName).Scan(&roleID)
	if err == sql.ErrNoRows {
		permsJSON, _ := json.Marshal(perms)
		ins, err := s.db.Exec(s.db.Rebind(`
			INSERT INTO roles (name, description, is_system, permissions)
			VALUES (?, 'Joined via team link', 0, ?)`), res.RoleName, string(permsJSON))
		if err != nil {
			return info, fmt.Errorf("could not create the %s role: %v", res.RoleName, err)
		}
		roleID, _ = ins.LastInsertId()
	} else if err != nil {
		return info, err
	}

	// 4. The worker's account: they picked the PIN themselves, so no
	// rotation dance. The random password exists only to satisfy the schema.
	base := slugifyUsername(name)
	if len(base) < 3 {
		base = base + "-worker"
	}
	username := base
	for i := 2; ; i++ {
		var one int
		if err := s.db.QueryRow(s.db.Rebind(`SELECT COUNT(*) FROM users WHERE LOWER(username) = LOWER(?)`), username).Scan(&one); err != nil {
			return info, err
		}
		if one == 0 {
			break
		}
		username = fmt.Sprintf("%s-%d", base, i)
	}
	pwHash, err := hash.Password(randToken(12))
	if err != nil {
		return info, err
	}
	pinHash, err := hash.Password(pin)
	if err != nil {
		return info, err
	}
	ins, err := s.db.Exec(s.db.Rebind(`
		INSERT INTO users (username, full_name, password_hash, pin_hash, role_id, is_active, must_rotate)
		VALUES (?, ?, ?, ?, ?, 1, 0)`), username, name, pwHash, pinHash, roleID)
	if err != nil {
		return info, fmt.Errorf("could not create the worker account: %v", err)
	}
	userID, _ := ins.LastInsertId()
	info.Username, info.UserID = username, userID

	// 5. Skip the full admin onboarding — the shop is already set up and
	// its config (store name, currency, receipt) syncs from the team.
	_ = s.settings.Set("onboarding_done", "true")
	s.Audit(userID, username, "TEAM_JOIN_LINK_USED", "user", username,
		"team "+res.TeamCode+" role "+res.RoleName)
	log.Printf("[team] join link redeemed: %s joined %s as %s", username, res.TeamCode, res.RoleName)
	return info, nil
}

// authIsWeakPIN keeps the most guessable PINs out of worker accounts.
func authIsWeakPIN(pin string) bool {
	switch pin {
	case "0000", "1234", "1111", "1212", "2580":
		return true
	}
	return false
}

// ---- Owner portal ----

// PortalPublishCredentials pushes an owner-level user's bcrypt password
// hash to the cloud so the public website's owner portal can verify their
// login. Best-effort: offline tills simply publish on the next password
// save. The plaintext never leaves this machine.
func (s *Service) PortalPublishCredentials(username, password string) {
	c, ok := s.syncConfig()
	if !ok || c.mode != "rpc" || !s.settings.GetBool("sync_approved", false) {
		return // portal rides on the cloud link; nothing to do offline
	}
	bh, err := hash.Password(password)
	if err != nil {
		return
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := rpcCall(c.base, "portal_set_credentials", map[string]any{
		"p_device_id":     c.device,
		"p_secret_hash":   c.secretHash,
		"p_username":      username,
		"p_password_hash": bh,
	}, &out); err != nil || !out.OK {
		log.Printf("[portal] publish credentials: %v", err)
		return
	}
	log.Printf("[portal] owner credentials published — the website portal is live for %s", s.settings.Get("sync_team_code"))
}
