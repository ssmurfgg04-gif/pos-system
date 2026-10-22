// teamsync_devices.go — device roster, store registry, and the
// multi-store OWNER operations (create store, approve/assign/remove
// devices). Split out of teamsync.go (file-level, same package — no
// behavior change).

package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"posapp/internal/models"
	"posapp/internal/settings"
)

func (c *syncClient) fetchDevicesRPC(s *Service) ([]models.TeamDevice, error) {
	var out struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Devices []struct {
			DeviceID   string `json:"device_id"`
			DeviceName string `json:"device_name"`
			AppVersion string `json:"app_version"`
			LastSeen   string `json:"last_seen"`
			Approved   bool   `json:"approved"`
			Revoked    bool   `json:"revoked"`
			ThisDevice bool   `json:"this_device"`
		} `json:"devices"`
	}
	if err := rpcCall(c.base, "sync_roster", map[string]any{
		"p_device_id": c.device, "p_secret_hash": c.secretHash,
	}, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("roster: %s", out.Error)
	}
	roster := make([]models.TeamDevice, 0, len(out.Devices))
	for _, d := range out.Devices {
		roster = append(roster, models.TeamDevice{
			DeviceID:   d.DeviceID,
			DeviceName: d.DeviceName,
			AppVersion: d.AppVersion,
			LastSeen:   d.LastSeen,
			Approved:   d.Approved,
			Revoked:    d.Revoked,
			ThisDevice: d.ThisDevice || d.DeviceID == c.device,
		})
	}
	return roster, nil
}

// fetchStoresRPC reads the store registry (slug, name, team code, device
// counts) through the identity-checked RPC.

func (c *syncClient) fetchStoresRPC() ([]models.TeamStore, error) {
	var out struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		Stores []struct {
			Slug     string `json:"slug"`
			Name     string `json:"name"`
			TeamCode string `json:"team_code"`
			Devices  int    `json:"devices"`
		} `json:"stores"`
	}
	if err := rpcCall(c.base, "sync_list_stores", map[string]any{
		"p_device_id": c.device, "p_secret_hash": c.secretHash,
	}, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("stores: %s", out.Error)
	}
	stores := make([]models.TeamStore, 0, len(out.Stores))
	for _, st := range out.Stores {
		stores = append(stores, models.TeamStore{
			Slug: st.Slug, Name: st.Name,
			TeamCode: st.TeamCode, Devices: st.Devices,
		})
	}
	return stores, nil
}

// fetchPendingRPC lists devices registered but not yet assigned to a store.

func (c *syncClient) fetchPendingRPC(s *Service) ([]models.TeamDevice, error) {
	var out struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Devices []struct {
			DeviceID   string `json:"device_id"`
			DeviceName string `json:"device_name"`
			AppVersion string `json:"app_version"`
			LastSeen   string `json:"last_seen"`
		} `json:"devices"`
	}
	if err := rpcCall(c.base, "sync_list_pending", map[string]any{
		"p_device_id": c.device, "p_secret_hash": c.secretHash,
	}, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("pending: %s", out.Error)
	}
	devs := make([]models.TeamDevice, 0, len(out.Devices))
	for _, d := range out.Devices {
		devs = append(devs, models.TeamDevice{
			DeviceID: d.DeviceID, DeviceName: d.DeviceName,
			AppVersion: d.AppVersion, LastSeen: d.LastSeen,
		})
	}
	return devs, nil
}

// ---- Multi-store owner operations ----

// rpcOwnerCall runs an owner RPC with this till's identity and surfaces the
// RPC's error string (owner actions must report WHY, not just fail).
// v1.3.2: cloud owner RPCs additionally verify an owner-level HUMAN via a
// short-lived capability (minted from the owner's portal credential in the
// Owner-approval dialog). A cached capability rides along automatically;
// without one the RPC answers 'owner approval required' and the UI prompts.

func (c *syncClient) rpcOwnerCall(fn string, args map[string]any) (ok bool, errMsg string, err error) {
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	full := map[string]any{
		"p_device_id":   c.device,
		"p_secret_hash": c.secretHash,
	}
	for k, v := range args {
		full[k] = v
	}
	return rpcOwnerCallArgs(c, fn, full, &out)
}

