# Forced Credential Rotation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** No LedgerPOS box keeps working with seeded default credentials — every seeded user must set their own password and PIN on first login before any other API call succeeds.

**Architecture:** A `must_rotate` column on `users` (migration v4) checked in the auth middleware allowlist style: login succeeds and returns `mustRotate: true`, and every other authenticated endpoint (except `/me`, password/PIN change, branding, health) returns 403 `password rotation required` until rotated. Seeded users ship with `must_rotate = 1`; admin-created users default to 0.

**Tech Stack:** Go (gin, modernc.org/sqlite + postgres via `Rebind`), React frontend (custom `useRoute` router in `frontend/src/lib/router.ts`, `api` client in `frontend/src/lib/api.ts`).

**Spec:** `docs/ROADMAP.md` (production hardening for shop rollout); README "change it in Settings on first run" becomes enforced.

## Global Constraints

- Repo Go files are mixed tabs/spaces PER FILE — match each file's own style, never run gofmt -w.
- Money stays integer cents; this plan touches no money code.
- JWT stays 12h; permissions reload per request (existing behavior, keep).
- PostgreSQL dialect must keep working — every SQL change needs both SQLite and Pg bodies via `d.Rebind` where placeholders appear.

---

### Task 1: Migration v4 — `users.must_rotate`

**Files:**
- Modify: `internal/database/migrations.go` (append v5 entry after v3 block)
- Test: manual verify via sqlite3 CLI that column exists after boot (no Go test file for database package exists; keep it that way)

**Interfaces:**
- Consumes: `Migration{Version, SQLite, Pg, Go}` struct (see v3 entry).
- Produces: `users.must_rotate INTEGER NOT NULL DEFAULT 0` on both dialects; all pre-existing rows set to 1.

- [ ] **Step 1: Add the v5 migration**

```go
{
        Version: 5,
        SQLite: `
ALTER TABLE users ADD COLUMN must_rotate INTEGER NOT NULL DEFAULT 0;
UPDATE users SET must_rotate = 1;
`,
        Pg: `
ALTER TABLE users ADD COLUMN IF NOT EXISTS must_rotate INTEGER NOT NULL DEFAULT 0;
UPDATE users SET must_rotate = 1;
`,
},
```

- [ ] **Step 2: Boot the app once and confirm the column**

Run: `go run . version` (compiles) then boot desktop or run tests (any boot runs `Migrate()`).
Then: `sqlite3 <data-dir>/pos.db "PRAGMA table_info(users);"` — expect a `must_rotate` row.
Expected: column present.

- [ ] **Step 3: Commit**

```bash
git add internal/database/migrations.go
git commit -m "feat: v5 migration adds users.must_rotate, flags existing rows"
```

### Task 2: Seed new users with `must_rotate = 1`

**Files:**
- Modify: `internal/database/seed.go` (`seedUsers` INSERT — add `must_rotate` literal `1`)
- Test: `internal/handlers/handlers_test.go` — new test asserts seeded admin has mustRotate on login (Task 4 covers the test; this task is code-only plus compile check)

**Interfaces:**
- Consumes: `seedUsers` slice and roles lookup in `seed.go`.
- Produces: every seeded user row carries `must_rotate = 1`.

- [ ] **Step 1: Edit the seed INSERT to include must_rotate**

Find the `INSERT INTO users (username, full_name, password_hash, pin_hash, role_id)` statement in `seedUsers()` and change the column list and SELECT to include `must_rotate` with literal `1`:

```go
q := d.Rebind(`INSERT INTO users (username, full_name, password_hash, pin_hash, role_id, must_rotate)
        SELECT ?, ?, ?, ?, (SELECT id FROM roles WHERE name = ?), 1
        WHERE NOT EXISTS (SELECT 1 FROM users WHERE username = ?)`)
```

- [ ] **Step 2: Compile**

Run: `C:\Users\Jackb\tools\go\bin\go.exe build ./internal/database/`
Expected: exit 0.

- [ ] **Step 3: Commit**

```bash
git add internal/database/seed.go
git commit -m "feat: seeded users require rotation on first login"
```

### Task 3: Middleware gate + login flag

