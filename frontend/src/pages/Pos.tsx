// POS terminal — the money screen. 65/35 split: product grid + barcode
// search + category chips on the left, sticky cart on the right. Touch
// targets ≥ 64px on tiles, ≥44px everywhere else. Stacks under 1024px with
// a cart bottom-sheet (view/edit/remove lines on phones too).
//
// Barcode entry works two ways: focused typing into the search box (exact
// match auto-adds) AND a global HID listener so hardware USB/Bluetooth
// scanners fire straight into the cart with no input focused at all
// (scanners "type" very fast and press Enter — we buffer rapid keystrokes
// and only accept the burst pattern, so human typing never triggers it).

import { useEffect, useMemo, useRef, useState } from 'react'
import { api, Category, CheckoutRequest, Customer, Order, Product } from '../lib/api'
import { useCart } from '../stores/cart'
import { useBranding } from '../stores/branding'
import { useAuth } from '../stores/auth'
import { formatMoneyCompact, formatMoney, normalizePhoneKe } from '../lib/money'
import { Button, EmptyState, Input, Modal, Field, MoneyInput, Spinner, Tabs } from '../components/ui'
import { MpesaModal } from '../components/MpesaModal'
import { ReceiptModal } from '../components/Receipt'
import { toast } from '../stores/toasts'
import { enqueue, newClientUuid } from '../offline/queue'
import { useNet } from '../offline/heartbeat'
import {
  ShoppingCart, Search, Banknote, Smartphone, X, Minus, Plus, ScanBarcode, AlertTriangle, BookUser,
} from 'lucide-react'

