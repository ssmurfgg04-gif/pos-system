package services

import (
        "context"
        "database/sql"
        "errors"
        "fmt"
        "math"
        "strconv"
        "strings"
        "time"

        "posapp/internal/auth"
        "posapp/internal/models"
        "posapp/internal/mpesa"
)

var (
        ErrNotFound          = errors.New("not found")
        ErrInsufficientStock = errors.New("insufficient stock")
        ErrInvalidState      = errors.New("invalid state transition")
        ErrDuplicateReceipt  = errors.New("receipt code already recorded")
        ErrOrderAlreadyPaid  = errors.New("order already paid")
        ErrOverpayment       = errors.New("payment exceeds balance")
        ErrCreditLimit       = errors.New("tab would exceed customer credit limit")
        ErrNoStoreCredit     = errors.New("not enough store credit")
        ErrLoyaltyPoints     = errors.New("not enough loyalty points")
        ErrNotConfigured     = errors.New("payment provider not configured")
)

// nowStamp is the fixed-width millisecond timestamp used for all
// app-written time columns. Fixed width keeps string comparisons
// chronologically exact (variable RFC3339Nano trimming would not).
func nowStamp() string {
        return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// NowStamp is the exported form for handlers: product/customer writes MUST
// use the same fixed-width format as sync events, or the cross-device LWW
// comparison (a byte-wise string compare) breaks — SQLite's default
// CURRENT_TIMESTAMP ('2026-10-03 18:00:00') loses every same-day compare
// against the sync format ('2026-10-03T18:00:00.000Z').
func NowStamp() string { return nowStamp() }

// checkoutLine is a validated cart line with server-side pricing.
type checkoutLine struct {
        productID      int64
        name, sku      string
        qty            int
        unitPriceCents int64
        trackStock     bool
}

// roundToShilling rounds cents to the nearest whole shilling (M-Pesa STK
// only accepts integer amounts).
func roundToShilling(cents int64) int64 {
        return int64(math.Round(float64(cents)/100.0) * 100)
}

// Checkout creates an order atomically. Cash orders complete immediately;
// M-Pesa orders start PENDING and (in auto/stk mode) receive an STK push
// AFTER commit — network calls never run inside the transaction.
func (s *Service) Checkout(ctx context.Context, p *auth.Principal, req models.CheckoutRequest) (*models.Order, error) {
        // Idempotent replay (offline sync double-submits are the norm).
        if req.ClientUUID != "" {
                if existing, err := s.GetOrderByClientUUID(req.ClientUUID); err == nil {
                        return existing, nil
                }
        }

        method := req.PaymentMethod
        mode := req.PaymentMode
        if method == models.MethodMpesa {
                switch mode {
                case "", models.ModeAuto, models.ModeSTK, models.ModeManual:
                        if mode == "" {
                                mode = s.settings.GetString("payment_mode", models.ModeAuto)
                        }
                default:
                        return nil, fmt.Errorf("invalid payment mode %q", mode)
                }
        } else if method != models.MethodCash && method != models.MethodAccount && method != models.MethodCredit && method != models.MethodPaystack {
                return nil, fmt.Errorf("invalid payment method %q", method)
        }
        if method == models.MethodCredit && !s.settings.GetBool("credit_enabled", true) {
                return nil, fmt.Errorf("store credit is disabled")
        }
        // Tab checkout needs a live customer up front (limit enforced at
        // insert time inside the transaction). Credit checkout needs a
        // live customer with enough prepaid store credit.
        var tabCustomer *models.Customer
        if method == models.MethodAccount || method == models.MethodCredit {
                if req.CustomerID == 0 {
                        return nil, fmt.Errorf("%s checkout needs a customer", method)
                }
                var err error
                tabCustomer, err = s.GetCustomer(req.CustomerID)
                if err != nil {
                        return nil, err
                }
                if !tabCustomer.Active {
                        return nil, fmt.Errorf("customer is inactive")
                }
        }
        // Loyalty redemption spends points as payment: always tied to a
        // customer, permission-gated, capped by settings.
        var redeemCents int64
        var redeemSpent int64
        if req.RedeemPoints > 0 {
                if !p.Can("loyalty.redeem") {
                        return nil, fmt.Errorf("redeeming points requires loyalty.redeem permission")
                }
                if !s.settings.GetBool("loyalty_enabled", true) {
                        return nil, fmt.Errorf("loyalty program is disabled")
                }
                if req.CustomerID == 0 {
                        return nil, fmt.Errorf("redeeming points needs a customer")
                }
                if tabCustomer == nil {
                        var err error
                        tabCustomer, err = s.GetCustomer(req.CustomerID)
                        if err != nil {
                                return nil, err
                        }
                        if !tabCustomer.Active {
                                return nil, fmt.Errorf("customer is inactive")
                        }
                }
        }

        // CRM auto-capture (shared customer history): a sale carrying a
        // phone with no explicit customer picks up the CRM record for that
        // phone — or creates a lightweight one. This is what makes the
        // customer book fill itself during normal trading.
        if req.CustomerID == 0 && s.settings.GetBool("crm_auto_capture", true) {
                phone := strings.TrimSpace(req.CustomerPhone)
                if phone == "" && req.CustomerName != "" {
                        // No phone at all: nothing to key history on.
                        phone = ""
                }
                if phone != "" {
                        if existing := s.FindCustomerByPhone(phone); existing != nil {
                                req.CustomerID = existing.ID
                                if req.CustomerName == "" {
                                        req.CustomerName = existing.Name
                                }
                        } else if name := strings.TrimSpace(req.CustomerName); name != "" {
                                if c, err := s.CreateCustomer(name, phone, "", "auto-captured at checkout", 0, p); err == nil {
                                        req.CustomerID = c.ID
                                }
                        }
                }
        }

        // Validate lines with server-side price re-read (never trust client
        // prices). Duplicate product ids merge quantities. Hard ceilings
        // keep price*qty below int64 range (overflow would flip totals
        // negative) — far above any real sale.
        if len(req.Items) == 0 {
                return nil, errors.New("empty cart")
        }
        if len(req.Items) > models.MaxOrderLines {
                return nil, fmt.Errorf("too many lines (max %d)", models.MaxOrderLines)
        }
        acc := map[int64]checkoutLine{}
        var orderIdx []int64
        for _, it := range req.Items {
                if it.Qty <= 0 {
                        return nil, errors.New("quantity must be positive")
                }
                if it.Qty > models.MaxOrderQty {
                        return nil, fmt.Errorf("quantity exceeds maximum (%d)", models.MaxOrderQty)
                }
                if prev, ok := acc[it.ProductID]; ok {
                        prev.qty += it.Qty
                        if prev.qty > models.MaxOrderQty {
                                return nil, fmt.Errorf("quantity exceeds maximum (%d)", models.MaxOrderQty)
                        }
                        acc[it.ProductID] = prev
                        continue
                }
                var l checkoutLine
                var track, active int
                err := s.db.QueryRow(s.db.Rebind(
                        `SELECT id, name, COALESCE(sku,''), price_cents, COALESCE(track_stock,1), COALESCE(is_active,1)
                         FROM products WHERE id = ?`), it.ProductID).
                        Scan(&l.productID, &l.name, &l.sku, &l.unitPriceCents, &track, &active)
                if err != nil {
                        return nil, fmt.Errorf("product %d: %w", it.ProductID, err)
                }
                if active != 1 {
                        return nil, fmt.Errorf("product %d is inactive", it.ProductID)
                }
                l.trackStock = track == 1
                // Price override is a permission-gated feature.
                if it.UnitPriceCents != 0 && it.UnitPriceCents != l.unitPriceCents {
                        if !p.Can("payments.override_price") {
                                return nil, fmt.Errorf("price override requires payments.override_price permission")
                        }
                        if it.UnitPriceCents < 0 || it.UnitPriceCents > models.MaxPriceCents {
                                return nil, errors.New("price override out of range")
                        }
                        l.unitPriceCents = it.UnitPriceCents
                }
                if l.unitPriceCents > models.MaxPriceCents {
                        return nil, fmt.Errorf("product %d price out of range", it.ProductID)
                }
                l.qty = it.Qty
                acc[it.ProductID] = l
                orderIdx = append(orderIdx, it.ProductID)
        }
        lines := make([]checkoutLine, 0, len(orderIdx))
        for _, pid := range orderIdx {
                lines = append(lines, acc[pid])
        }

        // Totals with configurable, VAT-inclusive-by-default math (integer cents).
        pct := s.settings.GetFloat("tax_percent", 16)
        if pct < 0 || pct > models.MaxTaxPercent {
                return nil, fmt.Errorf("tax_percent misconfigured (0-%d)", models.MaxTaxPercent)
        }
        included := s.settings.GetBool("tax_included", true)
        var subtotal int64
        for _, l := range lines {
                line := l.unitPriceCents * int64(l.qty)
                if l.qty > 0 && line/int64(l.qty) != l.unitPriceCents {
                        return nil, errors.New("line total overflow")
                }
                subtotal += line
                if subtotal < 0 {
                        return nil, errors.New("order total overflow")
                }
        }

        // Order-level discount: permission-gated, validated against the
        // subtotal, carried on the order for receipts and reports. A zero-value
        // basket (0.00-priced items) accepts only a zero discount — the old
        // check rejected EVERY discount when subtotal was 0 ("range (0 to -1)"),
        // making zero-priced products unsellable.
        discount := req.DiscountCents
        if discount < 0 || (subtotal > 0 && discount >= subtotal) || (subtotal == 0 && discount != 0) {
                return nil, fmt.Errorf("discount out of range (0 to %d)", max64(subtotal-1, 0))
        }
        if discount > 0 && !p.Can("payments.apply_discount") {
                return nil, fmt.Errorf("applying a discount requires payments.apply_discount permission")
        }
        discountedSub := subtotal - discount

        var tax, total int64
        if included {
                total = discountedSub
                tax = int64(math.Round(float64(discountedSub) * pct / (100 + pct)))
        } else {
                tax = int64(math.Round(float64(discountedSub) * pct / 100))
                total = discountedSub + tax
        }

        // Loyalty redemption: points × point value, capped at a configured
        // share of the order total. The spent points are deducted now and
        // refunded automatically if the order is later voided.
        if req.RedeemPoints > 0 {
                pc := s.settings.GetInt("loyalty_point_cents", 100)
                maxPct := s.settings.GetInt("loyalty_max_percent", 50)
                if pc < 0 {
                        pc = 100
                }
                if maxPct < 0 || maxPct > 100 {
                        maxPct = 50
                }
                capCents := total * int64(maxPct) / 100
                wantCents := req.RedeemPoints * int64(pc)
                redeemCents = wantCents
                if redeemCents > capCents {
                        redeemCents = capCents - (capCents % int64(pc)) // whole points only
                }
                if redeemCents < 0 {
                        redeemCents = 0
                }
                redeemSpent = 0
                if pc > 0 {
                        redeemSpent = redeemCents / int64(pc)
                }
                if redeemSpent == 0 {
                        return nil, fmt.Errorf("points value too small to apply on this order")
                }
                if tabCustomer.LoyaltyPoints < redeemSpent {
                        return nil, fmt.Errorf("%w: has %d, wants %d", ErrLoyaltyPoints, tabCustomer.LoyaltyPoints, redeemSpent)
                }
        }
        // Payable is what changes hands (discount + points already applied).
        payable := total - redeemCents

        immediateCash := method == models.MethodCash
        immediateCredit := method == models.MethodCredit
        isManual := method == models.MethodMpesa && mode == models.ModeManual
        isPaystack := method == models.MethodPaystack

        // ---- Split / mixed tender (optional multi-leg payment) ----
        // Loyalty redemption is not a leg (it already reduced the payable);
        // account tabs cannot be split; at most one async leg (mpesa or
        // paystack) stays PENDING — cash/credit legs complete at the till.
        var legs []models.SplitLeg
        isSplit := len(req.SplitPayments) > 0
        splitAllInstant := false
        splitAsyncMethod := ""
        if isSplit {
                if method == models.MethodAccount {
                        return nil, fmt.Errorf("split tender cannot include account tabs")
                }
                sum := int64(0)
                asyncCount := 0
                for _, lg := range req.SplitPayments {
                        if lg.AmountCents <= 0 {
                                return nil, fmt.Errorf("split legs need positive amounts")
                        }
                        sum += lg.AmountCents
                        switch lg.Method {
                        case models.MethodCash, models.MethodCredit:
                        case models.MethodMpesa, models.MethodPaystack:
                                asyncCount++
                                if splitAsyncMethod == "" {
                                        splitAsyncMethod = lg.Method
                                }
                        default:
                                return nil, fmt.Errorf("invalid split method %q", lg.Method)
                        }
                }
                if sum != payable {
                        return nil, fmt.Errorf("split total (%d) does not match the amount due (%d)", sum, payable)
                }
                if asyncCount > 1 {
                        return nil, fmt.Errorf("only one asynchronous leg (M-Pesa or card) per split")
                }
                legs = req.SplitPayments
                splitAllInstant = asyncCount == 0
                if splitAsyncMethod == models.MethodMpesa && req.PaymentMode == models.ModeManual {
                        isManual = true // async leg settled by receipt entry
                }
                // A credit leg pays from prepaid store credit — load the
                // customer now (single-method checkout does this earlier).
                for _, lg := range legs {
                        if lg.Method == models.MethodCredit {
                                if req.CustomerID == 0 {
                                        return nil, fmt.Errorf("credit split needs a customer")
                                }
                                if tabCustomer == nil {
                                        var err error
                                        tabCustomer, err = s.GetCustomer(req.CustomerID)
                                        if err != nil {
                                                return nil, err
                                        }
                                        if !tabCustomer.Active {
                                                return nil, fmt.Errorf("customer is inactive")
                                        }
                                }
                                break
                        }
                }
        }

        var orderID int64
        var orderNumber string

        // Retry loop: order numbers are unique per day (ORD{YYYYMMDD}{seq}).
        // Six attempts: the allocator heals forward past any existing number and
        // skips ahead per attempt, so a collision can no longer deadlock checkout
        // (the old 3-attempt loop re-minted the SAME number every time — its
        // counter bump rolled back with the failed tx — and every payment method
        // died with "could not allocate order number" all day).
        var lastNumber string
        for attempt := 0; attempt < 6; attempt++ {
                tx, err := s.db.Begin()
                if err != nil {
                        return nil, err
                }
                number, err := s.nextOrderNumber(tx, attempt)
                if err != nil {
                        tx.Rollback()
                        return nil, err
                }
                lastNumber = number
                now := nowStamp()
                // All-instant = single cash/credit OR a split with no async
                // leg. A split's paymentMethod is only the primary display
                // method — the legs decide, so ignore the flags when splitting.
                allInstant := (!isSplit && (immediateCash || immediateCredit)) ||
                        (isSplit && splitAllInstant)
                status := models.OrderPending
                paidAt := ""
                if allInstant {
                        status = models.OrderPaid
                        paidAt = now
                }
                taxIncludedInt := 0
                if included {
                        taxIncludedInt = 1
                }
                res, err := tx.Exec(s.db.Rebind(`
                        INSERT INTO orders (number, status, subtotal_cents, tax_cents, total_cents, tax_percent, tax_included, discount_cents, discount_label, points_redeemed, cashier_id, customer_name, note, client_uuid, created_at, paid_at, customer_id, buyer_pin, invoice_number)
                        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
                        number, status, subtotal, tax, payable, pct, taxIncludedInt, discount, truncStr(req.DiscountLabel, 120), redeemSpent, p.ID, req.CustomerName, req.Note, req.ClientUUID, now, paidAt, req.CustomerID, strings.ToUpper(strings.TrimSpace(req.BuyerPIN)), number)
                if err != nil {
                        tx.Rollback()
                        if isUniqueViolation(err) {
                                // A concurrent request won the race. If it was
                                // our own client_uuid, return the winner instead
                                // of minting a duplicate (offline sync bursts
                                // collide here, not on the order number). Spin
                                // briefly: the winner may not have committed yet.
                                if req.ClientUUID != "" {
                                        for i := 0; i < 20; i++ {
                                                if existing, qerr := s.GetOrderByClientUUID(req.ClientUUID); qerr == nil {
                                                        return existing, nil
                                                }
                                                time.Sleep(25 * time.Millisecond)
                                        }
                                }
                                continue // concurrent number allocation — retry with next seq
                        }
                        return nil, err
                }
                id, _ := res.LastInsertId()

                for _, l := range lines {
                        if _, err := tx.Exec(s.db.Rebind(`
                                INSERT INTO order_items (order_id, product_id, name, sku, qty, unit_price_cents, line_total_cents)
                                VALUES (?, ?, ?, ?, ?, ?, ?)`),
                                id, l.productID, l.name, l.sku, l.qty, l.unitPriceCents, l.unitPriceCents*int64(l.qty)); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                }

                if isSplit {
                        // Mixed tender: one payment row per leg. Instant legs
                        // (cash, credit) complete inside this tx — that money
                        // is already in the drawer / off the credit balance.
                        // An async leg stays PENDING; stock deducts exactly
                        // once — now if everything is instant, otherwise when
                        // the async leg completes via completePayment.
                        for _, lg := range legs {
                                switch lg.Method {
                                case models.MethodCash:
                                        if _, err := tx.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, created_at, completed_at)
                                                VALUES (?, 'cash', '', ?, 'COMPLETED', ?, ?)`), id, lg.AmountCents, now, now); err != nil {
                                                tx.Rollback()
                                                return nil, err
                                        }
                                case models.MethodCredit:
                                        if tabCustomer == nil {
                                                tx.Rollback()
                                                return nil, fmt.Errorf("credit leg needs a customer")
                                        }
                                        var credit int64
                                        if err := tx.QueryRow(`SELECT store_credit_cents FROM customers WHERE id = ?`, tabCustomer.ID).Scan(&credit); err != nil {
                                                tx.Rollback()
                                                return nil, err
                                        }
                                        if credit < lg.AmountCents {
                                                tx.Rollback()
                                                return nil, fmt.Errorf("%w: %s has %d, leg needs %d", ErrNoStoreCredit, tabCustomer.Name, credit, lg.AmountCents)
                                        }
                                        if _, err := tx.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, created_at, completed_at)
                                                VALUES (?, 'credit', '', ?, 'COMPLETED', ?, ?)`), id, lg.AmountCents, now, now); err != nil {
                                                tx.Rollback()
                                                return nil, err
                                        }
                                        if err := recordLedgerTx(tx, s.db.Rebind, tabCustomer.ID, id, models.LedgerCreditRedeem,
                                                -lg.AmountCents, 0, "store credit (split) "+number, p.ID); err != nil {
                                                tx.Rollback()
                                                return nil, err
                                        }
                                case models.MethodMpesa:
                                        amount := lg.AmountCents
                                        phone := ""
                                        legMode := mode
                                        if legMode == "" {
                                                legMode = models.ModeAuto
                                        }
                                        if legMode != models.ModeManual {
                                                amount = roundToShilling(lg.AmountCents)
                                                ph, err := mpesa.NormalizePhone(lg.Phone)
                                                if err != nil {
                                                        tx.Rollback()
                                                        return nil, fmt.Errorf("split M-Pesa leg: %w", err)
                                                }
                                                phone = ph
                                        }
                                        if _, err := tx.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, phone, created_at)
                                                VALUES (?, 'mpesa', ?, ?, 'PENDING', ?, ?)`), id, legMode, amount, phone, now); err != nil {
                                                tx.Rollback()
                                                return nil, err
                                        }
                                case models.MethodPaystack:
                                        if _, err := tx.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, email, created_at)
                                                VALUES (?, 'paystack', 'popup', ?, 'PENDING', ?, ?)`), id, lg.AmountCents, truncStr(strings.TrimSpace(lg.Email), 200), now); err != nil {
                                                tx.Rollback()
                                                return nil, err
                                        }
                                }
                        }
                        // Points redeemed on a split deduct now; a void refunds them.
                        if redeemSpent > 0 {
                                var pts int64
                                if err := tx.QueryRow(`SELECT loyalty_points FROM customers WHERE id = ?`, tabCustomer.ID).Scan(&pts); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                                if pts < redeemSpent {
                                        tx.Rollback()
                                        return nil, fmt.Errorf("%w: %s has %d, wants %d", ErrLoyaltyPoints, tabCustomer.Name, pts, redeemSpent)
                                }
                                if err := recordLedgerTx(tx, s.db.Rebind, tabCustomer.ID, id, models.LedgerLoyalty,
                                        0, -redeemSpent, "points redeemed "+number, p.ID); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                        }
                        if splitAllInstant {
                                // Fail fast on stock BEFORE taking the money.
                                for _, l := range lines {
                                        if l.trackStock {
                                                var stock int
                                                if err := tx.QueryRow(`SELECT stock_qty FROM products WHERE id = ?`, l.productID).Scan(&stock); err != nil {
                                                        tx.Rollback()
                                                        return nil, err
                                                }
                                                if stock < l.qty {
                                                        tx.Rollback()
                                                        return nil, fmt.Errorf("%w: %s (have %d, need %d)", ErrInsufficientStock, l.name, stock, l.qty)
                                                }
                                        }
                                }
                                for _, l := range lines {
                                        if l.trackStock {
                                                gres, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty = stock_qty - ? WHERE id = ? AND stock_qty >= ?`), l.qty, l.productID, l.qty)
                                                if err != nil {
                                                        tx.Rollback()
                                                        return nil, err
                                                }
                                                if n, _ := gres.RowsAffected(); n != 1 {
                                                        tx.Rollback()
                                                        return nil, fmt.Errorf("%w: %s", ErrInsufficientStock, l.name)
                                                }
                                        }
                                }
                                if _, err := tx.Exec(`UPDATE orders SET status = 'PAID' WHERE id = ? AND status = 'PENDING'`, id); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                                // Loyalty earns here too — a settled tab is a paid order.
                                if err := s.earnLoyaltyTx(tx, id); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                                // Gift-card products mint one redeemable code per unit.
                                if err := issueGiftCardsTx(tx, s.db.Rebind, id); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                        }
                } else if immediateCash || immediateCredit {
                        // Fail fast on stock BEFORE taking money.
                        for _, l := range lines {
                                if l.trackStock {
                                        var stock int
                                        if err := tx.QueryRow(`SELECT stock_qty FROM products WHERE id = ?`, l.productID).Scan(&stock); err != nil {
                                                tx.Rollback()
                                                return nil, err
                                        }
                                        if stock < l.qty {
                                                tx.Rollback()
                                                return nil, fmt.Errorf("%w: %s (have %d, need %d)", ErrInsufficientStock, l.name, stock, l.qty)
                                        }
                                }
                        }
                        payMethod := "cash"
                        if immediateCredit {
                                payMethod = "credit"
                        }
                        if _, err := tx.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, created_at, completed_at)
                                VALUES (?, ?, '', ?, 'COMPLETED', ?, ?)`), id, payMethod, payable, now, now); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                        if immediateCredit {
                                // Prepaid store credit: deduct the payable from
                                // the customer's credit balance inside the same
                                // tx (guarded re-read — balance can move).
                                var credit int64
                                if err := tx.QueryRow(`SELECT store_credit_cents FROM customers WHERE id = ?`, tabCustomer.ID).Scan(&credit); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                                if credit < payable {
                                        tx.Rollback()
                                        return nil, fmt.Errorf("%w: %s has %d, order needs %d", ErrNoStoreCredit, tabCustomer.Name, credit, payable)
                                }
                                if err := recordLedgerTx(tx, s.db.Rebind, tabCustomer.ID, id, models.LedgerCreditRedeem,
                                        -payable, 0, "store credit "+number, p.ID); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                        }
                        // Loyalty redemption deducts points for every immediate method.
                        if redeemSpent > 0 {
                                var pts int64
                                if err := tx.QueryRow(`SELECT loyalty_points FROM customers WHERE id = ?`, tabCustomer.ID).Scan(&pts); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                                if pts < redeemSpent {
                                        tx.Rollback()
                                        return nil, fmt.Errorf("%w: %s has %d, wants %d", ErrLoyaltyPoints, tabCustomer.Name, pts, redeemSpent)
                                }
                                if err := recordLedgerTx(tx, s.db.Rebind, tabCustomer.ID, id, models.LedgerLoyalty,
                                        0, -redeemSpent, "points redeemed "+number, p.ID); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                        }
                        // Guarded stock deduction (WHERE guard = no double-deduct ever).
                        for _, l := range lines {
                                if l.trackStock {
                                        gres, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty = stock_qty - ? WHERE id = ? AND stock_qty >= ?`), l.qty, l.productID, l.qty)
                                        if err != nil {
                                                tx.Rollback()
                                                return nil, err
                                        }
                                        if n, _ := gres.RowsAffected(); n != 1 {
                                                tx.Rollback()
                                                return nil, fmt.Errorf("%w: %s", ErrInsufficientStock, l.name)
                                        }
                                }
                        }
                        if _, err := tx.Exec(`UPDATE orders SET status = 'PAID' WHERE id = ? AND status = 'PENDING'`, id); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                        // Loyalty earns on immediate cash/credit too — same rule as
                        // every other paid order (once, inside this guarded tx).
                        if err := s.earnLoyaltyTx(tx, id); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                        // Gift-card products mint one redeemable code per unit.
                        if err := issueGiftCardsTx(tx, s.db.Rebind, id); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                } else if method == models.MethodAccount {
                        // Tab: like M-Pesa, the order stays PENDING and stock
                        // deducts once at settle via completePayment — never
                        // here, or settling would double-deduct. Limit is
                        // enforced here inside the tx (0 = cash only, no tab)
                        // against the payable (after discount + points).
                        if tabCustomer.CreditLimitCents <= 0 {
                                tx.Rollback()
                                return nil, fmt.Errorf("customer has no credit — cash only")
                        }
                        // Limit enforced ATOMICALLY against the in-tx balance:
                        // tabCustomer above was read before BEGIN, so two
                        // concurrent checkouts could both pass a stale check.
                        // The guarded UPDATE inside the same immediate
                        // transaction serializes the decision (balance can
                        // never jump past the limit, whatever the interleaving).
                        gres, err := tx.Exec(s.db.Rebind(`UPDATE customers SET balance_cents = balance_cents + ?, updated_at = ?
                                WHERE id = ? AND balance_cents + ? <= credit_limit_cents`),
                                payable, now, tabCustomer.ID, payable)
                        if err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                        if n, _ := gres.RowsAffected(); n != 1 {
                                tx.Rollback()
                                return nil, fmt.Errorf("%w (%s)", ErrCreditLimit, tabCustomer.Name)
                        }
                        // Fail fast on stock BEFORE creating the tab (read
                        // check only — the guarded deduction happens once,
                        // at settle time, like every other PENDING order).
                        for _, l := range lines {
                                if l.trackStock {
                                        var stock int
                                        if err := tx.QueryRow(`SELECT stock_qty FROM products WHERE id = ?`, l.productID).Scan(&stock); err != nil {
                                                tx.Rollback()
                                                return nil, err
                                        }
                                        if stock < l.qty {
                                                tx.Rollback()
                                                return nil, fmt.Errorf("%w: %s (have %d, need %d)", ErrInsufficientStock, l.name, stock, l.qty)
                                        }
                                }
                        }
                        if _, err := tx.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, created_at)
                                VALUES (?, 'account', '', ?, 'PENDING', ?)`), id, payable, now); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                        if err := recordLedgerTxNoBalance(tx, s.db.Rebind, tabCustomer.ID, id, models.LedgerCharge,
                                payable, 0, "tab charge "+number, p.ID); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                        // Points redeemed on a tab deduct now; a void refunds them.
                        if redeemSpent > 0 {
                                var pts int64
                                if err := tx.QueryRow(`SELECT loyalty_points FROM customers WHERE id = ?`, tabCustomer.ID).Scan(&pts); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                                if pts < redeemSpent {
                                        tx.Rollback()
                                        return nil, fmt.Errorf("%w: %s has %d, wants %d", ErrLoyaltyPoints, tabCustomer.Name, pts, redeemSpent)
                                }
                                if err := recordLedgerTx(tx, s.db.Rebind, tabCustomer.ID, id, models.LedgerLoyalty,
                                        0, -redeemSpent, "points redeemed "+number, p.ID); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                        }
                } else if isPaystack {
                        // Paystack card / mobile-money: order PENDING, payment
                        // PENDING for the payable (points + discount already
                        // knocked off). Stock deducts once at completion via
                        // completePayment. The frontend opens checkout right
                        // after with POST /orders/:id/paystack/init.
                        if _, err := tx.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, email, created_at)
                                VALUES (?, 'paystack', 'popup', ?, 'PENDING', ?, ?)`), id, payable, truncStr(strings.TrimSpace(req.CustomerEmail), 200), now); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                        // Points redeemed on a paystack order deduct now; a
                        // void refunds them.
                        if redeemSpent > 0 {
                                var pts int64
                                if err := tx.QueryRow(`SELECT loyalty_points FROM customers WHERE id = ?`, tabCustomer.ID).Scan(&pts); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                                if pts < redeemSpent {
                                        tx.Rollback()
                                        return nil, fmt.Errorf("%w: %s has %d, wants %d", ErrLoyaltyPoints, tabCustomer.Name, pts, redeemSpent)
                                }
                                if err := recordLedgerTx(tx, s.db.Rebind, tabCustomer.ID, id, models.LedgerLoyalty,
                                        0, -redeemSpent, "points redeemed "+number, p.ID); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                        }
                } else {
                        // M-Pesa: payment PENDING for the payable (points +
                        // discount already knocked off). STK requests whole
                        // shillings; manual entries use the exact payable.
                        amount := payable
                        phone := ""
                        if !isManual {
                                amount = roundToShilling(payable)
                                ph, err := mpesa.NormalizePhone(req.CustomerPhone)
                                if err != nil {
                                        tx.Rollback()
                                        return nil, fmt.Errorf("customer phone: %w", err)
                                }
                                phone = ph
                        }
                        if _, err := tx.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, phone, created_at)
                                VALUES (?, 'mpesa', ?, ?, 'PENDING', ?, ?)`), id, mode, amount, phone, now); err != nil {
                                tx.Rollback()
                                return nil, err
                        }
                        // Points redeemed on an M-Pesa order deduct now; a void
                        // refunds them (the discount part just evaporates).
                        if redeemSpent > 0 {
                                var pts int64
                                if err := tx.QueryRow(`SELECT loyalty_points FROM customers WHERE id = ?`, tabCustomer.ID).Scan(&pts); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                                if pts < redeemSpent {
                                        tx.Rollback()
                                        return nil, fmt.Errorf("%w: %s has %d, wants %d", ErrLoyaltyPoints, tabCustomer.Name, pts, redeemSpent)
                                }
                                if err := recordLedgerTx(tx, s.db.Rebind, tabCustomer.ID, id, models.LedgerLoyalty,
                                        0, -redeemSpent, "points redeemed "+number, p.ID); err != nil {
                                        tx.Rollback()
                                        return nil, err
                                }
                        }
                }

                if err := tx.Commit(); err != nil {
                        return nil, err
                }
                orderID, orderNumber = id, number
                break
        }
        if orderID == 0 {
                return nil, fmt.Errorf("could not allocate a free order number after 6 attempts (last candidate %s; the day sequence may be behind the orders table)", lastNumber)
        }

        // Team sync: broadcast the order + its stock movements (best-effort).
        // This MUST run before the STK attempt below: an InitiateSTK failure
        // returns early, and an order+its deltas that never reach the cloud
        // would make every other till keep stock it doesn't have (the origin
        // deducts at completion). Deltas are keyed by SKU and applied exactly
        // once, so emitting them here — regardless of when the money lands —
        // is the canonical move; a later void restores them (see applyVoid).
        s.EmitOrder(orderID)
        for _, l := range lines {
                if l.trackStock {
                        s.EmitStockDelta(l.sku, -l.qty)
                }
        }
        // Money movements recorded at checkout (tab charge, credit/loyalty
        // redemptions) follow the team as ledger events — without this, a
        // customer's tab/credit balance existed only on the till that made
        // the sale (emit-once guarded by customer_ledger.synced_at).
        s.emitLedgerForOrder(orderID)

        // Post-commit side effects (network + broadcasts never inside the tx).
        if method == models.MethodAccount {
                // Tab charged, not paid: audit only. No receipt print and no
                // paid broadcast — those happen on settle when money lands.
                s.Audit(p.ID, p.Username, "TAB_CHARGED", "order", orderNumber, fmt.Sprintf("total %d", payable))
        } else if isSplit && splitAsyncMethod != "" {
                // Mixed tender with one async leg: instant legs are already
                // banked; the async leg drives completion (stock + print +
                // broadcast happen via completePayment, exactly once).
                if splitAsyncMethod == models.MethodPaystack {
                        s.Audit(p.ID, p.Username, "CHECKOUT_SPLIT", "order", orderNumber,
                                fmt.Sprintf("total %d, awaiting card/mobile leg", payable))
                } else if !isManual {
                        if _, err := s.InitiateSTK(ctx, orderID, p); err != nil {
                                return s.GetOrder(orderID)
                        }
                } else {
                        s.Audit(p.ID, p.Username, "CHECKOUT_SPLIT", "order", orderNumber,
                                fmt.Sprintf("total %d, awaiting M-Pesa receipt", payable))
                }
        } else if isSplit {
                s.Audit(p.ID, p.Username, "CHECKOUT_SPLIT", "order", orderNumber, fmt.Sprintf("total %d, mixed tender", payable))
                if order, err := s.GetOrder(orderID); err == nil {
                        s.printer.Enqueue(order)
                        s.broadcast(EventOrderPaid, order)
                }
        } else if immediateCredit {
                s.Audit(p.ID, p.Username, "CHECKOUT_CREDIT", "order", orderNumber, fmt.Sprintf("total %d, store credit", payable))
                if order, err := s.GetOrder(orderID); err == nil {
                        s.printer.Enqueue(order)
                        s.broadcast(EventOrderPaid, order)
                }
        } else if immediateCash {
                s.Audit(p.ID, p.Username, "CHECKOUT_CASH", "order", orderNumber, fmt.Sprintf("total %d", payable))
                if order, err := s.GetOrder(orderID); err == nil {
                        s.printer.Enqueue(order)
                        s.broadcast(EventOrderPaid, order)
                }
        } else if isPaystack {
                s.Audit(p.ID, p.Username, "CHECKOUT_PAYSTACK", "order", orderNumber, fmt.Sprintf("awaiting checkout, total %d", payable))
        } else if !isManual {
                if _, err := s.InitiateSTK(ctx, orderID, p); err != nil {
                        // Order stays PENDING — cashier sees failure, can retry or void.
                        // (Order + stock deltas already synced above.)
                        return s.GetOrder(orderID)
                }
        } else {
                s.Audit(p.ID, p.Username, "CHECKOUT_MPESA_MANUAL", "order", orderNumber, fmt.Sprintf("awaiting receipt code, total %d", payable))
        }
        return s.GetOrder(orderID)
}

// isUniqueViolation matches sqlite/pg unique constraint errors.
func isUniqueViolation(err error) bool {
        if err == nil {
                return false
        }
        msg := strings.ToLower(err.Error())
        return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "duplicate key")
}

// nextOrderNumber allocates ORD{YYYYMMDD}{####} inside the caller's tx.
// skip = how many numbers this caller already minted and lost to rollbacks
// (the counter bump rolls back with the order tx), so retries jump ahead.
func (s *Service) nextOrderNumber(tx *sql.Tx, skip int) (string, error) {
        return s.nextDocNumber(tx, "ORD", skip)
}

// nextDocNumber allocates PREFIX{YYYYMMDD}{####} inside the caller's tx.
// Uses a mutex to serialize number generation, avoiding race conditions
// under concurrent load. The day sequence is shared across prefixes.
//
// Collision-proofing (the "could not allocate order number" bug): the day
// counter lives in order_sequences, but the uniqueness that matters is on
// orders.number — and the counter can LAG reality, because cross-device
// sync ingests other tills' numbers verbatim (they are only suffixed when
// they collide at ingest time), a restored snapshot can resurrect old
// rows, and rolled-back txs discard counter bumps. So every mint:
//   - atomically bumps the counter (RETURNING — race-free even when two
//     processes share one Postgres),
//   - heals forward past the highest number that already exists for this
//     prefix+day,
//   - and adds the caller's skip so a retried tx never mints the same
//     number twice. Retries can no longer deadlock on a collision.
func (s *Service) nextDocNumber(tx *sql.Tx, prefix string, skip int) (string, error) {
        day := time.Now().Format("20060102")

        // Initialize sequence table if not exists (idempotent)
        if _, err := tx.Exec(`
                CREATE TABLE IF NOT EXISTS order_sequences (
                        day TEXT PRIMARY KEY,
                        seq INTEGER NOT NULL DEFAULT 0
                )
        `); err != nil {
                return "", err
        }

        // Serialize number generation across goroutines of this process.
        s.orderSeqMu.Lock()
        defer s.orderSeqMu.Unlock()

        daySeq, err := bumpDaySeq(tx, s.db.Rebind, day)
        if err != nil {
                return "", err
        }

        // Heal: never mint at or below the highest number that already
        // exists for this prefix+day. LENGTH() first so suffixed ingest
        // numbers ("...0005-XF") and 5-digit seqs sort ahead of 4-digit.
        var maxNum string
        if err := tx.QueryRow(s.db.Rebind(`
                SELECT number FROM orders WHERE number LIKE ?
                ORDER BY LENGTH(number) DESC, number DESC LIMIT 1
        `), prefix+day+"%").Scan(&maxNum); err == nil && strings.HasPrefix(maxNum, prefix+day) {
                tail := maxNum[len(prefix)+len(day):]
                if i := strings.IndexByte(tail, '-'); i >= 0 { // ingest suffix
                        tail = tail[:i]
                }
                if v, perr := strconv.Atoi(tail); perr == nil && v > daySeq-1 {
                        daySeq = v + 1
                }
        }

        return fmt.Sprintf(prefix+"%s%04d", day, daySeq+skip), nil
}

// bumpDaySeq advances the order_sequences row for day by one and returns
// the new value. Modern SQLite (3.35+) and Postgres both support
// INSERT .. ON CONFLICT .. DO UPDATE .. RETURNING, which makes the bump
// atomic across processes sharing one database; engines without RETURNING
// fall back to the classic read-modify-write (still safe inside the
// caller's tx + the process mutex).
func bumpDaySeq(tx *sql.Tx, rebind func(string) string, day string) (int, error) {
        var seq int
        err := tx.QueryRow(rebind(`
                INSERT INTO order_sequences (day, seq) VALUES (?, 1)
                ON CONFLICT(day) DO UPDATE SET seq = order_sequences.seq + 1
                RETURNING seq
        `), day).Scan(&seq)
        if err == nil {
                return seq, nil
        }
        var cur int
        err = tx.QueryRow(rebind(`SELECT seq FROM order_sequences WHERE day = ?`), day).Scan(&cur)
        if err == sql.ErrNoRows {
                cur = 0
        } else if err != nil {
                return 0, err
        }
        cur++
        if _, err := tx.Exec(rebind(`
                INSERT INTO order_sequences (day, seq) VALUES (?, ?)
                ON CONFLICT(day) DO UPDATE SET seq = excluded.seq
        `), day, cur); err != nil {
                return 0, err
        }
        return cur, nil
}

// InitiateSTK sends (or re-sends) the push for a pending order's pending
// payment. Runs OUTSIDE any transaction; failures mark the payment FAILED.
func (s *Service) InitiateSTK(ctx context.Context, orderID int64, p *auth.Principal) (*models.Order, error) {
        order, err := s.GetOrder(orderID)
        if err != nil {
                return nil, err
        }
        if order.Status != models.OrderPending {
                return nil, fmt.Errorf("%w: cannot push STK for %s order", ErrInvalidState, order.Status)
        }
        var pay *models.Payment
        for i := range order.Payments {
                if order.Payments[i].Status == models.PaymentPending {
                        pay = &order.Payments[i]
                }
        }
        if pay == nil {
                return nil, fmt.Errorf("%w: order has no pending payment", ErrInvalidState)
        }
        if pay.Phone == "" {
                return nil, errors.New("payment has no phone number; collect the customer's number and retry")
        }
        // REAL MONEY ROUTE: when the shop's Paystack integration is live,
        // M-Pesa STK rides Paystack's mobile-money charge — a real prompt on
        // the customer's phone, completed only after server-side verify.
        if s.MpesaRoute() == "paystack" {
                return s.paystackMpesaSTK(ctx, order, pay, p)
        }
        provider, err := s.GetProvider()
        if err != nil {
                s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = ? WHERE id = ? AND status = 'PENDING'`),
                        truncStr(err.Error(), 200), pay.ID)
                s.Audit(p.ID, p.Username, "STK_FAILED", "order", order.Number, err.Error())
                return nil, err
        }
        if provider == nil {
                return nil, errors.New("M-Pesa STK is not configured — connect Paystack in Settings (or switch to manual receipt entry)")
        }
        resp, err := provider.InitiateSTK(ctx, mpesa.STKRequest{
                OrderID:          order.ID,
                PaymentID:        pay.ID,
                Phone:            pay.Phone,
                AmountCents:      pay.AmountCents,
                AccountReference: order.Number,
                Description:      "Payment " + order.Number,
        })
        if err != nil {
                s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = ? WHERE id = ? AND status = 'PENDING'`),
                        truncStr(err.Error(), 200), pay.ID)
                s.Audit(p.ID, p.Username, "STK_FAILED", "order", order.Number, err.Error())
                return s.GetOrder(orderID)
        }
        s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'PENDING', checkout_request_id = ?, merchant_request_id = ?, result_desc = ? WHERE id = ?`),
                resp.CheckoutRequestID, resp.MerchantRequestID, resp.CustomerMessage, pay.ID)
        s.Audit(p.ID, p.Username, "STK_INITIATED", "order", order.Number, resp.CheckoutRequestID)
        // Reload AFTER STK state changes so the response reflects reality
        // (the original build returned a stale snapshot here).
        return s.GetOrder(orderID)
}

