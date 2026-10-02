# LedgerPOS — ERP/CRM Deep Study & Feature-Gap Analysis

**Date:** 2026-10-02 · **Repo state audited:** `main` @ `755d7b8` (v1.1.10) · **Live:** awesomeposs.netlify.app
**Method:** Odoo cloned from source (`github.com/odoo/odoo`, sparse checkout of `point_of_sale`, `pos_sale`, `pos_restaurant`, `pos_hr`, `crm`, `sale`, `sale_management`, `contacts`, `project`, `stock`, `hr`, `hr_attendance`, `mail`, `account`) and the models read directly; LedgerPOS audited file-by-file at the commit above; official documentation fetched for Loyverse (`help.loyverse.com`: offline mode, access rights, open-ticket sync), Shopify POS (`shopify.com/pos`), Zoho One (`zoho.com/one`), Square support. Lightspeed and the Kenyan-POS landscape are drawn from product knowledge and are explicitly marked **[to verify hands-on]**.

This document is the *findings before architecture changes* required by the development brief (§11). Nothing here has been implemented yet.

---

## 1. Executive summary

LedgerPOS today is a **solid, correctly-architected POS core with a missing business layer**. The payments stack (M-Pesa STK via Paystack) is more defensive than what many competitors ship — phone-validated, webhook-verified, duplicate-guarded, with late-money recovery. Backend RBAC is real middleware enforcement, not button-hiding. The gap is everything *around* the till: orders live and die as `PENDING/PAID/VOIDED`, the design board is a parallel universe disconnected from sales, there is no customer lifecycle beyond a money ledger, no notification capability of any kind, and cloud sync is an event relay rather than a shared source of truth.

Odoo's lesson — read from its actual source — is not its feature list but its **connective tissue**: one shared customer record (`res.partner`) referenced by every app; stage pipelines stored as *data rows*, not enums; a single confirm hook from which all downstream actions hang; a chatter/activity engine that turns every record into an auditable conversation; and record rules that make "a designer cannot see sales" a database property rather than a UI choice. Zoho's lesson is the same thesis productized: the differentiator is the hand-offs between CRM → sale → job → finance, not any single module.

**The strategic recommendation** mirrors what the owner already articulated: LedgerPOS should not become Odoo. It should borrow Odoo's *patterns* (§3), implement the brief's unified job lifecycle (§5, Phase 3), and keep everyday checkout as fast as it is today. Its differentiator — walk-in sale, creative production, and WhatsApp customer contact in one flow — is something none of the studied products does natively.

---

## 2. What LedgerPOS is today (verified inventory)

### 2.1 Architecture
- **Go backend** (Gin + SQLite per shop; Postgres single-shop mode), **React/TS frontend** (Vite), desktop tills packaged with installers + Supabase OTA manifest, Netlify-hosted web build with an in-browser demo backend (`frontend/src/demo/`).
- **Multi-store:** one SQLite file per shop on a device (`internal/tenants/`), cloud registry `sync_stores` partitioned by `team_code`; device identity (`dev-<host>-<rand>` + secret, only SHA-256 on the wire) approved/assigned per store via `sync_register` / `sync_assign_device`.
- **Sync:** append-only event log. Mutating devices write to a local `sync_outbox`; a 20 s loop pushes batches (200) to Supabase `sync_events` and pulls others' events, applying each exactly once via cursor + `(team_code, client_uuid)` uniqueness. Local SQLite is the *working truth*; the cloud is a conduit.

### 2.2 What already satisfies the brief (do not re-do)
| Brief area | Status |
|---|---|
| PIN is exactly 4 digits | ✅ everywhere (`binding:"len=4"` in auth/users; UIs slice to 4). The 6-digit option does not exist. |
| Phone mandatory for STK | ✅ UI gate + API rejection (`payment has no phone number…`), E.164 `+254…` on the wire |
| Long confirmation window | ✅ Paystack 15-min window, 2 s polling, `sent → waiting → slow → paid/failed` stage machine; order stays PENDING on timeout |
| Webhook / polling / retry / duplicate protection | ✅ HMAC-SHA512 webhook, recovered references, exactly-once `completePayment`, 24 h late-money recheck + failed-ref sweep, receipt uniqueness, paid-after-void refused |
| Backend permission enforcement | ✅ `RequirePermission` middleware on ~70 routes, principal reloaded from DB per request, money permissions re-checked in the service layer |
| Store pairing not by Wi-Fi | ✅ cloud join links (role + permissions, 7-day, single-use), device approval/revocation, secrets never typed into tills |
| Deactivation preserves history | ✅ `DELETE /users/:id` is soft-delete (`is_active=0`), sessions revoked, orders keep cashier FK |

