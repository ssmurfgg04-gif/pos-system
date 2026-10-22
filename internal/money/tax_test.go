package money

import "testing"

// GOLDEN FIXTURES — cross-language contract with frontend/src/lib/money.test.ts.
// Both tables MUST stay identical (same inputs, same expected cents). Kenya
// default 16% VAT; fixtures include classic rounding edges (x.5 boundaries,
// repeating fractions like 1/3 rates, zero, tiny and large amounts).
func TestTaxGoldenFixtures(t *testing.T) {
	type row struct {
		amount       int64
		percent      float64
		inclusiveTax int64
		exclusiveTax int64
	}
	rows := []row{
		// zero and one-cent edges
		{0, 16, 0, 0},
		{1, 16, 0, 0},
		// standard 16% cases
		{10000, 16, 1379, 1600},   // KES 100.00
		{11600, 16, 1600, 1856},   // the canonical KES 116 sale
		{100000, 16, 13793, 16000}, // KES 1,000.00
		{99999, 16, 13793, 16000}, // rounding down on the inclusive side
		{999999, 16, 137931, 160000},
		// .5 rounding boundary (half-away-from-zero)
		{625, 16, 86, 100},  // 86.2 → 86
		{3125, 16, 431, 500}, // 431.03 → 431
		{5, 16, 1, 1},       // 0.689… → 1
		// zero-rate
		{500000, 0, 0, 0},
		// 8% (reduced rate) — repeating binary fraction
		{12345, 8, 914, 988},
		// 18% (EAC neighbors)
		{50000, 18, 7627, 9000},
		// fractional percent (16.5)
		{100000, 16.5, 14163, 16500},
	}
	for _, r := range rows {
		if got := TaxFromInclusive(r.amount, r.percent); got != r.inclusiveTax {
			t.Errorf("TaxFromInclusive(%d, %v) = %d, want %d", r.amount, r.percent, got, r.inclusiveTax)
		}
		if got := TaxFromExclusive(r.amount, r.percent); got != r.exclusiveTax {
			t.Errorf("TaxFromExclusive(%d, %v) = %d, want %d", r.amount, r.percent, got, r.exclusiveTax)
		}
	}
}

// TestTotalsSemantics — inclusive keeps the total pinned; exclusive adds.
func TestTotalsSemantics(t *testing.T) {
	tax, total := Totals(11600, 16, true)
	if tax != 1600 || total != 11600 {
		t.Fatalf("inclusive Totals = %d, %d; want 1600, 11600", tax, total)
	}
	tax, total = Totals(10000, 16, false)
	if tax != 1600 || total != 11600 {
		t.Fatalf("exclusive Totals = %d, %d; want 1600, 11600", tax, total)
	}
}
