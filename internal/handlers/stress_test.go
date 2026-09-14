package handlers_test

import (
        "fmt"
        "sync"
        "sync/atomic"
        "testing"
)

// Stress: N cashiers race for the LAST unit of a tracked product. Exactly
// one checkout may succeed; stock must end at exactly 0, never negative.
// This pins the guarded-deduction claim (WHERE status != 'PAID').
func TestStressLastUnitRace(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)

        w := do(t, engine, "POST", "/api/v1/products", admin, map[string]any{
                "name": "Stress Last Unit", "categoryId": 1, "priceCents": 100,
                "stockQty": 1, "trackStock": true,
        })
        if w.Code != 201 {
                t.Fatalf("create product: %d %s", w.Code, w.Body.String())
        }
        prods := productIDs(t, engine, cashier, "Stress Last Unit")
        pid := int(prods[0]["id"].(float64))

        const racers = 25
        var okCount atomic.Int32
        var conflictCount atomic.Int32
        var otherCount atomic.Int32
        start := make(chan struct{})
        var wg sync.WaitGroup
        for i := 0; i < racers; i++ {
                wg.Add(1)
                go func(i int) {
                        defer wg.Done()
                        <-start
                        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                                "items":          []map[string]any{{"productId": pid, "qty": 1}},
                                "paymentMethod":  "cash",
                                "clientUuid":     fmt.Sprintf("stress-race-%d", i),
                        })
                        switch w.Code {
                        case 201:
                                okCount.Add(1)
                        case 409:
                                conflictCount.Add(1)
                        default:
                                otherCount.Add(1)
                                t.Errorf("racer %d: unexpected code %d %s", i, w.Code, w.Body.String())
                        }
                }(i)
        }
        close(start)
        wg.Wait()

        if got := okCount.Load(); got != 1 {
                t.Fatalf("exactly one racer must win, got %d successes (%d conflicts, %d other)",
                        got, conflictCount.Load(), otherCount.Load())
        }
        prods = productIDs(t, engine, cashier, "Stress Last Unit")
        if stock := int(prods[0]["stockQty"].(float64)); stock != 0 {
                t.Fatalf("stock must end at 0, got %d", stock)
        }
}

// Stress: one client_uuid submitted concurrently 20x collapses to a single
// order with a single stock deduction.
func TestStressSyncBurstIdempotent(t *testing.T) {
        engine, admin, cashier, _ := newTestServer(t)

        w := do(t, engine, "POST", "/api/v1/products", admin, map[string]any{
                "name": "Stress Burst", "categoryId": 1, "priceCents": 100,
                "stockQty": 50, "trackStock": true,
        })
        if w.Code != 201 {
                t.Fatalf("create product: %d %s", w.Code, w.Body.String())
        }
        pid := int(productIDs(t, engine, cashier, "Stress Burst")[0]["id"].(float64))

        const burst = 20
        ids := make([]any, burst)
        var failed atomic.Int32
        start := make(chan struct{})
        var wg sync.WaitGroup
        for i := 0; i < burst; i++ {
                wg.Add(1)
                go func(i int) {
                        defer wg.Done()
                        <-start
                        w := do(t, engine, "POST", "/api/v1/orders/checkout", cashier, map[string]any{
                                "items":          []map[string]any{{"productId": pid, "qty": 1}},
                                "paymentMethod":  "cash",
                                "clientUuid":     "stress-burst-1",
                        })
                        if w.Code != 201 {
                                failed.Add(1)
                                t.Errorf("burst %d: code %d %s", i, w.Code, w.Body.String())
                                return
                        }
                        ids[i] = dataMap(t, w)["id"]
                }(i)
        }
        close(start)
        wg.Wait()
        if failed.Load() != 0 {
                t.Fatalf("%d burst requests failed", failed.Load())
        }
        for i := 1; i < burst; i++ {
                if ids[i] != ids[0] {
                        t.Fatalf("burst split into multiple orders: %v vs %v", ids[0], ids[i])
                }
        }
        prods := productIDs(t, engine, cashier, "Stress Burst")
        if stock := int(prods[0]["stockQty"].(float64)); stock != 49 {
                t.Fatalf("burst must deduct exactly once (50->49), got %d", stock)
        }
}

// Stress: a burst of logins stays healthy. Every response must be either a
// success or an explicit rate-limit — never a 500 or a hang.
func TestStressLoginBurst(t *testing.T) {
        engine, _, _, _ := newTestServer(t)

        const burst = 30
        var ok, limited, other atomic.Int32
        start := make(chan struct{})
        var wg sync.WaitGroup
        for i := 0; i < burst; i++ {
                wg.Add(1)
                go func() {
                        defer wg.Done()
                        <-start
                        w := do(t, engine, "POST", "/api/v1/auth/login", "", map[string]any{
                                "username": "cashier", "password": "cashier123",
                        })
                        switch w.Code {
                        case 200:
                                ok.Add(1)
                        case 429:
                                limited.Add(1)
                        default:
                                other.Add(1)
                                t.Errorf("login burst: code %d %s", w.Code, w.Body.String())
                        }
                }()
        }
        close(start)
        wg.Wait()
        t.Logf("login burst: %d ok, %d rate-limited, %d other", ok.Load(), limited.Load(), other.Load())
        if other.Load() != 0 {
                t.Fatalf("%d login attempts failed unexpectedly", other.Load())
        }
        if ok.Load() == 0 {
                t.Fatal("rate limiter rejected the entire legitimate burst")
        }
}
