import { describe, expect, it, beforeEach } from 'vitest'
import { useCart } from '../src/stores/cart'
import { useBranding } from '../src/stores/branding'
import type { Product } from '../src/lib/api'

const p = (id: number, price: number, opts: Partial<Product> = {}): Product => ({
  id, sku: `SKU-${id}`, barcode: '', name: `Product ${id}`, categoryId: 1, categoryName: 'Cat',
  priceCents: price, costCents: 0, stockQty: 100, trackStock: true, active: true, updatedAt: '',
  ...opts,
})

beforeEach(() => {
  useCart.getState().clear()
  useBranding.setState({
    branding: {
      app_name: 'Test', store_name: '', brand_color: '#10B981', currency_symbol: 'KES',
      currency_code: 'KES', tax_percent: 16, tax_included: true, payment_mode: 'auto',
      till_number: '', paybill_number: '', mpesa_env: 'mock',
    },
    loaded: true,
  })
})

describe('cart store', () => {
  it('adds products and merges duplicates', () => {
    const { add } = useCart.getState()
    add(p(1, 55000))
    add(p(1, 55000))
    add(p(2, 45000))
    const lines = useCart.getState().lines
    expect(lines).toHaveLength(2)
    expect(lines[0].qty).toBe(2)
    expect(useCart.getState().totals().count).toBe(3)
  })

  it('computes VAT-inclusive totals', () => {
    const { add } = useCart.getState()
    add(p(1, 55000))
    add(p(2, 45000, { trackStock: false }))
    const t = useCart.getState().totals()
    expect(t.subtotal).toBe(100000)
    expect(t.tax).toBe(13793) // 16/116 of 1000.00
    expect(t.total).toBe(100000)
  })

  it('setQty removes at zero and caps absurd quantities upstream', () => {
    const { add, setQty } = useCart.getState()
    add(p(1, 1000))
    setQty(1, 5)
    expect(useCart.getState().lines[0].qty).toBe(5)
    setQty(1, 0)
    expect(useCart.getState().lines).toHaveLength(0)
  })

  it('remove drops a line', () => {
    const { add, remove } = useCart.getState()
    add(p(1, 1000))
    add(p(2, 2000))
    remove(1)
    expect(useCart.getState().lines.map((l) => l.productId)).toEqual([2])
  })

  it('clear resets everything including customer meta', () => {
    const { add, setCustomerName, setNote, clear } = useCart.getState()
    add(p(1, 1000))
    setCustomerName('Jane')
    setNote('note')
    clear()
    const s = useCart.getState()
    expect(s.lines).toHaveLength(0)
    expect(s.customerName).toBe('')
    expect(s.note).toBe('')
  })
})
