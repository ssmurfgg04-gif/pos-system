# LedgerPOS Roadmap, approved 2026-09-14

Direction: shop features first, then distribution. Hygiene throughout.
Out of scope for now: paid code signing, macOS/Linux native windows.

## Phase 1, Shop features

### 1. Customer tabs & credit
Customer records, running tabs, pay-against-balance, credit limits with
till warnings, simple loyalty points. New tables; touches checkout plus a
new Customers screen.

### 2. Suppliers & stock-in
Supplier records, purchase orders (receive → stock up, cost averaging),
stock-take counts with a variance report.

### 3. Checkout polish
Cash drawer kick, brand logo on printed receipts, quicker tender flow.

## Phase 2, Distribution

### 4. Guided onboarding
First-run wizard: store name → logo → receipt → printer test →
users/PINs. Replaces Settings-hunting for new shops.

### 5. Auto-updates
Version check against GitHub Releases on boot, one-click download +
install. Ends manual re-downloads.

## Throughout every phase
- Error boundary in the frontend (no more white-screen crashes).
- Tests for every new backend package + new UI flows.
- README/CHANGELOG updated with each phase.

## Later / unpicked
- Code signing certificate (SmartScreen/Gatekeeper warnings stay
  documented until then).
- macOS/Linux native windows (browser fallback remains there).

## Shop-floor review findings (live run, 2026-10-22)

A full cashier's-day journey was run against the actual shipped binary
(38 behaviours exercised: login, PIN pad, shifts, split tender, credit
tabs, loyalty, stocktake, KRA CSV, offline replay). Verdict: the feature
depth is real — what follows is the customer-facing gap list, in the
review's own priority order.

- **[DONE] Stale shipped artifacts** (was the review's Critical): the
  committed binary predates HEAD — no quotes routes, no order-event
  timeline, no X/Z shift report in what customers downloaded. Fixed by
  the CI release train (`.github/workflows/release.yml`): installers
  build from the tagged commit, history was purged, Releases are the
  only binary home.
- **[DONE] Stocktake trust event**: an unreproducible 500 on count, and
  a found silent-no-op — products created after a count opened returned
  200 without recording (data loss with a green light). Now rejected
  loudly (422) with a regression suite
  (`internal/handlers/stocktake_regression_test.go`).
- **High — returns/refunds/exchanges**: void + manual adjustment only.
  Real shops live on returns ("brought it back Tuesday"); a till that
  cannot do them gets bypassed by a notebook. Needs a returns document
  type (reuse the order tables with a direction flag), stock restore,
  KRA credit-note line.
- **High — customer-facing receipt delivery**: print + browser HTML
  only. In Kenya a WhatsApp/PDF receipt is table stakes. The notify
  endpoint exists but serves design-job readiness, not receipts.
  Cheaper-than-WABA alternatives to evaluate when this lands.
- **High — checkout rail responsiveness**: `CheckoutRail.tsx` is a
  1,740-line component with zero responsive breakpoints — the money
  surface is desktop-locked. Split the god-file and add the tablet/
  phone layouts together, not separately.
- **High — eTIMS is a text box**: the buyer-KRA-PIN field makes a tax
  invoice on paper only; no KRA SDC/VSCU integration yet. Compliance
  debt, not a bug — plan the integration before KRA enforcement bites.
- **Medium — a11y is uneven**: Settings 35 aria hooks, Signup 0, Shifts
  1, Suppliers 2. The PIN pad — used by semi-literate staff on touch
  screens — deserves the most accessibility, not mid-tier.
- **Medium — offline queue is cash-only**: held sales, credit tabs and
  manual M-Pesa do not survive a network drop. Document the scope in
  the offline banner or extend the queue; silent limits become "your
  app ate my sale."
- **Medium — cashier keystrokes**: barcode-wedge works (unfocused scan
  fires into the cart), but there is no hotkey map (F2 hold, F4
  tender…). POS UX is keystrokes-per-sale.
- **Nice-to-have**: dark/night theme (bet-shops and bars run at night);
  surface loyalty/gift-cards/design-board in onboarding — they are
  differentiators buried in nav; keep the demo mode public, it sells.
- Aesthetics verdict from the review, kept as a design constraint: warm
  gray + navy + cobalt with contrast ratios documented in CSS comments,
  Inter with offline fallback, `tabular-nums` money columns, 13.5px
  dense type, real print stylesheet. It reads like a till — keep it
  that way; fix structure (god-file, responsiveness, a11y), not the
  look.
