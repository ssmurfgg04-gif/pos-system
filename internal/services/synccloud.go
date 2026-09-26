package services

// synccloud.go — zero-config team identity. A fresh till needs NO setup:
// it already knows the project URL (a public constant) and the public anon
// key; it reads the sync_bootstrap row from the database to learn WHICH
// team it belongs to, then registers itself with a device-generated secret
// (only the SHA-256 hash ever leaves the machine). The database decides
// who is who: sync_devices.approved / .revoked gate every push and pull.
//
// No join codes. No keys to ship or type. Revocation is a SQL UPDATE.

import (
        "bytes"
        "crypto/sha256"
        "flag"
        "encoding/hex"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        "net/url"
        "strings"
        "time"
)

// Public, non-secret constants. The anon key is designed to ship in
// clients; it grants nothing beyond what RLS policies allow (which is:
// read the bootstrap row, call the device-authenticated RPCs).
const (
        cloudProjectURL = "https://ixxiqrobcwkvyjtxdkvh.supabase.co"
        cloudAnonKey    = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzdXBhYmFzZSIsInJlZiI6Iml4eGlxcm9iY3drdnlqdHhka3ZoIiwicm9sZSI6ImFub24iLCJpYXQiOjE3ODk1MTU0MDUsImV4cCI6MjEwNTA5MTQwNX0.hBO1kg4hLSHLUl5m-MCSHGCVFa4a7SRXF2uSioCV9Yk"

        bootstrapRefresh = 6 * time.Hour
)

type cloudBootstrap struct {
        ProjectURL  string `json:"project_url"`
        TeamCode    string `json:"team_code"`
        AutoApprove bool   `json:"auto_approve"`
}

// cloudStore is one row of the sync_stores registry — the multi-store
// umbrella. Each store owns exactly one team_code (the sync partition).
type cloudStore struct {
        Slug        string `json:"slug"`
        Name        string `json:"name"`
        TeamCode    string `json:"team_code"`
        AutoApprove bool   `json:"auto_approve"`
}

// ensureCloudBootstrap resolves the cloud link (project URL, team code,
// auto-approve) from the cloud store registry, cached in local settings for
// bootstrapRefresh so a temporarily unreachable cloud never blocks a till.
// Returns nil when the cloud is not usable yet (never fetched + unreachable).
func (s *Service) ensureCloudBootstrap() *cloudBootstrap {
        if s.settings.Get("sync_source") == "manual" {
                return nil // owner configured this till by hand; manual wins
        }
        // A cache is only valid if it came from a real fetch (sync_source was
        // set to "cloud" then) — never reuse a manual endpoint as team state.
        if s.settings.Get("sync_source") == "cloud" && time.Since(s.bootstrapAt()) < bootstrapRefresh {
                if bs := s.cachedBootstrap(); bs != nil {
                        return bs
                }
        }
        bs := s.resolveCloudStore()
        if bs == nil {
                if cached := s.cachedBootstrap(); cached != nil {
                        return cached // offline tolerance: keep last known team
                }
                return nil
        }
        _ = s.settings.Set("sync_endpoint", strings.TrimRight(bs.ProjectURL, "/"))
        _ = s.settings.Set("sync_bootstrap_at", nowStamp())
        _ = s.settings.Set("sync_source", "cloud")
        if bs.TeamCode != "" {
                _ = s.settings.Set("sync_team_code", bs.TeamCode)
                _ = s.settings.Set("sync_auto_approve", boolStr(bs.AutoApprove))
                _ = s.settings.Set("sync_store_pending", "false")
                if !s.settings.GetBool("sync_enabled", false) {
                        _ = s.settings.Set("sync_enabled", "true")
                }
        } else {
                // Multi-store cloud and this till has no store yet: it registers
                // as pending and the owner assigns it from an approved till.
                _ = s.settings.Set("sync_store_pending", "true")
                _ = s.settings.Set("sync_enabled", "true")
        }
        return bs
}

// resolveCloudStore decides which team this till belongs to, from the
// cloud's own registry (never from the client):
//   - exactly one active store → join it (zero-config, unchanged behaviour);
//   - several stores → the till's previously assigned team still wins (if
//     the registry still contains it); a fresh till gets TeamCode ""
//     (pending) and learns its fate from sync_register;
//   - registry unreachable → legacy sync_bootstrap row 1 (single-store
//     deployments predating the registry, and offline tolerance upstream).
//
// The project URL always comes from the cloud's own answer (the bootstrap
// row carries its project_url; the registry is read from the constant) so
// tests can point the client at a fake cloud.
func (s *Service) resolveCloudStore() *cloudBootstrap {
        stores, serr := fetchStores()
        // cloudBaseURL is the host the registry was read from (the constant in
        // production, the fake server in tests) — never point the sync client
        // anywhere else when the registry answered.
        projectURL := strings.TrimRight(cloudBaseURL, "/")
        if serr != nil || len(stores) == 0 {
                if bs, err2 := fetchBootstrap(); err2 == nil {
                        projectURL = bs.ProjectURL
                        stores = []cloudStore{{Slug: "main", Name: "Main Store",
                                TeamCode: bs.TeamCode, AutoApprove: bs.AutoApprove}}
                }
        }
        if len(stores) == 0 {
                return nil
        }
        if len(stores) == 1 {
                return &cloudBootstrap{ProjectURL: projectURL,
                        TeamCode: stores[0].TeamCode, AutoApprove: stores[0].AutoApprove}
        }
        // Several stores: a previously assigned till keeps its team; everyone
        // else registers pending and is assigned by the owner.
        team := s.settings.Get("sync_team_code")
        for _, st := range stores {
                if team != "" && st.TeamCode == team {
                        return &cloudBootstrap{ProjectURL: projectURL, TeamCode: team, AutoApprove: st.AutoApprove}
                }
        }
        return &cloudBootstrap{ProjectURL: projectURL, TeamCode: "", AutoApprove: false}
}

