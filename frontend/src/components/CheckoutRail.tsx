// CheckoutRail — the right-hand checkout rail (desktop 430px; the same
// component fills the mobile bottom sheet). Everything a cashier needs for
// one sale lives here in one vertical flow:
//   cart header → item rows → totals → customer → payment tiles → charge.
// Payment feedback renders IN-RAIL (STK waiting, Paystack inline, paid,
// failed + retry) — the customer never leaves the POS screen and no
// separate browser windows/tabs are ever opened. Card/mobile-money rides
// Paystack Popup V2 (server-minted access_code → in-page iframe), and
// M-Pesa STK pushes straight to the customer's phone captured first in the
// Customer section. Email is optional (receipt only); phone is the primary
// M-Pesa identifier.

import { useEffect, useMemo, useRef, useState } from 'react'
import { api, CheckoutRequest, Customer, Order, PaymentConfig, Product, SplitLeg } from '../lib/api'
import { openPaystackPopup } from '../lib/api'
import { useCart } from '../stores/cart'
import { useBranding } from '../stores/branding'
import { useAuth } from '../stores/auth'
import { formatMoney, formatMoneyCompact, normalizePhoneKe, splitRemaining } from '../lib/money'
import { Button, EmptyState, Field, Input, MoneyInput, Spinner, StatusPill, Modal } from './ui'
import { SplitTenderEditor, isAsyncLeg } from './SplitTenderEditor'
import { ReceiptModal } from './Receipt'
import { toast } from '../stores/toasts'
import { enqueue, newClientUuid } from '../offline/queue'
import { useNet } from '../offline/heartbeat'
import {
  ShoppingCart, Smartphone, CreditCard, Banknote, BookUser, Wallet, X, Minus, Plus,
  Check, AlertTriangle, RotateCcw, Ban, Star, SplitSquareHorizontal, Pause, Archive, Trash2,
} from 'lucide-react'

type Method = 'mpesa' | 'card' | 'cash' | 'tab' | 'credit'
type StkStage = 'sending' | 'waiting' | 'paid' | 'failed'
type InlineStage = 'init' | 'popup' | 'verifying' | 'paid' | 'failed'

