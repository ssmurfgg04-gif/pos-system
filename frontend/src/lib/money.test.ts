import { describe, expect, it } from 'vitest'
import { taxFromInclusive, taxFromExclusive } from './money'

// GOLDEN FIXTURES — cross-language contract with internal/money/tax_test.go
// (the Go canonical helper). Both tables MUST stay identical (same inputs,
// same expected cents). Kenya default 16% VAT; fixtures include classic
// rounding edges (.5 boundaries, repeating fractions, zero, tiny, large).
// NOTE: this suite runs in the Node demo (jsdom) and in the browser demo
// build — Math.round on positive halves behaves identically in both.
describe('VAT golden fixtures (must match internal/money/tax_test.go)', () => {
  const rows: [number, number, number, number][] = [
    // [amount, percent, inclusiveTax, exclusiveTax]
    [0, 16, 0, 0],
    [1, 16, 0, 0],
    [10000, 16, 1379, 1600], // KES 100.00
    [11600, 16, 1600, 1856], // the canonical KES 116 sale
    [100000, 16, 13793, 16000], // KES 1,000.00
    [99999, 16, 13793, 16000], // rounding down on the inclusive side
    [999999, 16, 137931, 160000],
    [625, 16, 86, 100], // .5 boundary behavior
    [3125, 16, 431, 500],
    [5, 16, 1, 1],
    [500000, 0, 0, 0], // zero-rate
    [12345, 8, 914, 988], // 8% reduced rate
    [50000, 18, 7627, 9000], // 18%
    [100000, 16.5, 14163, 16500], // fractional percent
  ]
  it.each(rows)('taxFromInclusive(%d, %v) = %d / taxFromExclusive = %d', (amount, pct, inc, exc) => {
    expect(taxFromInclusive(amount, pct)).toBe(inc)
    expect(taxFromExclusive(amount, pct)).toBe(exc)
  })
})
