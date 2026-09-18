# Multi-Tenant Shops Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One box serves many shops with hard isolation — signup creates a shop, login routes to the correct shop, and a user can never see another shop's data.

**Architecture:** Database-per-shop (one SQLite file per tenant, selected per request from the JWT shop claim). Isolation by construction: separate files, separate pools, separate backups. A tiny JSON registry maps usernames to shops. No shared-table `shop_id` columns anywhere — that migration would touch every query and still leak on one missed WHERE.

**Tech Stack:** Go (gin, `database/sql`, `golang.org/x/crypto` for password hashing via existing `auth` helpers); JWT gains a shop claim; React signup page.

**Spec:** Owner requirement 2026-09-16: signup opens a new shop; login lands in the correct shop only; cross-shop data invisible. Sub-20MB discipline: shop files stay tiny (VACUUM-compacted; measured 0.2MB seeded); no action needed beyond measuring in Task 6.

**Refined 2026-09-16 — Web Research Best Practices Applied:**

- **Multi-tenancy model:** Research confirms DB-per-tenant (our SQLite-file-per-shop) is the *strongest* isolation (file-level, not just RLS WHERE clauses) and is explicitly recommended for compliance-driven cases (Securestartkit, Makerkit). Shared-schema `tenant_id` + RLS is cheaper at 10K+ tenants but requires `(select auth.uid())` wrapping, composite indexes `tenant_id`-first, and `WITH CHECK` on every write — our file-per-shop avoids all of that while keeping each shop <20MB. If we ever migrate to Supabase Postgres for cloud sync, we will add `tenant_id` + `as restrictive` RLS + `security definer` helpers per Makerkit's production checklist.

- **VAT historization:** StackOverflow/DBA consensus is unanimous: *store calculated tax at posting, never recalculate*. Tax rates change; reports must read stored `tax_cents`/`tax_percent` per order line, not current settings. Our Task 4 already stores `tax_cents` per order — we now also store `tax_percent` per order for correct "VAT at X%" labels in historic reports (see Task 4 refinements).

- **Shop switcher UX:** ABP/React guides show `TenantContext` + `queryKey: ['tenant-users', tenantId]` + `enabled: !!tenantId` + dropdown with "Create New". Our frontend will use the same pattern: `useTenant()` context, `tenantId` in every query key, and a shop avatar dropdown (shadcn) — not a full page reload.

- **CSV injection:** OWASP: prefix cells starting with `=+-@` with `'` and quote fields containing `,”\n`. Already implemented in `csv()`/`csvField()`; research confirms this is the correct 2024 mitigation.

- **Supabase Storage RLS (future):** When off-site moves to Supabase Storage, encode `tenantId` into object path and enforce via `storage.foldername(name)[1]` in RLS (per Supabase docs) — not yet needed for DB-per-shop files.

## Global Constraints

- Repo Go files are mixed tabs/spaces PER FILE — match each file's own style, never run gofmt -w.
- Every SQL change needs SQLite + Pg bodies (registry itself is JSON, no SQL).
- Usernames unique per BOX (registry-enforced at signup); login never reveals which shops exist (uniform 401).
- Signup endpoint gated by `ALLOW_SIGNUP` env (default off; LAN appliances stay closed).
- Existing single-shop boxes upgrade with zero data migration (registry points `default` at the existing `pos.db`).

---

### Task 1: Tenant registry + shop pool

**Files:**
- Create: `internal/tenants/tenants.go` (tabs, new package): `Registry{path}`, `Shop{ID, Name, DBFile, CreatedAt}`, `Load()`, `CreateShop(name)`, `FindByUsername(username) (shopID)`, `RegisterUser(username, shopID)`, pool `Open(dbPath)` with mutex-cached `*database.DB` handles (each migrated on open).
- Test: `internal/tenants/tenants_test.go` (create two shops, username routing, duplicate username rejected, pool returns same handle).

**Interfaces:**
- Consumes: `database.Open/Migrate`.
- Produces: `tenants.Registry`, `OpenShop(shopID) (*database.DB, error)`, username index.

Registry file `<datadir>/shops.json`: `{"shops":[{...}], "users":{"alice": "shop-id", ...}}`. Write atomically (temp + rename). Shop IDs: `sh-<8 hex>` from crypto/rand (never sequential).

- [ ] **Step 1: Registry CRUD + pool** (as above; pool closes handles on `CloseAll` for tests).
- [ ] **Step 2: Tests** — create shop A + B, register alice→A and bob→B, duplicate alice→B fails, reopen returns same handle, registry survives reload from disk.
- [ ] **Step 3: Run** `C:\Users\Jackb\tools\go\bin\go.exe test ./internal/tenants/ -count=1` — expect PASS.
- [ ] **Step 4: Commit**

```bash
git add internal/tenants/
git commit -m "feat: tenant registry with username routing and pooled handles"
```

### Task 2: Signup + shop-scoped JWT + login routing

**Files:**
- Modify: `internal/auth/middleware.go`: `Principal` gains `ShopID string`; JWT `IssueToken` gains shop claim (check its signature first — extend claims struct, keep 12h expiry); `LoadPrincipal` unchanged (per-shop DB passed in).
- Modify: `internal/router/router.go`: `authRequired` resolves shop from token claim → opens pooled handle → stores `(db, shopID)` in gin context; unknown shop → 403. Public `POST /auth/signup` (rate-limited like login) gated by `ALLOW_SIGNUP=true`.
- Create: signup handler in `internal/handlers/auth.go`: `Signup` validates username/password, checks registry uniqueness, `CreateShop` + migrate + `SeedShop` (Task 3), issues shop-scoped token.
- Test: `internal/handlers/tenants_test.go`: signup creates shop; login alice lands in A (token carries shopA); crafted cross-shop access fails (see Task 5).

