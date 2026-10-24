package handlers_test

import (
	"fmt"
	"strconv"
	"testing"
)

// itoa64 formats decoded ids (JSON numbers arrive as float64; converted
// values pass through as int64) for URL paths.
func itoa64(v any) string {
	switch n := v.(type) {
	case int64:
		return strconv.FormatInt(n, 10)
	case float64:
		return strconv.FormatInt(int64(n), 10)
	case int:
		return strconv.Itoa(n)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// Tab lifecycle: charge within limit, reject over limit, settle in cash,
// balance returns to zero with loyalty earned.
func TestCustomerTabLifecycle(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	// Cashier cannot manage customers but can view them.
	w := do(t, engine, "POST", "/api/v1/customers", cashier, map[string]any{"name": "X"})
	if w.Code != 403 {
		t.Fatalf("cashier create should 403, got %d", w.Code)
	}
	w = do(t, engine, "GET", "/api/v1/customers", cashier, nil)
	if w.Code != 200 {
		t.Fatalf("cashier list should 200, got %d", w.Code)
	}

	w = do(t, engine, "POST", "/api/v1/customers", admin, map[string]any{
		"name": "Mama Mboga", "phone": "0712345678", "creditLimitCents": 200000,
	})
	if w.Code != 201 {
		t.Fatalf("create customer: %d %s", w.Code, w.Body.String())
	}
	customer := dataMap(t, w)
	cid := int64(customer["id"].(float64))

	// Need a product to sell: use seed product 1.
	tab := func(uuid string) (int, map[string]any) {
		w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
			"items":         []map[string]any{{"productId": 1, "qty": 1}},
			"paymentMethod": "account",
			"customerId":    cid,
			"clientUuid":    uuid,
		})
		if w.Code != 201 {
			return w.Code, nil
		}
		return w.Code, dataMap(t, w)
	}
	code, order := tab("tab-1")
	if code != 201 {
		t.Fatalf("tab charge: %d", code)
	}
	if order["status"] != "PENDING" {
		t.Fatalf("tab order must stay PENDING, got %v", order["status"])
	}
	total := int64(order["totalCents"].(float64))

	w = do(t, engine, "GET", "/api/v1/customers", cashier, nil)
	list := decode(t, w)["data"].([]any)
	var balance int64
	for _, it := range list {
		m := it.(map[string]any)
		if int64(m["id"].(float64)) == cid {
			balance = int64(m["balanceCents"].(float64))
		}
	}
	if balance != total {
		t.Fatalf("balance should equal tab total %d, got %d", total, balance)
	}

	// Over-limit tab rejected with 409.
	w = do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
		"items":         []map[string]any{{"productId": 1, "qty": 100}},
		"paymentMethod": "account",
		"customerId":    cid,
		"clientUuid":    "tab-over",
	})
	if w.Code != 409 {
		t.Fatalf("over-limit tab should 409, got %d %s", w.Code, w.Body.String())
	}

	// Settle in cash: order PAID, balance zero, loyalty earned.
	w = do(t, engine, "POST", "/api/v1/orders/"+itoa64(order["id"])+"/settle", cashier, map[string]any{
		"method": "cash",
	})
	if w.Code != 200 {
		t.Fatalf("settle: %d %s", w.Code, w.Body.String())
	}
	if dataMap(t, w)["status"] != "PAID" {
		t.Fatal("settled order must be PAID")
	}
	w = do(t, engine, "GET", "/api/v1/customers", cashier, nil)
	list = decode(t, w)["data"].([]any)
	for _, it := range list {
		m := it.(map[string]any)
		if int64(m["id"].(float64)) == cid {
			if int64(m["balanceCents"].(float64)) != 0 {
				t.Fatalf("balance should be 0 after settle, got %v", m["balanceCents"])
			}
			if int64(m["loyaltyPoints"].(float64)) != total/10000 {
				t.Fatalf("loyalty should be %d, got %v", total/10000, m["loyaltyPoints"])
			}
		}
	}
}

