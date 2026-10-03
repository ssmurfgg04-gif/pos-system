// POS terminal — the money screen. Desktop is a deliberately designed
// three-zone layout: app chrome (header + dark nav rail live in shell.tsx),
// the product workspace (search → categories → toolbar → 5-column grid with
// photos, stock badges and dedicated + buttons), and the 430px checkout rail
// (CheckoutRail) that owns cart + payment — including in-rail STK/Paystack
// feedback, so nothing ever opens in an external window or tab.
//
// Barcode entry works two ways: focused typing into the search box (exact
// match auto-adds) AND a global HID listener so hardware USB/Bluetooth
// scanners fire straight into the cart with no input focused at all.
//
// Under 1024px the checkout rail becomes a bottom sheet with the SAME
// component — one checkout experience everywhere.

import { useEffect, useMemo, useRef, useState } from 'react'
import { api, Category, Product } from '../lib/api'
import { useCart } from '../stores/cart'
import { useBranding } from '../stores/branding'
import { useAuth } from '../stores/auth'
import { usePosSearch } from '../stores/search'
import { formatMoney, formatMoneyCompact } from '../lib/money'
import { Button, EmptyState, Input, Modal, Spinner, ErrorBoundary } from '../components/ui'
import { ProductPhoto } from '../components/ProductPhoto'
import { CheckoutRail } from '../components/CheckoutRail'
import { HeldSalesDrawer } from '../components/HeldSalesDrawer'
import { ReceiptModal } from '../components/Receipt'
import { toast } from '../stores/toasts'
import { onWsEvent, onWsReconnect } from '../ws/client'
import type { WsEvent } from '../ws/client'
import { Order } from '../lib/api'
import {
  ShoppingCart, Search, ScanBarcode, Plus, Minus, LayoutGrid, List, ArrowUpDown,
} from 'lucide-react'

type SortKey = 'featured' | 'name' | 'price-asc' | 'price-desc'

