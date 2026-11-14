// Pins computeLines/spread to the SAME fixture as internal/money (Go).
import { describe, expect, test } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { cartTotals, computeLines, spread, taxFromExclusive, taxFromInclusive } from '../src/lib/money'

const fx = JSON.parse(readFileSync(join(__dirname, '../../testdata/money_lines.json'), 'utf8'))

describe('per-line VAT (shared fixture with Go)', () => {
  for (const c of fx.cases) {
    test(c.name, () => {
      const r = computeLines(c.lines.map(([a, rate]: number[]) => ({ amountCents: a, rate })), c.discount, c.points, c.inclusive)
      expect(r.taxCents).toBe(c.want.tax)
      expect(r.totalCents).toBe(c.want.total)
      expect(r.lines.map((l) => [l.netCents, l.taxCents, l.paidCents])).toEqual(c.want.lines)
      expect(r.lines.reduce((a, l) => a + l.taxCents, 0)).toBe(r.taxCents)
      expect(r.lines.reduce((a, l) => a + l.paidCents, 0)).toBe(r.totalCents - c.points)
    })
  }
  test('spread', () => {
    for (const s of fx.spreads) expect(spread(s.weights, s.amount)).toEqual(s.want)
  })
  test('single rate is exactly the old maths', () => {
    for (const amt of [0, 1, 5, 625, 10000, 11600, 99999, 999999]) {
      for (const incl of [true, false]) {
        const t = cartTotals([{ qtyMilli: 1000, unitPriceCents: amt }], 16, incl)
        const tax = incl ? taxFromInclusive(amt, 16) : taxFromExclusive(amt, 16)
        expect([t.tax, t.total]).toEqual([tax, incl ? amt : amt + tax])
      }
    }
  })
  test('cart with an exempt line charges VAT only on the taxed line', () => {
    const t = cartTotals([
      { qtyMilli: 1000, unitPriceCents: 116000, vatRate: 16 },
      { qtyMilli: 2000, unitPriceCents: 2500, vatRate: 0 },
    ], 16, true)
    expect(t).toMatchObject({ subtotal: 121000, tax: 16000, total: 121000 })
  })
})
