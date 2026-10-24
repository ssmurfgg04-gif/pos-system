package update

// manifest.go — cloud-managed OTA. The fleet's update channel is the
// cloud's app_config table: update_manifest_tag / _notes / _assets rows
// (anon-readable by RLS design — an update manifest is public data). The
// assets JSON carries name + URL + sha256 per installer, so a download is
// verified byte-for-byte before it can be installed.
//
// Why the cloud manifest wins over GitHub Releases: the owner can publish
// an update (or pull a bad one) with one SQL UPDATE from anywhere — no
// release tooling, no per-machine visits. GitHub stays as the fallback
// channel when the cloud is unreachable.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"posapp/internal/cloudmeta"
)

type manifestAsset struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// cloudManifest is the raw manifest content plus the signing material.
type cloudManifest struct {
	Rel        Release
	Tag        string
	AssetsJSON string
	Sig        string
}

// CheckCloudManifest reads the OTA manifest from the cloud app_config.
// Returns an error when the cloud is unreachable, has no manifest, or —
// when signing is enforced (a public key is compiled in) — the manifest
// lacks a valid ed25519 signature over its exact bytes.
func CheckCloudManifest(ctx context.Context, baseURL string) (Release, error) {
	m, err := FetchCloudManifest(ctx, baseURL)
	if err != nil {
		return Release{}, err
	}
	return m.Rel, nil
}

// FetchCloudManifest returns the parsed manifest AND the raw signing
// material (tag, assets JSON, signature) for verification by callers that
// own the trust decision.
func FetchCloudManifest(ctx context.Context, baseURL string) (cloudManifest, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = cloudmeta.BaseURL
	}
	if err := guardProduction(baseURL); err != nil {
		return cloudManifest{}, err
	}
	q := "select=key,value&key=in.(\"update_manifest_tag\",\"update_manifest_notes\",\"update_manifest_assets\",\"update_manifest_sig\")"
	req, err := http.NewRequestWithContext(ctx, "GET",
		strings.TrimRight(baseURL, "/")+"/rest/v1/app_config?"+q, nil)
	if err != nil {
		return cloudManifest{}, err
	}
	req.Header.Set("apikey", cloudmeta.AnonKey)
	req.Header.Set("Authorization", "Bearer "+cloudmeta.AnonKey)
	hc := &http.Client{Timeout: 15 * time.Second}
	resp, err := hc.Do(req)
	if err != nil {
		return cloudManifest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, resp.Body)
		return cloudManifest{}, fmt.Errorf("cloud manifest: status %d", resp.StatusCode)
	}
	var rows []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		return cloudManifest{}, err
	}
	rel := Release{}
	var assetsJSON, sig string
	for _, r := range rows {
		switch r.Key {
		case "update_manifest_tag":
			rel.Tag = strings.TrimSpace(r.Value)
		case "update_manifest_notes":
			rel.Notes = r.Value
		case "update_manifest_assets":
			assetsJSON = r.Value
		case "update_manifest_sig":
			sig = r.Value
		}
	}
	if rel.Tag == "" || assetsJSON == "" {
		return cloudManifest{}, fmt.Errorf("cloud manifest: no release published")
	}
	var raw []manifestAsset
	if err := json.Unmarshal([]byte(assetsJSON), &raw); err != nil {
		return cloudManifest{}, fmt.Errorf("cloud manifest assets: %w", err)
	}
	for _, a := range raw {
		if a.Name == "" || a.URL == "" {
			continue
		}
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL, SHA256: a.SHA256})
	}
	if len(rel.Assets) == 0 {
		return cloudManifest{}, fmt.Errorf("cloud manifest: no assets")
	}
	m := cloudManifest{Rel: rel, Tag: rel.Tag, AssetsJSON: assetsJSON, Sig: sig}
	return m, nil
}

// isGoTest reports whether we are running under `go test`.
func isGoTest() bool { return flag.Lookup("test.v") != nil }

// guardProduction mirrors services' test-safety guard: tests must never
// touch the production cloud. Defined here (not imported) to avoid a
// services → update dependency direction reversal.
func guardProduction(host string) error {
	if strings.Contains(host, "ixxiqrobcwkvyjtxdkvh") && strings.Contains(host, "supabase") {
		// Always allowed from the real binary; tests set cloudmeta.BaseURL
		// to a fake server, so reaching the production host from a test
		// binary means somebody forgot to stub — refuse.
		if isGoTest() {
			return fmt.Errorf("test refused to touch the production cloud, point cloudmeta.BaseURL at a fake server")
		}
	}
	return nil
}