export function Pos() {
  const { user } = useAuth()
  const cart = useCart()
  const branding = useBranding((s) => s.branding)
  const [products, setProducts] = useState<Product[] | null>(null)
  const [categories, setCategories] = useState<Category[]>([])
  const search = usePosSearch((s) => s.query)
  const setSearch = usePosSearch((s) => s.setQuery)
  const [cat, setCat] = useState<number | 'all'>('all')
  const [sort, setSort] = useState<SortKey>('featured')
  const [view, setView] = useState<'grid' | 'list'>('grid')
  const [sheetOpen, setSheetOpen] = useState(false)
  const [receiptFor, setReceiptFor] = useState<Order | null>(null)
  const [payConfig, setPayConfig] = useState<import('../lib/api').PaymentConfig | null>(null)
  const [heldOpen, setHeldOpen] = useState(false)
  // A payment in flight (or an open receipt/park modal inside the rail) must
  // keep the mobile sheet mounted: closing it kills the poll timers while
  // the cart survives — the cashier would re-charge the same basket.
  const [railBusy, setRailBusy] = useState(false)
  const [heldCount, setHeldCount] = useState(0)
  const searchRef = useRef<HTMLInputElement>(null)

  const canHold = !!user?.permissions.includes('pos.hold')

  const load = async () => {
    try {
      const [p, c] = await Promise.all([
        api.get<Product[]>('/api/v1/products'),
        api.get<Category[]>('/api/v1/categories'),
      ])
      setProducts(p)
      setCategories(c)
    } catch (e: any) {
      // Offline is an expected, non-alarming state (banner handles it).
      if (navigator.onLine) toast.error('Could not load products', e?.message)
    }
  }

  useEffect(() => {
    load()
    // Barcode/keyboard focus shortcut.
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'F2') {
        e.preventDefault()
        searchRef.current?.focus()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  // The socket reconnecting means we were cut off — other tills may have
  // changed prices/stock during the gap. Refetch once (cheap; this page
  // previously sold at stale prices after every reconnect).
  useEffect(() => onWsReconnect(load), [])

  // Tender capabilities (Paystack switch, credit & loyalty programs).
  useEffect(() => {
    api.get<import('../lib/api').PaymentConfig>('/api/v1/payments/config')
      .then((c) => setPayConfig(c && !Array.isArray(c) ? c : null))
      .catch(() => setPayConfig(null))
  }, [])

  // Parked-sale count — refreshes on WS pushes and local park/resume.
  useEffect(() => {
    if (!canHold) return
    const refreshHeld = () => {
      api.get<import('../lib/api').HeldSale[]>('/api/v1/held-sales')
        .then((l) => setHeldCount(Array.isArray(l) ? l.length : 0))
        .catch(() => {}) // offline — the banner already explains
    }
    refreshHeld()
    // Static demo builds have no socket; count refreshes on local actions.
    return onWsEvent('HELD_SALES_UPDATED' as WsEvent, refreshHeld)
  }, [canHold])

  // Global HID barcode scanner listener — no input focus required.
  useEffect(() => {
    let buf = ''
    let lastKey = 0
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null
      const inInput = !!t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)
      if (inInput) {
        buf = '' // the focused search box handles scanners itself
        return
      }
      if (e.key === 'Enter') {
        const burst = buf.length >= 4 && Date.now() - lastKey < 150
        const code = buf
        buf = ''
        if (burst && products) {
          const hit = products.find((p) => p.active && p.barcode && p.barcode === code)
          if (hit) {
            addToCart(hit)
            if (navigator.vibrate) navigator.vibrate(15)
          } else {
            toast.error('Unknown barcode', code)
          }
          e.preventDefault()
        }
        return
      }
      if (e.key.length === 1) {
        if (Date.now() - lastKey > 150) buf = '' // too slow — human typing
        buf += e.key
        lastKey = Date.now()
      } else if (e.key !== 'Shift') {
        buf = ''
      }
    }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [products])

  const filtered = useMemo(() => {
    if (!products) return []
    const q = search.trim().toLowerCase()
    const list = products.filter((p) => {
      if (!p.active) return false
      if (cat !== 'all' && p.categoryId !== cat) return false
      if (q && !p.name.toLowerCase().includes(q) && !p.sku.toLowerCase().includes(q) && !p.barcode.includes(q)) return false
      return true
    })
    switch (sort) {
      case 'name': return [...list].sort((a, b) => a.name.localeCompare(b.name))
      case 'price-asc': return [...list].sort((a, b) => a.priceCents - b.priceCents)
      case 'price-desc': return [...list].sort((a, b) => b.priceCents - a.priceCents)
      default: return list
    }
  }, [products, search, cat, sort])

  // Barcode exact-match auto-add (scanner "types" the code + Enter into the focused search).
  useEffect(() => {
    const q = search.trim()
    if (!q || !products) return
    const hit = products.find((p) => p.barcode && p.barcode === q)
    if (hit) {
      cart.add(hit)
      setSearch('')
      toast.success(hit.name, 'Added to cart')
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search, products])

  const totals = cart.totals()

  /** Live quantity of a product in the shared cart (0 when absent) —
   *  drives the quantity-aware product card controls. */
  const cartQty = (productId: number) =>
    cart.lines.find((l) => l.productId === productId)?.qty ?? 0

  const addToCart = (p: Product) => {
    if (p.trackStock && p.stockQty <= 0) {
      toast.error('Out of stock', p.name)
      return
    }
    cart.add(p)
    if (navigator.vibrate) navigator.vibrate(10)
  }

  // Rehydrate a parked cart: match held productIds against the loaded
  // catalog (deleted products are skipped with a warning), restore the
  // customer + note. The drawer deletes the hold only after this succeeds.
  const resumeHeld = async (sale: import('../lib/api').HeldSale) => {
    if (!products) throw new Error('Products are still loading — try again in a second')
    // A hold IS a sale: resuming replaces whatever is in the cart instead of
    // merging (two merged baskets under one customer is a money bug, not a
    // convenience). The replaced basket is auto-held so nothing is lost.
    if (useCart.getState().lines.length > 0) {
      const replaced = useCart.getState().lines.map((l) => ({ productId: l.productId, qty: l.qty }))
      const name = useCart.getState().customerName || 'Replaced basket'
      try {
        await api.post('/api/v1/held-sales', {
          refName: name,
          cart: {
            items: replaced,
            paymentMethod: 'cash' as const,
          },
        })
        toast.info('Previous cart held', 'It moved to Held sales — resume or discard it there')
      } catch {
        toast.error('Cart not empty', 'Clear or hold the current cart before resuming a held sale')
        return
      }
    }
    useCart.getState().clear()
    let missing = 0
    for (const it of sale.items) {
      const p = products.find((x) => x.id === it.productId)
      if (!p) {
        missing++
        continue
      }
      cart.add(p, it.qty)
    }
    if (sale.customerName) cart.setCustomerName(sale.customerName)
    if (sale.note) cart.setNote(sale.note)
    if (missing > 0) {
      toast.error('Some items skipped', `${missing} product(s) no longer exist in the catalog — check the cart`)
    }
  }

  const rail = (
    <ErrorBoundary label="checkout-rail" compact>
      <CheckoutRail
        products={products}
        payConfig={payConfig}
        onOrderPaid={(o) => {
          if (!o) { cart.clear(); return } // offline-queued sale — start fresh
          try {
            // Verified success: reset the workspace NOW so product cards
            // stop showing "IN CART" badges for items that just sold. The
            // Paid panel + receipt are order-driven and stay put.
            cart.clear()
            load()
            toast.success(`Sale complete — ${o.number}`, formatMoney(o.totalCents))
            setReceiptFor(o)
          } catch (e) {
            // A toast/receipt hiccup must never take the POS down after money moved.
            console.error('post-payment UI error', e)
          }
        }}
        onOrderVoided={(o) => {
          load()
          toast.success(`Order ${o.number} voided`, o.voidReason || undefined)
        }}
        canHold={canHold}
        heldCount={heldCount}
        onOpenHeld={() => setHeldOpen(true)}
        onBusyChange={setRailBusy}
      />
    </ErrorBoundary>
  )

  return (
    <div className="h-full flex min-h-0">
      {/* ── Product workspace ─────────────────────────────────────────── */}
      <div className="flex-1 min-w-0 min-h-0 flex flex-col">
        {/* Search + mobile cart button */}
        <div className="flex gap-2 items-center shrink-0">
          <div className="relative flex-1">
            <Search size={16} strokeWidth={2.5} className="absolute left-3 top-1/2 -translate-y-1/2 text-ink-subtle pointer-events-none" aria-hidden />
            <Input
              ref={searchRef as any}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Scan barcode or search products… (F2)"
              className="h-11 text-[14px] pl-9"
              autoFocus
            />
          </div>
          <div className="lg:hidden shrink-0">
            <Button variant="primary" onClick={() => setSheetOpen(true)} disabled={cart.lines.length === 0} className="h-11 relative">
              <ShoppingCart size={17} strokeWidth={2.25} aria-hidden />
              {formatMoneyCompact(totals.total)}
              {cart.lines.length > 0 && (
                <span className="absolute -top-2 -right-2 min-w-5 h-5 px-1 rounded-pill bg-danger text-white text-[11px] font-bold flex items-center justify-center border-2 border-surface tabular">
                  {totals.count}
                </span>
              )}
            </Button>
          </div>
        </div>

        {/* Categories */}
        <div className="pt-2.5 pb-2 flex gap-2 overflow-x-auto shrink-0" role="tablist" aria-label="Categories">
          <CategoryChip active={cat === 'all'} onClick={() => setCat('all')}>All</CategoryChip>
          {categories.map((c) => (
            <CategoryChip key={c.id} active={cat === c.id} onClick={() => setCat(c.id)}>{c.name}</CategoryChip>
          ))}
        </div>

        {/* Toolbar: count + sort + view toggle */}
        <div className="pb-2.5 flex items-center gap-2 shrink-0">
          <h1 className="text-[17px] font-bold text-ink">
            {cat === 'all' ? 'All Products' : categories.find((c) => c.id === cat)?.name || 'Products'}
            <span className="text-ink-subtle font-semibold"> ({filtered.length})</span>
          </h1>
          <div className="ml-auto flex items-center gap-2">
            <div className="relative">
              <ArrowUpDown size={14} strokeWidth={2.5} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-ink-subtle pointer-events-none" aria-hidden />
              <select
                value={sort}
                onChange={(e) => setSort(e.target.value as SortKey)}
                aria-label="Sort products"
                className="min-h-9 pl-8 pr-7 bg-surface border border-line-strong rounded-input text-[12.5px] font-semibold text-ink appearance-none"
              >
                <option value="featured">Sort: Featured</option>
                <option value="name">Name A→Z</option>
                <option value="price-asc">Price low→high</option>
                <option value="price-desc">Price high→low</option>
              </select>
            </div>
            <div className="flex bg-surface border border-line-strong rounded-input overflow-hidden" role="group" aria-label="View">
              <button
                onClick={() => setView('grid')}
                aria-pressed={view === 'grid'}
                title="Grid view"
                className={`w-9 h-9 flex items-center justify-center ${view === 'grid' ? 'bg-brand-soft text-brand' : 'text-ink-subtle hover:text-ink'}`}
              >
                <LayoutGrid size={15} strokeWidth={2.25} aria-hidden />
              </button>
              <button
                onClick={() => setView('list')}
                aria-pressed={view === 'list'}
                title="List view"
                className={`w-9 h-9 flex items-center justify-center ${view === 'list' ? 'bg-brand-soft text-brand' : 'text-ink-subtle hover:text-ink'}`}
              >
                <List size={15} strokeWidth={2.25} aria-hidden />
              </button>
            </div>
          </div>
        </div>

        {/* Products */}
        <div className="flex-1 min-h-0 overflow-y-auto bg-surface border border-line rounded-card shadow-brutal p-3">
          {!products ? (
            <div className="py-16 flex justify-center"><Spinner className="w-7 h-7 border-4" /></div>
          ) : filtered.length === 0 ? (
            <EmptyState icon={<ScanBarcode size={24} strokeWidth={2.25} />} title="No products match" body={search ? `Nothing found for “${search}”.` : 'Add products in Inventory first.'} />
          ) : view === 'grid' ? (
            <div className="grid grid-cols-2 min-[480px]:grid-cols-3 md:grid-cols-4 2xl:grid-cols-5 gap-2.5">
              {filtered.map((p) => (
                <ProductCard key={p.id} p={p} qty={cartQty(p.id)} onAdd={() => addToCart(p)} onInc={() => addToCart(p)} onDec={() => cart.setQty(p.id, cartQty(p.id) - 1)} />
              ))}
            </div>
          ) : (
            <div className="space-y-1.5">
              {filtered.map((p) => (
                <ProductRow key={p.id} p={p} qty={cartQty(p.id)} onAdd={() => addToCart(p)} onInc={() => addToCart(p)} onDec={() => cart.setQty(p.id, cartQty(p.id) - 1)} />
              ))}
            </div>
          )}
        </div>
      </div>

      {/* ── Checkout rail (desktop) ───────────────────────────────────── */}
      <aside className="hidden lg:flex w-[430px] shrink-0 ml-4 min-h-0 bg-surface border border-line rounded-card shadow-brutal">
        {rail}
      </aside>

      {/* Mobile checkout sheet — the same rail in a modal */}
      <Modal
        open={sheetOpen}
        onClose={() => {
          if (railBusy) {
            toast.info('Payment in progress', 'Wait for the payment to finish or cancel it first')
            return
          }
          setSheetOpen(false)
        }}
        title={`Cart — ${formatMoney(totals.total)}`}
        size="md"
      >
        <div className="h-[70vh] -mx-5 -my-4">
          {rail}
        </div>
      </Modal>

      <HeldSalesDrawer
        open={heldOpen}
        onClose={() => setHeldOpen(false)}
        onResume={resumeHeld}
        onChanged={() => {
          api.get<import('../lib/api').HeldSale[]>('/api/v1/held-sales')
            .then((l) => setHeldCount(Array.isArray(l) ? l.length : 0))
            .catch(() => {})
        }}
      />

      <ReceiptModal
        open={!!receiptFor}
        order={receiptFor!}
        branding={branding}
        onClose={() => setReceiptFor(null)}
      />
    </div>
  )
}

function CategoryChip({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button
      onClick={onClick}
      aria-pressed={active}
      className={`min-h-9 px-4 rounded-pill border font-semibold text-[13px] whitespace-nowrap transition-colors ${
        active ? 'bg-brand border-brand text-white' : 'bg-surface text-ink-muted border-line hover:text-ink hover:border-line-strong'
      }`}
    >
      {children}
    </button>
  )
}

function StockBadge({ p }: { p: Product }) {
  if (!p.trackStock) return null
  const out = p.stockQty <= 0
  const low = !out && p.stockQty <= 5
  return (
    <span
      className={`absolute top-1.5 right-1.5 z-10 px-1.5 py-0.5 rounded-pill text-[10px] font-bold tabular ${
        out ? 'bg-danger-bg text-danger-text' : low ? 'bg-pending-bg text-pending-text' : 'bg-surface-muted text-ink-subtle'
      }`}
    >
      {out ? 'OUT' : low ? `${p.stockQty} low` : `${p.stockQty}`}
    </span>
  )
}

function ProductCard({ p, qty, onAdd, onInc, onDec }: {
  p: Product
  qty: number
  onAdd: () => void
  onInc: () => void
  onDec: () => void
}) {
  const out = p.trackStock && p.stockQty <= 0
  const inCart = qty > 0
  return (
    <div
      className={`relative flex flex-col rounded-input border bg-surface overflow-hidden transition-colors ${
        out ? 'border-line opacity-60' : inCart ? 'border-brand/50' : 'border-line hover:border-line-strong hover:shadow-brutal-sm'
      }`}
    >
      <StockBadge p={p} />
      <button
        onClick={onAdd}
        disabled={out}
        aria-label={out ? `${p.name} — out of stock` : `Add ${p.name} to cart`}
        className="text-left flex flex-col flex-1 min-h-0"
      >
        <div className="h-24 sm:h-28 w-full bg-surface-muted border-b border-line overflow-hidden shrink-0 p-1">
          <ProductPhoto p={p} />
        </div>
        <div className="flex-1 min-h-0 p-2 pb-1 flex flex-col">
          <p className="font-semibold text-[13px] leading-snug text-ink line-clamp-2 min-h-[2.1em]">{p.name}</p>
          <p className="text-[11px] text-ink-subtle mt-0.5 truncate">{p.sku || p.categoryName || '\u00a0'}</p>
        </div>
      </button>
      {/* Full-width control zone — the + Add button and the − qty + stepper
          span the whole card, so the stepper can never be squeezed off the
          card by a long price (the "cards only have a + button" bug). */}
      <div className="px-2 pb-2 pt-1 space-y-1.5">
        <div className="flex items-center justify-between gap-1.5">
          <span className={`font-bold text-[15px] tabular leading-none ${inCart ? 'text-brand' : 'text-ink'}`}>{formatMoneyCompact(p.priceCents)}</span>
          {inCart && <span className="text-[10px] font-bold uppercase tracking-wide text-brand shrink-0">in cart</span>}
        </div>
        {inCart ? (
          // Quantity-aware control: once the item is in the sale the card
          // becomes a stepper (− qty +) so the cashier never has to hunt
          // through the cart to adjust a count.
          <div className="flex items-stretch rounded-input border border-brand overflow-hidden h-9 bg-brand-soft" role="group" aria-label={`${p.name} quantity in cart`}>
            <button
              onClick={onDec}
              aria-label={qty === 1 ? `Remove ${p.name} from cart` : `Decrease ${p.name} quantity`}
              className="w-10 font-bold text-brand hover:bg-brand/10 flex items-center justify-center"
            >
              <Minus size={15} strokeWidth={3} aria-hidden />
            </button>
            <span className="flex-1 text-center font-extrabold text-brand text-[14px] tabular flex items-center justify-center" aria-live="polite">{qty}</span>
            <button
              onClick={onInc}
              aria-label={`Increase ${p.name} quantity`}
              disabled={out || (p.trackStock && qty >= p.stockQty)}
              className="w-10 font-bold text-brand hover:bg-brand/10 disabled:opacity-55 flex items-center justify-center"
            >
              <Plus size={15} strokeWidth={3} aria-hidden />
            </button>
          </div>
        ) : (
          <button
            onClick={onAdd}
            disabled={out}
            aria-label={`Add ${p.name} to cart`}
            className={`w-full h-9 rounded-input font-bold text-[13px] flex items-center justify-center gap-1 transition-colors ${
              out
                ? 'bg-surface-muted text-ink-subtle cursor-not-allowed'
                : 'bg-brand text-white hover:brightness-110 active:brightness-95'
            }`}
          >
            <Plus size={15} strokeWidth={2.75} aria-hidden />
            {out ? 'Out of stock' : 'Add'}
          </button>
        )}
      </div>
    </div>
  )
}

function ProductRow({ p, qty, onAdd, onInc, onDec }: {
  p: Product
  qty: number
  onAdd: () => void
  onInc: () => void
  onDec: () => void
}) {
  const out = p.trackStock && p.stockQty <= 0
  const inCart = qty > 0
  return (
    <div className={`flex items-center gap-3 p-2 rounded-input border bg-surface ${out ? 'border-line opacity-60' : inCart ? 'border-brand/50' : 'border-line'}`}>
      <div className="w-11 h-11 rounded-input overflow-hidden bg-surface-muted border border-line shrink-0 p-0.5">
        <ProductPhoto p={p} />
      </div>
      <button onClick={onAdd} disabled={out} className="flex-1 min-w-0 text-left">
        <p className="font-semibold text-[13px] text-ink leading-snug truncate">{p.name}</p>
        <p className="text-[11px] text-ink-subtle">{p.sku || p.categoryName}</p>
      </button>
      {p.trackStock && (
        <span className={`text-[10px] font-bold tabular whitespace-nowrap px-1.5 py-0.5 rounded-pill ${out ? 'bg-danger-bg text-danger-text' : 'bg-surface-muted text-ink-subtle'}`}>
          {out ? 'OUT' : `${p.stockQty}`}
        </span>
      )}
      <span className={`font-bold text-[14px] tabular whitespace-nowrap ${inCart ? 'text-brand' : 'text-ink'}`}>{formatMoneyCompact(p.priceCents)}</span>
      {inCart ? (
        <div className="flex items-center rounded-pill border border-brand overflow-hidden h-8 bg-brand-soft shrink-0" role="group" aria-label={`${p.name} quantity in cart`}>
          <button
            onClick={onDec}
            aria-label={qty === 1 ? `Remove ${p.name} from cart` : `Decrease ${p.name} quantity`}
            className="w-7 h-full font-bold text-brand hover:bg-brand/10 flex items-center justify-center"
          >
            <Minus size={13} strokeWidth={3} aria-hidden />
          </button>
          <span className="w-6 text-center font-extrabold text-brand text-[13px] tabular" aria-live="polite">{qty}</span>
          <button
            onClick={onInc}
            aria-label={`Increase ${p.name} quantity`}
            disabled={out || (p.trackStock && qty >= p.stockQty)}
            className="w-7 h-full font-bold text-brand hover:bg-brand/10 disabled:opacity-55 flex items-center justify-center"
          >
            <Plus size={13} strokeWidth={3} aria-hidden />
          </button>
        </div>
      ) : (
        <button
          onClick={onAdd}
          disabled={out}
          aria-label={`Add ${p.name} to cart`}
          className={`w-8 h-8 rounded-full flex items-center justify-center shrink-0 ${
            out ? 'bg-surface-muted text-ink-subtle cursor-not-allowed' : 'bg-brand text-white hover:brightness-110'
          }`}
        >
          <Plus size={16} strokeWidth={2.75} aria-hidden />
        </button>
      )}
    </div>
  )
}
