import { describe, expect, it } from 'vitest'
import {
  parseToCents, centsToAmount, formatMoney, formatMoneyCompact,
  taxFromInclusive, taxFromExclusive, cartTotals, normalizePhoneKe, configureMoney,
} from '../src/lib/money'

configureMoney('KES')

describe('parseToCents', () => {
  it('parses plain amounts', () => {
    expect(parseToCents('550')).toBe(55000)
    expect(parseToCents('0')).toBe(0)
  })
  it('parses decimals', () => {
    expect(parseToCents('550.5')).toBe(55050)
    expect(parseToCents('550.50')).toBe(55050)
    expect(parseToCents('.5')).toBe(50)
    expect(parseToCents('0.05')).toBe(5)
  })
  it('parses grouped amounts', () => {
    expect(parseToCents('1,550.50')).toBe(155050)
    expect(parseToCents('1,234,567.89')).toBe(123456789)
  })
  it('rejects garbage', () => {
    expect(parseToCents('abc')).toBeNull()
    expect(parseToCents('12.345')).toBeNull()
    expect(parseToCents('-5')).toBeNull()
    expect(parseToCents('')).toBeNull()
  })
})

describe('formatting', () => {
  it('formats with separators', () => {
    expect(centsToAmount(55000)).toBe('550.00')
    expect(centsToAmount(123456789)).toBe('1,234,567.89')
    expect(centsToAmount(5)).toBe('0.05')
    expect(centsToAmount(-250)).toBe('-2.50')
  })
  it('prepends the symbol', () => {
    expect(formatMoney(55000)).toBe('KES 550.00')
  })
  it('compact drops .00 only when there is no fraction', () => {
    expect(formatMoneyCompact(55000)).toBe('KES 550')
    expect(formatMoneyCompact(123400000)).toBe('KES 1,234,000')
    expect(formatMoneyCompact(55050)).toBe('KES 550.50') // the decimal-path bug fix
    expect(formatMoneyCompact(5)).toBe('KES 0.05')
  })
})

describe('tax math', () => {
  it('extracts VAT from inclusive totals', () => {
    expect(taxFromInclusive(116000, 16)).toBe(16000)
    expect(taxFromInclusive(100000, 16)).toBeCloseTo(13793.10, 0)
  })
  it('adds VAT on top for exclusive', () => {
    expect(taxFromExclusive(100000, 16)).toBe(16000)
  })
  it('cart totals in both modes', () => {
    const lines = [
      { qty: 2, unitPriceCents: 55000 },
      { qty: 1, unitPriceCents: 45000 },
    ]
    expect(cartTotals(lines, 16, true)).toEqual({ subtotal: 155000, tax: 21379, total: 155000 })
    expect(cartTotals(lines, 16, false)).toEqual({ subtotal: 155000, tax: 24800, total: 179800 })
  })
  it('zero-rated carts', () => {
    expect(cartTotals([{ qty: 1, unitPriceCents: 100 }], 0, true)).toEqual({ subtotal: 100, tax: 0, total: 100 })
  })
})

describe('phone normalization (Kenya)', () => {
  it.each([
    ['0722123456', '254722123456'],
    ['722123456', '254722123456'],
    ['+254722123456', '254722123456'],
    ['254722123456', '254722123456'],
    ['0110123456', '254110123456'],
    ['0722 123 456', '254722123456'],
  ])('%s → %s', (input, want) => {
    expect(normalizePhoneKe(input)).toBe(want)
  })

  it.each(['', '12345', '07221234567', '254822123456', 'abc', '+255722123456'])(
    'rejects %s',
    (input) => {
      expect(normalizePhoneKe(input)).toBeNull()
    },
  )
})