// RetrySTKWithPhone lets the cashier fix a bad number / re-push after a
// failure: pending payment is marked FAILED, a fresh one is created.
func (s *Service) RetrySTKWithPhone(ctx context.Context, orderID int64, phone string, p *auth.Principal) (*models.Order, error) {
        norm, err := mpesa.NormalizePhone(phone)
        if err != nil {
                return nil, fmt.Errorf("customer phone: %w", err)
        }
        order, err := s.GetOrder(orderID)
        if err != nil {
                return nil, err
        }
        if order.Status != models.OrderPending {
                return nil, fmt.Errorf("%w: cannot push STK for %s order", ErrInvalidState, order.Status)
        }
        // Supersede existing pending payments for this order.
        for _, pm := range order.Payments {
                if pm.Status == models.PaymentPending {
                        s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = 'superseded by retry' WHERE id = ?`), pm.ID)
                }
        }
        res, err := s.db.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, phone, created_at)
                VALUES (?, 'mpesa', 'stk', ?, 'PENDING', ?, ?)`),
                orderID, roundToShilling(order.TotalCents), norm, nowStamp())
        if err != nil {
                return nil, err
        }
        _ = res
        return s.InitiateSTK(ctx, orderID, p)
}

// completePayment is the single guarded transition PENDING → PAID.
// Sources: STK query result, STK callback, manual receipt entry. Idempotent;
// amount mismatches are flagged, never silently accepted.
func (s *Service) completePayment(paymentID int64, receipt string, amountCents int64, resultDesc string) (*models.Order, error) {
        tx, err := s.db.Begin()
        if err != nil {
                return nil, err
        }
        defer tx.Rollback()

        var orderID int64
        var payStatus, payMethod string
        var payAmount int64
        err = tx.QueryRow(`SELECT order_id, status, method, amount_cents FROM payments WHERE id = ?`, paymentID).
                Scan(&orderID, &payStatus, &payMethod, &payAmount)
        if err != nil {
                return nil, fmt.Errorf("payment %d: %w", paymentID, err)
        }
        if payStatus == models.PaymentCompleted {
                // CRITICAL: release the tx's connection BEFORE any new query —
                // with a single pooled connection, querying while this tx is open
                // self-deadlocks (the bug class this codebase is hardened against).
                tx.Rollback()
                return s.GetOrder(orderID) // idempotent: already completed
        }
        // Money after void: a voided sale can never be completed — the stock
        // was restored and the till shows it gone. Mark the payment FAILED so
        // staff see a refund requirement instead of a phantom completed sale.
        var orderStatus string
        if err := tx.QueryRow(`SELECT status FROM orders WHERE id = ?`, orderID).Scan(&orderStatus); err != nil {
                return nil, err
        }
        if orderStatus == models.OrderVoided {
                tx.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = 'paid after void — refund required' WHERE id = ?`), paymentID)
                tx.Commit()
                s.Audit(0, "system", "PAYMENT_AFTER_VOID", "order", fmt.Sprint(orderID),
                        "money arrived for a voided order — refund from the provider dashboard")
                return nil, fmt.Errorf("%w: order was voided — refund this payment, do not re-open the sale", ErrInvalidState)
        }

        // Receipt uniqueness (dedupe manual re-entry / double callbacks).
        if receipt != "" {
                var other int64
                err := tx.QueryRow(s.db.Rebind(`SELECT id FROM payments WHERE mpesa_receipt = ? AND id != ?`), receipt, paymentID).Scan(&other)
                if err == nil {
                        return nil, fmt.Errorf("%w: %s", ErrDuplicateReceipt, receipt)
                }
                if err != sql.ErrNoRows {
                        return nil, err
                }
        }

        // Amount mismatch → flag, don't block (the money moved; staff must see it).
        // An unknown/zero amount is UNVERIFIED — public callback endpoints must
        // never wave a payment through silently; flag it for staff review.
        discrepancy := 0
        if amountCents <= 0 || amountCents != payAmount {
                discrepancy = 1
        }

        // Guarded order transition — exactly one winner.
        res, err := tx.Exec(`UPDATE orders SET status = 'PAID', paid_at = ? WHERE id = ? AND status = 'PENDING'`,
                nowStamp(), orderID)
        if err != nil {
                return nil, err
        }
        won, _ := res.RowsAffected()
        if won != 1 {
                // Lost the race or the state moved. A PAID order that receives a
                // second completing payment is a double-payment (flag it); a
                // VOIDED order here means money landed after a void (refund path).
                var nowStatus string
                if err := tx.QueryRow(`SELECT status FROM orders WHERE id = ?`, orderID).Scan(&nowStatus); err != nil {
                        return nil, err
                }
                if nowStatus != models.OrderPending {
                        discrepancy = 1
                        tx.Exec(`UPDATE orders SET discrepancy = 1 WHERE id = ?`, orderID)
                }
        }
        if won == 1 {
                // Deduct stock exactly once (guarded).
                items, err := s.loadItemsTx(tx, orderID)
                if err != nil {
                        return nil, err
                }
                for _, it := range items {
                        var track int
                        if err := tx.QueryRow(`SELECT COALESCE(track_stock,1) FROM products WHERE id = ?`, it.ProductID).Scan(&track); err != nil {
                                return nil, err
                        }
                        if track != 1 {
                                continue
                        }
                        gres, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty = stock_qty - ? WHERE id = ? AND stock_qty >= ?`),
                                it.Qty, it.ProductID, it.Qty)
                        if err != nil {
                                return nil, err
                        }
                        if n, _ := gres.RowsAffected(); n != 1 {
                                // Money received; stock can't cover. Flag for staff, keep paid.
                                discrepancy = 1
                                tx.Exec(`UPDATE orders SET discrepancy = 1 WHERE id = ?`, orderID)
                        }
                }
                // Loyalty earns on EVERY paid order tied to a customer —
                // cash, M-Pesa, credit, and tab settles alike (once, guarded
                // by the same transition that deducts stock).
                if err := s.earnLoyaltyTx(tx, orderID); err != nil {
                        return nil, err
                }
        }

        if receipt != "" {
                _, err = tx.Exec(s.db.Rebind(`UPDATE payments SET status = 'COMPLETED', mpesa_receipt = ?, completed_at = ?, result_desc = ?, discrepancy = ? WHERE id = ? AND status != 'COMPLETED'`),
                        receipt, nowStamp(), truncStr(resultDesc, 200), discrepancy, paymentID)
        } else {
                _, err = tx.Exec(s.db.Rebind(`UPDATE payments SET status = 'COMPLETED', completed_at = ?, result_desc = ?, discrepancy = ? WHERE id = ? AND status != 'COMPLETED'`),
                        nowStamp(), truncStr(resultDesc, 200), discrepancy, paymentID)
        }
        if err != nil {
                return nil, err
        }
        if discrepancy == 1 {
                tx.Exec(`UPDATE orders SET discrepancy = 1 WHERE id = ?`, orderID)
        }
        // Gift-card products mint one redeemable code per unit (async pay
        // paths: tab settle, M-Pesa, Paystack, split with an async leg) —
        // WINNER ONLY: a completing leg that lost the PENDING→PAID race (the
        // order was already completed by another leg) must not mint a second
        // set of spendable codes for the same basket.
        if won == 1 {
                if err := issueGiftCardsTx(tx, s.db.Rebind, orderID); err != nil {
                        return nil, err
                }
        }
        if err := tx.Commit(); err != nil {
                return nil, err
        }

        order, err := s.GetOrder(orderID)
        if err != nil {
                return nil, err
        }
        s.printer.Enqueue(order)
        if payMethod == models.MethodCash {
                // Best-effort drawer kick after commit — never fails the sale.
                if err := s.printer.Kick(); err != nil {
                        s.Audit(0, payMethod, "DRAWER_KICK_FAILED", "order", order.Number, err.Error())
                }
        }
        s.broadcast(EventOrderPaid, order)
        s.Audit(0, payMethod, "PAYMENT_COMPLETED", "order", order.Number, fmt.Sprintf("payment %d, receipt %s, amount %d", paymentID, receipt, payAmount))
        s.EmitOrder(orderID)
        s.emitLedgerForOrder(orderID) // the loyalty earn (and any other movement) follows the team
        return order, nil
}

