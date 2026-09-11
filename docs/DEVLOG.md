# Worklog

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
- Cloned + dissected via parallel Explore agents: madebyaris/poinf-of-sales (Go+Gin+React18, entity model worth keeping, hardcoded-JWT-secret/SQLi flaws to avoid), mannyd209/GO-POS-Server (PIN auth pattern but plaintext+no lockout+no WAL — re-implement properly; NO license so patterns only), Tuma-Solutions/mpesa-pos (aggregator not direct Daraja; real captured callback JSON; OSPOS permission model + cash_up shift model + CSV template patterns; manual receipt entry missing — we build it)
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
- E2E smoke script (scripts/smoke_test.py): 41/41 PASS — auth, RBAC 403s, cash checkout+stock+idempotent replay, insufficient stock, M-Pesa mock STK via sweeper w/ receipt, manual entry + dedupe + format validation, offline sync + replay safety, void+restore, design board, shifts, reports, receipt HTML, callback completion + amount-mismatch flagging, roles CRUD, audit
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
- Delivered full spec in final message (document only — no code files written to any project, per instructions)

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
- KEY: NO direct Daraja integration — payments go through Tuma's own aggregator (https://api.tuma.co.ke, POST /auth/token + POST /payment/stk-push only). 30+ banks/Airtel are aggregator-side claims (README); no Safaricom/Daraja URLs anywhere in code
- Verified real production callback payload from writable/logs/log-2026-03-03.log (flat JSON: status/merchant_request_id/checkout_request_id/result_code/result_desc/timestamp/mpesa_receipt_number/amount; result_code=0 + status=completed = success)
- Traced full sale linkage: initiate (insert tuma_pos_tuma_payments w/ sale_id often -1) -> JS polls /tuma/status/{checkout_request_id} every 2s x60 -> success -> hidden input -> session tuma_checkout_request_id -> after sale save, _update_tuma_payment_sale_id() back-fills sale_id (session-dependent; 4/6 dump rows orphaned at -1)
- Dissected tuma_mpesa_pos.sql dump: full schema for sales/sales_items/sales_items_taxes/sales_taxes/sales_payments/cash_up/employees/grants/permissions/modules/items/inventory + tuma_pos_tuma_payments table (indexes non-unique, no dedupe)
- Documented phone normalization (07xx/7xx/+254 -> 254XXXXXXXXX, dead +254 branch), encrypted api_key storage (CI encrypter + .env key), token NOT cached (re-auth per request), webhook PUBLIC + unsigned (no HMAC/IP check), no server-side verification of payment before sale completion (client-trust flaw), Tuma controller permission = 'sales' not 'config'
- Audited Kenyan UX: KES currency settings, Africa/Nairobi TZ, tax_included VAT-inclusive support in Tax_lib (this deploy: tax-exclusive, rate 8), NO Swahili locale (en only for Tuma strings), M-Pesa receipt line on receipts + Takings table column, no offline/PWA, Sms_lib is UK-stub
- Catalogued permissions model (modules/permissions/grants, menu_group home/office/both/--, location-scoped *_stock subperms, sales_change_price/sales_delete), cash_up open/close flow (float, transfers, auto-computed closed amounts from Summary_payments — M-Pesa NOT bucketed), items CSV import (template download, all-or-rollback txn, barcode dedupe, per-location qty + attribute columns)

Stage Summary:
- Payment-integration reference decoded: aggregator pattern (auth token -> STK push -> flat webhook -> local status polling), NOT raw Daraja — our Go design must implement Daraja directly (base64(shortcode+passkey+timestamp), Body.stkCallback.CallbackMetadata parsing, sandbox/prod toggle, token caching w/ expiry) which this fork offloads to Tuma
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
- SQLite: mattn/go-sqlite3, DSN = bare file path, NO WAL/pragma/pool config (default journal mode, unbounded conns) — WAL must be ADDED in our build; mDNS: grandcat/zeroconf registering hostname as instance, service _pos-server._tcp, domain local., port 8000, TXT version=1.0.0
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
- Web research: Netlify drag-and-drop guidance = deploy <50MB total AND no file over ~10MB (answers.netlify.com staff) — our 10.31-10.39MB installers were over; research on value-prop landing pages: value validation above the fold, ONE primary CTA (single-CTA pages ~13.5% conversion), lean scannable content
- Repacked all installers under 10,000,000 bytes with byte-identical binaries (sha256-verified): zopfli-deflated zips via scripts/pkgutil.py (raw deflate extracted from zopfli's zlib container, guarded FDICT check; hand-rolled ZIP writer w/ unix perms) — win 10.31->9.92MB, mac-arm 9.56->9.17MB, mac-intel 10.39->9.99MB; Linux switched to tar.xz 10.16->7.32MB; clean member roots (LedgerPOS/, LedgerPOS.app/); fixed mac app_root dirname bug (Contents vs .app — bundle was missing its Contents/ level, caught by member-name verification); checksums.txt generated
- Landing page redesigned (scripts/assets/download-page.html): hero with ONE auto-detected primary CTA (25-line progressive-enhancement JS; Windows default fallback; mobile note), alt-platform links, trust strip; the Big-7 value props (minute-to-first-sale, offline-first, M-Pesa STK+receipt-code, KRA monthly VAT, existing hardware, data-stays-local, MIT white-label); slim 3-step strip; relative downloads/ links with download attr; OG/Twitter meta; kept Ledger design language
- scripts/build_site.py: fast-path site assembly (no Go/npm) — renders template, regenerates checksums, href link check, drag-and-drop zip with installers STORED (not double-compressed); 36.5MB / 14 files / ~37MB deployed
- scripts/package_desktop.py updated to match (pkgutil zopfli zips, tar.xz, size guard, embed-by-default, LEDGERPOS_LIGHT_SITE=1 opt-out); scripts/create_release.py rewritten in-repo; GitHub release v1.0.0 re-synced (4 smaller packages + checksums.txt asset, legacy tar.gz deleted, notes patched)
- Verification: local http.server — every href 200 with byte-exact sizes; browser E2E — title/Big-7 count 7/CTA auto-detect (Linux host -> Linux tar.xz + alt hidden)/emerald brand button; demo band clickthrough -> /demo/ loads app w/ Demo-mode pill; desktop E2E 17/17 on the repacked tar.xz artifact
- VLM critique round on the landing page: applied real findings (note text contrast #94A3B8->#475569, dropped 'SQLite WAL' jargon from card 02, softened SmartScreen/Gatekeeper phrasing); rejected false positive ('Download for Linux' hero is correct per-host auto-detection)
- README updated: downloads served straight from Netlify, build_site.py/repack.py commands, checksums, LEDGERPOS_LIGHT_SITE escape hatch, tar.xz
- Committed + pushed: ba66960 (self-hosted downloads + Big-7 + sub-10MB packages) then polish commit

Stage Summary:
- Netlify page now serves installers DIRECTLY: drag download/ledgerpos-netlify-site.zip (36.5MB) onto Netlify and the download buttons hand users the file from the same site — no GitHub redirect
- All packages <10MB (Netlify drag-drop safe), binaries byte-identical to tested release, checksums published (site + release)
- Landing page conversion-hardened: one-CTA hero w/ OS auto-detect, Big-7 props, VLM-reviewed
- Repo fully pushed (source + scripts + DEVLOG); release v1.0.0 synced as mirror
