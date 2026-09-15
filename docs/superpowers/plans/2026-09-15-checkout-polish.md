# Checkout Polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The till feels instant and the paper looks branded — cash drawer kicks automatically on cash sales, the shop logo prints on every receipt, and exact-cash tender is one tap.

**Architecture:** All three ride the existing printer pipeline (`printer.Worker` → `Render` → escpos bytes → TCP/USB target): drawer kick is a 5-byte ESC/POS command appended after cash completions; the logo reuses the vendored `PrintImage(image.Image)` rasterizer fed by the already-uploadable `brand-logo.png`; one-tap exact tender is frontend-only.

**Tech Stack:** Go (`internal/escpos`, `internal/printer`), React (`Pos.tsx` charge modal).

**Spec:** `docs/ROADMAP.md` §1.3: "Cash drawer kick, brand logo on printed receipts, quicker tender flow."

## Global Constraints

- Repo Go files are mixed tabs/spaces PER FILE — match each file's own style, never run gofmt -w.
- `internal/escpos` is vendored MIT code with CGO-free constraint — new code must stay CGO-free (stdlib `image/...` only).
- Printer target parsing lives in `internal/printer/target.go` — reuse it, do not reimplement addressing.

---

### Task 1: ESC/POS drawer kick + worker support

**Files:**
- Modify: `internal/escpos/main.go` (tabs): add `KickDrawer`.
- Modify: `internal/printer/worker.go`: add `Kick()` that opens the configured target and writes the kick sequence (no job row — fire-and-forget with error log, like `Enqueue` skips when unconfigured).
- Modify: `internal/handlers/system.go` or `payments.go`: `POST /api/v1/printer/kick` (perm `printer.test`) → `w.Kick()`.
- Modify: `internal/router/router.go`: register the route next to `test-print`.
- Test: `internal/escpos` test file (barcodes_test.go exists — mirror its style) asserting exact bytes.

**Interfaces:**
- Consumes: `target.go` dial helper (read it for the exact open function name/signature).
- Produces: `(e *Escpos) KickDrawer(pin, onTime, offTime uint8)`, `(w *Worker) Kick() error`.

- [ ] **Step 1: KickDrawer command**

ESC/POS drawer kick is `ESC p m t1 t2` (m = pin 0/1, t = time ×2ms):

```go
// KickDrawer pulses the cash-drawer kick connector: pin 0 or 1,
// on/off times in 2ms units (typical: pin 0, 25, 250).
func (e *Escpos) KickDrawer(pin, onTime, offTime uint8) (int, error) {
        if pin > 1 {
                pin = 0
        }
        return e.WriteRaw([]byte{esc, 'p', pin, onTime, offTime})
}
```

- [ ] **Step 2: Unit test the bytes**

```go
func TestKickDrawerBytes(t *testing.T) {
        var buf bytes.Buffer
        e := New(&buf)
        if _, err := e.KickDrawer(0, 25, 250); err != nil { t.Fatal(err) }
        if err := e.Print(); err != nil { t.Fatal(err) }
        want := []byte{0x1B, 'p', 0, 25, 250}
        if !bytes.Equal(buf.Bytes(), want) { t.Fatalf("got %v want %v", buf.Bytes(), want) }
}
```

Run: `C:\Users\Jackb\tools\go\bin\go.exe test ./internal/escpos/ -count=1` — expect PASS.

- [ ] **Step 3: Worker.Kick + HTTP endpoint + route**

`Kick()` opens the target (same helper `Enqueue`/`TestPrint` uses), writes a kick with pin 0 / 25 / 250, closes. Returns error when no target configured (handler maps to 422 with "no printer configured"). Register `authd.POST("/printer/kick", perm("printer.test"), h.KickDrawer)` in router.go.

- [ ] **Step 4: Compile + commit**

Run `go build ./...` (expect 0), then:

```bash
git add internal/escpos/main.go internal/escpos/*_test.go internal/printer/worker.go internal/handlers/system.go internal/router/router.go
git commit -m "feat: cash drawer kick command, worker support, test endpoint"
```

### Task 2: Kick on every completed cash sale

**Files:**
- Modify: `internal/services/orders.go` (spaces): in `completePayment` success path for cash (find where payment flips to COMPLETED — kick when `method == cash`), call `s.printer.Kick()` best-effort (log, never fail the sale).
- Test: `internal/handlers/handlers_test.go`: cash checkout with printer target unset must still 201/PAID (kick skipped silently). This is already covered by existing cash tests — assert no regression by running them.

