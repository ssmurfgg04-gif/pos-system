# Deadlock & Concurrency Discipline (SQLite)

The rules that keep checkout latency flat and the money paths race-free.
Enforced by code review and by the tests named at the bottom. The same
summary lives in the header of `internal/database/database.go`.

## The pool

- Tills run SQLite in WAL mode with a **small pool and `_txlock=immediate`**.
  Writes serialize inside one SQLite file no matter what the pool size is —
  raising the pool adds SQLITE_BUSY contention, not throughput. Multi-
  terminal scale comes from one process per till, not from one big DB.
- **Never issue a query while rows from another query are open** on the
  same database. Collect the rows, close them, then enrich. Nested queries
  on a saturated pool are the classic self-deadlock (the connection you
  need is the one you are holding).

## Transactions

- `Begin()` is IMMEDIATE — it takes the write lock up front. Keep the
  critical section small and do any preparatory reads (settings, identity
  claims) BEFORE opening the tx. The v1.4.4 example: the device-id claim
  (`deviceID()`) does its own write, so checkout resolves it before its
  transaction opens — calling it inside was a guaranteed SQLITE_BUSY on
  the second pooled connection.
- A tx that fails rolls back COMPLETELY; counters bumped inside it
  (order numbers) roll back with it. The allocator therefore skips
  forward on retries instead of re-minting the same number.
- Order-number generation serializes per process with `orderSeqMu` AND
  uses atomic `INSERT .. ON CONFLICT .. DO UPDATE .. RETURNING` on
  `order_sequences` — cross-process safety comes from the unique index on
  `orders.number` plus the retry loop in Checkout.

## Patterns to copy (and where they live)

| Pattern | Where |
|---|---|
| Batch-enrich after rows close | `internal/services/queries.go` (ListOrders) |
| One-tx supersede of payment legs | `internal/services/payments_buni_cloud.go` |
| Single-winner guarded transition | `completePayment` (`internal/services/orders.go`) |
| Claim-then-apply with exactly-once RPC | `SweepBuni` |
| Pre-probe before IMMEDIATE begin | `internal/printer/worker.go` (tick) |
| First-writer-wins identity claim | `internal/settings/settings.go` (`SetOnce`) |

## Tests that catch regressions

- `internal/handlers/adversarial_probe_test.go` — concurrent double-
  completion, void-exactly-once.
- `internal/services/payments_buni_test.go` — supersede races, orphan
  binds.
- `internal/handlers/split_tender_test.go` — split legs under the same
  write pressure.
- CI's Postgres job runs the suite against PG 16 to keep both dialects
  honest.
