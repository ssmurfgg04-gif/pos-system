package money

// Pins ComputeLines/Spread to the SAME fixture the frontend twin is
// tested against (frontend/__tests__/moneyLines.test.ts): if Go and
// TypeScript ever price a sale differently, both suites fail.

import (
	"encoding/json"
	"os"
	"testing"
)

type fixture struct {
	Cases []struct {
		Name      string      `json:"name"`
		Lines     [][]float64 `json:"lines"`
		Discount  int64       `json:"discount"`
		Points    int64       `json:"points"`
		Inclusive bool        `json:"inclusive"`
		Want      struct {
			Tax   int64     `json:"tax"`
			Total int64     `json:"total"`
			Lines [][]int64 `json:"lines"`
		} `json:"want"`
	} `json:"cases"`
	Spreads []struct {
		Weights []int64 `json:"weights"`
		Amount  int64   `json:"amount"`
		Want    []int64 `json:"want"`
	} `json:"spreads"`
}

func TestComputeLinesSharedFixture(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/money_lines.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("empty fixture")
	}
	for _, c := range fx.Cases {
		in := make([]Line, len(c.Lines))
		for i, l := range c.Lines {
			in[i] = Line{AmountCents: int64(l[0]), Rate: l[1]}
		}
		got := ComputeLines(in, c.Discount, c.Points, c.Inclusive)
		if got.TaxCents != c.Want.Tax || got.TotalCents != c.Want.Total {
			t.Errorf("%s: tax/total = %d/%d, want %d/%d", c.Name, got.TaxCents, got.TotalCents, c.Want.Tax, c.Want.Total)
		}
		for i, w := range c.Want.Lines {
			g := got.Lines[i]
			if g.NetCents != w[0] || g.TaxCents != w[1] || g.PaidCents != w[2] {
				t.Errorf("%s line %d: %+v, want %v", c.Name, i, g, w)
			}
		}
		// invariants: line VAT adds up; paid adds up to total - points
		var tax, paid int64
		for _, l := range got.Lines {
			tax += l.TaxCents
			paid += l.PaidCents
		}
		if tax != got.TaxCents || paid != got.TotalCents-c.Points {
			t.Errorf("%s: lines don't add up (tax %d vs %d, paid %d vs %d)", c.Name, tax, got.TaxCents, paid, got.TotalCents-c.Points)
		}
	}
	for _, s := range fx.Spreads {
		got := Spread(s.Weights, s.Amount)
		for i := range s.Want {
			if got[i] != s.Want[i] {
				t.Errorf("Spread(%v, %d) = %v, want %v", s.Weights, s.Amount, got, s.Want)
				break
			}
		}
	}
}

// A sale where every line has the same rate is EXACTLY the old maths.
func TestComputeLinesSingleRateIsLegacy(t *testing.T) {
	for _, amt := range []int64{0, 1, 5, 625, 10000, 11600, 99999, 999999, 123456789} {
		for _, incl := range []bool{true, false} {
			for _, disc := range []int64{0, 1, amt / 3} {
				if disc >= amt && disc > 0 {
					continue // checkout rejects a discount >= the subtotal
				}
				got := ComputeLines([]Line{{amt - amt/2, 16}, {amt / 2, 16}}, disc, 0, incl)
				tax, total := Totals(amt-disc, 16, incl)
				if got.TaxCents != tax || got.TotalCents != total {
					t.Fatalf("amt %d disc %d incl %v: %d/%d, legacy %d/%d", amt, disc, incl, got.TaxCents, got.TotalCents, tax, total)
				}
			}
		}
	}
}
