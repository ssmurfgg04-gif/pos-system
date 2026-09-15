# Auto-Updates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Shops stop manually re-downloading releases — the app checks GitHub Releases on boot, tells the admin a newer build exists, and installs it in one click.

**Architecture:** A tiny `internal/update` package (pure version-compare + GitHub API client with injectable base URL for tests) + two admin endpoints (check, download-and-stage) + a frontend banner/Settings card. Windows install = download the NSIS setup asset and launch it, then quit self via the existing `POST /system/quit`; the installer replaces the running exe on next launch flow (documented behavior, verified manually once). Linux/macOS: download staged + instructions (best-effort restart documented, not automated).

**Tech Stack:** Go stdlib (`net/http`, `encoding/json`); GitHub Releases API; existing NSIS installer artifacts.

**Spec:** `docs/ROADMAP.md` §5: "Version check against GitHub Releases on boot, one-click download + install. Ends manual re-downloads."

## Global Constraints

- Never auto-install without an admin click (shops update between rush hours, not during).
- `main.version` is `dev` in dev builds — dev builds must never report an update.
- No new Go module dependencies. CGO-free.

---

### Task 1: Recon release asset names (no guessing)

**Files:** read-only.

- [ ] **Step 1: Read `scripts/create_release.py` + `scripts/package_desktop.py` + `scripts/build_installer.py`** and record: release tag format (`vX.Y.Z`?), exact asset filenames for Windows (setup exe name pattern), Linux tarball name. Write findings as a comment block at the top of the Task-2 client file (so the strings live next to the code that consumes them).

Expected: a short list like `tag: v1.2.0 / windows: LedgerPOS-Setup-1.2.0.exe / linux: ledgerpos-linux-x64.tar.xz` (illustrative — use what the scripts actually say).

### Task 2: `internal/update` package — compare + check

**Files:**
- Create: `internal/update/update.go` (tabs or spaces? New package — use tabs, canonical Go): `ParseVersion(s) (major, minor, patch int, ok bool)`, `IsNewer(current, latest string) bool`, `Check(ctx, apiBase, repo, current string) (Release, error)` hitting `{apiBase}/repos/{repo}/releases/latest`, `Release{Tag, Name, Notes, Assets[{Name, URL}]}`.
- Test: `internal/update/update_test.go`.

**Interfaces:**
- Consumes: `main.version` string at call site (Task 3).
- Produces: `IsNewer`, `Check`, `Release` + `PickAsset(release, goos)` (windows → setup exe pattern from Task 1; linux → tarball; darwin → best-effort, may return none).

- [ ] **Step 1: Version compare**

```go
// IsNewer reports whether latest is a newer semver than current.
// Non-semver inputs (dev builds, empty) never update: returns false.
func IsNewer(current, latest string) bool
```

Strip leading `v`, split `.`, compare numerically, missing parts = 0.

- [ ] **Step 2: Check client** (5s timeout, `Accept: application/vnd.github+json`, no auth).
- [ ] **Step 3: Tests**

```go
func TestIsNewer(t *testing.T) {
        cases := []struct{ cur, lat string; want bool }{
                {"1.1.0", "v1.2.0", true},
                {"v1.2.0", "v1.2.0", false},
                {"1.3.0", "v1.2.0", false},
                {"dev", "v9.9.9", false},
                {"", "v1.0.0", false},
                {"1.2", "v1.2.1", true},
        }
        // assert each
}
func TestCheckAgainstStub(t *testing.T) {
        // httptest server returning a canned releases/latest JSON → Check returns tag + asset URLs
}
```

- [ ] **Step 4: Run** `C:\Users\Jackb\tools\go\bin\go.exe test ./internal/update/ -count=1` — expect PASS.
- [ ] **Step 5: Commit**

```bash
git add internal/update/
git commit -m "feat: update checker package with semver compare"
```

### Task 3: Check endpoint + boot check + staged download

**Files:**
- Modify: `main.go` or services wiring: on boot (both modes), if `update_channel != "off"`, run `update.Check` once in background and cache result (in-memory struct + mutex; re-check every 24h via the backup-scheduler pattern — read `StartBackupScheduler` and mirror the loop shape).
- Modify: `internal/handlers/system.go`: `GET /system/update` (perm `settings.manage`) → `{current, latest, notes, url, updateAvailable, checkedAt}`; `POST /system/update/download` → downloads picked asset to temp dir, returns `{stagedPath, asset}`.
- Modify: `internal/router/router.go`: register both routes.
- Modify: `internal/database/seed.go`: settings `update_channel` (`"stable"`, `"off"` disables).
- Test: `internal/handlers` test with `httptest` stub as GitHub API (override base URL via settings key `update_api_base` default `https://api.github.com` — add it to seed too, hidden from UI).

**Interfaces:**
- Consumes: `update.Check/PickAsset`, settings store.
- Produces: status JSON + staged file path.

- [ ] **Step 1: Settings keys (`update_channel`, `update_api_base`) + allowlist.**
- [ ] **Step 2: Cached checker (boot + 24h loop) + endpoints + routes.**
- [ ] **Step 3: Handler test** (stub server → `updateAvailable: true`, correct asset picked for windows).
- [ ] **Step 4: Full backend suite green.**
- [ ] **Step 5: Commit.**

### Task 4: One-click install + frontend banner

**Files:**
- Modify: `internal/handlers/system.go`: `POST /system/update/install` → Windows: `exec.Command(stagedSetup, "/S").Start()` (silent NSIS flag — verify `/S` against the NSIS docs comment in `build_installer.py`; if the installer doesn't support silent, launch normally), then trigger existing quit path (same code `QuitApp` uses); other OS: 501 `{"error":"manual install on this platform"}` with the download URL.
- Modify: `frontend/src/App.tsx`: update banner (admin-only, `updateAvailable` from status endpoint polled on load) linking to Settings → System.
- Modify: `frontend/src/pages/Settings.tsx`: System card section — Check now / Download / Install buttons wired to the three endpoints, notes rendered.
- Test: demo backend stubs the three endpoints (static fixture: update available v9.9.9); `npx tsc --noEmit` + `npm test`.

- [ ] **Step 1: Install endpoint (Windows-first).**
- [ ] **Step 2: Banner + Settings UI.**
- [ ] **Step 3: Demo parity + tests green.**
- [ ] **Step 4: Commit.**

### Task 5: Manual install verification + README + full gates

- [ ] **Step 1:** Real-machine check (Windows): stage an older build, confirm banner appears, download stages, installer launches. Record result in the commit message (pass/fail + build numbers). This is a manual gate — no command can click an installer for you.
- [ ] **Step 2:** README: updates section (channel off switch, manual fallback = download site).
- [ ] **Step 3:** Full gates: `go build`, `go test ./internal/...`, `npx tsc --noEmit`, `npm test`.
- [ ] **Step 4: Commit.**

## Self-Review

- Spec coverage: boot check (T3), one-click download+install (T4), manual fallback documented (T5). Admin-click-only invariant held (no silent installs anywhere).
- Type consistency: `update.Release/IsNewer/Check/PickAsset`, `GET|POST /system/update[/download]`, `POST /system/update/install`, settings `update_channel`/`update_api_base`.
- Recon-first (T1) exists precisely so no asset filename is ever guessed.