**Interfaces:**
- Consumes: `Service.printer` (`*printer.Worker`, already a field — verify name).
- Produces: no signature changes; side effect only.

- [ ] **Step 1: Add the kick call** (3 lines + comment, inside the cash-completion branch after status flip, before return).
- [ ] **Step 2: Run cash tests**: `go test ./internal/handlers/ -run "TestCash|TestCheckout|TestPriceOverride" -count=1` — expect PASS.
- [ ] **Step 3: Commit**

```bash
git add internal/services/orders.go
git commit -m "feat: auto drawer kick on completed cash sales"
```

### Task 3: Logo on printed receipts

**Files:**
- Modify: `internal/printer/render.go`: `ReceiptData` gains `Logo image.Image` (nil = skip); `Render` prints it centered at top via existing `PrintImage` when non-nil.
- Modify: `internal/printer/worker.go`: when building receipt data (`BuildReceiptData` call site in `printJob`/`Enqueue` path), load `<data-dir>/brand-logo.png` (the file the `brand_logo` setting flags — verify exact filename in the logo handler first), decode (stdlib `image/png`, `image/jpeg` imports for side effects), resize to printer width (80mm → 576px, 58mm → 384px from `printer_width` setting; keep aspect; threshold handled by existing `makeGrayscale`), on any error log + proceed without logo (never fail a print).
- Modify: `internal/database/seed.go`: settings add `"receipt_logo": "true"`.
- Modify: settings allowlist if it enumerates keys (check `AllowedKeys` — if present, add `receipt_logo`).
- Test: `internal/printer/printer_test.go`: render with a generated 16×16 test logo asserts output contains the GS v 0 raster header bytes the existing `PrintImage` emits (read `bitimage.go`/`PrintImage` for the exact header: `GS v 0 m ...`).

**Interfaces:**
- Consumes: `PrintImage(image.Image)`, `BuildReceiptData(...)`, `brand-logo.png` on disk.
- Produces: logo-first receipts when enabled + file present.

- [ ] **Step 1: ReceiptData.Logo + Render branch.**
- [ ] **Step 2: Worker logo load (best-effort) + resize helper** (stdlib only: `golang.org/x/image` is NOT vendored — implement nearest-neighbor resize in ~20 lines, no new dependency).
- [ ] **Step 3: Setting key + allowlist.**
- [ ] **Step 4: Test + run** `go test ./internal/printer/ -count=1` — expect PASS.
- [ ] **Step 5: Commit**

```bash
git add internal/printer/render.go internal/printer/worker.go internal/printer/printer_test.go internal/database/seed.go internal/settings/settings.go
git commit -m "feat: brand logo prints on receipts"
```

### Task 4: One-tap exact tender (frontend only)

**Files:**
- Modify: `frontend/src/pages/Pos.tsx` (charge modal cash tab): add an `Exact` quick button that sets cash-received = total; keep existing quick amounts.
- Modify: `frontend/src/pages/Settings.tsx` (printer section): add `Kick drawer` test button calling `POST /api/v1/printer/kick` (mirror the existing test-print button — read it first).
- Test: extend `frontend/__tests__/demo.test.ts`? Cash tender is pure UI state — instead add a demo-backend assertion that kick endpoint exists: `POST /api/v1/printer/kick` with no printer configured → 422 (add this route to demo backend returning 422 `no printer configured`, perm-gated `printer.test`).

- [ ] **Step 1: Exact button in Pos.tsx.**
- [ ] **Step 2: Kick button in Settings.tsx.**
- [ ] **Step 3: Demo parity for kick endpoint + test.**
- [ ] **Step 4: `npx tsc --noEmit` + `npm test`** — expect clean/all pass.
- [ ] **Step 5: Commit** (frontend files + demo test).

### Task 5: README + full verification

- [ ] **Step 1:** README bullet: drawer kick + receipt logo + exact tender.
- [ ] **Step 2:** Full gates: `go build`, `go test ./internal/...`, `npx tsc --noEmit`, `npm test`.
- [ ] **Step 3: Commit.**

## Self-Review

- Spec coverage: drawer kick (T1–T2 + test button T4), receipt logo (T3), quicker tender (T4 exact-tap). No new migrations, no new permissions (reuses `printer.test`), no new tables.
- Type consistency: `KickDrawer(pin, onTime, offTime uint8)`, `Worker.Kick() error`, `ReceiptData.Logo image.Image`, `POST /api/v1/printer/kick`, setting `receipt_logo`.
- CGO-free: only stdlib `image`, `image/png`, `image/jpeg`, hand-rolled resize.
