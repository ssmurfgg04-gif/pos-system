// Package update checks GitHub Releases for newer builds and stages
// downloads. Release facts (from scripts/create_release.py):
// repo ssmurfgg04-gif/pos-system, tags vX.Y.Z, assets:
// ledgerpos-setup-windows-x64.exe (NSIS installer),
// ledgerpos-macos-apple-silicon.zip, ledgerpos-macos-intel.zip,
// ledgerpos-linux-x64.tar.xz. Admins click to install; nothing is silent.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Release is one GitHub release with downloadable assets.
type Release struct {
	Tag    string
	Name   string
	Notes  string
	Assets []Asset
}

// Asset is one downloadable file.
type Asset struct {
	Name string
	URL  string
}

// ParseVersion splits "v1.2.3" / "1.2" into numbers (missing parts are 0).
func ParseVersion(s string) (major, minor, patch int, ok bool) {
	s = strings.TrimSpace(strings.TrimPrefix(s, "v"))
	if s == "" {
		return 0, 0, 0, false
	}
	parts := strings.Split(s, ".")
	if len(parts) > 3 {
		return 0, 0, 0, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, 0, 0, false
		}
		nums[i] = n
	}
	return nums[0], nums[1], nums[2], true
}

// IsNewer reports whether latest is newer than current. Non-semver inputs
// (dev builds, empty strings) never update.
func IsNewer(current, latest string) bool {
	cMaj, cMin, cPat, cOK := ParseVersion(current)
	lMaj, lMin, lPat, lOK := ParseVersion(latest)
	if !cOK || !lOK {
		return false
	}
	if lMaj != cMaj {
		return lMaj > cMaj
	}
	if lMin != cMin {
		return lMin > cMin
	}
	return lPat > cPat
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Check fetches the latest release from {apiBase}/repos/{repo}/releases/latest.
func Check(ctx context.Context, apiBase, repo string) (Release, error) {
	url := strings.TrimSuffix(apiBase, "/") + "/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Release{}, fmt.Errorf("releases API: status %d", resp.StatusCode)
	}
	var raw struct {
		TagName string `json:"tag_name"`
		Name    string `json:"name"`
		Body    string `json:"body"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Release{}, err
	}
	rel := Release{Tag: raw.TagName, Name: raw.Name, Notes: raw.Body}
	for _, a := range raw.Assets {
		rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL})
	}
	return rel, nil
}

// PickAsset selects the installer for this platform ("" = none shipped).
func PickAsset(rel Release, goos string) Asset {
	var want string
	switch goos {
	case "windows":
		want = "ledgerpos-setup-windows-x64.exe"
	case "darwin":
		if runtime.GOARCH == "arm64" {
			want = "ledgerpos-macos-apple-silicon.zip"
		} else {
			want = "ledgerpos-macos-intel.zip"
		}
	case "linux":
		want = "ledgerpos-linux-x64.tar.xz"
	default:
		return Asset{}
	}
	for _, a := range rel.Assets {
		if a.Name == want {
			return a
		}
	}
	return Asset{}
}

// CurrentPlatformAsset is PickAsset for the running binary.
func CurrentPlatformAsset(rel Release) Asset {
	return PickAsset(rel, runtime.GOOS)
}
