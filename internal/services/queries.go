package services

import (
        "context"
        "fmt"
        "strings"

        "posapp/internal/auth"
        "posapp/internal/models"
)

// OrderFilter narrows ListOrders.
type OrderFilter struct {
        Status  string // PENDING | PAID | VOIDED | ""
        Search  string // order number, customer, receipt, phone
        Cashier int64  // 0 = all
        From    string // YYYY-MM-DD inclusive (optional)
        To      string // YYYY-MM-DD inclusive (optional)
        Limit   int
        Offset  int
}

// ListOrders returns enriched orders.
//
// DEADLOCK DISCIPLINE: the settings reads (tax/currency are display-only and
// not needed here) and all enrichment happen AFTER the main rows are fully
// collected and closed. No query ever runs while rows are open.
func (s *Service) ListOrders(f OrderFilter) ([]models.Order, error) {
        q := strings.Builder{}
        q.WriteString(`
                SELECT o.id, o.number, o.status, o.subtotal_cents, o.tax_cents, o.total_cents,
                        o.cashier_id, COALESCE(u.full_name, u.username, ''), COALESCE(o.customer_name,''),
                        COALESCE(o.note,''), COALESCE(o.client_uuid,''), COALESCE(o.discrepancy,0),
                        o.created_at, COALESCE(o.paid_at,''), COALESCE(o.voided_at,''), COALESCE(o.void_reason,'')
                FROM orders o LEFT JOIN users u ON u.id = o.cashier_id WHERE 1=1`)
        var args []any
        if f.Status != "" {
                q.WriteString(` AND o.status = ?`)
                args = append(args, f.Status)
        }
        if f.Cashier != 0 {
                q.WriteString(` AND o.cashier_id = ?`)
                args = append(args, f.Cashier)
        }
        if f.Search != "" {
                like := "%" + strings.ToLower(f.Search) + "%"
                q.WriteString(` AND (LOWER(o.number) LIKE ? OR LOWER(COALESCE(o.customer_name,'')) LIKE ? OR o.id IN (SELECT order_id FROM payments WHERE LOWER(COALESCE(mpesa_receipt,'')) LIKE ? OR phone LIKE ?))`)
                args = append(args, like, like, like, "%"+f.Search+"%")
        }
        if f.From != "" {
                q.WriteString(` AND o.created_at >= ?`)
                args = append(args, f.From+"T00:00:00")
        }
        if f.To != "" {
                q.WriteString(` AND o.created_at <= ?`)
                args = append(args, f.To+"T23:59:59")
        }
        q.WriteString(` ORDER BY o.id DESC`)
        if f.Limit <= 0 || f.Limit > 200 {
                f.Limit = 50
        }
        q.WriteString(fmt.Sprintf(` LIMIT %d OFFSET %d`, f.Limit, f.Offset))

        rows, err := s.db.Query(s.db.Rebind(q.String()), args...)
        if err != nil {
                return nil, err
        }
        var orders []models.Order
        for rows.Next() {
                var o models.Order
                if err := rows.Scan(&o.ID, &o.Number, &o.Status, &o.SubtotalCents, &o.TaxCents, &o.TotalCents,
                        &o.CashierID, &o.CashierName, &o.CustomerName, &o.Note, &o.ClientUUID, &o.Discrepancy,
                        &o.CreatedAt, &o.PaidAt, &o.VoidedAt, &o.VoidReason); err != nil {
                        rows.Close()
                        return nil, err
                }
                orders = append(orders, o)
        }
        rows.Close()
        if err := rows.Err(); err != nil {
                return nil, err
        }

        // Enrich in batch — rows are closed, the connection is free again.
        if len(orders) > 0 {
                ids := make([]string, len(orders))
                for i, o := range orders {
                        ids[i] = fmt.Sprint(o.ID)
                }
                itemsByOrder, err := s.itemsForOrders(ids)
                if err != nil {
                        return orders, nil // degrade gracefully
                }
                paysByOrder, err := s.paymentsForOrders(ids)
                if err != nil {
                        return orders, nil
                }
                for i := range orders {
                        orders[i].Items = itemsByOrder[orders[i].ID]
                        orders[i].Payments = paysByOrder[orders[i].ID]
                }
        }
        return orders, nil
}

func (s *Service) itemsForOrders(ids []string) (map[int64][]models.OrderItem, error) {
        out := map[int64][]models.OrderItem{}
        q := s.db.Rebind(`SELECT order_id, id, product_id, name, COALESCE(sku,''), qty, unit_price_cents, line_total_cents
                FROM order_items WHERE order_id IN (` + placeholders(len(ids)) + `) ORDER BY id`)
        args := toAny(ids)
        rows, err := s.db.Query(q, args...)
        if err != nil {
                return out, err
        }
        defer rows.Close()
        for rows.Next() {
                var orderID int64
                var it models.OrderItem
                if err := rows.Scan(&orderID, &it.ID, &it.ProductID, &it.Name, &it.SKU, &it.Qty, &it.UnitPriceCents, &it.LineTotalCents); err != nil {
                        return out, err
                }
                out[orderID] = append(out[orderID], it)
        }
        return out, rows.Err()
}