func (s *Service) loadItemsTx(tx *sql.Tx, orderID int64) ([]models.OrderItem, error) {
        rows, err := tx.Query(`SELECT product_id, qty FROM order_items WHERE order_id = ?`, orderID)
        if err != nil {
                return nil, err
        }
        defer rows.Close()
        var out []models.OrderItem
        for rows.Next() {
                var it models.OrderItem
                if err := rows.Scan(&it.ProductID, &it.Qty); err != nil {
                        return nil, err
                }
                out = append(out, it)
        }
        return out, rows.Err()
}

// ManualConfirm completes an order from a cashier-typed 10-character
// receipt code (customer paid at the till/paybill manually).
func (s *Service) ManualConfirm(orderID int64, rawCode string, p *auth.Principal) (*models.Order, error) {
        code := mpesa.CanonicalReceipt(rawCode)
        if err := mpesa.ValidateReceiptCode(code); err != nil {
                return nil, err
        }
        order, err := s.GetOrder(orderID)
        if err != nil {
                return nil, err
        }
        if order.Status != models.OrderPending {
                if order.Status == models.OrderPaid {
                        return nil, ErrOrderAlreadyPaid
                }
                return nil, fmt.Errorf("%w: cannot confirm payment on %s order", ErrInvalidState, order.Status)
        }
        // Prefer the order's pending payment (keeps the audit trail on one row);
        // otherwise create a manual payment row.
        var pay *models.Payment
        for i := range order.Payments {
                if order.Payments[i].Status == models.PaymentPending {
                        pay = &order.Payments[i]
                }
        }
        if pay == nil {
                // No pending leg: collect the REMAINING balance, not the order
                // total — a partially-paid split (cash done, async leg failed)
                // would otherwise insert a full-total manual leg and the tender
                // sums would overshoot the sale (KES 700 collected for KES 500).
                amount := order.TotalCents
                for i := range order.Payments {
                        if order.Payments[i].Status == models.PaymentCompleted {
                                amount -= order.Payments[i].AmountCents
                        }
                }
                if amount <= 0 {
                        return nil, fmt.Errorf("%w: order has nothing left to collect", ErrInvalidState)
                }
                res, err := s.db.Exec(s.db.Rebind(`INSERT INTO payments (order_id, method, mode, amount_cents, status, created_at)
                        VALUES (?, 'mpesa', 'manual', ?, 'PENDING', ?)`), orderID, amount, nowStamp())
                if err != nil {
                        return nil, err
                }
                id, _ := res.LastInsertId()
                pay = &models.Payment{ID: id, AmountCents: amount}
        }
        result, err := s.completePayment(pay.ID, code, pay.AmountCents, "Manual receipt entry")
        if err != nil {
                return nil, err
        }
        s.Audit(p.ID, p.Username, "PAYMENT_MANUAL", "order", order.Number, "receipt "+code)
        return result, nil
}

