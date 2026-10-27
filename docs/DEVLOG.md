# Worklog

---
Task ID: v1.4.6
Agent: main (Super Z)
Task: Finish WhatsApp receipt bot (manual + automated paths), team-sync shifts
  (the "shift not visible on Admin" report), Tab-charge guidance (the "button
  not clickable" report), receipt pending-label removal; release v1.4.6.

Work Log:
- WhatsApp service (internal/services/whatsapp.go): receiptText single source
  (bold store, fenced items/totals, M-Pesa code, VAT, footer); wa.me manual
  path (zero config); automated path = whatsapp_jobs queue + WhatsappWorker
  (5 attempts, 1-min backoff, dead-letter into audit + order timeline);
  presets whapi/ultramsg/custom; customer phone from the CRM record,
  normalized 2547…; queue best-effort — never blocks a sale
- Hooks: QueueWhatsappReceipt at every completion path (split/credit/cash
  checkout, completePayment); POST /orders/:id/whatsapp (pos.sell, rides
  GetOrderScoped visibility); Receipt screen gains the WhatsApp button;
  Settings gains the WhatsApp tab (provider, key masked __SET__, auto-send
  default OFF, sender display number); demo backend mirrors the endpoint
- Migration v20: shifts.client_uuid (unique partial index) + whatsapp_jobs
- Shifts ride team sync now: EmitShift on open/close (username resolved
  locally on receivers via the same fallback as orders; replay-keyed,
  close-only updates, idempotent)
- Tab UX: the guarded charge button now explains every blocked state (no
  customer / no match / cash-only / offline names the exact unlock); Enter
  picks the first search match; "no match" points to the Customers page
- Receipts: unconfirmed M-Pesa leg prints plain "M-Pesa" on paper, and
  "M-Pesa (pending)" in the WhatsApp text — never a fake "Paid"
- Tests: shift_whatsapp_test.go (open/close cross-device replay, text/link,
  manual path, queued delivery exactly-once, auto-send OFF default,
  dead-letter cap, phone normalization); checkoutrail regression +4 (tab
  guidance states, pick, Enter shortcut, charge enables)
- Docs: RECEIPTS-WHATSAPP rewritten to the shipped strategy (WABA rejected:
  verification + per-conversation billing not justified; linked-number
  gateway chosen); CHANGELOG v1.4.6 + backfilled v1.4.5
- Gates: gofmt clean, go vet clean, go test ./... 14/14 packages, vitest
  107/107, tsc + vite build green

Stage Summary:
- Client answers: receipt format = WhatsApp text (opens natively — their
  "any format" requirement); their number 0181 630 570 links via the
  gateway QR or just uses the manual button; pending section removed from
  the receipt; shifts cross-till visibility fixed; tab button unblocked
  (guidance) — customers need a one-time credit limit set from Customers

---
Task ID: v1.4.5
Agent: main (Super Z)
Task: Fix "can't add products — UNIQUE constraint failed: products.sku (2067)" + fleet-wide regression sweep

Work Log:
- Diagnosed from cloud data: the fleet catalog consumed ALL 50 fixed-window SKUs (SKU-100000..SKU-100049) during Sept-Oct catalog entry; uniqueSKU's exhausted-window fallback was the constant SKU-999999, already taken by "KRA - Pin Registration" — so every blank-SKU create died on UNIQUE(products.sku) 2067
- New services.AutoSKU(): date + random token (SKU-261027-1a2b3c4d) — unbounded, and two tills generating offline cannot realistically converge (protects the sku-keyed LWW sync from silently merging two distinct products into one)
- CreateProduct: user-typed duplicate SKUs pre-checked -> readable 409; auto SKUs regenerate+retry (3x) on a lost insert race; create response now carries the generated sku
- UpdateProduct: renaming onto another product's SKU -> readable 409 (the SKU is the cross-till sync identity)
- CSV import uniqueSKUTx: constant fallback replaced with checked AutoSKU candidates; teamsync_apply SKU-SYNC token 32->48 bits
- Frontend ProductModal: raw constraint dumps mapped to "That SKU is already in use. Change it, or clear the SKU field to auto-generate a new one."
- Regression tests (handlers): exhausted-window create, duplicate 409, 12-way parallel blank creates all-distinct, update-duplicate 409 + blank-sku keep

Stage Summary:
- Root cause was a bounded generator, not data corruption — no data migration needed; existing SKUs untouched
- Full sweep green: go vet ./... clean; go test ./... ALL packages (incl. p0 sync hardening, adversarial money paths); vitest 103/103; vite production build OK
- Shipped v1.4.5 via the CI release train; OTA manifest published to fleet app_config (tills on 1.4.4 pick it up at boot / 6h check)

---
Task ID: 1-3
Agent: main (Super Z)
Task: Install tooling (codegraph, design skills), clone smart-vat-kenya, analyze codebase

Work Log:
- Installed codegraph CLI v1.6.0 (npm i -g @colbymchenry/codegraph); ran `codegraph init` on repo: 223 files, 2,026 nodes, 4,480 edges
- Cloned design skill repos into /home/z/my-project/skills/: bergside-awesome-design-skills, anthropic-skills (frontend-design), jakubkrehel-skills, thedaviddias-front-end-checklist
- Cloned smart-vat-kenya (Astro 7 SSG + React islands + Tailwind 4, static, Netlify)
- Architecture: routes in src/pages, content components in src/components/resources (registered in map.ts = titles/metas), facts in src/lib/vat-facts.ts, shared widgets in components/
- Found bugs: netlify.toml corrupted ([[headers]] -> [eaders]]), tax-deadlines.astro line 85 malformed duplicated link markup, stale PAYE date

Stage Summary:
- Repo understood. Implementation plan confirmed: P1 title/meta rewrites (10 pages), P2 301 redirect + tax-deadlines optimization, P3 CTA boxes + internal links, P4 3 new pages, 4 viral articles, eTIMS diagnostic tool, WhatsApp CTA pivot to self-service routing

---
Task ID: 4-14
Agent: main (Super Z)
Task: Implement all SEO + content + business-model improvements on smart-vat-kenya

Work Log:
- P1: Rewrote titles/metas for top 10 pages in map.ts (exact plan copy); BaseLayout now appends brand suffix only when total <= 60 chars (SERP-safe titles)
- P2: Retired kra-vat-filing-deadline-august-2026 (removed from map/index/resources registries, deleted component); 301 redirects in netlify.toml [[redirects]], public/_redirects, vercel.json; updated all internal links; tax-deadlines.astro optimized (new title/meta, next-deadline callout, fixed malformed WhatsApp link bug, stale PAYE date, last-verified bump)
- P3: Created shared ServiceCtaBox (components/service-cta-box.tsx) - two-track CTA (free self-service tool first + priced service second); inserted after Quick Answer on itax-portal-not-working, how-to-register-for-vat-in-kenya, etims-pending-sync; added commercial anchor-text internal links on iTax page
- P4: Created 3 new resource pages: vat-registration-kenya-compare-options, etims-vs-kra-shuru-chatbot, vat-commercial-rent-kenya
- Viral: youtube-5-percent-tax-kenya (with /tools/youtube-tax-calculator/ interactive calculator), consolidated-cargo-benchmark-kenya (with /cargo-one-pager/ printable sheet), real-cost-of-taxes-kenyan-sme; updated kra-tax-amnesty-2026 title/meta to "Check If You Qualify" angle; built /tools/amnesty-checker/ eligibility quiz
- Built /tools/etims-diagnostic/ - decision-tree eTIMS error diagnostic (16 nodes: rejections, sync, lockouts, PIN failures)
- Business pivot: replaced over-promising WhatsApp diagnosis CTAs with self-service routing on home, kra-help, kra-status, navbar, index meta, etims-account-locked, etims-duplicate-invoice, etims-buyer-pin-missing, etims-can-i-claim-my-expenses; floating WhatsApp button reframed to quote/services
- Registered all new resources (map.ts, index.ts, lib/resources.tsx, resources/index.astro with renumbering); added 3 tools to tools page grid
- Fixed 21 pre-existing TypeScript errors (BaseLayout Props path/author, hreflang, className->class in etims-penalty-calculator.astro)