func (s *Service) paymentsForOrders(ids []string) (map[int64][]models.Payment, error) {
        out := map[int64][]models.Payment{}
        q := s.db.Rebind(`SELECT order_id, id, method, COALESCE(mode,''), amount_cents, status,
                COALESCE(phone,''), COALESCE(mpesa_receipt,''), COALESCE(checkout_request_id,''), COALESCE(result_desc,''),
                COALESCE(discrepancy,0), created_at, COALESCE(completed_at,'')
                FROM payments WHERE order_id IN (` + placeholders(len(ids)) + `) ORDER BY id`)
        args := toAny(ids)
        rows, err := s.db.Query(q, args...)
        if err != nil {
                return out, err
        }
        defer rows.Close()
        for rows.Next() {
                var orderID int64
                var pm models.Payment
                if err := rows.Scan(&orderID, &pm.ID, &pm.Method, &pm.Mode, &pm.AmountCents, &pm.Status,
                        &pm.Phone, &pm.MpesaReceipt, &pm.CheckoutRequestID, &pm.ResultDesc, &pm.Discrepancy,
                        &pm.CreatedAt, &pm.CompletedAt); err != nil {
                        return out, err
                }
                out[orderID] = append(out[orderID], pm)
        }
        return out, rows.Err()
}

func placeholders(n int) string {
        return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func toAny(ss []string) []any {
        out := make([]any, len(ss))
        for i, v := range ss {
                out[i] = v
        }
        return out
}

// GetOrder loads one fully-enriched order (sequential queries only).
func (s *Service) GetOrder(orderID int64) (*models.Order, error) {
        var o models.Order
        err := s.db.QueryRow(s.db.Rebind(`
                SELECT o.id, o.number, o.status, o.subtotal_cents, o.tax_cents, o.total_cents,
                        o.cashier_id, COALESCE(u.full_name, u.username, ''), COALESCE(o.customer_name,''),
                        COALESCE(o.note,''), COALESCE(o.client_uuid,''), COALESCE(o.discrepancy,0),
                        o.created_at, COALESCE(o.paid_at,''), COALESCE(o.voided_at,''), COALESCE(o.void_reason,'')
                FROM orders o LEFT JOIN users u ON u.id = o.cashier_id
                WHERE o.id = ?`), orderID).
                Scan(&o.ID, &o.Number, &o.Status, &o.SubtotalCents, &o.TaxCents, &o.TotalCents,
                        &o.CashierID, &o.CashierName, &o.CustomerName, &o.Note, &o.ClientUUID, &o.Discrepancy,
                        &o.CreatedAt, &o.PaidAt, &o.VoidedAt, &o.VoidReason)
        if err != nil {
                return nil, ErrNotFound
        }
        items, err := s.itemsForOrders([]string{fmt.Sprint(orderID)})
        if err == nil {
                o.Items = items[orderID]
        }
        pays, err := s.paymentsForOrders([]string{fmt.Sprint(orderID)})
        if err == nil {
                o.Payments = pays[orderID]
        }
        return &o, nil
}

func (s *Service) GetOrderByClientUUID(uuid string) (*models.Order, error) {
        var id int64
        if err := s.db.QueryRow(`SELECT id FROM orders WHERE client_uuid = ?`, uuid).Scan(&id); err != nil {
                return nil, ErrNotFound
        }
        return s.GetOrder(id)
}

func (s *Service) GetOrderByPayment(paymentID int64) (*models.Order, error) {
        var id int64
        if err := s.db.QueryRow(`SELECT order_id FROM payments WHERE id = ?`, paymentID).Scan(&id); err != nil {
                return nil, ErrNotFound
        }
        return s.GetOrder(id)
}

// Sync replays offline-queued checkouts idempotently (client_uuid keyed).
func (s *Service) Sync(ctx context.Context, p *auth.Principal, reqs []models.CheckoutRequest) []models.SyncResult {
        results := make([]models.SyncResult, 0, len(reqs))
        for _, r := range reqs {
                res := models.SyncResult{ClientUUID: r.ClientUUID}
                order, err := s.Checkout(ctx, p, r)
                if err != nil {
                        res.Error = err.Error()
                } else if order != nil {
                        res.OrderID = order.ID
                        res.OrderNumber = order.Number
                        res.Status = order.Status
                }
                results = append(results, res)
        }
        return results
}
