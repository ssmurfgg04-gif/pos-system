package services

// outbox_backfill.go — reconciliation for the transactional-outbox gap.
//
// Emit is deliberately best-effort (sync must never break a sale), so a
// committed business mutation CAN outlive its outbox row when the insert
// fails (disk full, crash between COMMIT and emit, lock contention). The
// review called this out (8.11): the event was lost forever with no error
// surfaced anywhere.
//
// Fix, in two halves:
//  1. noteOutboxFailure (teamsync.go) makes every failed emit visible in
//     the Team panel (counter + last error).
//  2. BackfillMissingEvents compares committed business rows against the
//     outbox by STABLE UUID and re-emits the missing ones. Emit helpers
//     read the row's CURRENT state, so a re-emit carries the truth even
//     when the original event was stale. Idempotent on the receiving side
//     (apply is client_uuid-keyed upsert / LWW).
//
// Money paths get full coverage: orders (clientUuid), voids
// (orderClientUuid), and unsynced ledger movements (synced_at cursor).

import "log"

const backfillWindowDays = 7
const backfillBatch = 200

// BackfillMissingEvents re-emits committed rows missing their outbox event.
// Returns how many events were (re)enqueued. Safe to run concurrently with
// the sync loop: every insert is an ordinary Emit and the sync cycle
// serializes pushes itself.
func (s *Service) BackfillMissingEvents() int {
        total := s.backfillOrders() + s.backfillVoids() + s.backfillLedger()
        return total
}

func (s *Service) backfillOrders() int {
        rows, err := s.db.Query(s.db.Rebind(`SELECT o.id FROM orders o
                WHERE COALESCE(o.client_uuid,'') != ''
                  AND o.created_at > datetime('now', '-' || ? || ' days')
                  AND NOT EXISTS (
                        SELECT 1 FROM sync_outbox ob
                        WHERE ob.entity = 'order'
                          AND json_extract(ob.payload, '$.clientUuid') = o.client_uuid)
                ORDER BY o.id DESC LIMIT ?`), backfillWindowDays, backfillBatch)
        if err != nil {
                return 0
        }
        defer rows.Close()
        var ids []int64
        for rows.Next() {
                var id int64
                if rows.Scan(&id) == nil {
                        ids = append(ids, id)
                }
        }
        for _, id := range ids {
                s.EmitOrder(id)
        }
        if len(ids) > 0 {
                log.Printf("[sync] backfill: re-emitted %d order event(s)", len(ids))
        }
        return len(ids)
}

func (s *Service) backfillVoids() int {
        rows, err := s.db.Query(s.db.Rebind(`SELECT o.id FROM orders o
                WHERE o.status = 'voided'
                  AND COALESCE(o.client_uuid,'') != ''
                  AND o.created_at > datetime('now', '-' || ? || ' days')
                  AND NOT EXISTS (
                        SELECT 1 FROM sync_outbox ob
                        WHERE ob.entity = 'void'
                          AND json_extract(ob.payload, '$.orderClientUuid') = o.client_uuid)
                ORDER BY o.id DESC LIMIT ?`), backfillWindowDays, backfillBatch)
        if err != nil {
                return 0
        }
        defer rows.Close()
        var ids []int64
        for rows.Next() {
                var id int64
                if rows.Scan(&id) == nil {
                        ids = append(ids, id)
                }
        }
        for _, id := range ids {
                s.EmitVoid(id, "backfill: void event was missing from the outbox")
        }
        if len(ids) > 0 {
                log.Printf("[sync] backfill: re-emitted %d void event(s)", len(ids))
        }
        return len(ids)
}

func (s *Service) backfillLedger() int {
        // Ledger rows carry their own emit-once cursor (synced_at). Any row that
        // moved money but still shows unsynced gets re-emitted; emitLedgerForOrder
        // marks the rows only after a successful outbox insert, so this is
        // idempotent by construction.
        rows, err := s.db.Query(s.db.Rebind(`SELECT DISTINCT l.order_id FROM customer_ledger l
                WHERE l.order_id > 0
                  AND COALESCE(l.synced_at,'') = ''
                ORDER BY l.order_id DESC LIMIT ?`), backfillBatch)
        if err != nil {
                return 0
        }
        defer rows.Close()
        var ids []int64
        for rows.Next() {
                var id int64
                if rows.Scan(&id) == nil {
                        ids = append(ids, id)
                }
        }
        for _, id := range ids {
                s.emitLedgerForOrder(id)
        }
        if len(ids) > 0 {
                log.Printf("[sync] backfill: re-emitted ledger for %d order(s)", len(ids))
        }
        return len(ids)
}
