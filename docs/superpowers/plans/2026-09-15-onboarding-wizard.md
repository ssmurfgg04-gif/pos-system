# Guided Onboarding Wizard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A new shop goes from double-click to selling without hunting Settings — store name → logo → receipt → printer test → staff/PINs — and finishes with real credentials, never defaults.

**Architecture:** Frontend-only flow plus one settings flag. `App.tsx` already has a custom router; add an `/onboarding` route shown to admins while `onboarding_done != "true"`. Every step reuses existing endpoints (settings PUT, logo upload, test-print, users CRUD, PIN set). Completion flips the flag. The forced-rotation plan is the safety net: even a skipped wizard leaves the box secure.

**Tech Stack:** React frontend (`App.tsx`, `lib/router.ts`, `lib/api.ts`); one new settings key; demo-backend parity.

**Spec:** `docs/ROADMAP.md` §4: "First-run wizard: store name → logo → receipt → printer test → users/PINs. Replaces Settings-hunting for new shops." Requires the forced-credentials plan (wizard edits the seeded admin instead of leaving defaults).

## Global Constraints

- No new Go tables or migrations — one settings key only.
- Every step must work against the demo backend too (mirror the flag + seed default).
- Wizard never blocks cashiers: only admins see it; staff logins work normally once created.

---

### Task 1: `onboarding_done` flag + App gate

**Files:**
- Modify: `internal/database/seed.go`: `DefaultSettings` add `"onboarding_done": "false"`.
- Modify: settings allowlist to include it (same file as Task 1 of backup plan touches — coordinate: single allowlist edit covers both plans' keys).
- Modify: `frontend/src/App.tsx`: after `me` load, `if (me.perms.includes('users.manage') && brandingOrSettings.onboarding_done !== 'true') return <Onboarding onDone={reload} />`. Settings values come from the existing settings fetch — read App.tsx first and reuse whichever snapshot it holds.
- Modify: `frontend/src/demo/backend.ts` + `seed.ts`: `onboarding_done: "false"` default, settable via PUT /settings.
- Test: demo test asserts fresh seed reports `onboarding_done === "false"`, PUT flips it.

**Interfaces:**
- Consumes: existing settings GET/PUT, `me` with permissions.
- Produces: gate condition `onboarding_done !== "true" && isAdmin`.

- [ ] **Step 1: Seed key + allowlist.**
- [ ] **Step 2: App gate (no Onboarding component yet — render a placeholder `<div>Onboarding coming</div>` so the gate is testable).**
- [ ] **Step 3: Demo parity + demo test.**
- [ ] **Step 4: `go build` + `npm test`** — expect green.
- [ ] **Step 5: Commit**

```bash
git add internal/database/seed.go internal/settings/settings.go internal/handlers/settings.go frontend/src/App.tsx frontend/src/demo/backend.ts frontend/src/demo/seed.ts frontend/__tests__/demo.test.ts
git commit -m "feat: onboarding gate on onboarding_done flag"
```

### Task 2: The five-step wizard UI

**Files:**
- Create: `frontend/src/pages/Onboarding.tsx` — stepper with 5 steps, Next/Back, per-step Save calling existing endpoints, final Finish calling `PUT /settings {onboarding_done: "true"}` then `onDone()`.

**Interfaces:**
- Consumes: `PUT /api/v1/settings` (values), `POST /api/v1/settings/logo`, `POST /api/v1/settings/test-print`, `PUT /api/v1/users/:id` + `/password` + `/pin`, `POST /api/v1/users`.
- Produces: `<Onboarding onDone: () => void>`.

Steps and their exact calls:
1. **Store**: `store_name`, `store_address`, `store_phone` via settings PUT.
2. **Logo**: file input → existing logo upload endpoint (copy the Settings.tsx upload code path — read it, reuse the same FormData shape).
3. **Receipt**: `receipt_footer`, `tax_percent`, `currency_code`, `currency_symbol`, `receipt_logo` toggle.
4. **Printer**: `printer_target`, `printer_width`, then test-print button (same call as Settings test button).
5. **Staff**: edit seeded admin (username stays `admin`, set new password + PIN via existing PUTs — this satisfies rotation by construction) + optional second user via `POST /api/v1/users` (cashier role id looked up from `GET /api/v1/roles`, not hardcoded).

- [ ] **Step 1: Write Onboarding.tsx** (follow existing page styling: `Card`, `Field`, `Input`, `Button` from `components/ui`, `toast` for errors).
- [ ] **Step 2: Replace the Task-1 placeholder with the real component.**
- [ ] **Step 3: `npx tsc --noEmit`** — expect clean.
- [ ] **Step 4: Commit**

```bash
git add frontend/src/pages/Onboarding.tsx frontend/src/App.tsx
git commit -m "feat: five-step onboarding wizard UI"
```

### Task 3: E2E + docs + verification

**Files:**
- Modify: `scripts/desktop_browser_e2e.sh`: extend the first-run section — fresh data dir → login page redirects/offers onboarding → complete with test values → `onboarding_done` true → wizard gone on relaunch. Read the script first; mirror its `ck` helper style.
- Modify: `README.md`: first-run section describes the wizard; remove "change it in Settings" wording if still present.

- [ ] **Step 1: E2E extension.**
- [ ] **Step 2: README.**
- [ ] **Step 3: Full gates** (`go build`, `go test ./internal/...`, `npx tsc --noEmit`, `npm test`).
- [ ] **Step 4: Commit.**

## Self-Review

- Spec coverage: all five wizard steps mapped to existing endpoints (T2), gate + completion flag (T1), E2E proof (T3). No new tables, no new permissions, no new backend endpoints — deliberately.
- Interplay: requires forced-credentials plan merged first (wizard sets real admin password; rotation plan is the backstop if the wizard is skipped). State in commit message if order flips.
- Type consistency: setting key `onboarding_done` verbatim in seed, allowlist, App gate, demo backend, tests.