// rpcOwnerCallArgs is the capability-aware core (split out so tests can
// observe the exact args the RPC receives).

func rpcOwnerCallArgs(c *syncClient, fn string, full map[string]any, out *struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}) (ok bool, errMsg string, err error) {
	if tok := ownerCapabilityTokenFor(c); tok != "" {
		full["p_owner_capability"] = tok
	}
	if err := rpcCall(c.base, fn, full, out); err != nil {
		return false, "", err
	}
	return out.OK, out.Error, nil
}

// ownerCapabilityTokenFor resolves the cached capability for this client.
// Package-level indirection keeps the cache test-swappable.

var ownerCapabilityTokenFor = func(c *syncClient) string {
	ocMu.Lock()
	defer ocMu.Unlock()
	if !oc.valid() {
		return ""
	}
	if c.team != "" && oc.team != "" && oc.team != c.team {
		return "" // minted for a different team — never reuse
	}
	return oc.token
}

func (s *Service) ownerClient() (*syncClient, error) {
	c, ok := s.syncConfig()
	if !ok || c.mode != "rpc" {
		return nil, fmt.Errorf("adding stores needs the LedgerPOS cloud link (Settings → Team)")
	}
	if !s.settings.GetBool("sync_approved", false) {
		return nil, fmt.Errorf("this till is not approved by the cloud yet")
	}
	return c, nil
}

// TeamCreateStore adds a store under this owner's cloud project. The team
// code is minted by the database — the client never invents one. The store's
// business timezone ("sales today" boundary on the owner portal) rides
// along: explicit setting first, else this till's local zone.

func (s *Service) TeamCreateStore(name, slug string) (models.TeamStore, error) {
	out := models.TeamStore{Slug: slug, Name: name}
	c, err := s.ownerClient()
	if err != nil {
		return out, err
	}
	tz := strings.TrimSpace(s.settings.Get("business_timezone"))
	if tz == "" {
		tz = time.Local.String()
	}
	_, locErr := time.LoadLocation(tz)
	if locErr != nil || tz == "Local" {
		tz = "Africa/Nairobi"
	}
	ok, errMsg, err := c.rpcOwnerCall("sync_create_store", map[string]any{
		"p_name": name, "p_slug": slug, "p_business_tz": tz,
	})
	if err != nil {
		return out, err
	}
	if !ok {
		return out, fmt.Errorf("%s", errMsg)
	}
	s.Audit(0, "system", "TEAM_STORE_CREATED", "settings", "",
		truncStr(name, 80))
	// Refresh from the registry so the caller gets the minted team code.
	if stores, err := c.fetchStoresRPC(); err == nil {
		for _, st := range stores {
			if st.Name == name || (slug != "" && st.Slug == slug) {
				return st, nil
			}
		}
	}
	return out, nil
}

// TeamAssignDevice assigns a pending (or previously assigned) device to a
// store team.

func (s *Service) TeamAssignDevice(deviceID, teamCode string) error {
	c, err := s.ownerClient()
	if err != nil {
		return err
	}
	ok, errMsg, err := c.rpcOwnerCall("sync_assign_device", map[string]any{
		"p_target_device_id": deviceID, "p_team_code": teamCode,
	})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s", errMsg)
	}
	s.Audit(0, "system", "TEAM_DEVICE_ASSIGNED", "settings", "",
		truncStr(deviceID, 80))
	return nil
}

// TeamRemoveDevice revokes a device outright (kill switch from Settings →
// Team). The database keeps the row so a stolen till can never rejoin.

func (s *Service) TeamRemoveDevice(deviceID string) error {
	c, err := s.ownerClient()
	if err != nil {
		return err
	}
	ok, errMsg, err := c.rpcOwnerCall("sync_remove_device", map[string]any{
		"p_target_device_id": deviceID,
	})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s", errMsg)
	}
	s.Audit(0, "system", "TEAM_DEVICE_REVOKED", "settings", "",
		truncStr(deviceID, 80))
	return nil
}