export function Pos() {
  const { user } = useAuth()
  const cart = useCart()
  const online = useNet((s) => s.online)
  const [products, setProducts] = useState<Product[] | null>(null)
  const [categories, setCategories] = useState<Category[]>([])
  const [search, setSearch] = useState('')
  const [cat, setCat] = useState<number | 'all'>('all')
  const [chargeOpen, setChargeOpen] = useState(false)
  const [cartSheetOpen, setCartSheetOpen] = useState(false)
  const [mpesaOrder, setMpesaOrder] = useState<Order | null>(null)
  const [mpesaOpen, setMpesaOpen] = useState(false)
  const [receiptFor, setReceiptFor] = useState<Order | null>(null)
  const searchRef = useRef<HTMLInputElement>(null)

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
    return products.filter((p) => {
      if (!p.active) return false
      if (cat !== 'all' && p.categoryId !== cat) return false
      if (q && !p.name.toLowerCase().includes(q) && !p.sku.toLowerCase().includes(q) && !p.barcode.includes(q)) return false
      return true
    })
  }, [products, search, cat])

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

  const addToCart = (p: Product) => {
    if (p.trackStock && p.stockQty <= 0) {
      toast.error('Out of stock', p.name)
      return
    }
    cart.add(p)
    if (navigator.vibrate) navigator.vibrate(10)
  }

  return (
    <div className="h-full flex flex-col -m-3 sm:-m-4 lg:-m-5">
      {/* Topbar: search + cart summary for small screens */}
      <div className="px-3 sm:px-4 lg:px-5 pt-3 sm:pt-4 lg:pt-5 pb-2 flex gap-2 items-center shrink-0">
        <div className="relative flex-1">
          <Search size={16} strokeWidth={2.5} className="absolute left-3 top-1/2 -translate-y-1/2 text-ink-subtle pointer-events-none" aria-hidden />
          <Input
            ref={searchRef as any}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Scan barcode or search products… (F2)"
            className="flex-1 h-12 text-base pl-9"
            autoFocus
          />
        </div>
        <div className="lg:hidden shrink-0">
          <Button variant="secondary" onClick={() => setCartSheetOpen(true)} disabled={cart.lines.length === 0} className="h-12 relative">
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

      {/* Category chips */}
      <div className="px-3 sm:px-4 lg:px-5 pb-3 flex gap-2 overflow-x-auto shrink-0" role="tablist" aria-label="Categories">
        <button
          onClick={() => setCat('all')}
          className={`min-h-11 px-4 rounded-pill border-2 font-semibold text-sm whitespace-nowrap ${
            cat === 'all' ? 'bg-line-strong text-surface border-line-strong' : 'bg-surface text-ink-muted border-line hover:text-ink'
          }`}
        >
          All
        </button>
        {categories.map((c) => (
          <button
            key={c.id}
            onClick={() => setCat(c.id)}
            className={`min-h-11 px-4 rounded-pill border-2 font-semibold text-sm whitespace-nowrap ${
              cat === c.id ? 'bg-line-strong text-surface border-line-strong' : 'bg-surface text-ink-muted border-line hover:text-ink'
            }`}
          >
            {c.name}
          </button>
        ))}
      </div>

      {/* 65/35 grid */}
      <div className="flex-1 min-h-0 grid gap-3 px-3 sm:px-4 lg:px-5 pb-3 sm:pb-4 lg:pb-5 lg:grid-cols-[65fr_35fr]">
        {/* Product grid */}
        <div className="min-h-0 overflow-y-auto bg-surface border-2 border-line-strong rounded-card p-2 sm:p-3 shadow-brutal">
          {!products ? (
            <div className="py-16 flex justify-center"><Spinner className="w-7 h-7 border-4" /></div>
          ) : filtered.length === 0 ? (
            <EmptyState icon={<ScanBarcode size={24} strokeWidth={2.25} />} title="No products match" body={search ? `Nothing found for “${search}”.` : 'Add products in Inventory first.'} />
          ) : (
            <div className="grid grid-cols-2 min-[480px]:grid-cols-3 md:grid-cols-4 xl:grid-cols-5 2xl:grid-cols-6 gap-2 sm:gap-2.5">
              {filtered.map((p) => {
                const low = p.trackStock && p.stockQty <= 5
                const out = p.trackStock && p.stockQty <= 0
                return (
                  <button
                    key={p.id}
                    onClick={() => addToCart(p)}
                    disabled={out}
                    className={`group text-left p-2.5 rounded-input border-2 shadow-brutal-sm transition-transform duration-75
                      active:translate-x-[2px] active:translate-y-[2px] active:shadow-none min-h-[104px] flex flex-col justify-between
                      ${out ? 'bg-surface-muted border-line opacity-50 cursor-not-allowed' : 'bg-surface border-line-strong hover:bg-surface-muted'}`}
                  >
                    <div>
                      <p className="font-bold text-[13px] leading-snug text-ink line-clamp-2">{p.name}</p>
                      <p className="text-[11px] text-ink-subtle mt-0.5">{p.sku || p.categoryName}</p>
                    </div>
                    <div className="flex items-end justify-between gap-1.5 mt-2">
                      <span className="font-black text-[15px] text-ink tabular leading-none whitespace-nowrap">{formatMoneyCompact(p.priceCents)}</span>
                      {p.trackStock && (
                        <span className={`text-[10px] font-bold tabular whitespace-nowrap ${out ? 'text-danger-text' : low ? 'text-pending-text' : 'text-ink-subtle'}`}>
                          {out ? 'OUT' : `${p.stockQty} left`}
                        </span>
                      )}
                    </div>
                  </button>
                )
              })}
            </div>
          )}
        </div>

        {/* Cart (sticky on desktop) */}
        <aside className="hidden lg:flex min-h-0 flex-col bg-surface border-2 border-line-strong rounded-card shadow-brutal">
          <CartBody onCharge={() => setChargeOpen(true)} />
        </aside>
      </div>

      {/* Mobile cart bottom-sheet — full cart editing on phones */}
      <Modal
        open={cartSheetOpen}
        onClose={() => setCartSheetOpen(false)}
        title={`Cart — ${formatMoney(totals.total)}`}
        footer={
          <>
            <Button variant="ghost" onClick={() => { cart.clear(); setCartSheetOpen(false) }} disabled={cart.lines.length === 0}>
              Clear all
            </Button>
            <Button variant="primary" onClick={() => { setCartSheetOpen(false); setChargeOpen(true) }} disabled={cart.lines.length === 0}>
              Charge {formatMoneyCompact(totals.total)}
            </Button>
          </>
        }
      >
        <CartBody onCharge={() => { setCartSheetOpen(false); setChargeOpen(true) }} sheet />
      </Modal>

      {/* Charge modal (shared by mobile button + desktop charge) */}
      <ChargeModal
        open={chargeOpen}
        onClose={() => setChargeOpen(false)}
        onMpesa={(o) => { setChargeOpen(false); setMpesaOrder(o); setMpesaOpen(true) }}
        onDone={(o) => {
          setChargeOpen(false)
          cart.clear()
          load()
          if (o) {
            toast.success(`Sale complete — ${o.number}`, formatMoney(o.totalCents))
            setReceiptFor(o) // show the printable receipt straight away
          }
        }}
        online={online}
        cashierName={user?.fullName || user?.username || ''}
      />

      <MpesaModal
        open={mpesaOpen}
        order={mpesaOrder}
        onClose={() => { setMpesaOpen(false); setMpesaOrder(null); cart.clear(); load() }}
        onPaid={(o) => { load(); setMpesaOrder(o) }}
      />

      <ReceiptModal
        open={!!receiptFor}
        order={receiptFor!}
        branding={useBranding.getState().branding}
        onClose={() => setReceiptFor(null)}
      />
    </div>
  )
}

