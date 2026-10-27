package handlers_test

// Regression tests for the Oct-2026 product-creation outage:
//
//      "We can't add products — constraint failed: UNIQUE constraint
//       failed: products.sku (2067)"
//
// Root cause: the blank-SKU generator walked a FIXED window
// (SKU-100000..SKU-100049) and, when exhausted, handed out the constant
// SKU-999999. The fleet's live catalog had consumed all 50 slots and
// SKU-999999 was a real product ("KRA - Pin Registration"), so every
// create failed with the raw constraint error. The generator is now
// date+random (services.AutoSKU, unbounded), creates regenerate+retry on
// a lost race, and duplicates return a readable 409 instead of a 500
// constraint dump.

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// legacyGeneratedSKUs is the exact SKU space the pre-fix generator could
// hand out: the 50-slot fixed window plus its constant fallback.
func legacyGeneratedSKUs() []string {
	out := []string{"SKU-999999"}
	for i := 0; i < 50; i++ {
		out = append(out, fmt.Sprintf("SKU-%06d", 100000+i))
	}
	return out
}

// TestCreateProductAutoSKUSurvivesExhaustedWindow reproduces the fleet's
// production state (window full, fallback taken) and proves blank-SKU
// creates succeed again with fresh, unique SKUs.
func TestCreateProductAutoSKUSurvivesExhaustedWindow(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)

	for _, sku := range legacyGeneratedSKUs() {
		w := do(t, engine, "POST", "/api/v1/products", admin, map[string]any{
			"name": "legacy " + sku, "sku": sku, "priceCents": 100,
		})
		if w.Code != 201 {
			t.Fatalf("seed %s: %d %s", sku, w.Code, w.Body.String())
		}
	}

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		w := do(t, engine, "POST", "/api/v1/products", admin, map[string]any{
			"name": fmt.Sprintf("Certificate of Good Conduct #%d", i+1),
			"sku":  "", "priceCents": 200,
		})
		if w.Code != 201 {
			t.Fatalf("blank-sku create #%d: %d %s", i+1, w.Code, w.Body.String())
		}
		sku, _ := dataMap(t, w)["sku"].(string)
		if sku == "" {
			t.Fatalf("create #%d returned no sku", i+1)
		}
		if seen[sku] {
			t.Fatalf("create #%d reused sku %q", i+1, sku)
		}
		seen[sku] = true
		for _, legacy := range legacyGeneratedSKUs() {
			if sku == legacy {
				t.Fatalf("create #%d handed out legacy sku %q", i+1, sku)
			}
		}
	}
}

// TestCreateProductDuplicateExplicitSKU409: a SKU the user typed that is
// already taken must be a readable 409, never a raw constraint dump.
func TestCreateProductDuplicateExplicitSKU409(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)

	first := do(t, engine, "POST", "/api/v1/products", admin, map[string]any{
		"name": "DTF Print A4", "sku": "DTF-A4", "priceCents": 500,
	})
	if first.Code != 201 {
		t.Fatalf("seed create: %d %s", first.Code, first.Body.String())
	}

	dup := do(t, engine, "POST", "/api/v1/products", admin, map[string]any{
		"name": "Another Product", "sku": "DTF-A4", "priceCents": 300,
	})
	if dup.Code != 409 {
		t.Fatalf("duplicate sku: want 409, got %d %s", dup.Code, dup.Body.String())
	}
	if !strings.Contains(dup.Body.String(), "SKU already exists") {
		t.Fatalf("duplicate sku message not friendly: %s", dup.Body.String())
	}
}

// TestCreateProductAutoSKUsDistinctUnderConcurrency: parallel blank-SKU
// creates (two clerks at two tills) must all succeed with distinct SKUs —
// the lost-race path regenerates instead of leaking the constraint error.
func TestCreateProductAutoSKUsDistinctUnderConcurrency(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)

	const n = 12
	type result struct {
		code int
		sku  string
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := do(t, engine, "POST", "/api/v1/products", admin, map[string]any{
				"name": fmt.Sprintf("Parallel Product %d", i+1),
				"sku":  "", "priceCents": 100,
			})
			r := result{code: w.Code}
			if w.Code == 201 {
				r.sku, _ = dataMap(t, w)["sku"].(string)
			}
			results[i] = r
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for i, r := range results {
		if r.code != 201 {
			t.Fatalf("parallel create #%d: code %d", i+1, r.code)
		}
		if r.sku == "" || seen[r.sku] {
			t.Fatalf("parallel create #%d: empty or duplicate sku %q", i+1, r.sku)
		}
		seen[r.sku] = true
	}
}

// TestUpdateProductDuplicateSKU409: renaming product B onto product A's
// SKU must 409 (the SKU is the cross-till sync identity — silently
// overwriting it would fork the catalog). A blank SKU on update keeps the
// product's existing SKU.
func TestUpdateProductDuplicateSKU409(t *testing.T) {
	engine, admin, _, _ := newTestServer(t)

	a5CreateProduct(t, engine, admin, map[string]any{
		"name": "Product A", "sku": "AAA-2", "priceCents": 100,
	})
	idB := a5CreateProduct(t, engine, admin, map[string]any{
		"name": "Product B", "sku": "BBB-2", "priceCents": 100,
	})

	w := do(t, engine, "PUT", fmt.Sprintf("/api/v1/products/%d", idB), admin, map[string]any{
		"name": "Product B Renamed", "sku": "AAA-2", "priceCents": 100,
	})
	if w.Code != 409 {
		t.Fatalf("update onto taken sku: want 409, got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "SKU already exists") {
		t.Fatalf("update duplicate message not friendly: %s", w.Body.String())
	}

	// Blank SKU on update keeps the product's existing SKU.
	keep := do(t, engine, "PUT", fmt.Sprintf("/api/v1/products/%d", idB), admin, map[string]any{
		"name": "Product B Renamed", "sku": "", "priceCents": 100,
	})
	if keep.Code != 200 {
		t.Fatalf("blank-sku update: %d %s", keep.Code, keep.Body.String())
	}
}
