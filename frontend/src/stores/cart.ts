import { create } from 'zustand'
import { Product } from '../lib/api'
import { cartTotals } from '../lib/money'
import { tierPriceCents } from '../lib/pricing'
import { useBranding } from './branding'
import { useAuth } from './auth'

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
  /** v1.6.3 price tier of the customer on this sale ('retail' = walk-in). */
  priceTier: 'retail' | 'wholesale'
  add: (p: Product, qty?: number) => void
  setQty: (productId: number, qty: number) => void
  remove: (productId: number) => void
  /** Cashier price override (payments.override_price) — writes the unit
   *  price back to the line so totals and the charge amount follow. */
  setUnitPrice: (productId: number, unitPriceCents: number) => void
  /** v1.6.3 wholesale tier — the price tier of the customer attached to
   *  this sale ('retail' = walk-in default). `byId` (product id → catalog
   *  row) reprices lines that still sit at a catalog price; lines the
   *  cashier manually re-priced keep their price. */
  setPriceTier: (tier: 'retail' | 'wholesale', byId?: Map<number, Product>) => void
  clear: () => void
  setCustomerName: (v: string) => void
  setNote: (v: string) => void
  totals: () => { subtotal: number; tax: number; total: number; count: number }
}

// The cart survives a full page reload in-session (sessionStorage): the 401
// interceptor hard-navigates to /login when a 12h JWT expires mid-sale, and
// a cashier who re-scans an entire basket because of an expired token WILL
// mis-scan something. Session-scoped storage means closing the tab still
// starts clean; login clears it when the USER changed (no cross-staff leaks).
const CART_KEY = 'pos_cart_session'
type PersistedCart = { lines: CartLine[]; customerName: string; note: string; priceTier: 'retail' | 'wholesale'; userId: number | null }

function loadPersistedCart(): PersistedCart {
  try {
    const raw = sessionStorage.getItem(CART_KEY)
    if (raw) {
      const p = JSON.parse(raw) as PersistedCart
      if (Array.isArray(p.lines))
        return {
          ...p,
          priceTier: p.priceTier === 'wholesale' ? 'wholesale' : 'retail',
          lines: p.lines.filter((l) => l && typeof l.productId === 'number'),
        }
    }
  } catch {
    /* corrupt storage = fresh cart */
  }
  return { lines: [], customerName: '', note: '', priceTier: 'retail', userId: null }
}

function writePersistedCart(s: { lines: CartLine[]; customerName: string; note: string; priceTier: 'retail' | 'wholesale' }) {
  try {
    sessionStorage.setItem(
      CART_KEY,
      JSON.stringify({ ...s, userId: useAuth.getState().user?.id ?? null } satisfies PersistedCart),
    )
  } catch {
    /* private mode quota — cart still works in memory */
  }
}

const persisted = loadPersistedCart()

export const useCart = create<CartState>((set, get) => ({
  lines: persisted.lines,
  customerName: persisted.customerName,
  note: persisted.note,
  priceTier: persisted.priceTier,
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
  setUnitPrice: (productId, unitPriceCents) =>
    set((s) => ({
      lines: s.lines.map((l) =>
        l.productId === productId ? { ...l, unitPriceCents: Math.max(0, Math.round(unitPriceCents)) } : l,
      ),
    })),
  // v1.6.3: picking/clearing a customer flips the sale's tier. Only lines
  // still priced at a CATALOG price follow the tier (a wholesale pick
  // moves retail-priced lines to the wholesale price; going back to retail
  // moves wholesale-priced lines down) — a manually overridden price is
  // never touched. tierPriceCents is the single pricing rule.
  setPriceTier: (tier, byId) =>
    set((s) => {
      if (s.priceTier === tier) return { priceTier: tier }
      const lines = byId
        ? s.lines.map((l) => {
            const p = byId.get(l.productId)
            if (!p) return l
            const target = tierPriceCents(p, tier)
            const untouched =
              tier === 'wholesale'
                ? l.unitPriceCents === p.priceCents
                : l.unitPriceCents === tierPriceCents(p, 'wholesale')
            return untouched && target !== l.unitPriceCents ? { ...l, unitPriceCents: target } : l
          })
        : s.lines
      return { priceTier: tier, lines }
    }),
  clear: () => set({ lines: [], customerName: '', note: '', priceTier: 'retail' }),
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

// Persist on every change + reset when the signed-in user changes.
useCart.subscribe((s) => writePersistedCart(s))

/** Called by the auth store on login/PIN and logout. Same user → the cart
 *  (and an in-flight sale) survives an expired-token reload; a different
 *  user starts clean — nobody sells another cashier's basket. */
export function cartResetForUser(userId: number | null) {
  const p = loadPersistedCart()
  if (p.userId !== null && p.userId !== userId) {
    useCart.getState().clear()
    try {
      sessionStorage.removeItem(CART_KEY)
    } catch {
      /* ignore */
    }
    return
  }
  if (userId === null) {
    useCart.getState().clear()
    try {
      sessionStorage.removeItem(CART_KEY)
    } catch {
      /* ignore */
    }
  }
}