**Files:**
- Modify: `internal/auth/middleware.go` (Principal struct gains `MustRotate bool`; `LoadPrincipal` selects `must_rotate`)
- Modify: `internal/router/router.go` (`authRequired`: after loading principal, allowlist `/api/v1/me`, `/api/v1/users/:id/password`, `/api/v1/users/:id/pin`, `/api/v1/branding`, `/api/v1/health`, `/api/v1/auth/*`; everything else with `MustRotate` → 403 `{"error":"password rotation required"}`)
- Modify: `internal/handlers/auth.go` (Login + PinLogin responses gain `mustRotate: bool`; SetPassword/SetPIN clear `must_rotate = 0` for that user on success)
- Test: `internal/handlers/handlers_test.go` (new test in Task 4)

**Interfaces:**
- Consumes: `auth.Principal` (add field `MustRotate bool`), `RequirePermission` (unchanged).
- Produces: login JSON `{token, user, mustRotate}`; 403 gate on stale credentials.

- [ ] **Step 1: Add MustRotate to Principal and LoadPrincipal**

In `internal/auth/middleware.go`, add the field and extend the user lookup query to select `must_rotate` into it. Exact column expression depends on the existing query — read the file first, keep its style.

- [ ] **Step 2: Gate in authRequired**

```go
allow := map[string]bool{
        "/api/v1/me": true, "/api/v1/branding": true, "/api/v1/health": true,
}
path := c.Request.URL.Path
if !allow[path] &&
        !strings.HasPrefix(path, "/api/v1/auth/") &&
        !strings.HasSuffix(path, "/password") &&
        !strings.HasSuffix(path, "/pin") &&
        p.MustRotate {
        c.AbortWithStatusJSON(403, gin.H{"error": "password rotation required"})
        return
}
```

Place after `auth.WithPrincipal(c, p)`, before `c.Next()`.

- [ ] **Step 3: Login returns the flag; rotation clears it**

In Login/PinLogin handlers, include `"mustRotate": p.MustRotate` in the JSON. In SetPassword/SetPIN handlers, after a successful update run `UPDATE users SET must_rotate = 0 WHERE id = ?`.

- [ ] **Step 4: Compile**