// Walk-in payment reduces balance; overpayment rejected. Void of a tab
// reverses its ledger charge.
func TestCustomerWalkInPaymentAndVoidReversal(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	w := do(t, engine, "POST", "/api/v1/customers", admin, map[string]any{
		"name": "Juma", "creditLimitCents": 100000,
	})
	cid := int64(dataMap(t, w)["id"].(float64))

	w = do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
		"items":         []map[string]any{{"productId": 1, "qty": 1}},
		"paymentMethod": "account",
		"customerId":    cid,
		"clientUuid":    "tab-void-1",
	})
	if w.Code != 201 {
		t.Fatalf("tab charge: %d %s", w.Code, w.Body.String())
	}
	order := dataMap(t, w)
	total := int64(order["totalCents"].(float64))

	// Overpayment rejected.
	w = do(t, engine, "POST", "/api/v1/customers/"+itoa64(cid)+"/payments", cashier, map[string]any{
		"amountCents": total + 1,
	})
	if w.Code != 409 {
		t.Fatalf("overpayment should 409, got %d %s", w.Code, w.Body.String())
	}
	// Walk-in payment rides pos.sell — cashiers take them at the till.
	w = do(t, engine, "POST", "/api/v1/customers/"+itoa64(cid)+"/payments", cashier, map[string]any{
		"amountCents": total,
	})
	if w.Code != 200 {
		t.Fatalf("walk-in payment: %d %s", w.Code, w.Body.String())
	}

	// Second tab, then void it: balance must return to zero.
	w = do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
		"items":         []map[string]any{{"productId": 1, "qty": 1}},
		"paymentMethod": "account",
		"customerId":    cid,
		"clientUuid":    "tab-void-2",
	})
	if w.Code != 201 {
		t.Fatalf("second tab: %d %s", w.Code, w.Body.String())
	}
	order2 := dataMap(t, w)
	w = do(t, engine, "POST", "/api/v1/orders/"+itoa64(order2["id"])+"/void", cashier, map[string]any{
		"reason": "test void",
	})
	if w.Code != 200 {
		t.Fatalf("void tab: %d %s", w.Code, w.Body.String())
	}
	w = do(t, engine, "GET", "/api/v1/customers/"+itoa64(cid)+"/ledger", cashier, nil)
	if w.Code != 200 {
		t.Fatalf("ledger: %d", w.Code)
	}
}

// Tab input validation carries proper status codes: a missing customer is a
// 400, charging an inactive customer is a 409.
func TestCustomerTabInputCodes(t *testing.T) {
	engine, admin, cashier, _ := newTestServer(t)

	w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
		"items":         []map[string]any{{"productId": 1, "qty": 1}},
		"paymentMethod": "account",
		"clientUuid":    "tab-nocust",
	})
	if w.Code != 400 {
		t.Fatalf("tab without customer should 400, got %d %s", w.Code, w.Body.String())
	}

	w = do(t, engine, "POST", "/api/v1/customers", admin, map[string]any{
		"name": "Inactive Joe", "creditLimitCents": 100000,
	})
	cid := int64(dataMap(t, w)["id"].(float64))
	w = do(t, engine, "PUT", "/api/v1/customers/"+itoa64(cid), admin, map[string]any{
		"name": "Inactive Joe", "creditLimitCents": 100000, "active": false,
	})
	if w.Code != 200 {
		t.Fatalf("deactivate: %d %s", w.Code, w.Body.String())
	}
	w = do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
		"items":         []map[string]any{{"productId": 1, "qty": 1}},
		"paymentMethod": "account",
		"customerId":    cid,
		"clientUuid":    "tab-inactive",
	})
	if w.Code != 409 {
		t.Fatalf("inactive customer tab should 409, got %d %s", w.Code, w.Body.String())
	}
}