### 2.3 The real gaps (evidence-based)
1. **Order lifecycle is 3 states.** No assigned-staff, no deadline, no in-progress/ready/collected, no per-order activity history (only a global `audit_log`), and `design_jobs` has **no `order_id`** — a paid custom-print order has no link to the job that fulfills it. *(models.go, migrations.go:147-158)*
2. **Sync is a relay, not a source of truth.** No materialized cloud entities, no reconciliation/backfill endpoint, no conflict UI (LWW is silent), failed applies are *permanently skipped* (cursor advances anyway), `applyLedger` has no idempotency key (cursor loss ⇒ balance corruption risk), outbox never prunes, sync status is admin-only. *(teamsync.go:282-296, 1196-1211)*
3. **No WhatsApp / notification layer at all** — zero messaging code. *(grep across repo)*
4. **Demo users are seeded into every production DB** (`admin/admin123`, `cashier`, `designer` with PINs `1234/2222/3333`) and the desktop first-run banner prints them. The web "demo chips" are demo-mode-only (fine). *(seed.go:121-127, Login.tsx:139-151)*
5. **No forgot-password.** No email/OTP/reset-token flow exists anywhere; lockout recovery is "have the owner mint a join link." *(Pin.tsx:174-181)*
6. **Roles are close but not the owner's vocabulary.** 24 permission keys + dynamic roles exist, but there are no seeded **Owner / Front-desk-Manager / Branding / Cyber** roles, no `orders.assign`/`orders.notify` keys, and `users.manage` holders can mint Admins (flat privilege model). *(permissions.go:69-74, users.go)*
7. **Customers are a money ledger, not a CRM.** No email/notes/address, no enquiry tracking, no per-customer order-history view, identity keyed on phone with trivially creatable duplicates. *(models.go:241-252)*
8. **One invite-token bug:** `sync_join_team` stores the *plaintext* token in `token_hash` (docs claim hashed). *(cloud_schema_v112.sql:109-112)*
9. **Daraja vs Paystack inconsistency:** 3-min vs 15-min STK timeout on the same button depending on route config. *(sweeper.go:24 vs payments_paystack.go:454)*

---

## 3. Odoo patterns worth borrowing (source-verified)

Each pattern: what Odoo does (with module/model evidence) → what LedgerPOS should adopt.

### 3.1 One customer record for the whole business — `res.partner`
`contacts/models/res_partner.py`; every app `_inherit`s it: `crm` adds opportunity counts, `sale` adds `sale_order_ids` + customer warnings surfaced *into the POS* (`pos_sale/models/res_partner.py`), `point_of_sale` uses it as the POS customer. Dedup is a generic merge wizard that re-points every Many2one reference, sums counters, and posts an audit note. CRM normalizes emails (`email_normalized`, trigram index) for duplicate detection.
**LedgerPOS:** keep the existing `customers` table as the single entity; add `email`, `notes`; key duplicates on normalized phone *and* email; make orders, design jobs, ledger rows, and future enquiries all FK to it; implement a merge action that re-points FKs and logs to the audit trail. No second customer database, ever — that is the brief's §9 made concrete.

### 3.2 Pipelines as data, not enums — `crm.stage`
`crm/models/crm_stage.py`: stages are rows with `sequence`, `is_won` (boolean, not a state), `fold`, `requirements`. "Won" and "lost" are *derived semantics* (`action_set_won` → move to first `is_won` stage, probability 100; lost → `lost_reason_id` + archive). Follow-ups are `mail.activity` rows with deadlines; overdue/today/planned is computed state.
**LedgerPOS:** the job pipeline (and any future enquiry pipeline) should be a `job_stages` table (sequence, is_won/is_final, fold) so the owner can rename/reorder stages without code. Even a minimal version of this pays off the moment the cyber/branding flow wants "Sent to designer" as a distinct column.