// Void cancels an order; PAID orders restore stock, payments become VOIDED.
func (s *Service) Void(orderID int64, reason string, p *auth.Principal) (*models.Order, error) {
        tx, err := s.db.Begin()
        if err != nil {
                return nil, err
        }
        defer tx.Rollback()

        var status string
        if err := tx.QueryRow(`SELECT status FROM orders WHERE id = ?`, orderID).Scan(&status); err != nil {
                return nil, ErrNotFound
        }
        if status == models.OrderVoided {
                return nil, fmt.Errorf("%w: order already voided", ErrInvalidState)
        }
        res, err := tx.Exec(`UPDATE orders SET status = 'VOIDED', voided_at = ?, void_reason = ? WHERE id = ? AND status != 'VOIDED'`,
                nowStamp(), reason, orderID)
        if err != nil {
                return nil, err
        }
        if n, _ := res.RowsAffected(); n != 1 {
                return nil, ErrInvalidState
        }
        items, err := s.loadItemsTx(tx, orderID)
        if err != nil {
                return nil, err
        }
        if status == models.OrderPaid {
                for _, it := range items {
                        if _, err := tx.Exec(s.db.Rebind(`UPDATE products SET stock_qty = stock_qty + ? WHERE id = ?`), it.Qty, it.ProductID); err != nil {
                                return nil, err
                        }
                }
        }
        // Collect the void-with-money signal BEFORE flipping the legs: a PAID
        // order whose online leg (Paystack/M-Pesa) already COMPLETED means real
        // money landed and is now being reversed — settlement still arrives at
        // the provider, so staff must see the refund obligation in reports
        // (discrepancy flag), not dig through audit_log.
        collectedOnline := 0
        if status == models.OrderPaid {
                tx.QueryRow(`SELECT COUNT(*) FROM payments WHERE order_id = ? AND status = 'COMPLETED'
                        AND method IN ('paystack','mpesa') AND amount_cents > 0`, orderID).Scan(&collectedOnline)
        }
        if _, err := tx.Exec(`UPDATE payments SET status = 'VOIDED', result_desc = ? WHERE order_id = ? AND status IN ('PENDING','COMPLETED')`,
                "voided: "+truncStr(reason, 180), orderID); err != nil {
                return nil, err
        }
        if collectedOnline > 0 {
                tx.Exec(`UPDATE orders SET discrepancy = 1 WHERE id = ?`, orderID)
                tx.Exec(s.db.Rebind(`UPDATE payments SET result_desc = result_desc || ' — refund owed via provider' WHERE order_id = ? AND status = 'VOIDED' AND method IN ('paystack','mpesa')`), orderID)
        }
        // Gift cards minted by this sale stop being shop liability the moment
        // the sale is voided — an ACTIVE code redeemable after its sale
        // vanished would mint store credit out of thin air.
        if status == models.OrderPaid {
                rowsGC, err := tx.Query(`SELECT code FROM gift_cards WHERE order_id = ? AND status = 'ACTIVE'`, orderID)
                if err != nil {
                        return nil, err
                }
                var voidedCodes []string
                for rowsGC.Next() {
                        var code string
                        if rowsGC.Scan(&code) == nil {
                                voidedCodes = append(voidedCodes, code)
                        }
                }
                rowsGC.Close()
                if len(voidedCodes) > 0 {
                        if _, err := tx.Exec(`UPDATE gift_cards SET status = 'VOID', remaining_cents = 0, redeemed_at = ? WHERE order_id = ? AND status = 'ACTIVE'`,
                                nowStamp(), orderID); err != nil {
                                return nil, err
                        }
                        s.Audit(p.ID, p.Username, "GIFT_CARDS_VOIDED", "order", fmt.Sprint(orderID),
                                fmt.Sprintf("%d code(s): %s", len(voidedCodes), truncStr(strings.Join(voidedCodes, ","), 160)))
                }
        }
        // Tab void: reverse the ledger charge so a cancelled sale leaves no
        // debt on the customer's balance. (Stock needs no restore: PENDING
        // tabs never deduct; settled tabs restore via the PAID path above.)
        var tabCustomer, tabTotal int64
        var tabCount int
        if err := tx.QueryRow(`SELECT customer_id, total_cents FROM orders WHERE id = ?`, orderID).
                Scan(&tabCustomer, &tabTotal); err != nil {
                return nil, err
        }
        if tabCustomer != 0 {
                // Refund the store credit this sale spent. The flip above set
                // every PENDING/COMPLETED leg to VOIDED but left amount_cents
                // intact — so sum the VOIDED credit legs. (Summing with a
                // PENDING/COMPLETED filter here would always read 0 and the
                // customer's prepaid credit would silently vanish.)
                var creditUsed int64
                if err := tx.QueryRow(`SELECT COALESCE(SUM(amount_cents),0) FROM payments
                        WHERE order_id = ? AND method = 'credit' AND status = 'VOIDED'`, orderID).
                        Scan(&creditUsed); err != nil {
                        return nil, err
                }
                if creditUsed > 0 {
                        if err := recordLedgerTx(tx, s.db.Rebind, tabCustomer, orderID, models.LedgerCreditTopup,
                                creditUsed, 0, "store credit refund (void)", p.ID); err != nil {
                                return nil, err
                        }
                }
                if err := tx.QueryRow(`SELECT COUNT(*) FROM payments WHERE order_id = ? AND method = 'account'`,
                        orderID).Scan(&tabCount); err != nil {
                        return nil, err
                }
                if tabCount > 0 {
                        if err := recordLedgerTx(tx, s.db.Rebind, tabCustomer, orderID, models.LedgerAdjust,
                                -tabTotal, 0, "void reversal", p.ID); err != nil {
                                return nil, err
                        }
                }
                // Reverse loyalty points EARNED on this order — otherwise a
                // buy→void cycle mints points out of thin air. Only positive
                // earn entries are reversed (redemptions are handled above).
                if status == models.OrderPaid {
                        var ptsEarned int64
                        if err := tx.QueryRow(`SELECT COALESCE(SUM(points_delta),0) FROM customer_ledger
                                WHERE order_id = ? AND kind = ? AND points_delta > 0`, orderID, models.LedgerLoyalty).
                                Scan(&ptsEarned); err != nil {
                                return nil, err
                        }
                        if ptsEarned > 0 {
                                if err := recordLedgerTx(tx, s.db.Rebind, tabCustomer, orderID, models.LedgerLoyalty,
                                        0, -ptsEarned, "loyalty reversal (void)", p.ID); err != nil {
                                        return nil, err
                                }
                        }
                }
                // Refund loyalty points redeemed at checkout — a cancelled
                // sale gives the points back.
                var ptsRedeemed int64
                if err := tx.QueryRow(`SELECT COALESCE(points_redeemed,0) FROM orders WHERE id = ?`, orderID).
                        Scan(&ptsRedeemed); err != nil {
                        return nil, err
                }
                if ptsRedeemed > 0 {
                        if err := recordLedgerTx(tx, s.db.Rebind, tabCustomer, orderID, models.LedgerLoyalty,
                                0, ptsRedeemed, "points refund (void)", p.ID); err != nil {
                                return nil, err
                        }
                }
                // (Store-credit refund is handled above, right after the
                // payment flip — summing PENDING/COMPLETED legs there read 0
                // and silently confiscated the customer's prepaid credit.)
        }
        if err := tx.Commit(); err != nil {
                return nil, err
        }
        s.Audit(p.ID, p.Username, "ORDER_VOIDED", "order", fmt.Sprint(orderID), reason)
        s.EmitVoid(orderID, reason)
        order, err := s.GetOrder(orderID)
        if err != nil {
                return nil, err
        }
        s.broadcast(EventOrderVoided, order)
        return order, nil
}