**Interfaces:**
- Consumes: `tenants.Registry/OpenShop`.
- Produces: JWT `{userID, shopID}`; `POST /auth/signup`; per-request shop in context (`ShopFromContext(c)` helper in auth package).

- [ ] **Step 1: JWT shop claim + context helper.**
- [ ] **Step 2: authRequired resolves shop per request (desktop mode: single default shop — see Task 4).**
- [ ] **Step 3: Signup handler (validates, creates, seeds, returns token + shop).**
- [ ] **Step 4: Tests for signup + claim shape (isolation tests come in Task 5).**
- [ ] **Step 5: Commit.**

### Task 3: SeedShop (fresh tenant without default passwords)

**Files:**
- Modify: `internal/database/seed.go`: extract `SeedShop(db, storeName, adminUsername, adminPassword)` = Migrate (already run by caller) + seedSettings (store_name override) + seedRoles + seedCatalog + ONE admin user (`must_rotate = 0` — they just chose it; NO cashier/designer defaults). Keep existing `Seed()` untouched for tests/desktop-first-run.

**Interfaces:**
- Consumes: existing `seedSettings/seedRoles/seedCatalog` internals (refactor to take overrides where needed).
- Produces: `SeedShop(...) error`.

- [ ] **Step 1: Refactor + implement.**
- [ ] **Step 2: Unit test** — SeedShop creates exactly 1 user (admin, must_rotate 0), catalog present, store name set.
- [ ] **Step 3: Commit.**

### Task 4: Per-request services + desktop default shop

**Files:**
- Modify: `internal/handlers/helpers.go`: add `func (h *H) svc(c *gin.Context) *services.Service` — returns shop-scoped service from a pool keyed by shop (pool lives in tenants or services package: `ShopServices` map with mutex, each `services.New(shopDB, settings.New(shopDB), hub, printer)`; hub/printer shared globals passed at construction).
- Mechanical: replace `h.Svc` with `h.svc(c)` at every handler call site (~100 sites; scripted `sed`-style replace + compile-fix loop, then full test suite).
- Modify: `main.go` desktop mode: registry init — if no registry, create `default` shop pointing at existing `pos.db` (zero migration); open it; wire pool with the single entry. Server mode: registry in data dir (or `SHOPS_DIR`).
- Modify: `internal/services/backup.go` `StartBackupScheduler` callers: start per opened shop (on open + at boot for existing entries); offsite prefix defaults to shop name.

**Interfaces:**
- Consumes: Tasks 1–2.
- Produces: every request served from its shop's service; desktop unchanged outwardly.

- [ ] **Step 1: ShopServices pool + svc(c) helper.**
- [ ] **Step 2: Mechanical h.Svc → h.svc(c) migration (compile after every file).**
- [ ] **Step 3: main.go registry bootstrap (default-shop upgrade path).**
- [ ] **Step 4: Per-shop backup schedulers.**
- [ ] **Step 5: Full suite green + commit.**

### Task 5: Isolation tests (the security case)

**Files:**
- Modify: `internal/handlers/tenants_test.go` (append).

- [ ] **Step 1: Write the tests**

```go
// Tenant isolation: data created in shop A is invisible from shop B, and a
// token minted for A cannot read B even with paths guessed.
func TestTenantIsolation(t *testing.T) {
        // signup alice (shop A) + bob (shop B) via POST /auth/signup (ALLOW_SIGNUP on in tests)
        // alice creates product + order + customer in A
        // bob lists products/orders/customers → alice's rows absent
        // bob GETs alice's order/product/customer IDs directly → 404 (not 403 — no existence leak)
        // alice with a tampered shop claim (re-signed? no — instead: use bob's token on A's IDs) → 404
}
```

Token-forgery negative: attempt `GET /api/v1/orders/<a-id>` with bob's token expects 404 (row simply isn't in B's file). Also assert JWT carries distinct shop IDs for alice/bob.

- [ ] **Step 2: Run full suite green.**
- [ ] **Step 3: Commit.**

### Task 6: Frontend signup + sizes + docs

**Files:**
- Create: `frontend/src/pages/Signup.tsx` (username + password + shop name → POST /auth/signup → store token → app).
- Modify: `frontend/src/App.tsx`: public `/signup` route (link from Login; hidden unless server advertises `signupAllowed` — add to branding/desktop status? Simplest: try route, 403 means disabled. Add `signup` flag to `/system/desktop` response).
- Modify: `internal/handlers/system.go` `DesktopInfo`: include `signupAllowed` from env.
- Modify: demo backend: signup creates a second demo dataset? NO — demo stays single-shop (out of scope, document it).
- Sizes: record measured numbers in README (binary 27MB, installer 10.5MB, seeded shop DB 0.2MB, frontend 0.4MB).

- [ ] **Step 1: DesktopInfo flag + Signup page + route.**
- [ ] **Step 2: Demo: signup endpoint returns 501 (documented single-shop demo).**
- [ ] **Step 3: `npx tsc --noEmit` + `npm test` green.**
- [ ] **Step 4: README (multi-shop section + sizes) + commit.**
- [ ] **Step 5: Full gates + push.**

## Self-Review

- Spec coverage: signup→new shop (T2/T3/T6), correct-shop login (T2), cross-shop invisibility (T5 tests), sub-20MB measured not assumed (T6).
- Upgrade: existing boxes get a `default` registry entry — zero data migration, all old tests keep passing through the same helpers.
- Type consistency: `ShopID string` (JWT + Principal + context), `Signup` request/response shapes reused in frontend, `offsite_prefix` per shop via existing key.
- Open risk (stated, not hidden): `h.Svc → h.svc(c)` is a wide mechanical refactor — Task 4 budgets compile-fix loops plus the full suite as the net.