### 3.3 One confirm hook, downstream hangs off it — `sale.order`
`sale/models/sale_order.py`: 4-state machine (draft/sent/sale/cancel); fulfilment and invoice status are **computed roll-ups over lines**, never hand-maintained booleans; `action_confirm()` is a single hook that downstream modules override to spawn deliveries/tasks/invoices. POS orders landing on a sale order auto-confirm it (`pos_sale/models/pos_order.py:64-118`).
**LedgerPOS:** `Checkout` completion should be the single spawn point: on `PENDING→PAID`, auto-create the production job(s) from the order's line items (product flags decide *whether* a line spawns work — Odoo's `qty_delivered_method` pluggability is the model), stamp deadline defaults, and emit the sync event. Everything downstream hangs off one hook instead of being re-derived in three places.

### 3.4 Jobs are tasks: assignees, deadlines, sub-tasks, rotting — `project.task`
`project/models/project_task.py`: M2M assignees (`user_ids`), `date_deadline`, sub-tasks with aggregated counts, dependency graph with cycle checks, per-user personal stages ("My Tasks"), and `rotting_threshold_days` on stages to flag stale jobs. Stages are shared across boards (M2M `project_ids`).
**LedgerPOS:** the design board's kanban is already close; what it needs is exactly Odoo's spine — assignee(s), deadline, order link, "My Jobs" view for designers, and stale-job highlighting. Sub-tasks can wait; multi-step branding jobs will want them later.

### 3.5 The POS triad — session, uuid idempotency, one-by-one replay — `point_of_sale`
`point_of_sale/models/pos_order.py`: client-generated `uuid` is the idempotency key; `sync_from_ui` upserts draft orders (duplicate CREATEs replayed as UPDATEs), *recomputes amounts server-side* ("we don't trust the client"), ignores already-paid orders, and the frontend pushes queued orders **one by one** so partial failure survives. `pos.session` (opening_control → opened → closing_control → closed) is the cash-drawer accounting boundary — journal entries post in batch at close, and every API refuses closed sessions. `pos_hr` stamps `employee_id` on orders and aggregates payments per employee at close.
**LedgerPOS:** (a) the checkout queue already keys on `clientUuid` — keep that forever; (b) server-side amount recompute on replay is worth copying for the offline queue path; (c) a **POS session** per till (open float → sell → close count with `closing_difference`) is the single most valuable missing accounting feature and maps directly onto the existing Shifts page; (d) per-cashier (per-employee) X/Z reports at shift close.