// HandleCallback processes a Daraja webhook (public endpoint).
func (s *Service) HandleCallback(cb mpesa.CallbackResult) (*models.Order, error) {
        var paymentID int64
        err := s.db.QueryRow(s.db.Rebind(`SELECT id FROM payments WHERE checkout_request_id = ?`), cb.CheckoutRequestID).Scan(&paymentID)
        if err != nil {
                return nil, fmt.Errorf("callback for unknown checkout %s: %w", cb.CheckoutRequestID, err)
        }
        if cb.ResultCode != 0 {
                // Customer cancelled / failed — payment FAILED, order stays PENDING
                // so the cashier can retry STK, take a manual code, or void.
                s.db.Exec(s.db.Rebind(`UPDATE payments SET status = 'FAILED', result_desc = ? WHERE id = ? AND status = 'PENDING'`),
                        truncStr(cb.ResultDesc, 200), paymentID)
                return s.GetOrderByPayment(paymentID)
        }
        return s.completePayment(paymentID, cb.MpesaReceipt, cb.AmountCents, cb.ResultDesc)
}

func truncStr(str string, n int) string {
        if len(str) > n {
                return str[:n]
        }
        return str
}

// max64 returns the larger of two int64 values.
func max64(a, b int64) int64 {
        if a > b {
                return a
        }
        return b
}

// earnLoyaltyTx records the loyalty earn for a paid order inside the
// caller's transaction. Every caller guards the PENDING→PAID transition
// first, so the earn happens exactly once per order.
func (s *Service) earnLoyaltyTx(tx *sql.Tx, orderID int64) error {
        var custID, orderTotal int64
        if err := tx.QueryRow(`SELECT COALESCE(customer_id,0), total_cents FROM orders WHERE id = ?`, orderID).
                Scan(&custID, &orderTotal); err != nil {
                return err
        }
        if custID == 0 || !s.settings.GetBool("loyalty_enabled", true) {
                return nil
        }
        per := s.settings.GetInt("loyalty_earn_per_cents", 10000)
        if per <= 0 {
                return nil
        }
        earn := orderTotal / int64(per)
        if earn <= 0 {
                return nil
        }
        return recordLedgerTx(tx, s.db.Rebind, custID, orderID, models.LedgerLoyalty,
                0, earn, "loyalty earned "+fmt.Sprint(orderID), 0)
}