Stage Summary:
- Build passes: 107 pages, Pagefind indexed, sitemap 105 URLs (August page removed, 10 new URLs added)
- All titles/metas verified in dist HTML; CTAs, internal links, redirect files, diagnostic tools all render
- Zero broken internal links; zero remaining "diagnose over WhatsApp" promises; zero type errors

---
Task ID: 15
Agent: main (Super Z)
Task: Final review, commit and push

Work Log:
- Final build: 107 pages, Pagefind indexed, sitemap 105 URLs, no errors
- astro check: 0 errors (21 pre-existing fixed)
- Link check: 122 internal links, 0 broken (all targets exist in dist)
- Restored pre-existing _redirects rules that were overwritten, appended new 301 rules
- Committed 43 files (+3741/-369) as 829328d and pushed to origin/main with provided token

Stage Summary:
- Live on GitHub main; Netlify/Vercel will auto-deploy. All plan priorities implemented and verified.

---
Task ID: 16
Agent: main (Super Z)
Task: Remove the WhatsApp number and WhatsApp CTA elements site-wide (user-reported skip from original brief)

Work Log:
- Located 159 occurrences of +254 717 344 440 / wa.me/254717344440 across 105 files
- Deleted components/whatsapp-button.tsx (floating desktop pill + mobile sticky bar) + BaseLayout mount, import, dns-prefetch, and wa.me click tracker (now contact_click)
- Removed number from JSON-LD (organization telephone, contactPoint -> email-based, sameAs), vat-facts.ts contact, navbar Get Started buttons (-> /services/), Footer phone link + trust item, llms.txt contact section
- Deleted unused components/status-banner.tsx (dead code with number); fixed 4 local StatusBanner copies in resource articles
- 4 scripted passes (scripts/remove_whatsapp*.py): routed every wa.me CTA by intent text -> /services/*, /kra-help/, /tax-deadlines/, /partners/, or mailto:info@smartvatkenya.co.ke with subject; service/contact pages forced to mailto to avoid self-links
- Rewrote WhatsApp-promise copy in ~95 resource articles + all service/geo pages + home/about/how-it-works/partners/forms/privacy-policy/testimonials/meta descriptions (email phrasing); kept legitimate generic references (KRA Shuru, WhatsApp groups, PDF sharing tips)
- Reframed vat-deadline-reminders article from WhatsApp broadcast promise to the deadline calendar; updated map.ts + resources index + llms.txt titles
- Fixed pass-1 collateral: greedy whatsappNumber prop regex corrupted one JSX call site (repaired), encoded ${WA_TEXT} mailto subjects (restored as template literals), CRLF line endings on 55 files (restored, diff kept at +537/-881)
- Stripped target=_blank/rel from internal + mailto anchors; renamed data-track whatsapp-cta -> contact-cta; ServiceCtaBox service links now internal (3 usage sites re-pointed)

Stage Summary:
- Commit cc11f84 pushed to origin/main: 118 files, +537/-881, 2 components deleted
- Verified: astro check 0 errors; build 107 pages; 4560 internal links 0 broken; 230 mailto CTAs; ZERO wa.me / 254717344440 / 717 344 440 matches in source and dist HTML
- Site conversion paths now: self-service tools -> service pages -> email; no WhatsApp channel anywhere

---
Task ID: 17
Agent: main (Super Z)
Task: Thorough verification of every page + fix all remaining deployment gaps (user-reported mixed state)

Work Log:
- Diagnosed live site vs user report: most reported failures were stale CDN cache (titles, redirect, sitemap, tools grid, header language all already live). Real issues: 3 pages at wrong slugs, VAT rates title trimmed, plus discovered deeper problems
- Verified netlify.toml is VALID TOML (the "[eaders]] corruption" was a display artifact swallowing 'h' chars in cat output - hexdump + tomllib confirmed; live immutable headers prove it)
- Renamed 3 pages to plan-specified slugs: vat-registration-options-kenya, etims-vs-shuru-comparison, real-tax-bill-kenyan-sme (git mv + all registries: map.ts, index.ts, resources/index.astro, lib/resources.tsx, component JSON-LD/currentSlug)
- Added 301 redirects old-slug -> new-slug in netlify.toml (6 rules), public/_redirects (6 rules), vercel.json (4 rules)
- DISCOVERED: 11 pages had full content components but were NEVER BUILT - llms.txt linked them and they 404'd on live. Root cause: commit 933ae1e accidentally deleted their resourceComponents registrations (eTIMS problem-graph cluster x4, VAT return authority cluster x3); kra-health-check/vat-threshold-explainer/how-to-create-kra-pin/vat-input-guide were imported but never registered
- Restored all 11: imports + component map + meta entries (recovered deleted metas from git history 933ae1e^), articles list, resources index listing, kra-help hub links, auto-populated-return cluster cross-links
- Fixed VAT rates title to intended "Kenya VAT Rates 2026: 16% Standard + Full Zero-Rated & Exempt List" (map.ts + H1 + JSON-LD headline + listing title)
- Fixed leftover WhatsApp title in lib/resources.tsx ("Free KRA VAT Deadline Reminders on WhatsApp" -> clean)
- Updated llms.txt: 6 new key pages + new tools in Free Tools line
- Added 6 strategic internal links to new articles (do-i-need->compare-options, etims-pending-sync->shuru-comparison, landlords->commercial-rent, TOT->real-tax-bill, freelancers->youtube-5pc, importers->cargo-benchmark)
- Built comprehensive verify_all_pages.py: 48 checks - page existence, sitemap integrity, WhatsApp purge scan, titles, internal links (incl. anchor-ID verification), llms.txt URLs, redirect configs, tools grid, header language, new internal links

Stage Summary:
- Commit 5cc8c30 pushed to origin/main: 20 files, +269/-40, 3 renames
- Build: 117 pages (was 106), sitemap 116 URLs, astro check 0 errors
- ALL 48 verification checks pass: 4536 internal links 0 broken, zero WhatsApp remnants, all llms.txt URLs resolve
- Netlify auto-deploy triggered; live verification pending


---
Task ID: 18
Agent: main (Super Z)
Task: Live deployment verification of commit 5cc8c30

Work Log:
- Waited for Netlify auto-deploy (~3 min), then verified live site
- All 3 renamed plan URLs return 200; all 3 interim slugs + August deadline page 301 to correct targets
- All 11 restored pages return 200 (spot-checked vat-registration-options, etims-vs-shuru-comparison, real-tax-bill, etims-device-not-registered, kra-health-check, vat-input-guide)
- Live sitemap: 116 URLs, includes all 13 new/restored slugs
- Live VAT rates title: "Kenya VAT Rates 2026: 16% Standard + Full Zero-Rated & Exempt List" (intended version)
- WhatsApp scan on live (home + restored pages): 0 remnants
- kra-help hub links to restored cluster pages: live

Stage Summary:
- Deployment fully verified. Site now: 117 pages, 116 sitemap URLs, 8 active 301 redirects, zero WhatsApp CTAs, consistent headers, complete tools grid, llms.txt fully resolving.

---
Task ID: 19 (new project: POS system)
Agent: main (Super Z)
Task: Research + refined plan for white-label Go+React POS (user's new primary project)

Work Log:
- Installed Go 1.27.1 (user-space tarball; apt needs root). Node 24, npm 11, git 2.47 present. Network OK (proxy.golang.org, npm, GitHub)
- Cloned + dissected via parallel Explore agents: madebyaris/poinf-of-sales (Go+Gin+React18, entity model worth keeping, hardcoded-JWT-secret/SQLi flaws to avoid), mannyd209/GO-POS-Server (PIN auth pattern but plaintext+no lockout+no WAL, re-implement properly; NO license so patterns only), Tuma-Solutions/mpesa-pos (aggregator not direct Daraja; real captured callback JSON; OSPOS permission model + cash_up shift model + CSV template patterns; manual receipt entry missing, we build it)
- Eliahhango/SmartPOS no longer exists on GitHub; 4-role concept covered by our dynamic permission-based RBAC
- frontend-styling-expert produced complete "Ledger" design system spec (Tailwind v4 @theme tokens, WCAG-verified contrast, neubrutalist component recipes, 65/35 POS layout)
- Verified via web search: Daraja endpoints current (POST /mpesa/stkpush/v1/processrequest), receipt codes 10-char alnum
- Refined architecture: permission-catalog RBAC (17 perms, admin-editable roles, seeded Admin/Cashier/Designer), PaymentProvider abstraction (mock/sandbox/production + auto/stk/manual modes), integer cents money, guarded state transitions, persistent print_jobs, mDNS broadcast, IndexedDB offline sync, go:embed single binary

Stage Summary:
- Project dir /home/z/my-project/pos-system (module posapp), served on :3000 in sandbox via gateway for preview
- Plan locked; token belongs to ssmurfgg04-gif (fresh repo at push time, name: pos-system)

---
Task ID: 20
Agent: main (Super Z)
Task: Build complete Go backend

Work Log:
- Packages: config, database (dual sqlite-WAL/postgres with ?→$N rebind, versioned migrations, idempotent generic seeds), models (camelCase DTOs, cents, RFC3339 strings), auth (JWT 12h + bcrypt + PIN lockout 5→30-300s + sliding-window rate limiter + per-request permission reload), settings (DB key/value, secrets masked "__SET__"), ws (hub, first-message token auth, ping/pong), mpesa (Provider interface; Daraja token cache/STK/query/callback parse; Manual validation ^[A-Z0-9]{10}$; Mock provider w/ configurable delay+result), printer (hennedo/escpos renderer 58/80mm, TCP/file targets, persistent print_jobs worker w/ 5-attempt backoff + crash recovery), mdns (zeroconf _pos-server._tcp), services (checkout w/ server-side price re-read + guarded stock deduction; completePayment guarded transition + amount-mismatch DISCREPANCY handling + receipt backfill; manual confirm w/ dedupe; void restores stock; idempotent offline sync via client_uuid; sweeper polls pending STK every 5s w/ 3min timeout; shifts w/ expected/variance; design board), handlers (auth/login/pin/pin-users, users, roles+permissions catalog, products+categories+CSV import/export, orders+receipt.html, payments stk/manual/callback, sync, reports, shifts, design, settings+test-print+audit), router (public health/login/pin/pin-users/callback; permission-gated rest), main (graceful shutdown, worker+sweeper lifecycle)
- Fixed during build: import cycle seed→settings; embed path must be in package dir (moved to root static.go); seed SQL category_id bug; checkout returned stale order after STK init (reload); portability (COALESCE, no COLLATE NOCASE, no ? IS NOT NULL patterns)
- E2E smoke script (scripts/smoke_test.py): 41/41 PASS, auth, RBAC 403s, cash checkout+stock+idempotent replay, insufficient stock, M-Pesa mock STK via sweeper w/ receipt, manual entry + dedupe + format validation, offline sync + replay safety, void+restore, design board, shifts, reports, receipt HTML, callback completion + amount-mismatch flagging, roles CRUD, audit
- Go unit tests all green: mpesa (phone norm, receipt validation, callback parse w/ real Daraja sample, password format, mock flows), printer (FormatMoney, 80mm render contains all fields + ESC @ + GS V cut, 58mm truncation, target parsing), handlers (12 in-process httptest E2E: checkout, STK query completion idempotent, manual dedupe, sync, void, RBAC, settings masking, price override permission gate, CSV import upsert)

Stage Summary:
- Binary builds clean, go vet clean, go test ./... green. 43.9MB unstripped binary
- Demo seed: 13 generic products, 4 categories, admin(admin123/PIN 1234) + cashier/designer demo accounts, mpesa_env=mock so demos work out of the box
- Next: React frontend (Ledger design system), embed, VLM critique rounds, push

---
Task ID: 3
Agent: design-expert
Task: POS design system spec

Work Log:
- Read worklog for context; inspected pos-research/poinf-of-sales/frontend (shadcn/Tailwind v3 reference app) and available design skills (visual-design-foundations, ui-ux-pro-max) for grounding
- Wrote complete implementable design system spec for the white-label Kenyan retail POS (React + Vite + Tailwind v4): Tailwind v4 @theme token block (colors/surfaces/lines/text levels, status colors paid-emerald #10B981 / pending-amber / danger / info / offline / void, radius, hard neubrutalist shadow tokens, type scale incl. 40px tabular money size, motion tokens + keyframes)
- Ran Python WCAG contrast verification on every text/surface pair in the palette: all pass AA (17.85:1 ink/white, 7.04:1 ink-on-emerald-500 primary button, 4.84:1 amber-700-on-amber-50 pending, etc.); caught and rejected white-on-emerald-500 (2.54:1 fail) and white-on-emerald-600 (3.77:1, large-text only)
- Confirmed PO's direction: dark slate-900 app shell + white content surfaces (glare rationale: critical reading zones get 17.9:1 contrast that survives bright-store washout; borders carry neubrutalism on dark canvas since hard shadows barely read dark-on-dark)
- Specified 20+ component recipes with exact class strings and enumerated states: Button (4 variants x 3 sizes, press = translate+shadow-collapse), Card, Modal (5 sizes), Input/Select/MoneyInput, StatusPill (never color-only), Table (sticky header), Toast (bottom-center, justified), 3x4 Keypad, ProductTile (grid math: 5/6/7 cols at 1024/1280/1536 → 112/120/125px tiles), CartLine (2-row layout to fit 300px panel), CategoryFilter, Tabs, EmptyState, OfflineBanner, Spinner, M-Pesa pending pulse/progress
- Specified POS terminal layout (h-16 topbar, 65/35 grid, exact paddings, <1024 stacked + bottom-sheet cart), and screen specs for Login/PIN quick-switch, M-Pesa checkout (4 states incl. manual receipt-code fallback), Inventory, Orders + drawer, Design/Production kanban, Shifts open/close with variance coloring, Reports + CSS bar chart, Users & Roles, Settings incl. Daraja + danger zone
- Accessibility rules section: verified contrast table, 2px focus rings with offset, 44px targets (enumerated exceptions), icon+text status, reduced-motion kill switch, KES Intl.NumberFormat + tabular-nums, <100ms touch feedback (duration-75 + navigator.vibrate)
- Delivered full spec in final message (document only, no code files written to any project, per instructions)

Stage Summary:
- Key decisions: dark slate-900 shell + white surfaces CONFIRMED; primary emerald button uses slate-900 text (7.04:1) not white; emerald reserved for paid/success semantics only (active category chip = black, not emerald); toasts bottom-center (topbar barcode search is the highest-frequency target and must never be covered); toasts z-60 above modals z-50; system font stack (no webfont on flaky connections); runtime --color-brand override hook for white-label customers
- Spec is implementation-ready: every value is an exact Tailwind class/CSS value; grid/tile/panel widths computed and verified at 768/1024/1280/1536 breakpoints

---
Task ID: 2-b
Agent: Explore (mpesa)
Task: Dissect Tuma-Solutions/mpesa-pos

Work Log:
- Read worklog context; surveyed repo structure (opensourcepos 3.4 fork on CodeIgniter 4 + Bootstrap 3; single squashed commit "bug fix: payment model")
- Read all 6 Tuma integration files: Controllers/Tuma.php + Tuma_callback.php, Libraries/Tuma_lib.php, Models/Tuma_payment.php, Views/configs/tuma_config.php + sales/tuma_payment.php (modal+JS), plus en/Tuma.php + Sales.php lang keys, Routes.php, locale_helper get_payment_options(), tabular_helper, receipt_default/email/short views
- KEY: NO direct Daraja integration, payments go through Tuma's own aggregator (https://api.tuma.co.ke, POST /auth/token + POST /payment/stk-push only). 30+ banks/Airtel are aggregator-side claims (README); no Safaricom/Daraja URLs anywhere in code
- Verified real production callback payload from writable/logs/log-2026-03-03.log (flat JSON: status/merchant_request_id/checkout_request_id/result_code/result_desc/timestamp/mpesa_receipt_number/amount; result_code=0 + status=completed = success)
- Traced full sale linkage: initiate (insert tuma_pos_tuma_payments w/ sale_id often -1) -> JS polls /tuma/status/{checkout_request_id} every 2s x60 -> success -> hidden input -> session tuma_checkout_request_id -> after sale save, _update_tuma_payment_sale_id() back-fills sale_id (session-dependent; 4/6 dump rows orphaned at -1)
- Dissected tuma_mpesa_pos.sql dump: full schema for sales/sales_items/sales_items_taxes/sales_taxes/sales_payments/cash_up/employees/grants/permissions/modules/items/inventory + tuma_pos_tuma_payments table (indexes non-unique, no dedupe)
- Documented phone normalization (07xx/7xx/+254 -> 254XXXXXXXXX, dead +254 branch), encrypted api_key storage (CI encrypter + .env key), token NOT cached (re-auth per request), webhook PUBLIC + unsigned (no HMAC/IP check), no server-side verification of payment before sale completion (client-trust flaw), Tuma controller permission = 'sales' not 'config'
- Audited Kenyan UX: KES currency settings, Africa/Nairobi TZ, tax_included VAT-inclusive support in Tax_lib (this deploy: tax-exclusive, rate 8), NO Swahili locale (en only for Tuma strings), M-Pesa receipt line on receipts + Takings table column, no offline/PWA, Sms_lib is UK-stub
- Catalogued permissions model (modules/permissions/grants, menu_group home/office/both/--, location-scoped *_stock subperms, sales_change_price/sales_delete), cash_up open/close flow (float, transfers, auto-computed closed amounts from Summary_payments, M-Pesa NOT bucketed), items CSV import (template download, all-or-rollback txn, barcode dedupe, per-location qty + attribute columns)

Stage Summary:
- Payment-integration reference decoded: aggregator pattern (auth token -> STK push -> flat webhook -> local status polling), NOT raw Daraja, our Go design must implement Daraja directly (base64(shortcode+passkey+timestamp), Body.stkCallback.CallbackMetadata parsing, sandbox/prod toggle, token caching w/ expiry) which this fork offloads to Tuma
- Steal from OSPOS: sales/sales_items/sales_payments/sales_taxes schema + cash_up shift model (open float, transfers, counted close, open/close employee ids), 3-table granular permission system w/ menu groups + location scoping, CSV import/export w/ template + transactional all-or-nothing import, token-based invoice numbering ({CO}, Q%y{QSEQ:6}), M-Pesa receipt code on receipt + sales table, app_config key-value white-label settings
- Must NOT copy (flaws): payment_type stored as localized display string; session-based cart/payment linkage; unsigned public webhook w/o idempotency or unique index on checkout_request_id; client-trusted payment amounts at sale completion; float money math; no reconciliation job for pending payments (model helpers get_pending_payments/get_total_paid are dead code); M-Pesa absent from cashup buckets; no sandbox env; no manual receipt-code entry flow (missing feature for Paybill/Till walk-ins)


---
Task ID: 2-a
Agent: Explore (repos)
Task: Dissect poinf-of-sales + GO-POS-Server

Work Log:
- Read worklog.md for context (white-label Go+React POS build)
- Mapped poinf-of-sales: backend (Gin, 4,431 LOC Go, 11 files) + frontend (React 18 + TanStack Router/Query + Vite + shadcn/Radix + Tailwind 3, ~11,000 LOC components, 80 src files)
- Extracted go.mod deps both repos (Gin+lib/pq+JWT v5+bcrypt vs Fiber v2+go-sqlite3+zeroconf+websocket)
- Documented full PostgreSQL schema (9 tables: users, categories, products, dining_tables, orders, order_items, payments, inventory, order_status_history) + seed data (7 users, 6 categories, 27 products, 14 tables; demo password admin123)
- Cataloged all 40+ API routes across public/protected/server/counter/admin/kitchen route groups; traced JWT auth (HS256, hardcoded secret, 24h expiry, Bearer header) and RBAC (5 roles hardcoded in CHECK constraint + RequireRole/RequireRoles middleware)
- Traced business logic: order creation tx (server-side price lookup, 10% hardcoded tax, ORD+date+4-digit order numbers), payment flow (partial payments, overpay guard, auto-complete + table free + status history), receipt display/print/download stubs
- Frontend inventory: RoleBasedLayout switches views by role; localStorage-based session (server verification DISABLED with enabled:false); react-query polling 2-5s for kitchen/counter "real-time"; found role type drift (frontend types say 'cashier', backend uses 'counter')
- Found defects worth noting: SQL injection in getKitchenOrders (status param concatenated), hardcoded JWT secret, no migrations (init SQL only via docker-entrypoint-initdb.d), no inventory/stock decrement on sale, CORS hardcoded to localhost ports
- Dissected GO-POS-Server: Fiber app, 10 SQLite tables (staff/category/item/modifier/option/discount/transaction/*), PIN auth via POST /staff/auth {pin} → cookie session (fiber/session); PIN STORED PLAINTEXT (docs falsely claim hashed); NO lockout logic; quick-switch = POST new PIN replaces session
- SQLite: mattn/go-sqlite3, DSN = bare file path, NO WAL/pragma/pool config (default journal mode, unbounded conns), WAL must be ADDED in our build; mDNS: grandcat/zeroconf registering hostname as instance, service _pos-server._tcp, domain local., port 8000, TXT version=1.0.0
- Noted /transactions routes are UNPROTECTED (no auth middleware), /ws websocket is dumb echo (docs claim event streams), rand-based IDs (sale000000, titem000000) not crypto-safe, deferred rows.Close in loops (leak under load), config via PORT/DB_PATH/APP_ENV env vars, graceful shutdown via SIGINT/SIGTERM + app.Shutdown()
- Confirmed: NO ESC/POS printer code, NO offline/IndexedDB/batch sync, NO shift management in either repo; no LICENSE file in GO-POS-Server (github.com/mannyd209), poinf-of-sales is MIT (Copyright 2024 "POS Full System", github.com/madebyaris)

Stage Summary:
- poinf-of-sales = solid restaurant-POS reference: clean entity naming (orders/order_items/payments/dining_tables/order_status_history), good payment/partial-payment business logic, correct table-free + status-history patterns to preserve. Re-architect: JWT secret→env, 10% tax→configurable tax rates, add stock decrement on sale, fix SQL injection + role naming drift, replace polling with websockets, drop Cursor/Claude branding.
- GO-POS-Server = source of integration patterns only: PIN quick-switch UX (session swap), zeroconf registration snippet (grandcat/zeroconf, _pos-server._tcp.local.:8000), Fiber graceful shutdown. Its SQLite layer is a cautionary tale: add WAL+busy_timeout+single-writer pool ourselves; hash PINs (bcrypt) + add lockout; its modifier/option/discount catalog model + event_price dual pricing + transaction summary SQL (cash/card split, tips, card fees) are worth porting into the new schema.

---
Task ID: 26
Agent: main (Super Z)
Task: Self-hosted downloads on Netlify (no GitHub redirect) + Big-7 landing redesign + thorough review

Work Log:
- SANDBOX WIPED AGAIN between messages (pos-system/, build artifacts, Go toolchain, download/ all gone; outer repo rolled back). Full recovery from GitHub: cloned repo @ c98b4e8, downloaded the 4 release assets byte-identical, npm ci for the demo build
- Web research: Netlify drag-and-drop guidance = deploy <50MB total AND no file over ~10MB (answers.netlify.com staff), our 10.31-10.39MB installers were over; research on value-prop landing pages: value validation above the fold, ONE primary CTA (single-CTA pages ~13.5% conversion), lean scannable content
- Repacked all installers under 10,000,000 bytes with byte-identical binaries (sha256-verified): zopfli-deflated zips via scripts/pkgutil.py (raw deflate extracted from zopfli's zlib container, guarded FDICT check; hand-rolled ZIP writer w/ unix perms), win 10.31->9.92MB, mac-arm 9.56->9.17MB, mac-intel 10.39->9.99MB; Linux switched to tar.xz 10.16->7.32MB; clean member roots (LedgerPOS/, LedgerPOS.app/); fixed mac app_root dirname bug (Contents vs .app, bundle was missing its Contents/ level, caught by member-name verification); checksums.txt generated
- Landing page redesigned (scripts/assets/download-page.html): hero with ONE auto-detected primary CTA (25-line progressive-enhancement JS; Windows default fallback; mobile note), alt-platform links, trust strip; the Big-7 value props (minute-to-first-sale, offline-first, M-Pesa STK+receipt-code, KRA monthly VAT, existing hardware, data-stays-local, MIT white-label); slim 3-step strip; relative downloads/ links with download attr; OG/Twitter meta; kept Ledger design language
- scripts/build_site.py: fast-path site assembly (no Go/npm), renders template, regenerates checksums, href link check, drag-and-drop zip with installers STORED (not double-compressed); 36.5MB / 14 files / ~37MB deployed
- scripts/package_desktop.py updated to match (pkgutil zopfli zips, tar.xz, size guard, embed-by-default, LEDGERPOS_LIGHT_SITE=1 opt-out); scripts/create_release.py rewritten in-repo; GitHub release v1.0.0 re-synced (4 smaller packages + checksums.txt asset, legacy tar.gz deleted, notes patched)
- Verification: local http.server, every href 200 with byte-exact sizes; browser E2E, title/Big-7 count 7/CTA auto-detect (Linux host -> Linux tar.xz + alt hidden)/emerald brand button; demo band clickthrough -> /demo/ loads app w/ Demo-mode pill; desktop E2E 17/17 on the repacked tar.xz artifact
- VLM critique round on the landing page: applied real findings (note text contrast #94A3B8->#475569, dropped 'SQLite WAL' jargon from card 02, softened SmartScreen/Gatekeeper phrasing); rejected false positive ('Download for Linux' hero is correct per-host auto-detection)
- README updated: downloads served straight from Netlify, build_site.py/repack.py commands, checksums, LEDGERPOS_LIGHT_SITE escape hatch, tar.xz
- Committed + pushed: ba66960 (self-hosted downloads + Big-7 + sub-10MB packages) then polish commit

Stage Summary:
- Netlify page now serves installers DIRECTLY: drag download/ledgerpos-netlify-site.zip (36.5MB) onto Netlify and the download buttons hand users the file from the same site, no GitHub redirect
- All packages <10MB (Netlify drag-drop safe), binaries byte-identical to tested release, checksums published (site + release)
- Landing page conversion-hardened: one-CTA hero w/ OS auto-detect, Big-7 props, VLM-reviewed
- Repo fully pushed (source + scripts + DEVLOG); release v1.0.0 synced as mirror

---

## Task 28, GitHub → Netlify continuous deployment + the typing freeze

**User report:** the live Netlify site showed the POS login page instead
of the download landing page, and typing into any field froze the tab.

**Root causes found (both reproduced before fixing):**

1. **Deployment**: `netlify.toml` still deployed the demo SPA at `/`
   (base=frontend, npm build, publish dist), the landing-page work from
   Task 26/27 only ever produced drag-and-drop zips. With the repo now
   wired to Netlify auto-deploy, the wrong thing was being deployed.
2. **Typing freeze**: `Login.tsx` (and `Settings.tsx`) registered
   promise-`.then(setState)` callbacks **in the render body**. With
   trusted keyboard events React flushes updates synchronously, the
   microtask state-sets land as render-phase updates, and the component
   re-registers new callbacks on every re-render → infinite
   render-phase loop at 80-100% CPU (stack captured via CDP:
   Login → useState → updateReducer loop). CDP `Input.insertText`
   reproduced the hang deterministically; synthetic events did not,
   which is why it slipped past earlier testing.

**Fixes:**

- `frontend/src/pages/Login.tsx`, `Settings.tsx`: moved the
  backend-mode/desktop-status promises into `useEffect` with `alive`
  flags, typing verified smooth via CDP insertText + Playwright
  fill/type on Login, Settings and POS barcode search.
- **Continuous deploy**: new `scripts/netlify_build.py` is the Netlify
  build command (netlify.toml: base=frontend, command, publish=dist).
  It runs the vite demo build with `VITE_BASE=/demo/`, restructures
  dist into `demo/`, renders the landing page at `index.html`, copies
  the installers from the repo (`downloads/`, committed, SHA-256
  verified against checksums.txt at build time) and writes `_redirects`
  (`/demo/*` SPA fallback). No network fetches → deterministic builds.
- **Subdirectory routing**: `lib/router.ts` gained `stripBase`/`withBase`
  (driven by `import.meta.env.BASE_URL`); `vite.config.ts` reads
  `VITE_BASE`. Root builds are byte-identical in behavior (BASE='/'
  identity), demo builds route under `/demo/`.
- Installers + checksums + icons committed to the repo
  (`downloads/`, `scripts/assets/icons/`), the deploy serves them from
  the same Netlify site as the buttons point to.

**Verification (all green before pushing):**

- typing: CDP insertText + fill/type on login (demo + real), Settings,
  POS search, no freeze anywhere; console clean
- demo under `/demo/`: deep-link `/demo/reports` (via a Netlify-like
  _redirects server) → login → Reports renders; router strips/re-adds
  base correctly
- real app: `go vet` clean, `go test ./...` ok, serve smoke 55/55 with
  the new frontend embedded, browser login → /reports on the real server
- frontend: tsc clean, vitest 51/51
- landing: all 14 internal hrefs resolve; download files served (200);
  VLM critique 9/10

**Deploy expectation:** push to main → Netlify builds
`netlify_build.py` → live site = landing at `/`, demo at `/demo/`,
installers at `/downloads/`.

## v1.1.1, multi-store cloud (2026-09-26)

**Cloud (Supabase, applied live, canonical SQL in `db/cloud_schema.sql`):**

- `sync_stores` registry: one row per store; team_code minted by the DB
  (client never invents one). Row 1 = Main Store (KIAMBU-MAIN).
- `sync_register` multi-store aware: 1 active store → auto-join
  (zero-config unchanged); >1 → fresh tills register PENDING (team_code
  NULL, approved=false) and wait for the owner.
- Owner RPCs (all identity-checked, SECURITY DEFINER): `sync_create_store`
  (mints slug+team code), `sync_list_stores` (with device counts),
  `sync_list_pending`, `sync_assign_device`, `sync_remove_device`
  (self-revoke forbidden).
- RLS: `sync_stores` anon-readable (active rows only); devices/events
  remain anon-denied; everything mutates through RPCs that verify
  (device_id, secret_hash) and stamp team_code server-side.
- `sync_bootstrap` row 1 kept in sync with store 1 → v1.1.0 tills keep
  working untouched.

**App:**

- `resolveCloudStore` (synccloud.go): store registry is the source of
  truth; served project_url wins (tests point cloudBaseURL at a fake
  client must never drift to the production host). Legacy bootstrap
  fallback preserved for pre-registry deployments.
- `registerCloud` handles `pending` → `sync_store_pending` flag; pending
  tills queue events (never pushed, never lost) and join on the next
  heartbeat after the owner assigns them.
- TeamSyncStatus now carries storePending/multiStore/stores/pendingDevices.
- New owner endpoints (settings.manage): POST /team-sync/stores,
  /team-sync/assign, /team-sync/remove.
- Settings → Team: Stores card + Add store, "New tills waiting for a
  store" assign UI, Revoke kill switch on roster rows.
- Demo mirrors the new status shape + handlers.

**Incident (caught + fixed in-session):** the first resolveCloudStore draft
hardcoded the production project URL as the client base, so the sync tests
registered test devices and pushed ONE test event into the production
cloud. Production impact: none, both real tills were offline during the
window (last_seen ~09:08, leak at 10:12), the event was deleted within
minutes, all test devices purged, and the fix derives the client base from
the host the registry was actually fetched from.

**Verification:** go build/vet clean; go test ./... ok (incl. new
TestMultiStorePendingThenAssigned); tsc clean; vitest 73/73; E2E multi-store
flow exercised against the real cloud (create → pending → assign → remove →
cleanup); site build green (35.1 MB dist, link check ok); v1.1.1 release
cut with 4 installers + checksums (ledgerpos-linux stamps `1.1.1`).

## v1.1.2, join links, owner portal, onboarding fix (2026-09-26)

**Team join links with role:**

- Cloud: `sync_invites` (single-use token stored as SHA-256, role_name +
  permissions snapshot, 7-day expiry, revocable). RPCs: `sync_create_invite`
  (token minted DB-side, shown once), `sync_list_invites`, `sync_revoke_invite`,
  `sync_join_team` (redeem → device approved into the team, invite marked used).
- App: owner mints from Settings → Team (role picker + note → link + Copy);
  worker pastes at /join (public route, also linked from Login + Onboarding
  step 1) → POST /auth/team-join → till joins the cloud, local role+user
  created from the invite's role snapshot (unknown roles get created with
  the carried permissions; empty → Cashier set), onboarding_done=true,
  session issued → straight into the POS. Shop config syncs in normally.

**Owner portal (website):**

- Cloud: `portal_accounts` (one bcrypt row per team, published from tills);
  `portal_set_credentials` (identity-checked) + `portal_login` (pgcrypto
  crypt-verified, replay of the team's order stream → sales today/7d/30d,
  payment mix, top products, cashiers, voids, devices, last seen).
- Till publishes owner-level credentials (settings.manage + users.manage)
  whenever such a password is set/created, bcrypt made locally, plaintext
  never leaves the machine.
- Website: /portal/ (static, vanilla JS, landing design language) with
  login gate + dashboard; linked from the landing nav ("Owner login").

**Fixed:** onboarding step 5 "Open shop" now saves and navigates straight
to the main page (the success banner relied on a second click).

**Approval flow:** auto-approve turned OFF on Main Store; roster rows get an
Approve button for pending devices (sync_approve_device RPC).

**Test-to-production guard:** handler tests boot ShopPools whose real sync
loops registered 28 test devices against production (twice). Root-caused;
`guardProduction()` now refuses every cloud network call under `go test`
unless the test points cloudBaseURL at a fake. Proven: full suite run leaves
the devices table untouched.

**Cloud bugs found by E2E and fixed:** anonymous-composite crashes when
passing JSONB/record fields into json_build_object (sync_join_team,
portal_login), typed locals now; cashiers block scoped wrong (c.aliases);
pgcrypto lives in the extensions schema → search_path widened.

**Verification:** go build/vet/test ./... green; tsc clean; vitest 73/73;
real-cloud E2E: invite mint → redeem → single-use enforced → heartbeat keeps
team → portal credentials → portal login (correct/wrong/unknown); VLM design
pass on the Team panel (screenshots + polish: wider invite inputs, section
spacing); v1.1.2 release cut (4 installers + checksums), site build green
with /portal/.

---
Task ID: v1.3.0
Agent: main (Super Z)
Task: quotations page, device un-brick (Salma PC), mom-friendly forgot-PIN, SawaPOS benchmark, codegraph deep review, NSIS-verified release.

Work Log:
- Field incident root-caused: revoking a till was a PERMANENT brick, sync_join_team refused revoked devices even with a valid owner-minted invite, owner_device_signin refused them, and sync_approve_device refused them as a TARGET (the UI's own advice pointed at an impossible action). %APPDATA% survives reinstall so the identity never resets. Fixed in cloud v117: all three owner-consented acts now heal the device (join token / portal credential / Approve), secret rotated on heal, revocation still gates every non-consented path.
- Forgot-PIN (cloud one-time code): locked till files sync_request_pin_reset → owner till lists pending requests (Settings → Team) → approve mints a single-use 8-digit code (SHA-256 stored, 15 min, device-bound, 6 tries) → redeem clears lockout + sets PIN + auto sign-in. Review fixes: PinUser carries username (display names don't match accounts, was a flow-killing bug), redeem prologue enforces device identity, approve can re-mint expired codes, list includes code-issued rows with New code action.
- Quotations (SawaPOS benchmark headline): quotes table (migration v17), draft→sent→accepted/declined/converted, same tax math as checkout, team-synced (client_uuid + LWW + renumber), ConvertQuote = held sale via SKU-first line resolution + discount carry + CAS double-click guard, A4 print with body-class print isolation, demo-mode parity, quotes.view/manage permissions seeded via backfillRolePermsV17.
- CodeGraph (colbymchenry/codegraph, 73.9k★) installed + indexed (4,689 nodes / 15,050 edges); 3 adversarial review agents ran against sync/auth/money/frontend. ALL findings fixed (see commit 0023b38): forgot-PIN username binding (critical), convert cross-till product-id collision, outbox cross-tenant leak on team change (quarantine), applyVoid loyalty-earn reversal on receivers, imagesync 'svg' Contains false-positive (~50% of photos dead-lettered), NaN tax_percent poisoning, PIN-redeem code-burn-before-validate, quote money-permission parity with checkout, invite single-use race, X/Z clock-skew window (+2s pad after real flake repro).
- SAWAPOS-COMPARISON.md: offline durability + sync integrity + governance favor LedgerPOS; their public shopfront/online orders is the real gap (documented, not built); quotations parity shipped.
- Login polish: welcome-back header, secondary actions block (forgot password / PIN pad / join link), welcome-screen password escape hatch, friendlier forgot-password copy.

Stage Summary:
- v1.3.0 = quotations + un-brick + forgot-PIN + deep-review hardening. All suites green (Go handlers 121s, services, settings; vitest 95; tsc; vite build). Docs: AI-HANDBOOK.md (orientation), MIGRATION-v17-ROLLBACK.md, SAWAPOS-COMPARISON.md.

---
Task ID: v1.3.1
Agent: main (Super Z)
Task: release-engineering diagnosis (site frozen at 1.2.1), security sweep riding the review checklist, v1.3.1 installers + OTA manifest.

Work Log:
- DIAGNOSED the "site still shows 1.2.1" report with GitHub API evidence: every "Deploy site to Netlify" run since the beginning, including the 10-03 run that coincided with the live 1.2.1 content, actually SKIPPED the deploy step (missing NETLIFY_AUTH_TOKEN/NETLIFY_SITE_ID secrets, logs fetched via API). No Netlify GitHub App check-runs/statuses exist on ANY commit in this repo, so the live site was a one-off deploy of the v1.2.1-era build (landing shows VERSION "1.1.10", the un-bumped constant at commit b08a1f5, with 1.2.1 installers). Not a size problem (all four packages 7.9-10.7 MB), not NSIS. Fix: replaced the workflow with .github/workflows/ci.yml, a real tests job (go vet/test + vitest) and a deploy job whose final step FAILS the run when the live site's checksums.txt does not match the repo's, with the exact 3 fix options printed.
- Review checklist sweep (v1.3.1 hardening, mom-invisible): backup dir 0700 + forced 0600 on VACUUM INTO snapshots (backup.go, update_rollback.go); pre-update archive index write is now fatal on failure (8.14 fail-closed); per-IP rate limiter added alongside per-account (login, owner sign-in, PIN, 8.7); branding/logo WebSocket broadcasts shop-scoped (8.10); TestRolesCrud rewritten to resolve roles by name (IDs come from a random-order seed map, the test was flaky by construction and tripped the new last-owner guard); go vet self-assignment in quotes.go cleaned.
- All suites green: go vet clean, go test ./... (handlers 124s), vitest 95/95, vite build, netlify_build.py end-to-end (landing v1.3.1, 14/14 links).
- v1.3.1 release: four installers rebuilt with NSIS solid LZMA (exe 8.3 MB; NSIS via apt-get download + dpkg -x with NSISDIR env override), committed to downloads/, checksums.txt updated.

Stage Summary:
- v1.3.1 = security sweep + release-engineering honesty. Paystack untouched. OTA manifest → 1.3.1 pushes the sweep to every till via the cloud manifest (SHA-256 verified).

---
Task ID: v1.3.5
Agent: main (Super Z)
Task: "backup database, change salma pc (08A5QMR) pin to 8188, fix approve never showing the code, remove the confirm-it's-you challenge after approve, make tills discoverable on the same WiFi, push update + OTA."

Work Log:
- Backed up all 56 tables of the Supabase project (supabase-full-20261016-080534.json.zip) BEFORE touching anything.
- Root-caused "tap Approve and no code ever comes": the v1.3.2 owner-capability gate requires a portal_accounts row to mint a capability, and portal_accounts was EMPTY on this deployment, so the confirm dialog could never succeed and the code could never be minted. Live SQL confirmed: sync_approve_pin_reset gated, portal_accounts = 0 rows.
- Unblocked Salma PC immediately: minted the approved reset code for pending request #3 directly in the DB (same sha256 format as the RPC, 24h expiry), device-bound to dev-desktop-o8a5qmr. The worker taps "Forgot PIN?", types the code, sets PIN 8188.
- Cloud migration cloud_schema_v135.sql: sync_approve_pin_reset no longer demands the owner capability (parameter kept for wire compatibility). Verified LIVE with a synthetic device: approve with no capability returns the 8-digit code, wrong secret refused, code single-use, redeem burns it.
- LAN discovery (the "tills are segmented" complaint): desktop mode previously bound 127.0.0.1 only and disabled mDNS. Now every till broadcasts _pos-server._tcp and serves a RESTRICTED listener (port+1..+9) with exactly two endpoints: /lan/whoami (device id/name/team, nothing secret) and /lan/pin-reset-code (accepted ONLY while that till has an active reset request with the matching id; the pushed code is a pre-fill, the cloud still verifies and burns it on redeem). Full API stays localhost-only.
- ApprovePinReset now walks the LAN (mDNS browse, 1.2s, short timeouts) and hands the code to the requesting till directly; the response carries delivered=true and the Settings UI shows "sent straight to that till over the WiFi". Fallback to read-aloud unchanged.
- The PIN screen polls forgot-pin/status while waiting and pre-fills the delivered code with a green "the code arrived on this till over the WiFi" banner; the worker only types their new PIN.
- Tests: 4 new Go service tests (approve needs no challenge, LAN delivery end-to-end with a real listener + fake mDNS, no-LAN fallback, foreign-request rejection), landelivery endpoint suite (whoami, matching-id acceptance, wrong id/short/alpha code refusals, rate limit, method guard), frontend suites for the PIN screen WiFi state and the Settings approve (code shown instantly, WiFi badge, NO confirm dialog). Live endpoint verification against a running server. Go suite all green, vitest 104/104, tsc clean, desktop e2e 20/20 with the new binary.
- Release v1.3.5: go-winres stamped, all four installers built (NSIS 3.11 via apt-get download + dpkg -x + NSISDIR), committed to downloads/, GitHub release cut with assets + checksums, cloud OTA manifest flipped to v1.3.5 with GitHub release URLs, verified with the exact anon till read path: all four SHA-256s match.

Stage Summary:
- Approve is one tap again, and on one WiFi the code hand-off needs zero typing on the owner side and only the new PIN on the worker side.
- Known gap (not new): the Netlify download site still serves 1.3.4 because the repo has no NETLIFY_AUTH_TOKEN/NETLIFY_SITE_ID secrets (CI deploy skips + fails its verify step). OTA does NOT depend on it (manifest uses GitHub release URLs). To heal the landing page: set those two secrets, or drag build/netlify-site onto Netlify.

---
Task ID: v1.3.6
Agent: main (Super Z)
Task: "v1.3.5 installed but nothing works: still can't see the other tills, salma pc still stuck on the code thingy, remove the confirm-it's-you popup on join-link generation, standardize 4-digit PINs, set salma pc to 8188, too many dev/test accounts, push updates + OTA for real."

Work Log:
- Found the workspace reset to a stale clone; re-synced to origin/main (3d72286). Backed up the whole cloud DB FIRST (db-backup-pre-v136-20261017-134442.json, 389 rows across 16 tables).
- Diagnosed the four reported failures against the LIVE cloud, not the repo:
  1. The "confirm it's you" popup: sync_create_invite (plus store/assign/remove/approve-device/portal_set_credentials) were STILL owner-capability-gated in the live database - v1.3.5 only un-gated sync_approve_pin_reset. portal_accounts had 0 rows, so the challenge could never succeed: every join-link generation dead-ended in a password dialog.
  2. The OTA that never lands: update.Checker only CHECKS (boot + 24h); download + install required a human to click Install on each machine. Fleet reality: 2o1cq9n + homepc on 1.3.5, salma pc (o8a5qmr) still on 1.3.3, 3mc3r31 on 1.2.1.
  3. "Can't see the other tills": LAN hand-off was mDNS-multicast only; the shop WiFi is segmented, so discovery found nobody and the code path degraded to read-aloud.
  4. Salma's "code thingy": her till is v1.3.3 (no OTA reached it), and her two earlier reset requests show status=used - the 8-digit flow burned codes without producing a PIN anyone remembers.
- Cloud migration cloud_schema_v136.sql (deployed + live-verified with a synthetic device):
  - RETIRED the owner-capability gate from all seven privileged RPCs (device identity = approved + non-revoked + secret-hash = authority). Dropped the old 4-arg sync_approve_pin_reset signature first so the new 5-arg form can never swallow the PIN into the capability slot.
  - DIRECT PIN GRANTS: sync_approve_pin_reset takes p_new_pin (4 digits, weak-PIN dam), stores ONLY crypt(pin, gen_salt('bf')) bound to the requesting device (24h window); new sync_poll_pin_grant + sync_claim_pin_grant give the locked till a single-use, device-bound collection path (stale claims re-serve after 30 min so a failed apply can never wedge the flow). The legacy 8-digit code path is preserved for older tills.
  - sync_register carries p_lan_addr -> sync_devices.lan_addr: cloud-assisted till discovery that works on networks where multicast is blocked. sync_approve_pin_reset returns the target till's lan_addr so the WiFi hand-off tries the direct unicast first, mDNS second.
- Go: ApprovePinReset(requestID, newPin) with grant semantics; PollPinGrant/ClaimAndApplyPinGrant + shared applyPinHash; heartbeat publishes the till's LAN delivery address (SetLanAddr from the landelivery listener, main.go); PushPinResetCode tries the cloud-registered address before mDNS; HasUnfinishedWork idle gate (pending payment = never interrupt; sale in the last 15 min = wait; forgotten open shift = does NOT block - durable on disk).
- REAL OTA: handler.ApplyStagedUpdate(actor) extracted from the manual endpoint; AutoApplyTick runs every 15 minutes - stages the release when one appears, installs (pre-update snapshot -> NSIS -> restart) at the first quiet moment, Windows-only, audit rows under actor "auto-ota". Checker cadence 24h -> 6h. This is the fix for "tills fell versions behind".
- Frontend: the Confirm-it's-you dialog + ownerGuard are GONE (Settings.tsx); PIN reset rows have a 4-digit "New PIN" box - Approve grants directly and the panel says "tell them to type it"; empty box = legacy code fallback. ForgotPinPanel: sends the LOGIN name (preselect.username, read-only when the face was picked), grant arrival replaces the code box with "go type your PIN", the pad itself listens while a user is picked and shows "Your PIN was set by the owner" - salma never opens any panel.
- Cloud hygiene (one-off, guarded): 29 stale dev/test device rows deleted (fake-cloud test clients + old homepc/desktop identities), 4 real tills remain; consumed pin_resets, stale invites and rate-limit dams cleared. Inserted salma pc's grant directly (device dev-desktop-o8a5qmr-4dd8c4a4, username 'Salma PC' - the exact name her till matched twice before, bcrypt of 8188, 7-day window). NO other account touched.
- Live verification with a synthetic device: register stores lan_addr; create_invite works with NO capability; approve-with-PIN -> poll -> claim (bcrypt verified against the plaintext via x/crypto) -> second claim refused; weak PIN refused. Synthetic rows deleted; salma's grant intact.
- Tests: 9 new Go tests (grant set/refused-weak/apply/single-use/device-bound/re-approve/legacy-code, lan_addr heartbeat + persistence, OTA idle gate) + landelivery/lan-addr paths; 3 new frontend suites (Settings grant approve + no-dialog + local 4-digit refusal + legacy fallback; ForgotPinPanel grant takeover + login-name request; pad-level grant note). go vet clean, go test ./... 14/14 packages, vitest 108/108, tsc clean.
- 2nd-round code review (12 findings, all triaged): CRITICAL fixed - the auto-OTA was launching the INTERACTIVE NSIS wizard and quitting the app (an unattended till would die at 3 am); it now runs /S silent and the NSIS script relaunches the new build by itself; auto-apply also refuses when there is no quit hook (serve appliance) or when the staged asset has no SHA-256 (GitHub fallback = manual only). HIGH fixed - a stale-claim re-serve could re-apply an old owner PIN over the worker's own rotation every 30 min (till now remembers applied grant ids); sync_remove_device/assign were cross-tenant once the challenge was dropped (both team-scoped now, verified live); grants no longer dead-end on old tills (1.3.3/1.2.1 get the legacy code automatically, version-checked in SQL). MEDIUM fixed - ForgotPinPanel no longer wipes lan_addr on an empty heartbeat; legacy redeem cannot damage a waiting grant; claim is atomic (conditional UPDATE + found); the @-trick/loopback registry address is refused. Live-verified all of it with synthetic devices (v136b), synthetic rows cleaned.
- VLM round-1 (8 screenshots): 6 PASS, 2 fixed (welcome footer reworded to mom-safe copy; the red "Last error: awaiting approval" duplicate removed from the Team panel - the calm yellow box already explains it). Mom-level UI journeys: 16/16 (welcome, register, onboarding wizard, add product, cash sale, reports, team panel, forgot-PIN).