function CartBody({ onCharge, sheet }: { onCharge: () => void; sheet?: boolean }) {
  const cart = useCart()
  const branding = useBranding((s) => s.branding)
  const totals = cart.totals()
  const canOverride = useAuth((s) => !!s.user?.permissions.includes('payments.override_price'))
  const [editing, setEditing] = useState<number | null>(null)
  const [overrideVal, setOverrideVal] = useState(0)

  return (
    <>
      {!sheet && (
        <header className="px-4 pt-4 pb-2 border-b-2 border-line flex items-center justify-between">
          <h2 className="font-bold text-ink">Cart</h2>
          <span className="text-ink-muted text-sm tabular font-semibold">{totals.count} item{totals.count === 1 ? '' : 's'}</span>
        </header>
      )}

      <div className={`${sheet ? '' : 'flex-1 min-h-0'} overflow-y-auto px-1 py-2 space-y-2`}>
        {cart.lines.length === 0 ? (
          <EmptyState icon={<ShoppingCart size={24} strokeWidth={2.25} />} title="Cart is empty" body="Tap products or scan a barcode to add them." />
        ) : (
          cart.lines.map((l) => (
            <div key={l.productId} className="border-2 border-line rounded-input p-2.5 bg-surface">
              {editing === l.productId ? (
                <div className="space-y-2">
                  <p className="text-[13px] font-bold text-ink">{l.name}</p>
                  <div className="flex gap-2 items-end">
                    <div className="flex-1">
                      <Field label="Unit price">
                        <MoneyInput value={overrideVal} onCents={setOverrideVal} autoFocus />
                      </Field>
                    </div>
                    <Button size="sm" variant="primary" onClick={() => { cart.setQty(l.productId, l.qty); setEditing(null) }}>
                      Set
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setEditing(null)}>Cancel</Button>
                  </div>
                </div>
              ) : (
                <>
                  <div className="flex items-start justify-between gap-2">
                    <p className="text-[13px] font-bold text-ink leading-snug flex-1">{l.name}</p>
                    <button
                      onClick={() => cart.remove(l.productId)}
                      aria-label={`Remove ${l.name} from cart`}
                      className="text-ink-subtle hover:text-danger-text min-w-9 min-h-9 flex items-center justify-center rounded-input hover:bg-danger-bg"
                    >
                      <X size={16} strokeWidth={2.5} aria-hidden />
                    </button>
                  </div>
                  <div className="flex items-center justify-between gap-2 mt-1.5">
                    {/* Qty stepper — minus at 0 removes the line */}
                    <div className="flex items-center border-2 border-line-strong rounded-input overflow-hidden h-11">
                      <button
                        onClick={() => cart.setQty(l.productId, l.qty - 1)}
                        aria-label={l.qty === 1 ? `Remove ${l.name}` : 'Decrease quantity'}
                        className="w-11 h-full bg-surface-muted font-black text-ink active:bg-line flex items-center justify-center"
                      >
                        {l.qty === 1 ? <X size={15} strokeWidth={2.75} aria-hidden /> : <Minus size={15} strokeWidth={2.75} aria-hidden />}
                      </button>
                      <span className="w-10 text-center font-black tabular text-ink">{l.qty}</span>
                      <button
                        onClick={() => cart.setQty(l.productId, Math.min(999, l.qty + 1))}
                        aria-label="Increase quantity"
                        className="w-11 h-full bg-surface-muted font-black text-ink active:bg-line flex items-center justify-center"
                      >
                        <Plus size={15} strokeWidth={2.75} aria-hidden />
                      </button>
                    </div>
                    <div className="text-right">
                      <p className="font-black text-ink tabular leading-none">{formatMoneyCompact(l.qty * l.unitPriceCents)}</p>
                      <button
                        onClick={() => { if (canOverride) { setEditing(l.productId); setOverrideVal(l.unitPriceCents) } else toast.error('Price override needs permission') }}
                        className={`text-[11px] font-semibold mt-1 ${canOverride ? 'text-ink-subtle hover:text-ink underline' : 'text-ink-subtle/50'}`}
                      >
                        @ {formatMoneyCompact(l.unitPriceCents)}
                      </button>
                    </div>
                  </div>
                </>
              )}
            </div>
          ))
        )}
      </div>

      {!sheet && (
        <footer className="border-t-2 border-line px-4 py-3 space-y-2 bg-surface-muted/60 rounded-b-card">
          <Row label="Subtotal" value={formatMoney(totals.subtotal)} />
          <Row label={`${branding.tax_percent}% ${branding.tax_included ? 'VAT (incl.)' : 'VAT'}`} value={formatMoney(totals.tax)} />
          <div className="flex items-baseline justify-between border-t-2 border-line pt-2">
            <span className="font-bold text-ink">Total</span>
            <span className="font-black text-[40px] leading-none text-ink tabular">{formatMoney(totals.total)}</span>
          </div>
          <Button
            variant="primary"
            size="lg"
            className="w-full h-16 text-xl"
            onClick={onCharge}
            disabled={cart.lines.length === 0}
          >
            Charge {formatMoney(totals.total)}
          </Button>
        </footer>
      )}
    </>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between text-sm">
      <span className="text-ink-muted">{label}</span>
      <span className="text-ink font-semibold tabular">{value}</span>
    </div>
  )
}

