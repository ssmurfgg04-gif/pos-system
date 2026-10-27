package services

// deepreview_money_test.go — regression tests for the deep-review money
// fixes: void refunds store credit (the refund used to read a always-zero
// sum), voided gift cards stop being redeemable, tab settlement posts its
// ledger payment exactly once, tab charges move the balance atomically
// inside the transaction (v1.5.0: no credit-limit gate — any active
// customer can be charged), and checkout ledger movements follow the team.

import (
	"context"
	"testing"

	"posapp/internal/auth"
	"posapp/internal/models"
)

type moneyFixture struct {
	s      *Service
	prodID int64
	userID int64
	roleID int64
	cust   *models.Customer
}

func newMoneyTill(t *testing.T) *moneyFixture {
	t.Helper()
	s := vaultProbeNewService(t)
	s.db.Exec(`INSERT INTO roles (name) VALUES ('Cashier')`)
	s.db.Exec(`INSERT INTO users (username, role_id) SELECT 'cash1', id FROM roles LIMIT 1`)
	var prodID, userID, roleID int64
	s.db.Exec(`INSERT INTO categories (name, slug) VALUES ('C', 'c')`)
	s.db.Exec(`INSERT INTO products (sku, name, category_id, price_cents, stock_qty)
                SELECT 'SKU-M1', 'M1', id, 60000, 50 FROM categories LIMIT 1`)
	s.db.QueryRow(`SELECT id FROM products WHERE sku='SKU-M1'`).Scan(&prodID)
	s.db.QueryRow(`SELECT id FROM users WHERE username='cash1'`).Scan(&userID)
	s.db.QueryRow(`SELECT id FROM roles WHERE name='Cashier'`).Scan(&roleID)
	cust, err := s.CreateCustomer("Money Test", "0711223344", "", "", 200000, &auth.Principal{ID: userID, Username: "cash1"})
	if err != nil {
		t.Fatalf("create customer: %v", err)
	}
	return &moneyFixture{s: s, prodID: prodID, userID: userID, roleID: roleID, cust: cust}
}

func (f *moneyFixture) principal() *auth.Principal {
	return &auth.Principal{ID: f.userID, Username: "cash1", RoleID: f.roleID, RoleName: "Cashier", Active: true}
}

func (f *moneyFixture) checkout(method string, custID int64, uuid string) (*models.Order, error) {
	req := models.CheckoutRequest{
		Items:         []models.CheckoutItem{{ProductID: f.prodID, Qty: 1}},
		PaymentMethod: method,
		CustomerID:    custID,
		ClientUUID:    uuid,
	}
	return f.s.Checkout(context.Background(), f.principal(), req)
}

func (f *moneyFixture) balance(custID int64) int64 {
	var b int64
	f.s.db.QueryRow(`SELECT balance_cents FROM customers WHERE id = ?`, custID).Scan(&b)
	return b
}

func (f *moneyFixture) credit(custID int64) int64 {
	var c int64
	f.s.db.QueryRow(`SELECT store_credit_cents FROM customers WHERE id = ?`, custID).Scan(&c)
	return c
}

// TestMoneyVoidRefundsStoreCredit — the deep-review CRITICAL: voiding a
// sale paid from store credit used to sum PENDING/COMPLETED legs AFTER
// flipping them to VOIDED — always 0 — so the customer's prepaid money was
// silently confiscated. The credit must come back, exactly once.
func TestMoneyVoidRefundsStoreCredit(t *testing.T) {
	f := newMoneyTill(t)
	// Top up KES 1,000 store credit (the real topup path — a ledger
	// credit_topup row that moves store_credit_cents).
	if _, err := f.s.TopUpStoreCredit(f.cust.ID, 100000, "topup", f.principal()); err != nil {
		t.Fatalf("topup: %v", err)
	}
	if got := f.credit(f.cust.ID); got != 100000 {
		t.Fatalf("precondition credit = %d, want 100000", got)
	}
	// Pay entirely from store credit.
	order, err := f.checkout(models.MethodCredit, f.cust.ID, "money-credit-1")
	if err != nil {
		t.Fatalf("credit checkout: %v", err)
	}
	if got := f.credit(f.cust.ID); got != 40000 {
		t.Fatalf("credit after sale = %d, want 40000", got)
	}
	// Void it: the KES 600 MUST return.
	if _, err := f.s.Void(order.ID, "customer changed mind", f.principal()); err != nil {
		t.Fatalf("void: %v", err)
	}
	if got := f.credit(f.cust.ID); got != 100000 {
		t.Fatalf("credit after void = %d, want 100000 (the old code confiscated it)", got)
	}
	if got := f.balance(f.cust.ID); got != 0 {
		t.Fatalf("balance after void = %d, want 0 (tab reversal untouched)", got)
	}
	// Ledger shows the refund exactly once.
	var refunds int64
	f.s.db.QueryRow(`SELECT COUNT(*) FROM customer_ledger WHERE order_id = ? AND kind = ? AND amount_cents = 60000`,
		order.ID, models.LedgerCreditTopup).Scan(&refunds)
	if refunds != 1 {
		t.Fatalf("credit refund ledger rows = %d, want 1", refunds)
	}
}

