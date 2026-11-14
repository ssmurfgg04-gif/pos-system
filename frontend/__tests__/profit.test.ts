import { describe, expect, test } from 'vitest'
import { saleProfit, unitProfit } from '../src/lib/profit'

describe('profit — sell minus buying price', () => {
  test('VAT-inclusive sale: profit = total - VAT - cost', () => {
    // 2 x KES 1,160 incl 16% VAT = 2,320; VAT 320; cost 2 x 600 = 1,200
    const p = saleProfit(232000, 32000, [{ qtyMilli: 2000, lineTotalCents: 232000, costCents: 60000 }])
    expect(p.costCents).toBe(120000)
    expect(p.profitCents).toBe(80000)
    expect(p.marginPct).toBe(40)
  })
  test('weighed line uses milli quantities', () => {
    const p = saleProfit(5000, 0, [{ qtyMilli: 250, lineTotalCents: 5000, costCents: 12000 }])
    expect(p.profitCents).toBe(2000)
  })
  test('a line with no buying price makes the sale "unknown", never 100%', () => {
    const p = saleProfit(10000, 0, [
      { qtyMilli: 1000, lineTotalCents: 6000, costCents: 3000 },
      { qtyMilli: 1000, lineTotalCents: 4000, costCents: 0 },
    ])
    expect(p.profitCents).toBeNull()
    expect(p.uncostedLines).toBe(1)
    expect(p.marginPct).toBeNull()
  })
  test('unit profit strips VAT from an inclusive price', () => {
    expect(unitProfit(116000, 60000, 16, true)).toEqual({ profitCents: 40000, marginPct: 40 })
    expect(unitProfit(100000, 60000, 16, false)).toEqual({ profitCents: 40000, marginPct: 40 })
    expect(unitProfit(100000, 0, 16, true)).toBeNull()
  })
})