export function CheckoutRail({
  products,
  payConfig,
  onOrderPaid,
  onOrderVoided,
  canHold,
  heldCount,
  onOpenHeld,
}: {
  products: Product[] | null
  payConfig: PaymentConfig | null
  onOrderPaid: (o: Order) => void
  onOrderVoided: (o: Order) => void
  canHold?: boolean
  heldCount?: number
  onOpenHeld?: () => void
}) {
  const cart = useCart()
  const branding = useBranding((s) => s.branding)
  const online = useNet((s) => s.online)
  const user = useAuth((s) => s.user)
  const totals = cart.totals()

  const [method, setMethod] = useState<Method>('mpesa')
  const [customerName, setCustomerName] = useState('')
  const [phone, setPhone] = useState('')
  const [email, setEmail] = useState('')
  const [received, setReceived] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // tab / credit picker
  const [tabQuery, setTabQuery] = useState('')
  const [tabOptions, setTabOptions] = useState<Customer[]>([])
  const [tabCustomer, setTabCustomer] = useState<Customer | null>(null)
  // order-level adjustments
  const [discountCents, setDiscountCents] = useState(0)
  const [discountLabel, setDiscountLabel] = useState('')
  const [redeem, setRedeem] = useState(0)
  const [splitMode, setSplitMode] = useState(false)
  const [splitLegs, setSplitLegs] = useState<SplitLeg[]>([])
  const [editing, setEditing] = useState<number | null>(null)
  const [overrideVal, setOverrideVal] = useState(0)
  // in-rail payment state machines
  const [mode, setMode] = useState<'idle' | 'stk' | 'inline'>('idle')
  const [stkStage, setStkStage] = useState<StkStage>('sending')
  const [inlineStage, setInlineStage] = useState<InlineStage>('init')
  const [activeOrder, setActiveOrder] = useState<Order | null>(null)
  const [lastRef, setLastRef] = useState('')
  const [elapsed, setElapsed] = useState(0)
  const [payError, setPayError] = useState('')
  const [receiptOpen, setReceiptOpen] = useState(false)
  const [parkOpen, setParkOpen] = useState(false)
  // Paystack fires onClose after the success callback on some flows —
  // once settled, late cancel events must be ignored.
  const settledRef = useRef(false)
  const pollRef = useRef<number | null>(null)
  const timerRef = useRef<number | null>(null)

  const canOverride = useAuth((s) => !!s.user?.permissions.includes('payments.override_price'))
  const canDiscount = !!user?.permissions.includes('payments.apply_discount')
  const canRedeem = !!user?.permissions.includes('loyalty.redeem')
  const paystackReady = !!(payConfig && !Array.isArray(payConfig) && payConfig.paystack?.enabled && payConfig.paystack?.configured)
  const creditEnabled = !!(payConfig && payConfig.creditEnabled)
  const loyaltyEnabled = !!(payConfig && payConfig.loyaltyEnabled)
  const manualMode = branding.payment_mode === 'manual'
  const manualOnly = manualMode || (!paystackReady && !online)

  const resetPayment = () => {
    settledRef.current = false
    stopTimers()
    setMode('idle')
    setStkStage('sending')
    setInlineStage('init')
    setActiveOrder(null)
    setLastRef('')
    setElapsed(0)
    setPayError('')
  }

  const stopTimers = () => {
    if (pollRef.current) { window.clearInterval(pollRef.current); pollRef.current = null }
    if (timerRef.current) { window.clearInterval(timerRef.current); timerRef.current = null }
  }

  // Reset tender form when the cart is cleared/replaced.
  useEffect(() => {
    setCustomerName(cart.customerName)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [cart.customerName])

  useEffect(() => () => stopTimers(), [])

  // Order-level adjustments — an APPROXIMATION of the server's math (the
  // backend recomputes tax on the discounted subtotal and caps redemption
  // authoritatively; it trims whole points when the cap bites).
  const discountedSub = Math.max(0, totals.subtotal - (canDiscount ? Math.min(discountCents, totals.subtotal) : 0))
  const estTotal = branding.tax_included
    ? discountedSub
    : discountedSub + Math.round((discountedSub * branding.tax_percent) / 100)
  const estRedeemCents = redeem > 0 ? Math.min(redeem * 100, Math.floor(estTotal / 2)) : 0
  const due = Math.max(0, estTotal - estRedeemCents)
  const tenderDue = (canDiscount && discountCents > 0) || estRedeemCents > 0 ? due : totals.total

  const change = received !== null ? received - tenderDue : null
  const canCash = received === null || (change !== null && change >= 0)

  // Kenyan phone sanity for the M-Pesa tile ("Required for M-Pesa").
  const normalizedPhone = normalizePhoneKe(phone)
  const phoneOk = !!normalizedPhone || manualMode || method !== 'mpesa'

  // ---- Split-tender validation (mirrors the backend rules) ----
  const splitNeedsCustomer = splitMode && splitLegs.some((l) => l.method === 'credit')
  const splitAsyncLeg = splitMode ? splitLegs.find(isAsyncLeg) : undefined
  const splitMpesaLeg = splitMode ? splitLegs.find((l) => l.method === 'mpesa') : undefined
  const splitValid =
    !!splitMode &&
    splitLegs.length > 0 &&
    splitLegs.every((l) => l.amountCents > 0) &&
    splitRemaining(tenderDue, splitLegs) === 0 &&
    splitLegs.filter(isAsyncLeg).length <= 1 &&
    (!splitLegs.some((l) => l.method === 'credit') || (!!tabCustomer && creditOf(tabCustomer) > 0 && online)) &&
    (!splitLegs.some((l) => l.method === 'paystack' || l.method === 'mpesa') || online) &&
    (!splitMpesaLeg || manualMode || !!normalizePhoneKe(splitMpesaLeg.phone || '') || !!normalizedPhone)

  // Tab / store-credit customer search (server enforces limits at charge time).
  useEffect(() => {
    if (mode !== 'idle') return
    if (method !== 'tab' && method !== 'credit' && !splitNeedsCustomer) return
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
  }, [method, mode, tabQuery, splitNeedsCustomer])

  // STK waiting loop — poll the order; the backend sweeper completes it via
  // Paystack verify (or the charge.success webhook) and the poll surfaces it.
  useEffect(() => {
    if (mode !== 'stk' || stkStage !== 'waiting' || !activeOrder) return
    pollRef.current = window.setInterval(async () => {
      try {
        const o = await api.get<Order>(`/api/v1/orders/${activeOrder.id}`)
        setActiveOrder(o)
        const pay = o.payments[o.payments.length - 1]
        if (o.status === 'PAID') {
          setStkStage('paid')
          stopTimers()
          onOrderPaid(o)
        } else if (pay?.status === 'FAILED') {
          setStkStage('failed')
          setPayError(pay.resultDesc || 'STK push failed or timed out')
          stopTimers()
        }
      } catch {
        /* transient network error — keep polling */
      }
    }, 2000)
    timerRef.current = window.setInterval(() => setElapsed((e) => e + 1), 1000)
    return stopTimers
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode, stkStage, activeOrder?.id])

  const pushStk = async (order: Order, forPhone: string) => {
    setStkStage('sending')
    setPayError('')
    try {
      const o = await api.post<Order>(`/api/v1/orders/${order.id}/stkpush`, { phone: forPhone })
      setActiveOrder(o)
      const pay = o.payments[o.payments.length - 1]
      if (pay?.status === 'FAILED') {
        setStkStage('failed')
        setPayError(pay.resultDesc || 'STK push failed')
      } else {
        setElapsed(0)
        setStkStage('waiting')
      }
    } catch (err: any) {
      setStkStage('failed')
      setPayError(err?.message || 'Could not send STK push')
    }
  }

  const paystackInit = async (order: Order, customerEmail: string) => {
    setInlineStage('init')
    setPayError('')
    try {
      const res = await api.post<{ order: Order; paystack: { reference: string; accessCode: string; authorizationUrl: string; publicKey: string } }>(
        `/api/v1/orders/${order.id}/paystack/init`,
        { email: customerEmail.trim() || undefined, phone: normalizedPhone || undefined },
      )
      setActiveOrder(res.order)
      setLastRef(res.paystack.reference)
      settledRef.current = false
      setInlineStage('popup')
      openPaystackPopup(
        { ...res.paystack, currency: payConfig?.paystack?.currency || 'KES', amountCents: res.order.totalCents },
        customerEmail.trim() || `${res.order.number}@paystack.local`,
        {
          onSuccess: (ref) => {
            if (settledRef.current) return
            settledRef.current = true
            setLastRef(ref)
            verify(order.id, ref)
          },
          onCancelled: () => {
            if (settledRef.current) return
            settledRef.current = true
            setInlineStage('failed')
            setPayError('Checkout closed before payment — the order stays pending. Retry or void it.')
          },
          onError: (message) => {
            if (settledRef.current) return
            settledRef.current = true
            setInlineStage('failed')
            setPayError(message || 'Paystack checkout failed to open — retry to mint a fresh reference.')
          },
        },
      )
    } catch (err: any) {
      setInlineStage('failed')
      setPayError(err?.message || 'Could not open checkout')
    }
  }

  const verify = async (orderID: number, ref: string) => {
    setInlineStage('verifying')
    setPayError('')
    try {
      const done = await api.post<Order>(`/api/v1/orders/${orderID}/paystack/verify`, { reference: ref })
      setActiveOrder(done)
      setInlineStage('paid')
      onOrderPaid(done)
    } catch (err: any) {
      setInlineStage('failed')
      setPayError(err?.message || 'Verification failed — try again')
    }
  }

  // ---- Checkout ----
  const checkout = (paymentMethod: Method | SplitLeg['method'], paymentMode?: 'auto' | 'stk' | 'manual', legs?: SplitLeg[]) => {
    if (busy) return
    setBusy(true)
    setError('')
    const wire = (m: Method | SplitLeg['method']): 'cash' | 'mpesa' | 'account' | 'credit' | 'paystack' =>
      m === 'tab' ? 'account' : m === 'card' ? 'paystack' : m
    const wiredMethod = wire(paymentMethod)
    const wiredLegs = legs?.map((l) => ({ ...l, method: wire(l.method) as SplitLeg['method'] }))
    const clientUuid = newClientUuid()
    // The async leg rides LAST so pending-payment readers of
    // payments[payments.length-1] find it on the raw order too.
    const asyncLeg = wiredLegs?.find(isAsyncLeg)
    const orderedLegs = asyncLeg
      ? [...(wiredLegs ?? []).filter((l) => !isAsyncLeg(l)), asyncLeg]
      : wiredLegs
    const body: CheckoutRequest = {
      items: cart.lines.map((l) => ({ productId: l.productId, qty: l.qty })),
      paymentMethod: wiredMethod,
      paymentMode: paymentMethod === 'mpesa' ? paymentMode : undefined,
      customerName: customerName.trim() || undefined,
      customerPhone: (normalizedPhone || phone.trim()) || undefined,
      customerEmail: email.trim() || undefined,
      customerId: tabCustomer ? tabCustomer.id : undefined,
      clientUuid,
      discountCents: canDiscount && discountCents > 0 ? discountCents : undefined,
      discountLabel: canDiscount && discountCents > 0 && discountLabel.trim() ? discountLabel.trim() : undefined,
      redeemPoints: redeem > 0 && tabCustomer ? redeem : undefined,
      splitPayments: orderedLegs && orderedLegs.length > 0 ? orderedLegs : undefined,
    }
    ;(async () => {
      try {
        const o = await api.post<Order>('/api/v1/orders/checkout', body)
        setActiveOrder(o)
        // Route to the in-rail payment state machine.
        if (o.status === 'PENDING') {
          if (asyncLeg?.method === 'paystack' || (!asyncLeg && wiredMethod === 'paystack')) {
            setMode('inline')
            paystackInit(o, asyncLeg?.email?.trim() || email.trim())
          } else if (asyncLeg?.method === 'mpesa' || (!asyncLeg && wiredMethod === 'mpesa')) {
            setMode('stk')
            const legPhone = normalizePhoneKe(asyncLeg?.phone || '') || normalizedPhone
            if (manualMode) {
              setStkStage('waiting') // manual: no auto push; the panel offers receipt entry
              if (!asyncLeg?.phone) void pushManualPhase(o)
            } else if (legPhone) {
              void pushStk(o, legPhone)
            } else {
              setStkStage('failed')
              setPayError('Add the customer phone number, then send the STK push.')
            }
          } else {
            onOrderPaid(o)
          }
        } else {
          onOrderPaid(o)
        }
      } catch (err: any) {
        // Network failure → queue offline (cash only; M-Pesa/Paystack need a
        // live connection to know payment state).
        const splitNeedsServer = !!legs && legs.some((l) => isAsyncLeg(l) || l.method === 'credit')
        if (!navigator.onLine || /network|fetch/i.test(String(err))) {
          if (paymentMethod === 'cash' && !splitNeedsServer) {
            try {
              await enqueue(body)
              toast.info('Saved offline', 'Will sync when back online.')
              onOrderPaid(null as unknown as Order)
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
    })()
  }

  // Manual M-Pesa mode: the order is pending; the rail shows the paybill
  // instructions + receipt-code entry (no STK).
  const pushManualPhase = async (_o: Order) => { /* stage already 'waiting'; UI shows manual panel */ }

  const submitManual = async (code: string) => {
    if (!activeOrder || busy) return
    setBusy(true)
    setPayError('')
    try {
      const o = await api.post<Order>(`/api/v1/orders/${activeOrder.id}/manual`, {
        receiptCode: code.trim().toUpperCase(),
      })
      setActiveOrder(o)
      setStkStage('paid')
      stopTimers()
      onOrderPaid(o)
      toast.success('Payment confirmed', `Receipt ${code.trim().toUpperCase()}`)
    } catch (err: any) {
      setPayError(err?.message || 'Invalid receipt code')
    } finally {
      setBusy(false)
    }
  }

  // Leave a pending async order without voiding: the cart is already an
  // order — clear it so the next sale doesn't double-charge the items.
  const abandonPending = () => {
    const n = activeOrder?.number
    resetPayment()
    cart.clear()
    if (n) toast.info(`Order ${n} stays pending`, 'Void it from Orders if the sale is abandoned.')
  }

  if (mode !== 'idle' && activeOrder) {
    return (
      <>
        <PaymentPanel
          mode={mode}
          stkStage={stkStage}
          inlineStage={inlineStage}
          order={activeOrder}
          reference={lastRef}
          elapsed={elapsed}
          phone={activeOrder.payments[activeOrder.payments.length - 1]?.phone || normalizedPhone || phone}
          manualOnly={manualOnly}
          branding={branding}
          payError={payError}
          busy={busy}
          onRetryStk={(p) => pushStk(activeOrder, p)}
          onRetryInline={() => paystackInit(activeOrder, email)}
          onVerifyAgain={() => lastRef && verify(activeOrder.id, lastRef)}
          onManualCode={submitManual}
          onBack={abandonPending}
          onVoidedOrder={(v) => { onOrderVoided(v); resetPayment(); cart.clear() }}
          onNewSale={() => { resetPayment(); cart.clear() }}
          onReceipt={() => setReceiptOpen(true)}
        />
        <ReceiptModal
          open={receiptOpen && !!activeOrder}
          order={activeOrder}
          branding={branding}
          onClose={() => setReceiptOpen(false)}
        />
      </>
    )
  }

  const quick = [tenderDue, 100000, 200000, 500000, 1000000]
  const productById = useMemo(() => {
    const m = new Map<number, Product>()
    for (const p of products ?? []) m.set(p.id, p)
    return m
  }, [products])

  return (
    <div className="h-full flex flex-col">
      {/* Rail header */}
      <header className="px-4 pt-4 pb-3 border-b border-line flex items-center justify-between bg-surface rounded-t-card">
        <div className="flex items-center gap-2.5 min-w-0">
          <span className="w-9 h-9 rounded-input bg-brand-soft text-brand flex items-center justify-center shrink-0" aria-hidden>
            <ShoppingCart size={17} strokeWidth={2.25} />
          </span>
          <div className="min-w-0">
            <h2 className="font-bold text-ink leading-tight">Current Sale</h2>
            <p className="text-[11px] text-ink-subtle leading-tight">
              {totals.count} item{totals.count === 1 ? '' : 's'}
              {canHold && heldCount ? ` · ${heldCount} parked` : ''}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-1">
          {canHold && onOpenHeld && (
            <Button size="sm" variant="ghost" onClick={onOpenHeld} title="Parked sales">
              <Archive size={14} strokeWidth={2.5} aria-hidden />
            </Button>
          )}
          <Button size="sm" variant="ghost" onClick={() => { cart.clear(); setError('') }} disabled={cart.lines.length === 0} title="Clear cart">
            <Trash2 size={14} strokeWidth={2.5} aria-hidden />
            Clear
          </Button>
        </div>
      </header>

      {/* Items */}
      <div className="flex-1 min-h-0 overflow-y-auto px-3 py-3 space-y-2">
        {cart.lines.length === 0 ? (
          <EmptyState icon={<ShoppingCart size={24} strokeWidth={2.25} />} title="Cart is empty" body="Tap products or scan a barcode to add them." />
        ) : (
          cart.lines.map((l) => {
            const p = productById.get(l.productId)
            return (
              <div key={l.productId} className="flex gap-2.5 p-2 rounded-input border border-line bg-surface">
                <Thumb p={p} />
                <div className="flex-1 min-w-0">
                  <div className="flex items-start justify-between gap-2">
                    <p className="text-[13px] font-semibold text-ink leading-snug line-clamp-1">{l.name}</p>
                    <button
                      onClick={() => cart.remove(l.productId)}
                      aria-label={`Remove ${l.name} from cart`}
                      className="text-ink-subtle hover:text-danger-text w-7 h-7 -mr-1 flex items-center justify-center rounded-input hover:bg-danger-bg shrink-0"
                    >
                      <X size={14} strokeWidth={2.5} aria-hidden />
                    </button>
                  </div>
                  <p className="text-[11px] text-ink-subtle">{l.sku || '\u00a0'}</p>
                  <div className="flex items-center justify-between gap-2 mt-1.5">
                    <div className="flex items-center border border-line-strong rounded-input overflow-hidden h-8">
                      <button
                        onClick={() => cart.setQty(l.productId, l.qty - 1)}
                        aria-label={l.qty === 1 ? `Remove ${l.name}` : 'Decrease quantity'}
                        className="w-8 h-full bg-surface-muted font-bold text-ink hover:bg-line flex items-center justify-center"
                      >
                        {l.qty === 1 ? <X size={13} strokeWidth={2.75} aria-hidden /> : <Minus size={13} strokeWidth={2.75} aria-hidden />}
                      </button>
                      <span className="w-8 text-center font-bold tabular text-ink text-[13px]">{l.qty}</span>
                      <button
                        onClick={() => cart.setQty(l.productId, Math.min(999, l.qty + 1))}
                        aria-label="Increase quantity"
                        className="w-8 h-full bg-surface-muted font-bold text-ink hover:bg-line flex items-center justify-center"
                      >
                        <Plus size={13} strokeWidth={2.75} aria-hidden />
                      </button>
                    </div>
                    <div className="text-right">
                      <p className="font-bold text-[14px] text-ink tabular leading-none">{formatMoneyCompact(l.qty * l.unitPriceCents)}</p>
                      <button
                        onClick={() => { if (canOverride) { setEditing(l.productId); setOverrideVal(l.unitPriceCents) } else toast.error('Price override needs permission') }}
                        className={`text-[11px] font-semibold mt-0.5 ${canOverride ? 'text-ink-subtle hover:text-ink underline' : 'text-ink-subtle/50'}`}
                      >
                        @ {formatMoneyCompact(l.unitPriceCents)}
                      </button>
                    </div>
                  </div>
                </div>
              </div>
            )
          })
        )}
        {editing !== null && (
          <div className="border border-line rounded-input p-2.5 bg-surface-muted space-y-2">
            <p className="text-[13px] font-bold text-ink">Override unit price</p>
            <div className="flex gap-2 items-end">
              <div className="flex-1">
                <MoneyInput value={overrideVal} onCents={setOverrideVal} autoFocus />
              </div>
              <Button size="sm" variant="primary" onClick={() => { cart.setUnitPrice(editing, overrideVal); setEditing(null) }}>Set</Button>
              <Button size="sm" variant="ghost" onClick={() => setEditing(null)}>Cancel</Button>
            </div>
          </div>
        )}
      </div>

      {/* Totals + payment */}
      <footer className="border-t border-line bg-shell-edge rounded-b-card px-4 pt-3 pb-4 space-y-3">
        <div className="space-y-1">
          <Row label="Subtotal" value={formatMoney(totals.subtotal)} />
          <Row label={`${branding.tax_percent}% ${branding.tax_included ? 'VAT (incl.)' : 'VAT'}`} value={formatMoney(totals.tax)} />
          <div className="flex items-baseline justify-between border-t border-line-strong pt-2 mt-2">
            <span className="font-bold text-ink">TOTAL</span>
            <span className="font-extrabold text-[30px] leading-none text-ink tabular tracking-tight">{formatMoney(totals.total)}</span>
          </div>
        </div>

        {cart.lines.length === 0 ? (
          <Button variant="primary" size="lg" className="w-full h-[50px] text-base" disabled>
            Charge {formatMoney(totals.total)} →
          </Button>
        ) : mode === 'idle' ? (
          <PaymentForm
            {...{
              method, setMethod, paystackReady, creditEnabled, manualMode, online,
              phone, setPhone, phoneOk, normalizedPhone, email, setEmail, customerName, setCustomerName,
              received, setReceived, quick, change, canCash,
              tabQuery, setTabQuery, tabOptions, tabCustomer, setTabCustomer,
              discountCents, setDiscountCents, discountLabel, setDiscountLabel, redeem, setRedeem,
              splitMode, setSplitMode, splitLegs, setSplitLegs, splitValid, splitNeedsCustomer, splitAsyncLeg,
              canDiscount, canRedeem, loyaltyEnabled, tabCustomerLoyalty: tabCustomer,
              tenderDue, totals, busy, error, branding,
              checkout,
            }}
            onPark={canHold ? () => setParkOpen(true) : undefined}
          />
        ) : null}
      </footer>

      <ParkModal
        open={parkOpen}
        onClose={() => setParkOpen(false)}
        onParked={(name) => {
          setParkOpen(false)
          cart.clear()
          toast.success('Sale parked', name)
        }}
      />
    </div>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between text-[13px]">
      <span className="text-ink-muted">{label}</span>
      <span className="text-ink font-semibold tabular">{value}</span>
    </div>
  )
}

function Thumb({ p }: { p?: Product }) {
  const [failed, setFailed] = useState(false)
  const src = p ? productImageUrlSafe(p) : ''
  if (!src || failed) {
    return (
      <div className="w-12 h-12 rounded-input bg-surface-muted border border-line flex items-center justify-center text-ink-subtle shrink-0" aria-hidden>
        <ShoppingCart size={16} strokeWidth={2} />
      </div>
    )
  }
  return (
    <img
      src={src}
      alt=""
      onError={() => setFailed(true)}
      className="w-12 h-12 rounded-input object-cover border border-line bg-surface-muted shrink-0"
      loading="lazy"
    />
  )
}

function productImageUrlSafe(p: Pick<Product, 'id' | 'updatedAt'>): string {
  return p.updatedAt ? `/api/v1/products/${p.id}/image?v=${encodeURIComponent(p.updatedAt)}` : `/api/v1/products/${p.id}/image`
}

function creditOf(c: Customer): number {
  return Number((c as unknown as { storeCreditCents?: number }).storeCreditCents ?? 0)
}

// ---- PaymentForm: tiles + customer + per-method inputs + charge button ----

function PaymentForm(props: {
  method: Method; setMethod: (m: Method) => void
  paystackReady: boolean; creditEnabled: boolean; manualMode: boolean; online: boolean
  phone: string; setPhone: (v: string) => void; phoneOk: boolean; normalizedPhone: string | null
  email: string; setEmail: (v: string) => void
  customerName: string; setCustomerName: (v: string) => void
  received: number | null; setReceived: (n: number | null) => void; quick: number[]; change: number | null; canCash: boolean
  tabQuery: string; setTabQuery: (v: string) => void; tabOptions: Customer[]; tabCustomer: Customer | null; setTabCustomer: (c: Customer | null) => void
  discountCents: number; setDiscountCents: (n: number) => void; discountLabel: string; setDiscountLabel: (v: string) => void
  redeem: number; setRedeem: (n: number) => void
  splitMode: boolean; setSplitMode: (b: boolean) => void; splitLegs: SplitLeg[]; setSplitLegs: (l: SplitLeg[]) => void
  splitValid: boolean; splitNeedsCustomer: boolean; splitAsyncLeg?: SplitLeg
  canDiscount: boolean; canRedeem: boolean; loyaltyEnabled: boolean; tabCustomerLoyalty: Customer | null
  tenderDue: number; totals: { subtotal: number; tax: number; total: number; count: number }
  busy: boolean; error: string; branding: ReturnType<typeof useBranding.getState>['branding']
  checkout: (m: Method, mode?: 'auto' | 'stk' | 'manual', legs?: SplitLeg[]) => void
  onPark?: () => void
}) {
  const {
    method, setMethod, paystackReady, creditEnabled, manualMode, online,
    phone, setPhone, phoneOk, normalizedPhone, email, setEmail, customerName, setCustomerName,
    received, setReceived, quick, change, canCash,
    tabQuery, setTabQuery, tabOptions, tabCustomer, setTabCustomer,
    discountCents, setDiscountCents, discountLabel, setDiscountLabel, redeem, setRedeem,
    splitMode, setSplitMode, splitLegs, setSplitLegs, splitValid, splitNeedsCustomer, splitAsyncLeg,
    canDiscount, canRedeem, loyaltyEnabled, tabCustomerLoyalty,
    tenderDue, busy, error, branding, checkout, onPark,
  } = props

  const tiles: { key: Method; label: string; sub: string; icon: React.ReactNode; enabled: boolean }[] = [
    { key: 'mpesa', label: 'M-Pesa', sub: manualMode ? 'Manual code' : 'STK Push', icon: <Smartphone size={17} strokeWidth={2.25} />, enabled: true },
    { key: 'card', label: 'Card', sub: 'Paystack', icon: <CreditCard size={17} strokeWidth={2.25} />, enabled: paystackReady },
    { key: 'cash', label: 'Cash', sub: 'Exact / change', icon: <Banknote size={17} strokeWidth={2.25} />, enabled: true },
  ]

  const chargeDisabled =
    busy ||
    (splitMode
      ? !splitValid
      : (method === 'cash' && !canCash) ||
        (canDiscount && discountCents >= props.totals.subtotal && discountCents > 0) ||
        (method === 'mpesa' && !manualMode && !normalizePhoneKe(phone)) ||
        ((method === 'tab' || method === 'credit') && (!online || !tabCustomer || (method === 'tab' ? tabCustomer.creditLimitCents <= 0 : creditOf(tabCustomer) <= 0))) ||
        ((method === 'card' || (method === 'mpesa' && !manualMode)) && !online))

  const chargeLabel = busy
    ? 'Working…'
    : splitMode
      ? `Charge ${formatMoney(tenderDue)} — ${splitLegs.length} payment${splitLegs.length === 1 ? '' : 's'}`
      : method === 'cash'
        ? `Take ${formatMoney(tenderDue)}`
        : method === 'tab'
          ? `Charge ${formatMoney(tenderDue)} to tab`
          : method === 'credit'
            ? `Take ${formatMoney(tenderDue)} from credit`
            : method === 'card'
              ? `Pay ${formatMoney(tenderDue)} →`
              : manualMode
                ? `Confirm ${formatMoney(tenderDue)} M-Pesa →`
                : `Charge ${formatMoney(tenderDue)} — Send STK →`

  return (
    <div className="space-y-3">
      {/* Payment method tiles */}
      <div className="grid grid-cols-3 gap-2">
        {tiles.map((t) => (
          <button
            key={t.key}
            type="button"
            disabled={!t.enabled}
            onClick={() => setMethod(t.key)}
            aria-pressed={method === t.key}
            className={`min-h-[62px] rounded-input border text-left px-3 py-2 transition-colors ${
              method === t.key
                ? 'border-paid-text bg-paid-bg'
                : 'border-line-strong bg-surface hover:bg-surface-muted'
            } ${!t.enabled ? 'opacity-40 cursor-not-allowed' : ''}`}
          >
            <span className={`flex items-center gap-1.5 text-[13px] font-bold ${method === t.key ? 'text-paid-text' : 'text-ink'}`}>
              {t.icon}
              {t.label}
            </span>
            <span className="block text-[11px] text-ink-subtle mt-0.5">{t.sub}</span>
          </button>
        ))}
      </div>
      {/* Secondary tenders */}
      <div className="flex items-center gap-2 flex-wrap">
        <button
          type="button"
          onClick={() => setMethod('tab')}
          className={`min-h-8 px-2.5 rounded-pill border text-[12px] font-semibold inline-flex items-center gap-1.5 ${
            method === 'tab' ? 'bg-brand-soft border-brand text-brand' : 'border-line text-ink-muted hover:text-ink'
          }`}
        >
          <BookUser size={13} strokeWidth={2.25} aria-hidden /> Tab
        </button>
        {creditEnabled && (
          <button
            type="button"
            onClick={() => setMethod('credit')}
            className={`min-h-8 px-2.5 rounded-pill border text-[12px] font-semibold inline-flex items-center gap-1.5 ${
              method === 'credit' ? 'bg-brand-soft border-brand text-brand' : 'border-line text-ink-muted hover:text-ink'
            }`}
          >
            <Wallet size={13} strokeWidth={2.25} aria-hidden /> Store credit
          </button>
        )}
        <label className="flex items-center gap-1.5 text-[12px] font-semibold text-ink cursor-pointer select-none ml-auto">
          <input
            type="checkbox"
            checked={splitMode}
            onChange={(e) => {
              const on = e.target.checked
              setSplitMode(on)
              if (on && splitLegs.length === 0) {
                setSplitLegs([
                  { method: 'cash', amountCents: tenderDue },
                  { method: 'cash', amountCents: 0 },
                ])
              }
            }}
            className="w-4 h-4 accent-[#0047AB]"
          />
          <SplitSquareHorizontal size={13} strokeWidth={2.25} aria-hidden />
          Split
        </label>
      </div>

      {/* Customer — phone is the primary M-Pesa identifier */}
      <div className="grid grid-cols-2 gap-2">
        <Field label="Customer phone" hint={method === 'mpesa' && !manualMode ? 'Required for M-Pesa' : 'Optional — for M-Pesa or delivery'}>
          <Input
            value={phone}
            onChange={(e) => setPhone(e.target.value)}
            inputMode="tel"
            placeholder="07XX XXX XXX"
            aria-invalid={method === 'mpesa' && !manualMode && !phoneOk}
          />
          {method === 'mpesa' && !manualMode && normalizedPhone && (
            <span className="flex items-center gap-1 text-[11px] font-bold text-paid-text mt-1">
              <Check size={12} strokeWidth={3} aria-hidden /> Valid — STK will be sent to {phone}
            </span>
          )}
        </Field>
        <Field label="Customer email" hint="Optional for receipt">
          <Input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            inputMode="email"
            placeholder="optional@email.com"
          />
        </Field>
      </div>
      <Field label="Customer name" hint="Optional — shown on the order">
        <Input value={customerName} onChange={(e) => setCustomerName(e.target.value)} placeholder="Walk-in" />
      </Field>

      {/* Per-method inputs */}
      {splitMode ? (
        <SplitTenderEditor
          legs={splitLegs}
          onChange={setSplitLegs}
          totalCents={tenderDue}
          creditEnabled={!!creditEnabled}
          paystackReady={!!paystackReady}
          creditReady={!!tabCustomer && creditOf(tabCustomer) > 0}
        />
      ) : method === 'cash' ? (
        <>
          <Field label="Cash received">
            <MoneyInput value={received ?? 0} onCents={(c) => setReceived(c)} placeholder="0.00" className="text-lg font-bold" />
          </Field>
          <div className="grid grid-cols-5 gap-2">
            {quick.map((q, i) => (
              <button
                key={i}
                onClick={() => setReceived(q)}
                className="min-h-11 text-[12px] font-bold bg-surface border border-line rounded-input hover:bg-surface-muted active:bg-surface-muted"
              >
                {i === 0 ? 'Exact' : formatMoneyCompact(q)}
              </button>
            ))}
          </div>
          {change !== null && (
            <div className={`rounded-input border p-3 flex justify-between items-baseline ${change < 0 ? 'bg-danger-bg border-danger-text/30' : 'bg-paid-bg border-paid-text/30'}`}>
              <span className={`font-bold text-sm ${change < 0 ? 'text-danger-text' : 'text-paid-text'}`}>
                {change < 0 ? 'Still owed' : 'Change'}
              </span>
              <span className={`font-extrabold text-2xl tabular ${change < 0 ? 'text-danger-text' : 'text-paid-text'}`}>
                {formatMoney(Math.abs(change))}
              </span>
            </div>
          )}
        </>
      ) : method === 'tab' || method === 'credit' || splitNeedsCustomer ? (
        <>
          <Field
            label={splitNeedsCustomer ? 'Customer (store credit leg)' : method === 'credit' ? 'Customer (store credit)' : 'Tab customer'}
            hint={splitNeedsCustomer || method === 'credit' ? 'Pays from their prepaid store credit.' : 'Their limit is checked automatically.'}
          >
            <Input
              value={
                tabCustomer
                  ? splitNeedsCustomer || method === 'credit'
                    ? `${tabCustomer.name} · credit ${formatMoney(creditOf(tabCustomer))}`
                    : `${tabCustomer.name} · owes ${formatMoney(tabCustomer.balanceCents)}`
                  : tabQuery
              }
              onChange={(e) => { setTabCustomer(null); setTabQuery(e.target.value) }}
              placeholder="Type a name or phone…"
            />
          </Field>
          {!tabCustomer && tabOptions.length > 0 && (
            <div className="border border-line rounded-input overflow-hidden">
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
                  <span className={`text-[12px] font-bold ${
                    method === 'credit'
                      ? creditOf(c) > 0 ? 'text-paid-text' : 'text-ink-subtle'
                      : c.creditLimitCents <= 0 ? 'text-ink-subtle' : c.balanceCents >= c.creditLimitCents ? 'text-danger-text' : 'text-ink-muted'
                  }`}>
                    {method === 'credit'
                      ? creditOf(c) > 0 ? `${formatMoneyCompact(creditOf(c))} credit` : 'no credit'
                      : c.creditLimitCents <= 0 ? 'cash only' : `owes ${formatMoney(c.balanceCents)} / ${formatMoney(c.creditLimitCents)}`}
                  </span>
                </button>
              ))}
            </div>
          )}
          {method === 'credit' && tabCustomer && creditOf(tabCustomer) <= 0 && (
            <p className="text-danger-text text-[13px] font-semibold">This customer has no store credit — top up from their profile first.</p>
          )}
          {method === 'tab' && tabCustomer && tabCustomer.creditLimitCents <= 0 && (
            <p className="text-danger-text text-[13px] font-semibold">This customer is cash-only — pick someone with credit.</p>
          )}
          {!online && (
            <p className="text-pending-text text-[13px] font-bold">You're offline — {method === 'credit' ? 'store credit' : 'tabs'} need the server for checks.</p>
          )}
        </>
      ) : method === 'card' ? (
        <div className="bg-surface-muted border border-line rounded-input p-3 text-[13px] text-ink-muted space-y-1">
          <p>A secure Paystack checkout opens right here in the app — card or mobile money, with the exact amount already set server-side.</p>
          {!online && (
            <p className="text-pending-text font-bold flex items-center gap-1.5">
              <AlertTriangle size={14} strokeWidth={2.5} aria-hidden />
              You're offline — Paystack needs a connection. Cash sales keep working.
            </p>
          )}
        </div>
      ) : method === 'mpesa' && manualMode ? (
        <div className="bg-surface-muted border border-line rounded-input p-3 text-[13px] text-ink-muted">
          The customer pays to {branding.paybill_number ? 'Paybill ' + branding.paybill_number : 'Till ' + (branding.till_number || '—')} (Account: the order number). You'll enter their receipt code next.
        </div>
      ) : method === 'mpesa' ? (
        <div className="bg-surface-muted border border-line rounded-input p-3 text-[13px] text-ink-muted space-y-1">
          <p>An M-Pesa STK prompt is sent to the customer's phone the moment you charge — they just enter their PIN.</p>
          {!online && (
            <p className="text-pending-text font-bold flex items-center gap-1.5">
              <AlertTriangle size={14} strokeWidth={2.5} aria-hidden />
              You're offline — M-Pesa needs a connection. Cash sales keep working.
            </p>
          )}
        </div>
      ) : null}

      {/* Order-level discount — permission-gated, all tenders */}
      {canDiscount && (
        <div className="grid grid-cols-2 gap-2 border-t border-line pt-3">
          <Field label="Discount label" hint="Shown on the order.">
            <Input value={discountLabel} onChange={(e) => setDiscountLabel(e.target.value)} placeholder="e.g. Staff 10%" />
          </Field>
          <Field label="Discount amount">
            <MoneyInput value={discountCents} onCents={setDiscountCents} placeholder="0.00" />
          </Field>
          {discountCents > 0 && (
            <p className="col-span-2 text-[12px] text-ink-muted -mt-1">
              New total ≈ <strong className="text-ink tabular">{formatMoney(Math.max(0, tenderDue))}</strong>
            </p>
          )}
        </div>
      )}

      {/* Loyalty redemption — needs an attached customer, the program on,
          and loyalty.redeem. The backend caps redemption authoritatively. */}
      {tabCustomerLoyalty && loyaltyEnabled && canRedeem && (
        <div className="border border-line rounded-input p-3 bg-surface-muted space-y-2">
          <div className="flex items-center justify-between gap-2">
            <span className="text-[13px] font-bold text-ink flex items-center gap-1.5">
              <Star size={14} strokeWidth={2.5} aria-hidden />
              {tabCustomerLoyalty.name} — {tabCustomerLoyalty.loyaltyPoints} point{tabCustomerLoyalty.loyaltyPoints === 1 ? '' : 's'}
            </span>
            <span className="text-[11px] text-ink-subtle">1 pt ≈ {formatMoneyCompact(100)}</span>
          </div>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => setRedeem(Math.max(0, redeem - 1))}
              aria-label="Redeem one point less"
              className="w-11 h-11 bg-surface border border-line-strong rounded-input font-bold text-ink hover:bg-surface-muted flex items-center justify-center"
            >
              <Minus size={15} strokeWidth={2.75} aria-hidden />
            </button>
            <Input
              value={redeem === 0 ? '' : String(redeem)}
              onChange={(e) => {
                const n = parseInt(e.target.value.replace(/\D/g, ''), 10)
                setRedeem(isNaN(n) ? 0 : Math.min(n, tabCustomerLoyalty.loyaltyPoints))
              }}
              inputMode="numeric"
              placeholder="0"
              className="text-center font-bold"
              aria-label="Points to redeem"
            />
            <button
              type="button"
              onClick={() => setRedeem(Math.min(tabCustomerLoyalty.loyaltyPoints, redeem + 1))}
              aria-label="Redeem one more point"
              className="w-11 h-11 bg-surface border border-line-strong rounded-input font-bold text-ink hover:bg-surface-muted flex items-center justify-center"
            >
              <Plus size={15} strokeWidth={2.75} aria-hidden />
            </button>
            {tabCustomerLoyalty.loyaltyPoints > 0 && (
              <Button size="sm" variant="ghost" onClick={() => setRedeem(tabCustomerLoyalty.loyaltyPoints)} className="shrink-0">
                All
              </Button>
            )}
          </div>
        </div>
      )}

      {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}

      {/* Charge */}
      <Button
        variant="primary"
        size="lg"
        className="w-full h-[50px] text-base"
        disabled={chargeDisabled}
        onClick={() =>
          splitMode
            ? checkout((splitLegs[0]?.method as Method) ?? 'cash', splitAsyncLeg?.method === 'mpesa' ? (branding.payment_mode as 'auto' | 'stk' | 'manual') : undefined, splitLegs)
            : checkout(method, method === 'mpesa' ? (branding.payment_mode as 'auto' | 'stk' | 'manual') : undefined)
        }
      >
        {busy ? <Spinner className="border-t-white" /> : chargeLabel}
      </Button>
      {onPark && (
        <Button variant="secondary" className="w-full" onClick={onPark}>
          <Pause size={15} strokeWidth={2.5} aria-hidden />
          Park for later
        </Button>
      )}
    </div>
  )
}

// ---- PaymentPanel: in-rail payment lifecycle (STK / inline Paystack) ----

function PaymentPanel({
  mode, stkStage, inlineStage, order, reference, elapsed, phone, manualOnly, branding,
  payError, busy, onRetryStk, onRetryInline, onVerifyAgain, onManualCode, onBack, onVoidedOrder, onNewSale, onReceipt,
}: {
  mode: 'stk' | 'inline'
  stkStage: StkStage
  inlineStage: InlineStage
  order: Order
  reference: string
  elapsed: number
  phone: string
  manualOnly: boolean
  branding: ReturnType<typeof useBranding.getState>['branding']
  payError: string
  busy: boolean
  onRetryStk: (phone: string) => void
  onRetryInline: () => void
  onVerifyAgain: () => void
  onManualCode: (code: string) => void
  onBack: () => void
  onVoidedOrder: (o: Order) => void
  onNewSale: () => void
  onReceipt: () => void
}) {
  const [code, setCode] = useState('')
  const [voiding, setVoiding] = useState(false)
  const paid = mode === 'stk' ? stkStage === 'paid' : inlineStage === 'paid'
  const [localPhone, setLocalPhone] = useState(phone)
  useEffect(() => { setLocalPhone(phone) }, [phone])

  return (
    <div className="h-full flex flex-col">
      <header className="px-4 pt-4 pb-3 border-b border-line flex items-center justify-between">
        <div>
          <h2 className="font-bold text-ink">Payment — {order.number}</h2>
          <p className="text-[11px] text-ink-subtle">Everything stays on this screen — no external windows.</p>
        </div>
        <StatusPill status={paid ? 'paid' : 'pending'} label={paid ? 'Paid' : 'Pending'} />
      </header>

      <div className="flex-1 min-h-0 overflow-y-auto px-4 py-4 space-y-4">
        <div className="flex items-baseline justify-between">
          <span className="text-ink-muted text-[13px] font-semibold">Amount due</span>
          <span className="text-[30px] leading-none font-extrabold text-ink tabular">{formatMoney(order.totalCents)}</span>
        </div>

        {mode === 'stk' && stkStage !== 'paid' && (
          <StkPanel
            stage={stkStage}
            elapsed={elapsed}
            phone={localPhone}
            setPhone={setLocalPhone}
            manualOnly={manualOnly}
            branding={branding}
            orderNumber={order.number}
            payError={payError}
            busy={busy}
            onSend={() => onRetryStk(normalizePhoneKe(localPhone) || localPhone)}
            code={code}
            setCode={setCode}
            onManualCode={onManualCode}
          />
        )}

        {mode === 'inline' && inlineStage !== 'paid' && (
          <InlinePanel stage={inlineStage} reference={reference} payError={payError} onRetry={onRetryInline} onVerifyAgain={onVerifyAgain} />
        )}

        {paid && (
          <div className="space-y-4 text-center py-3">
            <div className="w-16 h-16 mx-auto rounded-full bg-paid-bg border border-paid-text flex items-center justify-center text-paid-text" aria-hidden>
              <Check size={30} strokeWidth={2.75} />
            </div>
            <div>
              <p className="font-extrabold text-ink text-xl">Paid</p>
              <p className="text-ink-muted text-sm mt-1">
                {mode === 'stk' && order.payments[order.payments.length - 1]?.mpesaReceipt
                  ? <>M-Pesa receipt <strong className="text-ink tabular">{order.payments[order.payments.length - 1].mpesaReceipt}</strong></>
                  : 'Payment verified'}
              </p>
            </div>
            <div className="flex justify-center">
              <StatusPill status={order.discrepancy ? 'danger' : 'paid'} label={order.discrepancy ? 'Amount discrepancy — review' : 'Payment complete'} />
            </div>
          </div>
        )}

        {/* Void (pending orders only) */}
        {!paid && voiding && (
          <VoidReasonPicker
            order={order}
            onVoided={(v) => { setVoiding(false); onVoidedOrder(v) }}
            onCancel={() => setVoiding(false)}
          />
        )}
      </div>

      <footer className="border-t border-line px-4 py-3 space-y-2 bg-shell-edge rounded-b-card">
        {paid ? (
          <>
            <Button variant="primary" size="lg" className="w-full" onClick={onReceipt}>
              View receipt
            </Button>
            <Button variant="secondary" className="w-full" onClick={onNewSale}>
              Start a new sale
            </Button>
          </>
        ) : !voiding ? (
          <>
            <Button variant="secondary" className="w-full" onClick={onBack}>
              Leave pending &amp; start over…
            </Button>
            <Button variant="ghost" className="w-full text-danger-text hover:bg-danger-bg" onClick={() => setVoiding(true)}>
              <Ban size={15} strokeWidth={2.5} aria-hidden />
              Void this order…
            </Button>
          </>
        ) : null}
      </footer>
    </div>
  )
}

function StkPanel({
  stage, elapsed, phone, setPhone, manualOnly, branding, orderNumber, payError, busy, onSend,
  code, setCode, onManualCode,
}: {
  stage: StkStage
  elapsed: number
  phone: string
  setPhone: (v: string) => void
  manualOnly: boolean
  branding: ReturnType<typeof useBranding.getState>['branding']
  orderNumber: string
  payError: string
  busy: boolean
  onSend: () => void
  code: string
  setCode: (v: string) => void
  onManualCode: (code: string) => void
}) {
  const till = branding.till_number || branding.paybill_number
  if (stage === 'sending') {
    return (
      <div className="text-center py-8 space-y-3">
        <Spinner className="w-7 h-7 border-4 mx-auto" />
        <p className="font-bold text-ink">Sending STK push…</p>
        <p className="text-ink-muted text-sm">Dialling {phone || 'the customer phone'}</p>
      </div>
    )
  }
  if (stage === 'waiting' && !manualOnly) {
    return (
      <div className="space-y-5 text-center pt-2 pb-4">
        <div className="flex justify-center">
          <span className="relative flex w-16 h-16 items-center justify-center">
            <span className="absolute inset-0 rounded-full bg-pending-bg border-2 border-pending-text/40 anim-pulse-dot" aria-hidden />
            <Smartphone size={30} strokeWidth={2.25} className="text-pending-text" aria-hidden />
          </span>
        </div>
        <div>
          <p className="font-bold text-ink text-lg">Waiting for the customer…</p>
          <p className="text-ink-muted text-sm mt-1">An M-Pesa prompt was sent to this number — ask them to enter their PIN:</p>
          <p className="mt-2 inline-block tabular font-extrabold text-2xl text-ink bg-surface border border-line rounded-input px-4 py-2 select-none" aria-label="Customer phone number (read-only)">
            {phone}
          </p>
        </div>
        <div className="h-2 bg-surface-muted rounded-pill overflow-hidden border border-line" aria-hidden>
          <div className="h-full bg-pending-text transition-[width] duration-1000 ease-linear" style={{ width: `${Math.min(100, (elapsed / 180) * 100)}%` }} />
        </div>
        <p className="text-ink-subtle text-xs tabular">{Math.max(0, 180 - elapsed)}s left · auto-checks every 2s</p>
        {till && (
          <div className="text-left bg-surface-muted border border-line rounded-input p-3">
            <p className="text-[13px] font-bold text-ink">No prompt? Pay manually:</p>
            <p className="text-[13px] text-ink-muted mt-1">
              Send to <strong className="text-ink">{branding.paybill_number ? 'Paybill ' + branding.paybill_number : 'Till ' + till}</strong>
              {' '}(Account: <strong className="text-ink">{orderNumber}</strong>), then enter the receipt code below.
            </p>
          </div>
        )}
        <ManualEntry code={code} setCode={setCode} busy={busy} onSubmit={() => onManualCode(code)} error={payError} plain />
      </div>
    )
  }
  // failed (or manual waiting) — retry + manual entry
  return (
    <div className="space-y-4">
      {stage === 'failed' && (
        <div className="flex items-start gap-3 bg-danger-bg border border-danger-text/30 rounded-input p-3">
          <AlertTriangle size={20} strokeWidth={2.5} className="text-danger-text shrink-0 mt-0.5" aria-hidden />
          <div>
            <p className="font-bold text-danger-text text-sm">STK push didn't complete</p>
            <p role="alert" className="text-[13px] text-ink-muted mt-0.5">{payError || 'The customer may have cancelled or the request timed out.'}</p>
          </div>
        </div>
      )}
      <Field label="Customer M-Pesa phone" hint="Safaricom sends a payment prompt to this number.">
        <Input value={phone} onChange={(e) => setPhone(e.target.value)} inputMode="tel" placeholder="07XX XXX XXX" />
      </Field>
      <Button variant="primary" size="lg" className="w-full" onClick={onSend} disabled={busy || !phone.trim()}>
        <RotateCcw size={16} strokeWidth={2.5} aria-hidden />
        Send payment request again
      </Button>
      {till && (
        <div className="bg-surface-muted border border-line rounded-input p-3">
          <p className="text-[13px] font-bold text-ink">No prompt? Pay manually:</p>
          <p className="text-[13px] text-ink-muted mt-1">
            Send to <strong className="text-ink">{branding.paybill_number ? 'Paybill ' + branding.paybill_number : 'Till ' + till}</strong>
            {' '}(Account: <strong className="text-ink">{orderNumber}</strong>), then enter the receipt code below.
          </p>
        </div>
      )}
      <ManualEntry code={code} setCode={setCode} busy={busy} onSubmit={() => onManualCode(code)} error={payError} highlight />
    </div>
  )
}

function InlinePanel({
  stage, reference, payError, onRetry, onVerifyAgain,
}: {
  stage: InlineStage
  reference: string
  payError: string
  onRetry: () => void
  onVerifyAgain: () => void
}) {
  return (
    <div className="space-y-4">
      {(stage === 'init' || stage === 'popup' || stage === 'verifying') && (
        <div className="space-y-5 text-center pt-2 pb-4">
          <div className="flex justify-center">
            <span className="relative flex w-16 h-16 items-center justify-center">
              <span className={`absolute inset-0 rounded-full border-2 border-info-text/40 anim-pulse-dot ${stage === 'verifying' ? 'bg-pending-bg' : 'bg-info-bg'}`} aria-hidden />
              <CreditCard size={30} strokeWidth={2.25} className="text-info-text" aria-hidden />
            </span>
          </div>
          <div>
            {stage === 'init' && (
              <>
                <p className="font-bold text-ink text-lg flex items-center justify-center gap-2">
                  <Spinner className="border-t-info-text" /> Opening secure checkout…
                </p>
                <p className="text-ink-muted text-sm mt-1">Minting a Paystack access code server-side.</p>
              </>
            )}
            {stage === 'popup' && (
              <>
                <p className="font-bold text-ink text-lg">Checkout open</p>
                <p className="text-ink-muted text-sm mt-1">Ask the customer to complete payment in the secure checkout window (card or mobile money).</p>
              </>
            )}
            {stage === 'verifying' && (
              <>
                <p className="font-bold text-ink text-lg flex items-center justify-center gap-2">
                  <Spinner className="border-t-pending-text" /> Waiting for payment…
                </p>
                <p className="text-ink-muted text-sm mt-1">Confirming reference <span className="tabular">{reference}</span> with Paystack.</p>
              </>
            )}
          </div>
        </div>
      )}
      {stage === 'failed' && (
        <div className="space-y-4">
          <div className="flex items-start gap-3 bg-pending-bg border border-pending-text/30 rounded-input p-3">
            <AlertTriangle size={20} strokeWidth={2.5} className="text-pending-text shrink-0 mt-0.5" aria-hidden />
            <div>
              <p className="font-bold text-pending-text text-sm">Payment not completed</p>
              <p role="alert" className="text-[13px] text-ink-muted mt-0.5">{payError || 'The checkout was closed before payment. The order stays pending — retry or void it.'}</p>
            </div>
          </div>
          <Button variant="primary" size="lg" className="w-full" onClick={onRetry}>
            <RotateCcw size={16} strokeWidth={2.5} aria-hidden />
            Open checkout again
          </Button>
          {reference && (
            <Button variant="secondary" className="w-full" onClick={onVerifyAgain}>
              Verify payment again
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

function ManualEntry({
  code, setCode, busy, onSubmit, error, highlight, plain,
}: {
  code: string
  setCode: (v: string) => void
  busy: boolean
  onSubmit: () => void
  error: string
  highlight?: boolean
  plain?: boolean
}) {
  const valid = /^[A-Z0-9]{10}$/.test(code.trim().toUpperCase())
  return (
    <div className={plain ? 'pt-2 border-t border-line' : ''}>
      <Field
        label="Manual receipt code (fallback)"
        hint="Customer paid to the till themselves? Type the 10-character code from their M-Pesa SMS."
      >
        <Input
          value={code}
          onChange={(e) => setCode(e.target.value.toUpperCase().replace(/[^A-Z0-9]/g, '').slice(0, 10))}
          placeholder="e.g. NLJ7RT61SV"
          className="tabular tracking-widest font-bold text-center text-lg"
          autoComplete="off"
        />
      </Field>
      {error && !busy && <p role="alert" className="text-danger-text text-sm font-semibold mb-2">{error}</p>}
      <Button
        variant={highlight ? 'primary' : 'secondary'}
        className="w-full mt-1"
        onClick={onSubmit}
        disabled={busy || !valid}
      >
        Confirm payment with code
      </Button>
    </div>
  )
}

// ---- VoidReasonPicker — catalog-backed void (moved from PaystackModal) ----

export function VoidReasonPicker({
  order,
  onVoided,
  onCancel,
}: {
  order: Order
  onVoided: (o: Order) => void
  onCancel: () => void
}) {
  const [reasons, setReasons] = useState<{ id: number; label: string }[] | null>(null)
  const [catalogFailed, setCatalogFailed] = useState(false)
  const [label, setLabel] = useState('')
  const [extra, setExtra] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let alive = true
    api.get<{ id: number; label: string }[]>('/api/v1/void-reasons')
      .then((rs) => { if (alive) setReasons(Array.isArray(rs) ? rs : null) })
      .catch(() => { if (alive) setCatalogFailed(true) })
    return () => { alive = false }
  }, [])

  const combined = catalogFailed
    ? extra.trim()
    : (label ? label + (extra.trim() ? ' — ' + extra.trim() : '') : '')

  const submit = async () => {
    if (!combined || busy) return
    setBusy(true)
    setError('')
    try {
      const o = await api.post<Order>(`/api/v1/orders/${order.id}/void`, { reason: combined })
      onVoided(o)
    } catch (err: any) {
      setError(err?.message || 'Void failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-3">
      <div className="flex items-start gap-3 bg-danger-bg border border-danger-text/30 rounded-input p-3">
        <Ban size={20} strokeWidth={2.5} className="text-danger-text shrink-0 mt-0.5" aria-hidden />
        <div>
          <p className="font-bold text-danger-text text-sm">Void order {order.number}</p>
          <p className="text-[13px] text-ink-muted mt-0.5">Cancels the pending checkout. The reason is recorded in the audit log.</p>
        </div>
      </div>

      {catalogFailed ? (
        <Field label="Reason" hint="Reason catalog unavailable offline — type the reason.">
          <Input value={extra} onChange={(e) => setExtra(e.target.value)} placeholder="e.g. Customer changed mind" autoFocus />
        </Field>
      ) : !reasons ? (
        <div className="py-3 flex justify-center"><Spinner /></div>
      ) : (
        <>
          <Field label="Reason">
            <select
              value={label}
              onChange={(e) => setLabel(e.target.value)}
              className="w-full min-h-11 px-3 bg-surface border border-line-strong rounded-input text-ink"
            >
              <option value="">Pick a reason…</option>
              {reasons.map((r) => (
                <option key={r.id} value={r.label}>{r.label}</option>
              ))}
            </select>
          </Field>
          <Field label="Extra detail (optional)">
            <Input value={extra} onChange={(e) => setExtra(e.target.value)} placeholder="Anything worth remembering" />
          </Field>
        </>
      )}

      {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
      <div className="flex gap-2 justify-end">
        <Button variant="ghost" onClick={onCancel} disabled={busy}>Back</Button>
        <Button variant="danger" onClick={submit} disabled={busy || !combined}>
          {busy ? <Spinner className="border-t-white" /> : 'Void order'}
        </Button>
      </div>
    </div>
  )
}

// ---- ParkModal — freeze the current cart under a reference name ----

function ParkModal({
  open, onClose, onParked,
}: {
  open: boolean
  onClose: () => void
  onParked: (refName: string) => void
}) {
  const cart = useCart()
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const totals = cart.totals()

  const defaultRef = () =>
    cart.customerName.trim() || `Sale ${new Date().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`

  useEffect(() => {
    if (open) {
      setName(defaultRef())
      setError('')
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const park = async () => {
    if (busy) return
    setBusy(true)
    setError('')
    const refName = name.trim() || defaultRef()
    const body = {
      refName,
      cart: {
        items: cart.lines.map((l) => ({ productId: l.productId, qty: l.qty, unitPriceCents: l.unitPriceCents })),
        paymentMethod: 'cash' as const,
        customerName: cart.customerName.trim() || undefined,
        note: cart.note.trim() || undefined,
      },
    }
    try {
      await api.post('/api/v1/held-sales', body)
      onParked(refName)
    } catch (e: any) {
      setError(e?.message || 'Could not park the sale')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="Park this sale" size="sm" footer={
      <>
        <Button variant="ghost" onClick={onClose} disabled={busy}>Cancel</Button>
        <Button variant="primary" onClick={park} disabled={busy || cart.lines.length === 0}>
          {busy ? <Spinner className="border-t-white" /> : 'Park sale'}
        </Button>
      </>
    }>
      <div className="space-y-3">
        <p className="text-sm text-ink-muted">
          {totals.count} item{totals.count === 1 ? '' : 's'} · {formatMoney(totals.total)} — nothing is charged until the sale is resumed and completed.
        </p>
        <Field label="Reference name" hint="Find it again in the Parked drawer.">
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="e.g. Grace — hoodies"
            autoFocus
            onKeyDown={(e) => e.key === 'Enter' && park()}
          />
        </Field>
        {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
      </div>
    </Modal>
  )
}
