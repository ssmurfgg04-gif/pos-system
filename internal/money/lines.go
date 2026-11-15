package money

// lines.go — v1.11 per-line VAT (mixed rates in one sale).
//
// One rule, used by checkout, receipts and KRA eTIMS, mirrored exactly in
// frontend/src/lib/money.ts (computeLines) and pinned by the shared
// fixture testdata/money_lines.json:
//
//  1. The order discount is spread over the lines by value (integer
//     largest remainder, ties to the earlier line).
//  2. Lines are grouped by VAT rate. Each group's VAT is computed ONCE on
//     the group total (TaxFromInclusive / TaxFromExclusive): a sale where
//     every line has the same rate is therefore exactly the old
//     single-rate maths.
//  3. Each group's VAT is shared back over its lines (largest remainder),
//     so the per-line VAT always adds up to the group VAT.
//  4. Loyalty points (a reduction of what is paid) are spread over the
//     lines' gross amounts, and VAT becomes the VAT contained in what is
//     actually paid, per group. The receipt and KRA then show the same VAT.
//
// All amounts are integer cents; no step can drift.

import (
	"math/big"
	"sort"
)

type Line struct {
	AmountCents int64   // price x qty for the line, as priced (incl. or excl. VAT per the shop)
	Rate        float64 // VAT percent for this line (0 = exempt / zero / outside VAT)
}

type LineResult struct {
	NetCents  int64 // amount after the line's share of the discount (as priced)
	TaxCents  int64 // VAT on the line (after points when points were used)
	PaidCents int64 // what the customer pays for the line, VAT included, after discount and points
}

type Result struct {
	TaxCents   int64 // order VAT (after points)
	TotalCents int64 // order total before points (incl. VAT)
	Lines      []LineResult
}

// Spread shares `amount` over `weights` in proportion, exactly: the parts
// add up to amount (capped at the weight sum), larger remainders first,
// ties to the earlier index. Negative weights count as zero.
func Spread(weights []int64, amount int64) []int64 {
	out := make([]int64, len(weights))
	var total int64
	for _, w := range weights {
		if w > 0 {
			total += w
		}
	}
	if amount <= 0 || total <= 0 {
		return out
	}
	if amount > total {
		amount = total
	}
	type rem struct {
		r *big.Int
		i int
	}
	rems := make([]rem, len(weights))
	bt := big.NewInt(total)
	ba := big.NewInt(amount)
	var given int64
	for i, w := range weights {
		if w <= 0 {
			rems[i] = rem{big.NewInt(-1), i}
			continue
		}
		p := new(big.Int).Mul(big.NewInt(w), ba)
		q, r := new(big.Int).QuoRem(p, bt, new(big.Int))
		out[i] = q.Int64()
		given += out[i]
		rems[i] = rem{r, i}
	}
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].r.Cmp(rems[b].r) > 0 })
	for k := 0; given < amount && k < len(rems); k++ {
		if rems[k].r.Sign() < 0 {
			continue
		}
		out[rems[k].i]++
		given++
	}
	return out
}

// ComputeLines prices a sale with per-line VAT rates.
func ComputeLines(lines []Line, discountCents, pointsCents int64, inclusive bool) Result {
	n := len(lines)
	res := Result{Lines: make([]LineResult, n)}
	amounts := make([]int64, n)
	for i, l := range lines {
		amounts[i] = l.AmountCents
	}
	disc := Spread(amounts, discountCents)

	// groups by rate, in first-seen order (deterministic)
	var rates []float64
	members := map[float64][]int{}
	for i, l := range lines {
		res.Lines[i].NetCents = l.AmountCents - disc[i]
		if _, ok := members[l.Rate]; !ok {
			rates = append(rates, l.Rate)
		}
		members[l.Rate] = append(members[l.Rate], i)
	}
	for _, r := range rates {
		idx := members[r]
		var net int64
		w := make([]int64, len(idx))
		for k, i := range idx {
			net += res.Lines[i].NetCents
			w[k] = res.Lines[i].NetCents
		}
		var tax int64
		if inclusive {
			tax = TaxFromInclusive(net, r)
		} else {
			tax = TaxFromExclusive(net, r)
		}
		parts := Spread(w, tax)
		for k, i := range idx {
			res.Lines[i].TaxCents = parts[k]
			res.Lines[i].PaidCents = res.Lines[i].NetCents
			if !inclusive {
				res.Lines[i].PaidCents += parts[k]
			}
		}
		res.TaxCents += tax
		if inclusive {
			res.TotalCents += net
		} else {
			res.TotalCents += net + tax
		}
	}

	if pointsCents > 0 {
		gross := make([]int64, n)
		for i := range res.Lines {
			gross[i] = res.Lines[i].PaidCents
		}
		pts := Spread(gross, pointsCents)
		res.TaxCents = 0
		for _, r := range rates {
			idx := members[r]
			var paid int64
			w := make([]int64, len(idx))
			for k, i := range idx {
				res.Lines[i].PaidCents -= pts[i]
				paid += res.Lines[i].PaidCents
				w[k] = res.Lines[i].PaidCents
			}
			tax := TaxFromInclusive(paid, r)
			parts := Spread(w, tax)
			for k, i := range idx {
				res.Lines[i].TaxCents = parts[k]
			}
			res.TaxCents += tax
		}
	}
	return res
}