func (c *syncClient) fetchDevices(s *Service) ([]models.TeamDevice, error) {
	if c.mode == "rpc" {
		return c.fetchDevicesRPC(s)
	}
	q := url.Values{}
	q.Set("team_code", "eq."+c.team)
	q.Set("order", "last_seen.desc")
	req, err := http.NewRequest("GET", c.base+"/sync_devices?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header = c.headers()
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("devices: status %d", resp.StatusCode)
	}
	var rows []struct {
		DeviceID   string `json:"device_id"`
		DeviceName string `json:"device_name"`
		AppVersion string `json:"app_version"`
		LastSeen   string `json:"last_seen"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return nil, err
	}
	out := make([]models.TeamDevice, 0, len(rows))
	for _, r := range rows {
		out = append(out, models.TeamDevice{
			DeviceID: r.DeviceID, DeviceName: r.DeviceName,
			AppVersion: r.AppVersion, LastSeen: r.LastSeen,
			ThisDevice: r.DeviceID == c.device,
		})
	}
	return out, nil
}

// TeamSyncConfigure toggles the team link. Manual secret entry (project
// URL / service key / team code) was removed for security: the
// service_role key bypasses all row-level security and must never be
// typed into a till — anyone with it owns the entire cloud database. A
// till links itself via the cloud bootstrap row instead. Legacy fields
// sent by old clients are acknowledged but never stored; tills that were
// hand-configured before cloud identity keep working until switched to
// cloud (Settings → Team → Switch to LedgerPOS Cloud).

func (s *Service) TeamSyncConfigure(req models.TeamSyncConfigRequest) error {
	if req.ProjectURL != "" || (req.ServiceKey != "" && req.ServiceKey != settings.MaskToken) || req.TeamCode != "" {
		s.Audit(0, "system", "TEAM_SYNC_MANUAL_IGNORED", "settings", "",
			"manual sync keys are no longer accepted, tills link via cloud identity")
	}
	if req.Enabled == nil {
		return nil
	}
	if !*req.Enabled {
		_ = s.settings.Set("sync_enabled", "false")
		// Distinct marker so the zero-config bootstrap cannot silently
		// re-enable itself (sync_enabled doubles as "not configured yet"
		// on fresh tills — it can't carry the explicit-OFF meaning).
		_ = s.settings.Set("sync_disabled", "1")
		return nil
	}
	_ = s.settings.Set("sync_disabled", "")
	if s.ensureCloudBootstrap() != nil {
		_ = s.settings.Set("sync_enabled", "true")
		return nil
	}
	// Legacy manual configuration (saved before cloud identity existed).
	if s.settings.Get("sync_endpoint") != "" && s.settings.Get("sync_service_key") != "" && s.settings.Get("sync_team_code") != "" {
		_ = s.settings.Set("sync_enabled", "true")
		return nil
	}
	return fmt.Errorf("no team link yet, this till joins its team automatically once it can reach the internet")
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TeamSyncCreate mints a fresh team code and enables sync (first device).
// In cloud mode the team already exists in the database — this just returns
// it and enables sync (keeps the Settings button meaningful).

func (s *Service) TeamSyncCreate() (string, error) {
	if bs := s.ensureCloudBootstrap(); bs != nil {
		_ = s.settings.Set("sync_enabled", "true")
		return bs.TeamCode, nil
	}
	code := "TEAM-" + strings.ToUpper(randToken(3))
	_ = s.settings.Set("sync_team_code", code)
	if s.settings.Get("sync_endpoint") == "" || s.settings.Get("sync_service_key") == "" {
		return "", fmt.Errorf("save the Supabase endpoint and service key first")
	}
	_ = s.settings.Set("sync_enabled", "true")
	return code, nil
}

// TeamSyncUseCloud reverts a manually-configured till to automatic cloud
// identity (the sync_bootstrap row). Manual secrets are kept but unused.

func (s *Service) TeamSyncUseCloud() error {
	_ = s.settings.Set("sync_source", "")
	_ = s.settings.Set("sync_registered", "false")
	if s.ensureCloudBootstrap() == nil {
		return fmt.Errorf("could not reach the cloud, check internet and try again")
	}
	return nil
}

// TeamSyncLoop runs until ctx closes, syncing every interval when enabled.
