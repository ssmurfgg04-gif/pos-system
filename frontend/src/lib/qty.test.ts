import { describe, expect, it } from 'vitest'
import { MAX_QTY_MILLI, fmtQty, isFractionUnit, lineTotalCents, parseQtyInput, unitLabel } from './qty'

// GOLDEN FIXTURES — the qty contract with internal/services/batches.go
// (milliLineTotal) and models.OrderItem.QtyDisplay. Both sides MUST stay
// identical (same inputs, same expected values).
describe('parseQtyInput (human input → milli)', () => {
  it('parses whole and fractional units', () => {
    expect(parseQtyInput('0.5')).toBe(500)
    expect(parseQtyInput('2')).toBe(2000)
    expect(parseQtyInput('0.001')).toBe(1)
    expect(parseQtyInput('1.25')).toBe(1250)
    expect(parseQtyInput('  3 ')).toBe(3000)
  })
  it('rejects junk, zero and negatives', () => {
    expect(parseQtyInput('abc')).toBeNull()
    expect(parseQtyInput('0')).toBeNull()
    expect(parseQtyInput('')).toBeNull()
    expect(parseQtyInput('-1')).toBeNull()
    expect(parseQtyInput('1.2345')).toBeNull() // finer than milli cannot exist
    expect(parseQtyInput('1e3')).toBeNull()
  })
  it('clamps absurd quantities to MAX_QTY_MILLI', () => {
    // 100001 units = 100,001,000 milli > the 100,000,000 cap
    expect(parseQtyInput('100001')).toBe(MAX_QTY_MILLI)
    expect(parseQtyInput('99999999')).toBe(MAX_QTY_MILLI)
  })
})

describe('fmtQty (milli → display)', () => {
  it('whole units render plain', () => {
    expect(fmtQty(3000)).toBe('3')
    expect(fmtQty(1000)).toBe('1')
  })
  it('fractions render with trimmed decimals', () => {
    expect(fmtQty(500)).toBe('0.5')
    expect(fmtQty(1250)).toBe('1.25')
    expect(fmtQty(1)).toBe('0.001')
    expect(fmtQty(1500)).toBe('1.5')
  })
})

describe('lineTotalCents (the server formula, half-up)', () => {
  // Pinned against Go milliLineTotal: (price*milli + 500) / 1000.
  it.each([
    [25000, 3000, 75000],
    [333, 1500, 500], // 333*1500/1000 = 499.5 → 500 (half-up)
    [9999, 1, 10], // 9.999 → 10
    [100, 1, 0], // 0.1 → 0 (rounds down)
    [55000, 2000, 110000],
  ])('lineTotalCents(%d, %d) = %d', (price, milli, want) => {
    expect(lineTotalCents(price, milli)).toBe(want)
  })
})

describe('unit helpers', () => {
  it('isFractionUnit: weighable units only', () => {
    for (const u of ['kg', 'g', 'l', 'ml', 'm']) expect(isFractionUnit(u)).toBe(true)
    for (const u of ['pc', 'pack', 'box', 'dozen', undefined, '']) expect(isFractionUnit(u)).toBe(false)
  })
  it('unitLabel: empty falls back to pc', () => {
    expect(unitLabel('')).toBe('pc')
    expect(unitLabel(undefined)).toBe('pc')
    expect(unitLabel('kg')).toBe('kg')
  })
})
