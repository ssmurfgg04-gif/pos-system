// v1.6.3 wholesale price tier — the pricing rule and the cart's tier
// switch. tierPriceCents is the ONE rule every caller shares (POS adds,
// customer-change repricing); the cart tests pin the "never touch a
// manually overridden line" behaviour.
import { describe, expect, it, beforeEach } from 'vitest'
import { tierPriceCents } from '../src/lib/pricing'
import { useCart } from '../src/stores/cart'
import type { Product } from '../src/lib/api'

describe('tierPriceCents — the wholesale pricing rule', () => {
  const dual = { priceCents: 55000, wholesalePriceCents: 45000 }

  it('retail tier (and undefined) → retail price', () => {
    expect(tierPriceCents(dual, 'retail')).toBe(55000)
    expect(tierPriceCents(dual, undefined)).toBe(55000)
    expect(tierPriceCents(dual, 'anything-else')).toBe(55000)
  })

  it('wholesale tier with a wholesale price > 0 → wholesale price', () => {
    expect(tierPriceCents(dual, 'wholesale')).toBe(45000)
  })

  it('wholesale tier with 0 or absent wholesale price → retail price', () => {
    expect(tierPriceCents({ priceCents: 55000, wholesalePriceCents: 0 }, 'wholesale')).toBe(55000)
    expect(tierPriceCents({ priceCents: 55000 }, 'wholesale')).toBe(55000)
  })
})

describe('cart store — price tier', () => {
  const prod = (over: Partial<Product>): Product => ({
    id: 1, sku: 'SKU-1', barcode: '', name: 'Tee', categoryId: 1, categoryName: 'Cat',
    priceCents: 55000, costCents: 0, stockQty: 100, trackStock: true, active: true, updatedAt: '',
    ...over,
  })
  const line = (productId: number, unitPriceCents: number) => ({
    productId, name: `P${productId}`, sku: `S${productId}`, unitPriceCents, qty: 1, trackStock: false, stockQty: 0,
  })

  beforeEach(() => {
    useCart.getState().clear()
  })

  it('clear resets the tier back to retail', () => {
    useCart.setState({ priceTier: 'wholesale' })
    useCart.getState().clear()
    expect(useCart.getState().priceTier).toBe('retail')
  })

  it('wholesale pick reprices untouched retail lines; products without a wholesale price stay retail', () => {
    const byId = new Map([
      [1, prod({ id: 1, priceCents: 55000, wholesalePriceCents: 45000 })],
      [2, prod({ id: 2, priceCents: 20000 })], // no wholesale price
    ])
    useCart.setState({
      lines: [line(1, 55000), line(2, 20000)],
      priceTier: 'retail',
    })
    useCart.getState().setPriceTier('wholesale', byId)
    const lines = useCart.getState().lines
    expect(lines[0].unitPriceCents).toBe(45000)
    expect(lines[1].unitPriceCents).toBe(20000)
    expect(useCart.getState().priceTier).toBe('wholesale')
  })

  it('a manually overridden line keeps its price, and back to retail reverts wholesale-priced lines', () => {
    const byId = new Map([
      [1, prod({ id: 1, priceCents: 55000, wholesalePriceCents: 45000 })],
      [2, prod({ id: 2, priceCents: 30000, wholesalePriceCents: 25000 })],
    ])
    // Line 1 was manually re-priced to 999; line 2 sits at its wholesale price.
    useCart.setState({ lines: [line(1, 999), line(2, 25000)], priceTier: 'wholesale' })
    useCart.getState().setPriceTier('retail', byId)
    const lines = useCart.getState().lines
    expect(lines[0].unitPriceCents).toBe(999) // override untouched
    expect(lines[1].unitPriceCents).toBe(30000) // wholesale-priced line reverted to retail
  })
})
