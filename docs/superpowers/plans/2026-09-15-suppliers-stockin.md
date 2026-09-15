# Suppliers & Stock-In Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Shops can record buying stock: supplier records, purchase orders that add stock with weighted-average costing on receive, and stock-takes that reconcile counted vs expected with a variance report.

**Architecture:** Mirrors the customers/tabs implementation exactly (same authors' patterns): migration v5 for tables, `models` types, `services/suppliers.go` with transactional receive/apply, `handlers/suppliers.go` with permission groups, router groups, `Suppliers.tsx` page, demo-backend parity, handler + demo tests. Supplier permissions are Admin-only (seeded), so the existing v3 `backfillRolePerms` grants them to upgraded Admins with zero extra code.

**Tech Stack:** Go (gin, sqlite/postgres), React frontend, demo backend mirror in `frontend/src/demo/`.

**Spec:** `docs/ROADMAP.md` §1.2: "Supplier records, purchase orders (receive → stock up, cost averaging), stock-take counts with a variance report."

## Global Constraints

- Repo Go files are mixed tabs/spaces PER FILE — match each file's own style, never run gofmt -w.
- Money and quantities stay integer (`*_cents`, integer qty); cost averaging rounds half-up.
- Every SQL change needs SQLite + Pg bodies.
- Stock mutations happen only inside transactions with audit rows, like checkout/void.

---

### Task 1: Migration v5 — suppliers, purchase_orders, stock_takes

**Files:**
- Modify: `internal/database/migrations.go` (append v5 after v3; file uses tabs, SQL bodies use tabs)

**Interfaces:**
- Consumes: `Migration` struct.
- Produces: 5 new tables on both dialects.

- [ ] **Step 1: Add the v5 migration**

```go
{
        Version: 5,
        SQLite: `
CREATE TABLE IF NOT EXISTS suppliers (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        name TEXT NOT NULL,
        phone TEXT NOT NULL DEFAULT '',
        email TEXT NOT NULL DEFAULT '',
        address TEXT NOT NULL DEFAULT '',
        notes TEXT NOT NULL DEFAULT '',
        is_active INTEGER NOT NULL DEFAULT 1,
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_suppliers_name ON suppliers(name);
CREATE TABLE IF NOT EXISTS purchase_orders (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        number TEXT NOT NULL UNIQUE,
        supplier_id INTEGER NOT NULL REFERENCES suppliers(id),
        status TEXT NOT NULL DEFAULT 'PENDING',
        subtotal_cents INTEGER NOT NULL DEFAULT 0,
        note TEXT NOT NULL DEFAULT '',
        created_by INTEGER NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        received_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS purchase_order_items (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        po_id INTEGER NOT NULL REFERENCES purchase_orders(id) ON DELETE CASCADE,
        product_id INTEGER NOT NULL REFERENCES products(id),
        name TEXT NOT NULL DEFAULT '',
        sku TEXT NOT NULL DEFAULT '',
        qty INTEGER NOT NULL DEFAULT 0,
        cost_cents INTEGER NOT NULL DEFAULT 0,
        line_total_cents INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS stock_takes (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        number TEXT NOT NULL UNIQUE,
        status TEXT NOT NULL DEFAULT 'OPEN',
        note TEXT NOT NULL DEFAULT '',
        created_by INTEGER NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL DEFAULT (datetime('now')),
        applied_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS stock_take_items (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        take_id INTEGER NOT NULL REFERENCES stock_takes(id) ON DELETE CASCADE,
        product_id INTEGER NOT NULL REFERENCES products(id),
        name TEXT NOT NULL DEFAULT '',
        sku TEXT NOT NULL DEFAULT '',
        expected_qty INTEGER NOT NULL DEFAULT 0,
        counted_qty INTEGER NOT NULL DEFAULT 0
);
`,
        Pg: `
-- same five tables with SERIAL ids, BIGINT cents, TIMESTAMP defaults NOW(),
-- and IF NOT EXISTS guards (mirror the v2 Pg block style exactly)
`,
},
```

Write the Pg body fully (do not leave the comment above — expand every CREATE TABLE with `SERIAL`, `BIGINT`, `TIMESTAMP NOT NULL DEFAULT NOW()`).

- [ ] **Step 2: Boot + verify tables**

Run any boot/tests, then inspect: expect `suppliers`, `purchase_orders`, `purchase_order_items`, `stock_takes`, `stock_take_items`.
Expected: all five present.

- [ ] **Step 3: Commit**

```bash
git add internal/database/migrations.go
git commit -m "feat: v5 migration adds suppliers, purchase orders, stock takes"
```

### Task 2: Models + permissions

**Files:**
- Modify: `internal/models/models.go` (tabs file): `Supplier`, `PurchaseOrder` (+`POItem`), `StockTake` (+`StockTakeItem`) structs; status consts `PO Pending/Received/Cancelled`, `Take Open/Applied/Cancelled`.
- Modify: `internal/models/permissions.go` (tabs file): catalog `suppliers.view` + `suppliers.manage` (Selling group); add both to Admin via `AllPermissions()` (automatic) and to NO other seeded role.

**Interfaces:**
- Consumes: existing `Product`/`Order` JSON conventions (`id`, `createdAt` strings).
- Produces: types below; permission keys `suppliers.view`, `suppliers.manage`.

- [ ] **Step 1: Add the types**

```go
// ---- Suppliers & stock-in ----

const (
        POPending   = "PENDING"
        POReceived  = "RECEIVED"
        POCancelled = "CANCELLED"
        TakeOpen      = "OPEN"
        TakeApplied   = "APPLIED"
        TakeCancelled = "CANCELLED"
)

type Supplier struct {
        ID        int64  `json:"id"`
        Name      string `json:"name"`
        Phone     string `json:"phone"`
        Email     string `json:"email"`
        Address   string `json:"address"`
        Notes     string `json:"notes"`
        Active    bool   `json:"active"`
        CreatedAt string `json:"createdAt"`
        UpdatedAt string `json:"updatedAt"`
}

type POItem struct {
        ID            int64  `json:"id"`
        POID          int64  `json:"poId"`
        ProductID     int64  `json:"productId"`
        Name          string `json:"name"`
        SKU           string `json:"sku"`
        Qty           int    `json:"qty"`
        CostCents     int64  `json:"costCents"`
        LineTotalCents int64 `json:"lineTotalCents"`
}

type PurchaseOrder struct {
        ID            int64    `json:"id"`
        Number        string   `json:"number"`
        SupplierID    int64    `json:"supplierId"`
        SupplierName  string   `json:"supplierName"`
        Status        string   `json:"status"`
        SubtotalCents int64    `json:"subtotalCents"`
        Note          string   `json:"note"`
        Items         []POItem `json:"items"`
        CreatedAt     string   `json:"createdAt"`
        ReceivedAt    string   `json:"receivedAt"`
}

type StockTakeItem struct {
        ID          int64  `json:"id"`
        TakeID      int64  `json:"takeId"`
        ProductID   int64  `json:"productId"`
        Name        string `json:"name"`
        SKU         string `json:"sku"`
        ExpectedQty int    `json:"expectedQty"`
        CountedQty  int    `json:"countedQty"`
}

type StockTake struct {
        ID        int64           `json:"id"`
        Number    string          `json:"number"`
        Status    string          `json:"status"`
        Note      string          `json:"note"`
        Items     []StockTakeItem `json:"items"`
        CreatedAt string          `json:"createdAt"`
        AppliedAt string          `json:"appliedAt"`
}
```

- [ ] **Step 2: Add permissions (Admin only)**

Catalog entries after `customers.manage`; do NOT touch Cashier/Designer seed lists.

- [ ] **Step 3: Compile**

Run: `C:\Users\Jackb\tools\go\bin\go.exe build ./internal/models/`
Expected: exit 0.

- [ ] **Step 4: Commit**

```bash
git add internal/models/models.go internal/models/permissions.go
git commit -m "feat: supplier/PO/stock-take models, Admin-only supplier permissions"
```

### Task 3: `services/suppliers.go` — suppliers CRUD + PO lifecycle

**Files:**
- Create: `internal/services/suppliers.go` (spaces, like `orders.go`)
- Reuse: `s.Audit`, `nowStamp()`, `ErrNotFound`, `s.db.Rebind`

**Interfaces:**
- Consumes: `models.Supplier/PurchaseOrder/POItem`, `auth.Principal`.
- Produces: `ListSuppliers(search)`, `CreateSupplier`, `UpdateSupplier`, `CreatePO(supplierID, items[{productId,qty,costCents}], note, p)`, `ReceivePO(poID, p)`, `CancelPO(poID, reason, p)`. PO numbers `PO{YYYYMMDD}{####}` via a generalized `nextDocNumber(tx, prefix)` — refactor `nextOrderNumber` to call it (keep `ORD` behavior identical).

- [ ] **Step 1: Supplier CRUD (mirrors customers.go List/Create/Update)**

Name required, active-first/name ordering, limit 200. Audit `SUPPLIER_CREATED`/`SUPPLIER_UPDATED`.

- [ ] **Step 2: CreatePO with number allocation**

Validate supplier exists+active; validate each product exists+active, qty > 0, cost ≥ 0; snapshot name/sku/price from products (never trust client names); `lineTotal = qty*cost`; `subtotal = Σ`; INSERT order row status PENDING + items; audit `PO_CREATED`. Number via `nextDocNumber(tx, "PO")`.

Refactor (same file edit, `orders.go`):

```go
func (s *Service) nextOrderNumber(tx *sql.Tx) (string, error) {
        return s.nextDocNumber(tx, "ORD")
}

func (s *Service) nextDocNumber(tx *sql.Tx, prefix string) (string, error) {
        // ... existing body, with fmt.Sprintf(prefix+"%s%04d", day, seq)
}
```

Table `order_sequences` is prefix-agnostic already (day+seq shared across prefixes — acceptable; note it in a comment).

- [ ] **Step 3: ReceivePO — stock up with weighted-average cost**

```go
// ReceivePO posts a PENDING PO: stock_qty += qty per line and cost_cents =
// weighted average, all inside one tx. Only PENDING orders receive.
func (s *Service) ReceivePO(poID int64, p *auth.Principal) (*models.PurchaseOrder, error) {
        // load PO + items; require Status == POPending else ErrInvalidState
        // tx:
        //   for each item (track_stock only for qty; cost updates regardless):
        //     read current stock_qty, cost_cents
        //     newStock = old + qty
        //     newCost = qty==0 ? old : (old*oldCost + qty*unitCost + newStock/2) / newStock  // half-up; newStock==0 → unitCost
        //     UPDATE products SET stock_qty=?, cost_cents=? WHERE id=?
        //   UPDATE purchase_orders SET status='RECEIVED', received_at=? WHERE id=? AND status='PENDING' (require 1 row)
        // commit; audit PO_RECEIVED
}
```

- [ ] **Step 4: CancelPO** — PENDING → CANCELLED with reason in note/audit `PO_CANCELLED`; no stock movement (nothing was ever deducted).

- [ ] **Step 5: Compile**

Run: `C:\Users\Jackb\tools\go\bin\go.exe build ./internal/services/`
Expected: exit 0.

- [ ] **Step 6: Commit**

```bash
git add internal/services/suppliers.go internal/services/orders.go
git commit -m "feat: supplier CRUD, PO lifecycle with weighted-average costing"
```

### Task 4: Stock takes in the same service file

**Files:**
- Modify: `internal/services/suppliers.go` (append)

**Interfaces:**
- Produces: `CreateTake(productIDs[]|all, note, p)`, `CountTake(takeID, counts{productID:qty}, p)`, `ApplyTake(takeID, p)`, `CancelTake(takeID, reason, p)`, `TakeVariance(takeID)` (computed, no storage).

- [ ] **Step 1: CreateTake snapshots expected quantities**

`CreateTake` with empty product list means all active tracked products. INSERT take (number `STK{YYYYMMDD}{####}` via `nextDocNumber(tx, "STK")`) + one item per product with `expected_qty = current stock_qty`, `counted_qty = expected` (default = no variance until edited). Audit `TAKE_CREATED`.

- [ ] **Step 2: CountTake edits counted quantities (OPEN takes only)**

Upsert `counted_qty` per product; reject unknown product ids and non-OPEN takes (`ErrInvalidState`).

- [ ] **Step 3: ApplyTake sets stock and reports variance**

```go
// ApplyTake writes counted quantities into products.stock_qty inside one tx
// and returns the take with per-line variance (counted - expected).
// Only OPEN takes apply; sets status APPLIED + applied_at; audit TAKE_APPLIED.
```

- [ ] **Step 4: Compile + commit**

Run build (expect 0), then:

```bash
git add internal/services/suppliers.go
git commit -m "feat: stock takes with variance report"
```

### Task 5: Handlers + router + error codes

**Files:**
- Create: `internal/handlers/suppliers.go` (spaces): `ListSuppliers`, `CreateSupplier`, `UpdateSupplier`, `CreatePO`, `GetPO` (single-PO fetch — add `GetPO` to service in Task 3's file if missing: select + items), `ReceivePO`, `CancelPO`, `CreateTake`, `CountTake`, `ApplyTake`, `CancelTake`, `GetTake`.
- Modify: `internal/router/router.go` (spaces): `supv` group (`suppliers.view`): GET /suppliers, GET /purchase-orders, GET /purchase-orders/:id, GET /stock-takes, GET /stock-takes/:id; `supm` group (`suppliers.manage`): POST/PUT suppliers, POST POs, POST /:id/receive|cancel, POST takes, POST /:id/count|apply|cancel.
- Modify: `internal/handlers/helpers.go`: map `ErrInvalidState` already 409 — no change needed. PO/stock errors reuse `ErrNotFound`/`ErrInvalidState`.

**Interfaces:**
- Consumes: service funcs from Tasks 3–4.
- Produces: JSON envelopes via existing `h.ok`/`h.created`/`h.fail`/`h.mapErr`/`h.pathID`/`h.principal`.

- [ ] **Step 1: Write handlers** (request bodies: `{name, phone, email, address, notes, active?}`, `{supplierId, items:[{productId,qty,costCents}], note}`, `{productIds?, note}`, `{counts:{<id>:qty}}`, `{reason}`).
- [ ] **Step 2: Wire router groups.**
- [ ] **Step 3: Compile** (`go build ./...`, expect 0).
- [ ] **Step 4: Commit**

```bash
git add internal/handlers/suppliers.go internal/router/router.go
git commit -m "feat: supplier/PO/stock-take HTTP API"
```

### Task 6: Backend tests

**Files:**
- Create: `internal/handlers/suppliers_test.go` (spaces; reuse `newTestServer`, `do`, `dataMap`, `decode`, `itoa64`).

- [ ] **Step 1: Write the lifecycle test**

```go
// PO lifecycle: create → receive posts stock + averages cost; double-receive
// 409s; take counts variance and applies.
func TestSupplierPOLifecycle(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)
        // supplier
        w := do(t, engine, "POST", "/api/v1/suppliers", admin, map[string]any{"name": "Nairobi Wholesalers"})
        if w.Code != 201 { t.Fatalf("supplier: %d %s", w.Code, w.Body.String()) }
        sid := int64(dataMap(t, w)["id"].(float64))
        // PO: 10x product 1 @ 40000 (product 1 price 55000, seed cost unknown → read it)
        // read current cost/stock first via GET /products, then receive, then assert:
        //   stock = before + 10
        //   cost = (before*costBefore + 10*40000 + newStock/2) / newStock
        // receive twice → second is 409
}
```

Read product 1's `costCents`/`stockQty` from `GET /api/v1/products` (admin) at test start and compute expectations from those values — never hardcode seed numbers.

```go
// Stock take: create (all tracked), count one product ±, apply sets stock.
func TestStockTakeApply(t *testing.T) {
        engine, admin, _, _ := newTestServer(t)
        // create take with productIds [1]; count product 1 at expected+5 (read expected from GET take)
        // apply → GET /products shows stock = counted; take status APPLIED
}
```

- [ ] **Step 2: Run**

Run: `C:\Users\Jackb\tools\go\bin\go.exe test ./internal/handlers/ -run "TestSupplier|TestStockTake" -v -count=1`
Expected: PASS.

- [ ] **Step 3: Full suite**

Run: `C:\Users\Jackb\tools\go\bin\go.exe test ./internal/... -count=1`
Expected: all ok.

- [ ] **Step 4: Commit**

```bash
git add internal/handlers/suppliers_test.go
git commit -m "test: PO receive costing + stock-take apply pinned"
```

### Task 7: Frontend page + demo parity + demo tests

**Files:**
- Create: `frontend/src/pages/Suppliers.tsx` (Suppliers list + form; POs list + create/receive; takes list + count/apply + variance table), register route in `frontend/src/App.tsx` + nav in `frontend/src/components/shell.tsx` (mirrors Customers.tsx registration — read both first).
- Modify: `frontend/src/lib/api.ts` (Supplier/PO/Take interfaces).
- Modify: `frontend/src/demo/backend.ts` + `seed.ts` (in-memory suppliers/POs/takes mirroring the Go semantics incl. weighted-average math; seed 2 suppliers).
- Modify: `frontend/__tests__/demo.test.ts` (tab-style lifecycle test for receive + take apply).

- [ ] **Step 1: api.ts types.**
- [ ] **Step 2: Suppliers.tsx** (three cards: Suppliers, Purchase orders, Stock takes; variance shown red/green).
- [ ] **Step 3: Route + nav.**
- [ ] **Step 4: Demo backend mirror** (same permission gates: `suppliers.view`/`suppliers.manage`; Admin-only seed — demo Admin has ALL_PERMS automatically via catalog map).
- [ ] **Step 5: Demo test + run**: `npx tsc --noEmit` clean, `npm test` all pass.
- [ ] **Step 6: Commit** (one commit for the whole frontend slice).

### Task 8: README + full verification

- [ ] **Step 1:** README feature bullet: suppliers, POs with cost averaging, stock-takes.
- [ ] **Step 2:** Full gates: `go build`, `go test ./internal/...`, `npx tsc --noEmit`, `npm test`.
- [ ] **Step 3: Commit.**

## Self-Review

- Spec coverage: supplier records (T3/T5/T7), PO receive→stock+averaging (T3), variance report (T4/T7). Every behavior has a Go test (T6) and demo test (T7).
- Type consistency: `SupplierID/SupplierName/POID/TakeID`, `CostCents/LineTotalCents/ExpectedQty/CountedQty`, numbers `PO…/STK…`, statuses PENDING/RECEIVED/CANCELLED + OPEN/APPLIED/CANCELLED used identically in models, service, handlers, demo, tests.
- Upgrade path: Admin-only perms flow through existing v3 backfill; no new migration code needed for roles.