Run: `C:\Users\Jackb\tools\go\bin\go.exe build ./...`
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/auth/middleware.go internal/router/router.go internal/handlers/auth.go internal/handlers/users.go
git commit -m "feat: gate API behind credential rotation, login reports mustRotate"
```

### Task 4: Backend tests pinning the behavior

**Files:**
- Modify: `internal/handlers/handlers_test.go` (append new test; file uses spaces)

**Interfaces:**
- Consumes: `newTestServer(t)`, `do(t, engine, method, path, token, body)`, `dataMap(t, w)` helpers already in the file.
- Produces: passing `TestForcedRotation`.

- [ ] **Step 1: Write the failing test**

```go
// Seeded credentials force rotation: login flags it, other endpoints 403,
// rotating clears it.
func TestForcedRotation(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)
        w := do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
                "username": "admin", "password": "admin123",
        })
        if w.Code != 200 {
                t.Fatalf("login: %d %s", w.Code, w.Body.String())
        }
        if dataMap(t, w)["mustRotate"] != true {
                t.Fatal("seeded admin login must flag mustRotate")
        }
        w = do(t, engine, "GET", "/api/v1/products", admin, nil)
        if w.Code != 403 {
                t.Fatalf("pre-rotation products should 403, got %d", w.Code)
        }
        me := do(t, engine, "GET", "/api/v1/me", admin, nil)
        if me.Code != 200 {
                t.Fatalf("me must stay reachable, got %d", me.Code)
        }
        adminID := int64(dataMap(t, me)["id"].(float64))
        w = do(t, engine, "PUT", "/api/v1/users/"+itoa64(adminID)+"/password", admin, map[string]any{
                "password": "new-secret-1",
        })
        if w.Code != 200 {
                t.Fatalf("rotate password: %d %s", w.Code, w.Body.String())
        }
        w = do(t, engine, "GET", "/api/v1/products", admin, nil)
        if w.Code != 200 {
                t.Fatalf("post-rotation products should 200, got %d", w.Code)
        }
}
```

(`itoa64` lives in `customers_test.go`, same package — reuse, do not redefine.)

- [ ] **Step 2: Run it**

Run: `C:\Users\Jackb\tools\go\bin\go.exe test ./internal/handlers/ -run TestForcedRotation -v -count=1`
Expected: PASS. (If existing tests now 403 because they log in as seeded users, update those tests to rotate first — count failures and fix each.)

- [ ] **Step 3: Run the full backend suite**

Run: `C:\Users\Jackb\tools\go\bin\go.exe test ./internal/... -count=1`
Expected: all packages ok.

- [ ] **Step 4: Commit**

```bash
git add internal/handlers/handlers_test.go
git commit -m "test: forced rotation lifecycle pinned"
```

### Task 5: Frontend rotation screen + gate

**Files:**
- Create: `frontend/src/pages/Rotate.tsx` (password + PIN fields, calls existing `PUT /users/:id/password` and `PUT /users/:id/pin`, then re-fetches `/me`)
- Modify: `frontend/src/App.tsx` (after login/me load: if `me.mustRotate` → render `<Rotate/>` instead of routes)
- Modify: `frontend/src/lib/api.ts` (`Me` interface gains `mustRotate: boolean`; login response type gains `mustRotate: boolean`)
- Test: `frontend/__tests__/demo.test.ts` — demo backend must mirror the gate or frontend tests using seeded logins break; add `must_rotate` default 1 to demo seed users, `mustRotate` in demo login DTOs, same allowlist in `demoRequest` gate. Then: `npm test`.

**Interfaces:**
- Consumes: `GET /api/v1/me`, `PUT /api/v1/users/:id/password`, `PUT /api/v1/users/:id/pin` (all exist).
- Produces: blocked UI until rotation completes.

- [ ] **Step 1: Extend api.ts types**

```ts
export interface Me {
  // ... existing fields ...
  mustRotate: boolean
}
export interface LoginResponse {
  token: string
  user: Me
  mustRotate: boolean
}
```

Match the exact existing field names in `api.ts` (read them; do not rename).

- [ ] **Step 2: Write Rotate.tsx**

Two `Field`+`Input` pairs (new password ≥ 6 chars like server rule, new 4-digit PIN), one primary button calling both PUTs sequentially, error display via existing `toast`, success calls `onDone()` prop which reloads `me`.

- [ ] **Step 3: Gate in App.tsx**

After the `me` load: `if (me?.mustRotate) return <Rotate onDone={reloadMe} />`. Keep the existing login/public-route structure untouched.

- [ ] **Step 4: Demo-backend parity + tests**

Mirror in `frontend/src/demo/backend.ts` + `seed.ts`: seeded demo users carry `mustRotate: true`, demo login returns it, `demoRequest` enforces the same allowlist (return ApiError 403 otherwise). Update existing demo tests that log in as seeded users to rotate first where they hit gated endpoints.

- [ ] **Step 5: Typecheck + tests**

Run: `npx tsc --noEmit` (expect clean), then `npm test` (expect all pass).
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/pages/Rotate.tsx frontend/src/App.tsx frontend/src/lib/api.ts frontend/src/demo/backend.ts frontend/src/demo/seed.ts frontend/__tests__/demo.test.ts
git commit -m "feat: frontend forces credential rotation on first login"
```

### Task 6: README touch + full verification

**Files:**
- Modify: `README.md` (replace "change it in Settings on first run" with "forced change on first login").

- [ ] **Step 1: Update README**
- [ ] **Step 2: Full verification**: `go build`, `go test ./internal/...`, `npx tsc --noEmit`, `npm test` — all green.
- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: credential rotation now enforced"
```

## Self-Review

- Spec coverage: seeded defaults unusable until rotated (Tasks 1–3), pinned by tests (Task 4), enforced in UI incl. demo parity (Task 5), documented (Task 6). Onboarding wizard (separate plan) creates fresh credentials and can set `must_rotate = 0` at creation since the owner just chose them.
- No placeholders: every SQL statement, route, JSON shape, and test assertion is written out.
- Type consistency: `mustRotate` (JSON) ↔ `MustRotate` (Go) ↔ `must_rotate` (SQL) used consistently throughout.
