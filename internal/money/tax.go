// Package money is the canonical money/tax math for the Go side.
//
// CROSS-LANGUAGE CONTRACT: these exact formulas and the rounding rule are
// mirrored by frontend/src/lib/money.ts (taxFromInclusive/taxFromExclusive),
// and both sides pin the SAME golden fixture table —
// internal/money/tax_test.go and frontend/src/lib/money.test.ts must always
// agree. Change one, change both, keep the fixtures identical.
//
// Rounding rule: half-away-from-zero on the KES fraction (Go's math.Round,
// JS's Math.round on positive values — all amounts here are positive).
// Kenya VAT default is INCLUSIVE: the shelf price already contains tax, so
// the VAT portion is extracted (total * pct / (100 + pct)).
package money

import "math"

// TaxFromInclusive extracts the tax portion from a tax-INCLUSIVE amount.
func TaxFromInclusive(amountCents int64, percent float64) int64 {
	return int64(math.Round(float64(amountCents) * percent / (100 + percent)))
}

// TaxFromExclusive computes the tax ADDED on top of a tax-EXCLUSIVE amount.
func TaxFromExclusive(amountCents int64, percent float64) int64 {
	return int64(math.Round(float64(amountCents) * percent / 100))
}

// Totals returns (subtotal, tax, total) for a discounted amount under the
// shop's tax regime. For inclusive tax the total equals the discounted
// subtotal (tax already inside it); for exclusive tax the total adds it.
func Totals(discountedSubCents int64, percent float64, inclusive bool) (taxCents, totalCents int64) {
	if inclusive {
		return TaxFromInclusive(discountedSubCents, percent), discountedSubCents
	}
	tax := TaxFromExclusive(discountedSubCents, percent)
	return tax, discountedSubCents + tax
}
