import { create } from 'zustand'
import { Product } from '../lib/api'
import { cartTotals } from '../lib/money'
import { useBranding } from './branding'

export interface CartLine {
  productId: number
  name: string
  sku: string
  unitPriceCents: number
  qty: number
  trackStock: boolean
  stockQty: number
}

interface CartState {
  lines: CartLine[]
  customerName: string
  note: string
  add: (p: Product, qty?: number) => void
  setQty: (productId: number, qty: number) => void
  remove: (productId: number) => void
  clear: () => void
  setCustomerName: (v: string) => void
  setNote: (v: string) => void
  totals: () => { subtotal: number; tax: number; total: number; count: number }
}

export const useCart = create<CartState>((set, get) => ({
  lines: [],
  customerName: '',
  note: '',
  add: (p, qty = 1) =>
    set((s) => {
      const existing = s.lines.find((l) => l.productId === p.id)
      if (existing) {
        return {
          lines: s.lines.map((l) =>
            l.productId === p.id ? { ...l, qty: Math.min(999, l.qty + qty) } : l,
          ),
        }
      }
      return {
        lines: [
          ...s.lines,
          {
            productId: p.id,
            name: p.name,
            sku: p.sku,
            unitPriceCents: p.priceCents,
            qty,
            trackStock: p.trackStock,
            stockQty: p.stockQty,
          },
        ],
      }
    }),
  setQty: (productId, qty) =>
    set((s) => ({
      lines: qty <= 0 ? s.lines.filter((l) => l.productId !== productId) : s.lines.map((l) => (l.productId === productId ? { ...l, qty } : l)),
    })),
  remove: (productId) => set((s) => ({ lines: s.lines.filter((l) => l.productId !== productId) })),
  clear: () => set({ lines: [], customerName: '', note: '' }),
  setCustomerName: (v) => set({ customerName: v }),
  setNote: (v) => set({ note: v }),
  totals: () => {
    const b = useBranding.getState().branding
    const t = cartTotals(
      get().lines.map((l) => ({ qty: l.qty, unitPriceCents: l.unitPriceCents })),
      b.tax_percent,
      b.tax_included,
    )
    return { ...t, count: get().lines.reduce((n, l) => n + l.qty, 0) }
  },
}))