func (s *Service) cachedBootstrap() *cloudBootstrap {
        endpoint := strings.TrimRight(s.settings.Get("sync_endpoint"), "/")
        team := s.settings.Get("sync_team_code")
        if endpoint == "" || team == "" {
                return nil
        }
        auto := s.settings.GetBool("sync_auto_approve", true)
        return &cloudBootstrap{ProjectURL: endpoint, TeamCode: team, AutoApprove: auto}
}

func (s *Service) bootstrapAt() time.Time {
        t, err := time.Parse(time.RFC3339, s.settings.Get("sync_bootstrap_at"))
        if err != nil {
                return time.Time{}
        }
        return t
}

// cloudBaseURL is a var so tests can point it at a fake Supabase.
var cloudBaseURL = cloudProjectURL

// cloudProjectRef identifies the production project; any network call to it
// from a test binary is refused. This exists because a test that boots a
// ShopPool starts a REAL sync loop — 28 test devices once registered
// themselves against production before this guard existed. Tests that
// exercise sync must point cloudBaseURL at a fake server.
const cloudProjectRef = "ixxiqrobcwkvyjtxdkvh"

func underGoTest() bool {
        return flag.Lookup("test.v") != nil
}

func guardProduction(host string) error {
        if underGoTest() && strings.Contains(host, cloudProjectRef) {
                return fmt.Errorf("test refused to touch the production cloud — point cloudBaseURL at a fake server")
        }
        return nil
}

func fetchBootstrap() (*cloudBootstrap, error) {
        if err := guardProduction(cloudBaseURL); err != nil {
                return nil, err
        }
        q := url.Values{}
        q.Set("id", "eq.1")
        q.Set("select", "project_url,team_code,auto_approve")
        base := strings.TrimRight(cloudBaseURL, "/")
        req, err := http.NewRequest("GET", base+"/rest/v1/sync_bootstrap?"+q.Encode(), nil)
        if err != nil {
                return nil, err
        }
        req.Header.Set("apikey", cloudAnonKey)
        req.Header.Set("Authorization", "Bearer "+cloudAnonKey)
        hc := &http.Client{Timeout: syncHTTPTimeout}
        resp, err := hc.Do(req)
        if err != nil {
                return nil, err
        }
        defer resp.Body.Close()
        if resp.StatusCode != 200 {
                io.Copy(io.Discard, resp.Body)
                return nil, fmt.Errorf("bootstrap: status %d", resp.StatusCode)
        }
        var rows []cloudBootstrap
        if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
                return nil, err
        }
        if len(rows) == 0 {
                return nil, fmt.Errorf("bootstrap: no row")
        }
        return &rows[0], nil
}

// fetchStores reads the cloud's active store registry with the public anon
// key (RLS exposes exactly the active rows — no secrets, no device data).
func fetchStores() ([]cloudStore, error) {
        if err := guardProduction(cloudBaseURL); err != nil {
                return nil, err
        }
        q := url.Values{}
        q.Set("active", "eq.true")
        q.Set("select", "slug,name,team_code,auto_approve")
        q.Set("order", "id")
        base := strings.TrimRight(cloudBaseURL, "/")
        req, err := http.NewRequest("GET", base+"/rest/v1/sync_stores?"+q.Encode(), nil)
        if err != nil {
                return nil, err
        }
        req.Header.Set("apikey", cloudAnonKey)
        req.Header.Set("Authorization", "Bearer "+cloudAnonKey)
        hc := &http.Client{Timeout: syncHTTPTimeout}
        resp, err := hc.Do(req)
        if err != nil {
                return nil, err
        }
        defer resp.Body.Close()
        if resp.StatusCode != 200 {
                io.Copy(io.Discard, resp.Body)
                return nil, fmt.Errorf("stores: status %d", resp.StatusCode)
        }
        var rows []cloudStore
        if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
                return nil, err
        }
        return rows, nil
}

// deviceSecret returns this till's stable random secret (generated once,
// stored locally, never synced). Only its SHA-256 hash is ever transmitted.
func (s *Service) deviceSecret() string {
        if sec := s.settings.Get("sync_device_secret"); sec != "" {
                return sec
        }
        sec := randToken(32)
        _ = s.settings.Set("sync_device_secret", sec)
        return sec
}

func secretHash(sec string) string {
        sum := sha256.Sum256([]byte(sec))
        return hex.EncodeToString(sum[:])
}

// rpcCall invokes a sync_* Postgres RPC with the public anon key. Identity
// comes from the arguments (device id + secret hash), validated by the
// database on every call.
func rpcCall(base, fn string, args map[string]any, out any) error {
        if err := guardProduction(base); err != nil {
                return err
        }
        body, _ := json.Marshal(args)
        req, err := http.NewRequest("POST", strings.TrimRight(base, "/")+"/rest/v1/rpc/"+fn, bytes.NewReader(body))
        if err != nil {
                return err
        }
        req.Header.Set("apikey", cloudAnonKey)
        req.Header.Set("Authorization", "Bearer "+cloudAnonKey)
        req.Header.Set("Content-Type", "application/json")
        hc := &http.Client{Timeout: syncHTTPTimeout}
        resp, err := hc.Do(req)
        if err != nil {
                return err
        }
        defer resp.Body.Close()
        raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
        if resp.StatusCode != 200 {
                return fmt.Errorf("rpc %s: status %d", fn, resp.StatusCode)
        }
        if out == nil {
                return nil
        }
        return json.Unmarshal(raw, out)
}
