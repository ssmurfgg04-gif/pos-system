// Paystack checkout modal — card & mobile-money lifecycle:
//   init (server-minted access code) → popup → callback → verify → receipt.
// The popup success callback is only a HINT: money becomes real after
// POST /orders/:id/paystack/verify re-checks the reference server-side.
// Cancelled popups leave the order PENDING — the cashier can reopen
// checkout (a fresh init supersedes stale references) or void it with a
// reason from the catalog.

import { useEffect, useRef, useState } from 'react'
import { api, Order, PaystackInitResult, VoidReason, openPaystackPopup } from '../lib/api'
import { Button, Modal, Input, Field, Select, Spinner, StatusPill } from './ui'
import { ReceiptModal } from './Receipt'
import { formatMoney } from '../lib/money'
import { useBranding } from '../stores/branding'
import { toast } from '../stores/toasts'
import { CreditCard, Check, AlertTriangle, RotateCcw, Ban, ExternalLink } from 'lucide-react'

type Phase = 'init' | 'popup' | 'verifying' | 'paid' | 'pending' | 'void'

export function PaystackModal({
  open,
  onClose,
  order,
  email,
  onPaid,
  onVoided,
}: {
  open: boolean
  onClose: () => void
  order: Order | null
  email: string
  onPaid: (o: Order) => void
  onVoided: (o: Order) => void
}) {
  const branding = useBranding((s) => s.branding)
  const [phase, setPhase] = useState<Phase>('init')
  const [current, setCurrent] = useState<Order | null>(order)
  const [init, setInit] = useState<PaystackInitResult | null>(null)
  const [lastRef, setLastRef] = useState('')
  const [error, setError] = useState('')
  const [receiptOpen, setReceiptOpen] = useState(false)
  // Paystack fires onClose after the success callback on some flows —
  // once settled, late cancel events must be ignored.
  const settledRef = useRef(false)
  const busyRef = useRef(false)

  const start = async (o: Order) => {
    if (busyRef.current) return
    busyRef.current = true
    setPhase('init')
    setError('')
    try {
      const res = await api.post<{ order: Order; paystack: PaystackInitResult }>(
        `/api/v1/orders/${o.id}/paystack/init`,
        { email: email.trim() || undefined },
      )
      setCurrent(res.order)
      setInit(res.paystack)
      openPopup(res.paystack, o)
    } catch (err: any) {
      setError(err?.message || 'Could not open checkout')
      setPhase('pending')
    } finally {
      busyRef.current = false
    }
  }

  const openPopup = (initResult: PaystackInitResult, o: Order) => {
    settledRef.current = false
    try {
      openPaystackPopup(initResult, email.trim() || `${o.number}@paystack.local`, {
        onSuccess: (ref) => {
          if (settledRef.current) return
          settledRef.current = true
          setLastRef(ref)
          verify(o, ref)
        },
        onCancelled: () => {
          if (settledRef.current) return
          settledRef.current = true
          setPhase('pending')
          toast.info('Payment not completed', `Checkout reopened later from Orders — order ${o.number} stays pending`)
        },
      })
      setPhase('popup')
    } catch (err: any) {
      // Popup library missing/blocked — the authorization URL link still works.
      setError(err?.message || 'Paystack popup unavailable — use the checkout page link below.')
      setPhase('pending')
    }
  }

  const verify = async (o: Order, ref: string) => {
    setPhase('verifying')
    setError('')
    try {
      const done = await api.post<Order>(`/api/v1/orders/${o.id}/paystack/verify`, { reference: ref })
      setCurrent(done)
      setPhase('paid')
      onPaid(done)
    } catch (err: any) {
      setError(err?.message || 'Verification failed — try again')
      setPhase('pending')
    }
  }

  // Fresh lifecycle every time the modal opens for an order.
  useEffect(() => {
    if (!open || !order) return
    settledRef.current = false
    setCurrent(order)
    setInit(null)
    setLastRef('')
    setError('')
    setReceiptOpen(false)
    start(order)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, order?.id])

  if (!current) return null

  const checkoutLink = init?.authorizationUrl && (
    <a
      href={init.authorizationUrl}
      target="_blank"
      rel="noopener noreferrer"
      className="inline-flex items-center gap-1.5 text-[13px] font-semibold text-info-text underline hover:brightness-110"
    >
      <ExternalLink size={13} strokeWidth={2.5} aria-hidden />
      or open checkout page
    </a>
  )

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={`Card / Mobile Money — ${current.number}`}
      footer={
        phase === 'paid' ? (
          <Button variant="primary" onClick={() => setReceiptOpen(true)}>
            Done — view receipt
          </Button>
        ) : undefined
      }
    >
      <div className="flex items-baseline justify-between mb-4">
        <span className="text-ink-muted text-sm font-semibold">Amount due</span>
        <span className="text-[40px] leading-none font-black text-ink tabular">{formatMoney(current.totalCents)}</span>
      </div>

      {(phase === 'init' || phase === 'popup' || phase === 'verifying') && (
        <div className="space-y-5 text-center pt-2 pb-6">
          <div className="flex justify-center">
            <span className="relative flex w-16 h-16 items-center justify-center">
              <span className={`absolute inset-0 rounded-full border-2 border-info-text/40 anim-pulse-dot ${phase === 'verifying' ? 'bg-pending-bg' : 'bg-info-bg'}`} aria-hidden />
              <CreditCard size={30} strokeWidth={2.25} className="text-info-text" aria-hidden />
            </span>
          </div>
          <div>
            {phase === 'init' && (
              <>
                <p className="font-bold text-ink text-lg flex items-center justify-center gap-2">
                  <Spinner className="border-t-info-text" /> Opening checkout…
                </p>
                <p className="text-ink-muted text-sm mt-1">Minting a secure Paystack access code.</p>
              </>
            )}
            {phase === 'popup' && (
              <>
                <p className="font-bold text-ink text-lg">Checkout window open</p>
                <p className="text-ink-muted text-sm mt-1">Ask the customer to complete the payment in the Paystack popup.</p>
              </>
            )}
            {phase === 'verifying' && (
              <>
                <p className="font-bold text-ink text-lg flex items-center justify-center gap-2">
                  <Spinner className="border-t-pending-text" /> Waiting for payment…
                </p>
                <p className="text-ink-muted text-sm mt-1">Confirming reference {lastRef ? <span className="tabular">{lastRef}</span> : ''} with Paystack.</p>
              </>
            )}
          </div>
          {checkoutLink}
        </div>
      )}

      {phase === 'paid' && (
        <div className="space-y-4 text-center py-3">
          <div className="w-16 h-16 mx-auto rounded-full bg-paid-bg border-2 border-paid-text flex items-center justify-center text-paid-text" aria-hidden>
            <Check size={30} strokeWidth={2.75} />
          </div>
          <div>
            <p className="font-black text-ink text-xl">Paid</p>
            <p className="text-ink-muted text-sm mt-1">Payment verified with Paystack</p>
          </div>
          <div className="flex justify-center">
            <StatusPill status={current.discrepancy ? 'danger' : 'paid'} label={current.discrepancy ? 'Amount discrepancy — review' : 'Payment complete'} />
          </div>
        </div>
      )}

      {phase === 'pending' && (
        <div className="space-y-4">
          <div className="flex items-start gap-3 bg-pending-bg border-2 border-pending-text/30 rounded-input p-3">
            <AlertTriangle size={20} strokeWidth={2.5} className="text-pending-text shrink-0 mt-0.5" aria-hidden />
            <div>
              <p className="font-bold text-pending-text text-sm">Payment not completed</p>
              <p role="alert" className="text-[13px] text-ink-muted mt-0.5">
                {error || 'The popup was closed before payment. The order stays pending — you can reopen checkout, pay via the checkout page, or void it.'}
              </p>
            </div>
          </div>
          <Button variant="primary" size="lg" className="w-full" onClick={() => start(current)}>
            <RotateCcw size={16} strokeWidth={2.5} aria-hidden />
            Open checkout again
          </Button>
          {lastRef && (
            <Button variant="secondary" className="w-full" onClick={() => verify(current, lastRef)}>
              Verify payment again
            </Button>
          )}
          {checkoutLink && (
            <p className="text-center">{checkoutLink}</p>
          )}
          <div className="pt-2 border-t-2 border-line">
            <Button variant="ghost" className="w-full text-danger-text hover:bg-danger-bg" onClick={() => setPhase('void')}>
              <Ban size={15} strokeWidth={2.5} aria-hidden />
              Void this order…
            </Button>
          </div>
        </div>
      )}

      {phase === 'void' && (
        <VoidReasonPicker
          order={current}
          onVoided={onVoided}
          onCancel={() => setPhase('pending')}
        />
      )}

      <ReceiptModal
        open={receiptOpen && !!current}
        order={current}
        branding={branding}
        onClose={() => { setReceiptOpen(false); onClose() }}
      />
    </Modal>
  )
}

// VoidReasonPicker — catalog-backed void: a dropdown of the shop's active
// reasons plus optional free-text detail (joined with " — "). Falls back to
// a plain text input when the catalog can't be fetched (offline).
export function VoidReasonPicker({
  order,
  onVoided,
  onCancel,
}: {
  order: Order
  onVoided: (o: Order) => void
  onCancel: () => void
}) {
  const [reasons, setReasons] = useState<VoidReason[] | null>(null)
  const [catalogFailed, setCatalogFailed] = useState(false)
  const [label, setLabel] = useState('')
  const [extra, setExtra] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let alive = true
    api.get<VoidReason[]>('/api/v1/void-reasons')
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
      <div className="flex items-start gap-3 bg-danger-bg border-2 border-danger-text/30 rounded-input p-3">
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
            <Select value={label} onChange={(e) => setLabel(e.target.value)}>
              <option value="">Pick a reason…</option>
              {reasons.map((r) => (
                <option key={r.id} value={r.label}>{r.label}</option>
              ))}
            </Select>
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