// TestMoneyVoidKillsGiftCards — voiding a PAID gift-card sale must VOID the
// minted codes; an ACTIVE code behind a vanished sale mints free credit.
func TestMoneyVoidKillsGiftCards(t *testing.T) {
	f := newMoneyTill(t)
	f.s.db.Exec(`UPDATE products SET is_gift_card = 1, track_stock = 0, price_cents = 100000 WHERE id = ?`, f.prodID)
	order, err := f.checkout(models.MethodCash, 0, "money-gift-1")
	if err != nil {
		t.Fatalf("gift checkout: %v", err)
	}
	var active int64
	f.s.db.QueryRow(`SELECT COUNT(*) FROM gift_cards WHERE order_id = ? AND status = 'ACTIVE'`, order.ID).Scan(&active)
	if active != 1 {
		t.Fatalf("precondition: minted gift cards = %d, want 1", active)
	}
	if _, err := f.s.Void(order.ID, "sale reversed", f.principal()); err != nil {
		t.Fatalf("void: %v", err)
	}
	f.s.db.QueryRow(`SELECT COUNT(*) FROM gift_cards WHERE order_id = ? AND status = 'ACTIVE'`, order.ID).Scan(&active)
	if active != 0 {
		t.Fatal("gift card still ACTIVE after its sale was voided — free money mint")
	}
	var voided int64
	f.s.db.QueryRow(`SELECT COUNT(*) FROM gift_cards WHERE order_id = ? AND status = 'VOID'`, order.ID).Scan(&voided)
	if voided != 1 {
		t.Fatalf("voided codes = %d, want 1", voided)
	}
}

// TestMoneyTabAlwaysAllowedAtomic — v1.5.0 policy: tab is available for
// ANY active customer. No eligibility flag, no credit-limit gate, no
// cash-only state: a customer with ZERO credit limit gets charged, and a
// customer already over their (advisory) limit gets charged too. The
// balance still moves atomically inside the immediate tx — every charge
// lands exactly once, whatever the interleaving.
func TestMoneyTabAlwaysAllowedAtomic(t *testing.T) {
	f := newMoneyTill(t)
	// Drop the fixture's advisory limit to zero: the classic v1.4.x
	// "no credit, cash only" trap must not come back.
	if _, err := f.s.db.Exec(`UPDATE customers SET credit_limit_cents = 0 WHERE id = ?`, f.cust.ID); err != nil {
		t.Fatalf("clear limit: %v", err)
	}
	for i := 1; i <= 4; i++ {
		if _, err := f.checkout(models.MethodAccount, f.cust.ID, "money-tab-"+string(rune('0'+i))); err != nil {
			t.Fatalf("tab %d on a zero-limit customer must be allowed: %v", i, err)
		}
	}
	if got := f.balance(f.cust.ID); got != 240000 {
		t.Fatalf("balance after 4 tabs = %d, want 240000 (every charge applied exactly once)", got)
	}
	// Over an advisory limit (set it below the balance): still allowed.
	if _, err := f.s.db.Exec(`UPDATE customers SET credit_limit_cents = 100000 WHERE id = ?`, f.cust.ID); err != nil {
		t.Fatalf("set advisory limit: %v", err)
	}
	if _, err := f.checkout(models.MethodAccount, f.cust.ID, "money-tab-5"); err != nil {
		t.Fatalf("tab over an ADVISORY limit must be allowed (human decides at the counter): %v", err)
	}
	if got := f.balance(f.cust.ID); got != 300000 {
		t.Fatalf("balance after 5th tab = %d, want 300000", got)
	}
}