// ---- Charge modal: method pick, cash quick-tender, M-Pesa ----

function ChargeModal({
  open,
  onClose,
  onMpesa,
  onDone,
  online,
  cashierName,
}: {
  open: boolean
  onClose: () => void
  onMpesa: (o: Order) => void
  onDone: (o: Order | null) => void
  online: boolean
  cashierName: string
}) {
  const cart = useCart()
  const branding = useBranding((s) => s.branding)
  const [method, setMethod] = useState<'cash' | 'mpesa' | 'tab'>('cash')
  const [customerName, setCustomerName] = useState('')
  const [phone, setPhone] = useState('')
  const [received, setReceived] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [tabQuery, setTabQuery] = useState('')
  const [tabOptions, setTabOptions] = useState<Customer[]>([])
  const [tabCustomer, setTabCustomer] = useState<Customer | null>(null)
  const totals = cart.totals()

  // Fresh slate every time the modal opens (no stale tender amounts).
  useEffect(() => {
    if (open) {
      setMethod('cash')
      setReceived(null)
      setError('')
      setPhone('')
      setCustomerName(cart.customerName)
      setTabQuery('')
      setTabOptions([])
      setTabCustomer(null)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  // Tab customer search (server enforces the credit limit at charge time).
  useEffect(() => {
    if (method !== 'tab' || !open) return
    const q = tabQuery.trim()
    if (!q) {
      setTabOptions([])
      return
    }
    const t = window.setTimeout(async () => {
      try {
        setTabOptions(await api.get<Customer[]>(`/api/v1/customers?search=${encodeURIComponent(q)}`))
      } catch {
        /* offline — tabs need the server for limit checks */
      }
    }, 250)
    return () => window.clearTimeout(t)
  }, [method, open, tabQuery])

  const change = received !== null ? received - totals.total : null
  const canCash = received === null || change !== null && change >= 0

  const checkout = async (paymentMethod: 'cash' | 'mpesa' | 'tab', paymentMode?: 'auto' | 'stk' | 'manual') => {
    setBusy(true)
    setError('')
    const clientUuid = newClientUuid()
    const body: CheckoutRequest = {
      items: cart.lines.map((l) => ({ productId: l.productId, qty: l.qty })),
      paymentMethod: paymentMethod === 'tab' ? 'account' : paymentMethod,
      paymentMode,
      customerName: customerName.trim() || undefined,
      customerPhone: phone.trim() || undefined,
      customerId: paymentMethod === 'tab' ? tabCustomer?.id : undefined,
      clientUuid,
    }
    try {
      const o = await api.post<Order>('/api/v1/orders/checkout', body)
      if (paymentMethod === 'mpesa' && o.status === 'PENDING') {
        onMpesa(o)
      } else {
        onDone(o)
      }
    } catch (err: any) {
      // Network failure → queue offline (cash only; M-Pesa needs a live
      // connection to know push state).
      if (!navigator.onLine || /network|fetch/i.test(String(err))) {
        if (paymentMethod === 'cash') {
          try {
            await enqueue(body)
            toast.info('Saved offline', 'Will sync when back online.')
            onDone(null)
            return
          } catch {
            /* IndexedDB unavailable */
          }
        }
      }
      setError(err?.message || 'Checkout failed')
    } finally {
      setBusy(false)
    }
  }

  if (!open) return null

  const quick = [totals.total, 100000, 200000, 500000, 1000000]

  return (
    <Modal open={open} onClose={onClose} title="Charge" size="sm">
      <div className="text-center mb-4">
        <p className="text-ink-muted text-sm font-semibold">Total due</p>
        <p className="text-[40px] leading-none font-black text-ink tabular">{formatMoney(totals.total)}</p>
      </div>

      <div className="space-y-3">
        <Tabs
          tabs={[
            { key: 'cash' as const, label: 'Cash', icon: <Banknote size={15} strokeWidth={2.25} aria-hidden /> },
            { key: 'mpesa' as const, label: `M-Pesa${branding.mpesa_env === 'mock' ? ' (demo)' : ''}`, icon: <Smartphone size={15} strokeWidth={2.25} aria-hidden /> },
            { key: 'tab' as const, label: 'Tab', icon: <BookUser size={15} strokeWidth={2.25} aria-hidden /> },
          ]}
          value={method}
          onChange={setMethod}
        />

        <Field label="Customer name (optional)">
          <Input value={customerName} onChange={(e) => setCustomerName(e.target.value)} placeholder="Walk-in" />
        </Field>

        {method === 'cash' ? (
          <>
            <Field label="Cash received">
              <MoneyInput value={received ?? 0} onCents={(c) => setReceived(c)} placeholder="0.00" className="text-lg font-bold" />
            </Field>
            <div className="grid grid-cols-5 gap-2">
              {quick.map((q, i) => (
                <button
                  key={i}
                  onClick={() => setReceived(q)}
                  className="min-h-12 text-[12px] font-bold bg-surface-muted border-2 border-line rounded-input hover:border-line-strong active:translate-y-[1px]"
                >
                  {i === 0 ? 'Exact' : formatMoneyCompact(q)}
                </button>
              ))}
            </div>
            {change !== null && (
              <div className={`rounded-input border-2 p-3 flex justify-between items-baseline ${change < 0 ? 'bg-danger-bg border-danger-text/30' : 'bg-paid-bg border-paid-text/30'}`}>
                <span className={`font-bold text-sm ${change < 0 ? 'text-danger-text' : 'text-paid-text'}`}>
                  {change < 0 ? 'Still owed' : 'Change'}
                </span>
                <span className={`font-black text-2xl tabular ${change < 0 ? 'text-danger-text' : 'text-paid-text'}`}>
                  {formatMoney(Math.abs(change))}
                </span>
              </div>
            )}
          </>
        ) : method === 'tab' ? (
          <>
            <Field label="Tab customer" hint="Server enforces their credit limit at charge time.">
              <Input
                value={tabCustomer ? `${tabCustomer.name} · owes ${formatMoney(tabCustomer.balanceCents)}` : tabQuery}
                onChange={(e) => { setTabCustomer(null); setTabQuery(e.target.value) }}
                placeholder="Type a name or phone…"
              />
            </Field>
            {!tabCustomer && tabOptions.length > 0 && (
              <div className="border-2 border-line rounded-input overflow-hidden">
                {tabOptions.slice(0, 6).map((c) => (
                  <button
                    key={c.id}
                    type="button"
                    onClick={() => { setTabCustomer(c); setTabQuery('') }}
                    className="w-full flex items-center justify-between gap-2 px-3 py-2.5 text-left hover:bg-surface-muted active:bg-surface-muted"
                  >
                    <span>
                      <span className="block text-[13px] font-bold text-ink">{c.name}</span>
                      <span className="block text-[11px] text-ink-subtle">{c.phone || 'no phone'}</span>
                    </span>
                    <span className={`text-[12px] font-bold ${c.creditLimitCents <= 0 ? 'text-ink-subtle' : c.balanceCents >= c.creditLimitCents ? 'text-danger-text' : 'text-ink-muted'}`}>
                      {c.creditLimitCents <= 0 ? 'cash only' : `owes ${formatMoney(c.balanceCents)} / ${formatMoney(c.creditLimitCents)}`}
                    </span>
                  </button>
                ))}
              </div>
            )}
            {tabCustomer && tabCustomer.creditLimitCents <= 0 && (
              <p className="text-danger-text text-[13px] font-semibold">This customer is cash-only — pick someone with credit.</p>
            )}
            {!online && (
              <p className="text-pending-text text-[13px] font-bold">You're offline — tabs need the server for limit checks.</p>
            )}
          </>
        ) : (
          <>
            <Field label="Customer M-Pesa phone" hint={branding.payment_mode === 'manual' ? 'Optional — the customer pays to the till/paybill themselves.' : 'The payment prompt is sent to this number.'}>
              <Input
                value={phone}
                onChange={(e) => setPhone(e.target.value)}
                inputMode="tel"
                placeholder="07XX XXX XXX"
              />
            </Field>
            <div className="bg-surface-muted border-2 border-line rounded-input p-3 text-[13px] text-ink-muted space-y-1">
              {branding.payment_mode === 'manual' ? (
                <p>The customer pays to {branding.paybill_number ? 'Paybill ' + branding.paybill_number : 'Till ' + (branding.till_number || '—')} and you enter their receipt code next.</p>
              ) : (
                <>
                  <p>An M-Pesa STK prompt is sent to the customer's phone right away.</p>
                  <p>Manual receipt-code entry is always available as fallback.</p>
                </>
              )}
              {!online && (
                <p className="text-pending-text font-bold flex items-center gap-1.5">
                  <AlertTriangle size={14} strokeWidth={2.5} aria-hidden />
                  You're offline — M-Pesa needs a connection. Cash sales keep working.
                </p>
              )}
            </div>
          </>
        )}

        {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}

        <Button
          variant="primary"
          size="lg"
          className="w-full h-16 text-xl"
          disabled={busy || (method === 'cash' && !canCash) || (method === 'mpesa' && branding.payment_mode !== 'manual' && !normalizePhoneKe(phone)) || (method === 'tab' && (!online || !tabCustomer || tabCustomer.creditLimitCents <= 0))}
          onClick={() => checkout(method, method === 'mpesa' ? (branding.payment_mode as 'auto' | 'stk' | 'manual') : undefined)}
        >
          {busy ? <Spinner className="border-t-brand-ink" /> : method === 'cash' ? `Take ${formatMoney(totals.total)}` : method === 'tab' ? `Charge ${formatMoney(totals.total)} to tab` : 'Charge via M-Pesa →'}
        </Button>
        <p className="text-center text-[11px] text-ink-subtle">Served by {cashierName}</p>
      </div>
    </Modal>
  )
}
