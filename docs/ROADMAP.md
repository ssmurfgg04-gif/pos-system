# LedgerPOS Roadmap — approved 2026-09-14

Direction: shop features first, then distribution. Hygiene throughout.
Out of scope for now: paid code signing, macOS/Linux native windows.

## Phase 1 — Shop features

### 1. Customer tabs & credit
Customer records, running tabs, pay-against-balance, credit limits with
till warnings, simple loyalty points. New tables; touches checkout plus a
new Customers screen.

### 2. Suppliers & stock-in
Supplier records, purchase orders (receive → stock up, cost averaging),
stock-take counts with a variance report.

### 3. Checkout polish
Cash drawer kick, brand logo on printed receipts, quicker tender flow.

## Phase 2 — Distribution

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