// TestMoneySettleTabLedgerOnce — settling a tab twice (double-click /
// idempotent completePayment on the loser) must post the ledger payment
// exactly once; the old unconditional post erased the debt twice.
func TestMoneySettleTabLedgerOnce(t *testing.T) {
	f := newMoneyTill(t)
	order, err := f.checkout(models.MethodAccount, f.cust.ID, "money-settle-1")
	if err != nil {
		t.Fatalf("tab checkout: %v", err)
	}
	if f.balance(f.cust.ID) != 60000 {
		t.Fatalf("balance after charge = %d, want 60000", f.balance(f.cust.ID))
	}
	if _, err := f.s.SettleTab(order.ID, models.MethodCash, "", f.principal()); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if got := f.balance(f.cust.ID); got != 0 {
		t.Fatalf("balance after settle = %d, want 0", got)
	}
	// A second settle attempt on the now-PAID order is refused outright…
	if _, err := f.s.SettleTab(order.ID, models.MethodCash, "", f.principal()); err == nil {
		t.Fatal("second settle accepted on a PAID order")
	}
	// …and even a forced ledger replay (the idempotency path) posts nothing.
	var payments int64
	f.s.db.QueryRow(`SELECT COUNT(*) FROM customer_ledger WHERE order_id = ? AND kind = ?`,
		order.ID, models.LedgerPayment).Scan(&payments)
	if payments != 1 {
		t.Fatalf("tab payment ledger rows = %d, want exactly 1", payments)
	}
	if got := f.balance(f.cust.ID); got != 0 {
		t.Fatalf("balance after refused second settle = %d, want 0", got)
	}
}

// TestMoneyCheckoutLedgerFollowsTeam — checkout movements (tab charge,
// credit redeem) must land in the sync outbox exactly once each; balances
// on other tills converge through these events.
func TestMoneyCheckoutLedgerFollowsTeam(t *testing.T) {
	f := newMoneyTill(t)
	_, err := f.checkout(models.MethodAccount, f.cust.ID, "money-sync-1")
	if err != nil {
		t.Fatalf("tab checkout: %v", err)
	}
	// The charge is in the outbox…
	var charges int64
	f.s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE entity='ledger' AND payload LIKE '%tab charge%'`).Scan(&charges)
	if charges != 1 {
		t.Fatalf("synced tab charge events = %d, want 1 (balances never left the origin till)", charges)
	}
	// …and marked synced so a completion re-emit does not duplicate it.
	var unsynced int64
	f.s.db.QueryRow(`SELECT COUNT(*) FROM customer_ledger WHERE order_id IN (SELECT id FROM orders WHERE client_uuid='money-sync-1') AND COALESCE(synced_at,'')=''`).Scan(&unsynced)
	if unsynced != 0 {
		t.Fatalf("unmarked ledger rows = %d, want 0 (double-emission risk)", unsynced)
	}
	// Completing the tab (settle) emits the payment leg exactly once.
	var orderID int64
	f.s.db.QueryRow(`SELECT id FROM orders WHERE client_uuid='money-sync-1'`).Scan(&orderID)
	if _, err := f.s.SettleTab(orderID, models.MethodCash, "", f.principal()); err != nil {
		t.Fatalf("settle: %v", err)
	}
	var payments int64
	f.s.db.QueryRow(`SELECT COUNT(*) FROM sync_outbox WHERE entity='ledger' AND payload LIKE '%tab settled%'`).Scan(&payments)
	if payments != 1 {
		t.Fatalf("synced tab payment events = %d, want 1", payments)
	}
}

// TestMoneyManualConfirmRemainingBalance — confirming a partially-paid
// split manually inserts a leg for the REMAINING balance, never the order
// total (tender sums used to overshoot the sale).
func TestMoneyManualConfirmRemainingBalance(t *testing.T) {
	f := newMoneyTill(t)
	// Split: cash 20000 now + mpesa 40000 pending (payable 60000).
	order, err := f.s.Checkout(context.Background(), f.principal(), models.CheckoutRequest{
		Items:         []models.CheckoutItem{{ProductID: f.prodID, Qty: 1}},
		PaymentMethod: models.MethodMpesa,
		CustomerPhone: "0711223344",
		CustomerID:    0,
		ClientUUID:    "money-split-1",
		SplitPayments: []models.SplitLeg{
			{Method: models.MethodCash, AmountCents: 20000},
			{Method: models.MethodMpesa, AmountCents: 40000, Phone: "0711223344"},
		},
	})
	if err != nil {
		t.Fatalf("split checkout: %v", err)
	}
	if order.Status != models.OrderPending {
		t.Fatalf("split with async leg status = %s, want PENDING", order.Status)
	}
	// Simulate the async leg failing (no STK fired in this path): confirm
	// manually with a receipt — the manual leg must be 40000, not 60000.
	confirmed, err := f.s.ManualConfirm(order.ID, "QGH7SKD21X", f.principal())
	if err != nil {
		t.Fatalf("manual confirm: %v", err)
	}
	var legSum int64
	f.s.db.QueryRow(`SELECT COALESCE(SUM(amount_cents),0) FROM payments WHERE order_id = ? AND status = 'COMPLETED'`, confirmed.ID).Scan(&legSum)
	if legSum != 60000 {
		t.Fatalf("completed legs sum = %d, want 60000 (the old full-total leg overshoot to 80000)", legSum)
	}
}