### 3.6 Security as record rules, groups with implications — `ir.access.csv`
`point_of_sale/security/`: `group_pos_manager` *implies* `group_pos_user`; salesman vs manager is a **record-rule domain** on the same model (`user_id = user.id` vs empty domain = see all); menus declare `groups=`; multi-company is `[('company_id','in',company_ids)]` on every table with `_check_company_auto` as belt-and-braces.
**LedgerPOS:** the permission catalog + middleware is already the right shape. Add: an **owner-only** tier (implied-by-nobody, sole holder of `roles.manage` on system roles, invite minting, deletion rights), record-level scoping in the service layer where roles demand it (designers see assigned jobs, not the order book's money columns), and per-store role assignments when multi-store roles arrive. Enforce in SQL/service, mirror in UI — exactly what the brief demands.

### 3.7 Chatter + activities on any record — `mail.thread`, `mail.activity`
`mail/models/mail_thread.py`: `message_post()` gives any model an append-only, immutable-once-posted conversation; fields with `tracking=True` produce immutable "tracking" messages — that is the per-record audit trail. `mail.activity` adds scheduled actions with `date_deadline` and computed overdue/today/planned; completing one posts its feedback to the chatter and archives it. Crucially, **stage rows carry mail templates** (`project.task.type.mail_template_id`) — "task enters Ready ⇒ customer gets notified" is *data configuration*, not code; and computed field changes can auto-create activities (sale's upsell todo).
**LedgerPOS:** add `order_events` (append-only: actor, action, detail, created_at) written by every status change, payment event, note, and notification attempt — this is both the per-order history UI and the audit evidence. Add `activities` (subject_type/subject_id/type/due_date/assignee/state) for follow-ups. Make "Ready ⇒ WhatsApp draft opens" a stage-attached template, so SMS/email variants are configuration later.

### 3.8 Demand rows, not inline mutation — `stock.move` / `stock.rule`
`stock/models/stock_move.py`, `stock_rule.py`: confirming a sale creates *demand*; rules/schedulers materialize fulfilment; every generated document carries `origin` back to its driver; POS posts its inventory impact in batch at session close.
**LedgerPOS:** stock deltas are already event-sourced (`EmitStockDelta`) — good. The borrow is the *origin* discipline: every future derived record (jobs, purchase suggestions, notifications) must carry `order_id`/`source` so history stays reconstructable, and batch stock reconciliation at session close rather than more inline writes.

### 3.9 Multi-store isolation
Odoo: `company_id` on every business table + always-on global record rules + company *derived* from the register config, never client input.
**LedgerPOS:** the per-shop SQLite + `team_code` partitioning already achieves this for tills. When cloud entities materialize (Phase 5), every cloud table gets `team_code` + Postgres RLS with `team_code = current_setting('app.team')`, derived server-side from the authenticated device — never from the payload.

---

## 4. Zoho & competitor read-out

### 4.1 Zoho One (official site, verified)
The pitch is explicitly *"The Operating System for Business"*: 45+ apps across Sales (CRM, Bookings, SalesIQ), Finance (Books, Invoice, Expense, Billing, Payroll), Operations (Projects, Sprints, **Inventory**), Service (Desk, Assist), HR (People, Recruit), Collaboration (Cliq, Mail). The value thesis is interoperability: the customer lifecycle (lead → deal → invoice → project → support ticket) is one connected spine; departments share records rather than syncing copies.
**Transfer to LedgerPOS:** the owner's business runs CRM-shaped *enquiries* (branding jobs start as questions, not sales), project-shaped *production*, and Desk-shaped *customer follow-ups*. LedgerPOS's `customers` + new `enquiries` + `order_events` + `activities` tables are the minimal spine that reproduces Zoho's connectedness without 45 apps. Zoho also validates a hard line from the brief: **keep the till simple** — in Zoho, the operational simplicity lives in each app's dedicated UI, with the integration invisible underneath.

### 4.2 Loyverse POS (official help center, verified)
- **Roles:** default Owner (fixed, all rights — cannot be edited), Administrator, Manager, Cashier; custom roles via checkbox matrix, split into **POS rights** vs **Back-office rights**. POS rights include granular gems LedgerPOS should steal verbatim: *view all receipts* (else only 5 most recent), *accept payments*, *perform returns*, *manage all open tickets*, *open cash drawer without sale*, *view cost of items*, *view shift report*.
- **Escalation by PIN:** an employee tapping a forbidden action gets a PIN panel; entering a supervisor's PIN grants **one-time** access. This is the exact "front desk completes eligible work or delegates" mechanic the brief wants, and it is faster than role-switching.
- **Offline rules:** sales + shifts work offline; receipts marked *Unsynced* and auto-synced on reconnect (manual sync button too); **logout is blocked while unsynced receipts exist**; refunds and customer create/edit disabled offline; card terminals offline-blocked; inventory levels hidden offline (no lying with stale numbers).
- **Open-ticket sync:** tickets sync in real time across devices in one store; any device can edit/close another's ticket.
**Transfer:** escalation-by-PIN (supervisor override without switching users); logout/lock guard while the offline queue has unsynced sales; hide stock levels when the count is stale; open-ticket (held-sale) sync across tills.

### 4.3 Shopify POS (official site, verified)
Unified back office (products, orders, customers, staff in one place), staff permissions, **multi-location inventory** as first-class data, 99.9% uptime target, omnichannel (buy-online-pickup-in-store, in-store-ship-to-customer) — the pattern is: the till is a thin client over one authoritative back office; every action (checkout, customer capture) writes through immediately.
**Transfer:** the target-state sync model (§3.5 cloud authority) is "Shopify-shaped": the till works offline but treats the cloud as home. Also: customer capture *during* checkout as a one-tap flow (LedgerPOS already has the fields; make attaching a phone number to a walk-in the default, not the exception — it feeds M-Pesa AND the CRM).

### 4.4 Square POS **[to verify hands-on — page fetch landed on a generic help article]**
From product knowledge: Team members with permission sets per location (owner/admin/manager/employee), granular toggles (take payments, issue refunds, view reports, manage inventory, timecards), permission *to request* manager approval at the register, offline card payments with explicit risk caps and a liability disclaimer, and checkout speed as the design north star.
**Transfer:** the offline-payments *risk cap* idea (allow offline card/M-Pesa-manual only under a configurable per-sale ceiling) is a genuinely good guardrail for LedgerPOS's offline manual payments.

### 4.5 Lightspeed Retail **[to verify hands-on — direct fetches bot-blocked]**
From product knowledge: advanced retail inventory (variants, serials, work orders), purchasing/receiving with supplier workflows, multi-location transfers, deep reporting. Relevant mainly as a **roadmap reference for purchasing and stock-taking maturity**; nothing in it is needed before the unified job lifecycle exists.

### 4.6 Kenyan POS landscape **[to verify hands-on — search quota exhausted; candidates below need URLs/demos confirmed]**
Candidates commonly cited in the Kenyan market: **Solutech POS** (Kenyan retail-focused SaaS), **Loyverse** (free tier, widely resold/integrated in Kenya, typically paired with Paystack/Flutterwave-style M-Pesa gateways by third parties), **Odoo** via Kenyan implementation partners, generic Android POS resellers bundled with Daraja integrations. The durable, product-agnostic requirements for this market are:
- **M-Pesa STK (Daraja) with graceful *USSD/manual* fallback** — customers on feature phones or with STK failures must still be chargeable (LedgerPOS's manual receipt path already covers this; keep it).
- **KRA eTIMS awareness** — invoice formatting/receipt fields should anticipate eTIMS requirements for VAT-registered clients (order-level fields now, integration later).
- **Offline-first** — connectivity is intermittent outside malls; Loyverse's offline discipline is the market benchmark.
- **KES cash rounding and multi-tenancy for cyber-café + branding + retail** mixed businesses — exactly LedgerPOS's niche; no mainstream Kenyan product handles the creative-production workflow at all.

### 4.7 Feature matrix

| Capability | LedgerPOS today | Shopify POS | Loyverse | Square | Lightspeed | Odoo POS/ERP | Zoho One |
|---|---|---|---|---|---|---|---|
| Fast checkout (≤3 taps to pay) | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ❌ (not till-first) |
| Offline sales + reconnect replay | ✅ cash-only queue | ✅ | ✅ (refunds/customer blocked) | ✅ (risk caps) | ⚠️ limited | ✅ (queued orders) | ❌ |
| Sync = authoritative shared source | ❌ event relay | ✅ | ✅ back office | ✅ | ✅ | ✅ | ✅ |
| Sync status visible to staff | ⚠️ admin-only | ✅ | ✅ unsynced markers | ✅ | ✅ | ✅ | ✅ |
| Multi-location inventory | ⚠️ per-device shops | ✅ | ✅ (Advanced Inv.) | ✅ | ✅ | ✅ | ✅ |
| Granular staff permissions (backend) | ✅ 24 keys | ✅ | ✅ + PIN escalation | ✅ | ✅ | ✅ (groups+rules) | ✅ |
| Owner/Manager/Staff role presets fitting this business | ⚠️ Admin/Cashier/Designer only | ⚠️ generic | ✅ Owner/Adm/Mgr/Cashier | ✅ | ✅ | ✅ | ✅ |
| Customer CRM (history, notes, follow-ups) | ❌ money ledger only | ⚠️ retail CRM | ⚠️ basic + loyalty | ✅ | ✅ | ✅ full | ✅ full |
| Job/work delegation board | ⚠️ design board, disconnected | ❌ | ❌ (KDS for food) | ⚠️ appointments only | ⚠️ work orders | ✅ project tasks | ✅ projects |
| Production job spawned from sale | ❌ | ❌ | ❌ | ❌ | ⚠️ work orders | ✅ (sale→task) | ✅ |
| Customer notification on ready (WhatsApp) | ❌ | ⚠️ email marketing | ⚠️ email receipts | ⚠️ marketing | ⚠️ email | ✅ templated | ✅ (Cliq/WhatsApp via Tierra) |
| M-Pesa STK with verification + late recovery | ✅ strong | ❌ | ❌ natively | ❌ | ❌ | ⚠️ via providers | ⚠️ via integrations |
| Cash session control (open/close, variance) | ⚠️ shifts only | ✅ | ✅ shifts | ✅ | ✅ | ✅ pos.session | ⚠️ via Books |
| Refunds/reconciliation discipline | ✅ guarded, discrepancy flags | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Purchasing/receiving | ⚠️ suppliers + stock-in | ✅ | ✅ (advanced) | ✅ | ✅ strong | ✅ | ✅ |
| Held/open tickets synced across devices | ⚠️ local only | ✅ | ✅ | ✅ | ✅ | ✅ | n/a |

Legend: ✅ native · ⚠️ partial/caveated · ❌ absent.

**Reading the matrix honestly:** LedgerPOS's payment stack beats every global product *for Kenya*; its sync model, CRM, and job-spawning are the gaps that matter. Nothing on this table suggests rebuilding checkout — it suggests building the connective layer.

---

## 5. Gap analysis mapped to the brief → phased roadmap

Priority follows the brief: **sync & auth first**, then roles & workflows, then WhatsApp & refinements. Each phase is independently shippable and testable.

### Phase 0 — Sync & data-integrity hardening (P0, brief §2)
1. **Ledger apply idempotency:** give every `ledger`/`stock` event a deterministic `client_uuid` + apply-once table (or `INSERT OR IGNORE` on a unique key) so cursor loss cannot double-apply money. *(Closes the one known corruption path.)*
2. **Failed-apply dead-letter:** stop skipping failed applies silently — park them, surface them in sync status, retry with backoff. Cursor advances only on success or dead-letter.
3. **Outbox hygiene:** prune pushed rows after N days; exponential backoff + jitter on the 20 s loop.
4. **Sync status for all staff:** a per-screen sync chip (pending / synced / failed counts) — staff, not just admins, must see the truth (brief: "Show pending, synchronized and failed changes clearly").
5. **Token fix:** SHA-256 the invite token in `token_hash` (matches docs, kills plaintext-at-rest).
6. **Conflict hygiene:** keep LWW for catalog but log every LWW-overwrite to `audit_log` (who/what/old/new) so silent losses become visible events.
7. **Unify STK timeout** at 15 min for both Daraja and Paystack routes (config value, not constant).

### Phase 1 — Auth completion (P0, brief §7–8)
1. **Purge demo accounts:** stop seeding `admin/cashier/designer` into production DBs (seed them only under an explicit demo flag); remove the first-run banner advertising creds. Keep the shift-handoff PIN switch — it is a legitimate feature and the owner was told so.
2. **Owner forgot-password:** email-OTP flow via Supabase Auth for password accounts (owner supplies verified email at setup), plus a one-time printed **recovery code** generated at owner setup as the controlled fallback when email is unavailable. No password exposure, no auth bypass.
3. **Account deletion with history:** formalize delete = deactivate + anonymize PII (display name → "Former staff", PIN/password cleared, sessions revoked, role memberships dropped) while `orders.cashier_id` keeps its FK and historical attribution. Never orphan a sale.

### Phase 2 — Roles for this business (P0→P1, brief §3)
1. Seed the owner's vocabulary: **Owner** (new implied-nobody tier), **Front desk / Manager**, **Branding / Cyber**, **Designer** — mapped onto the existing 24-key catalog plus new keys: `orders.assign`, `orders.notify`, `orders.perform`, `orders.discount.request` (request/escalate instead of grant).
2. **Escalation-by-PIN** (Loyverse pattern): forbidden action → supervisor PIN modal → one-time grant, audited.
3. Owner-only restrictions on system roles, invites, deletions (fixes the flat `users.manage`-mints-Admin model).
4. Designer experience: assigned-jobs view only; no money columns, no sales UI — enforced in queries, not just routes.

### Phase 3 — Unified job lifecycle (P1, brief §4) — *the biggest build*
1. Extend orders: `PENDING → PAID → IN_PROGRESS → READY → COLLECTED` (+ `VOIDED` from any pre-collected state); add `assigned_to`, `deadline`, `priority`; per-order `order_events` timeline (chatter pattern §3.7).
2. Spawn jobs from paid orders (single confirm-hook, §3.3): product-level flag "spawns work" creates a job linked by `order_id` — **retire the orderless design board** by migrating `design_jobs` rows onto orders.
3. Role flow: front desk/branding creates order → takes payment → assigns (or self-assigns) → designer works (`IN_PROGRESS`) → marks `READY` → front desk verifies/collects → `COLLECTED`.
4. Designer UI: "My jobs" board with requirements, deadline, attachments, status controls only.
5. Held sales sync across tills (Loyverse open-ticket pattern) as part of the same event vocabulary.

### Phase 4 — WhatsApp customer contact (P1, brief §5)
1. On `READY`: prominent **"Contact customer on WhatsApp"** — `wa.me/2547…?text=` deep link with order ref + collection/delivery instructions from a stage-attached template (Odoo §3.7).
2. Staff review-and-send step; the notification **attempt** (who/when/template/outcome) is recorded to `order_events`; delivered-ness is *never* claimed — the brief explicitly forbids it.
3. Design keeps a seam for the WhatsApp Business API (template messages, webhooks) — store the phone E.164 now (already done for Paystack).

### Phase 5 — CRM & cloud authority (P2, brief §2+§9)
1. Customer enrichment: email, notes, tags; per-customer order + ledger history view; normalized-phone/email dedup + merge (res.partner pattern).
2. **Enquiries** entity (Zoho/CRM-lite): enquiry → quote (draft/sent states borrowed from sale.order) → order; follow-up activities with overdue flags.
3. **Cloud authority v1:** materialize `cloud_orders`/`cloud_customers` per `team_code` with Postgres RLS; add a reconciliation endpoint (uuid-keyed upsert, Odoo `sync_from_ui` pattern) + full-state backfill on cursor loss; owner-portal reporting reads tables, not JSON-over-`sync_events`.

### Phase 6 — ERP depth (P3, explicit non-defaults)
POS sessions with cash control (§3.5), purchasing/receiving, eTIMS-ready invoice fields, multi-store consolidated reporting. **Only after** Phases 0–5 are demonstrably stable on real devices.

### Explicit non-goals (do not copy)
- Odoo's module sprawl, quotations-to-invoice accounting chain, or its JS framework patterns — LedgerPOS's Go+React core is right.
- Zoho's per-app UI count — one app, one database, five roles.
- Competitor features whose only justification is "they have it" (brief §11). Every roadmap item above traces to a verified gap or a verified pattern.

---

## 6. Test & evidence plan (per completion requirement)
- **Sync:** two tills + one web session; kill network mid-sale, restart app, concurrent edits of the same product/customer; verify pending/failed chips, dead-letter surfacing, ledger balance invariants after forced cursor loss.
- **Roles:** per-role matrix script hitting every route expecting 200/403; escalation-PIN grants audited; designer sees assigned jobs and no money fields in payloads.
- **Lifecycle:** full job walkthrough with real accounts on real state — enquiry→order→M-Pesa STK (live sandbox)→assign→in-progress→ready→WhatsApp draft→collect; verify order_events timeline records each hop including notification attempt.
- **Auth:** demo accounts absent on fresh production DB; forgot-password via email OTP; recovery-code path; deletion preserves orders and reports.
- **Payments:** delayed webhook (payment succeeds after 15-min UI timeout), duplicate webhook replay, paid-after-void, late-money sweep recovery.
- **Deployment:** Go + vitest suites, production build, push to `main` (Netlify autodeploy); desktop changes follow the established version-bump → installers → GitHub release → Supabase OTA manifest flow.

---

## 7. Source evidence index
- **LedgerPOS audit:** `internal/services/teamsync.go` (event relay), `synccloud.go` (identity), `teamjoin.go` (invites), `internal/models/permissions.go` (24 keys), `internal/auth/middleware.go` (RequirePermission), `internal/services/orders.go` (allocator, completePayment), `payments_paystack.go` (15-min window, late recovery), `sweeper.go` (3-min Daraja), `internal/database/seed.go:121-127` (demo users), `frontend/src/pages/Login.tsx` (demo banner), `Pin.tsx` (lockout note), `db/cloud_schema_v112.sql:109-112` (token bug).
- **Odoo source:** `contacts/models/res_partner.py`; `crm/models/crm_lead.py`, `crm_stage.py`; `sale/models/sale_order.py` (states, `_compute_invoice_status`, `action_confirm`); `project/models/project_task.py`, `project_task_type.py`; `point_of_sale/models/pos_session.py`, `pos_order.py` (`sync_from_ui`, uuid), `pos_hr/models/*`; `point_of_sale/security/ir.access.csv` + `point_of_sale_security.xml`; `mail/models/mail_thread.py`, `mail_activity.py`; `stock/models/stock_move.py`, `stock_rule.py`.
- **Fetched docs:** `help.loyverse.com/help/offline-work-of-pos`, `/help/how-manage-access-rights-employees`, `/help/tickets-synchronizations`; `shopify.com/pos`; `zoho.com/one/`; `squareup.com` support (partial).
- **Research artifacts on disk:** Odoo checkout at `/home/z/my-project/research/odoo/`, page digests at `/home/z/my-project/research/digests/`.
