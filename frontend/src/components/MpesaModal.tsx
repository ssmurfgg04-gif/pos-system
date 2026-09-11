// M-Pesa checkout modal — the full payment lifecycle:
//   entering phone → STK pending (pulse + poll) → success
//   → failed/cancelled, with Manual Receipt Entry as the always-available
//   fallback (customer pays to the till/paybill, cashier types the
//   10-character M-Pesa receipt code).

import { useEffect, useRef, useState } from 'react'
import { api, Order } from '../lib/api'
import { Button, Modal, Input, Field, Spinner, StatusPill } from './ui'
import { formatMoney } from '../lib/money'
import { useBranding } from '../stores/branding'
import { toast } from '../stores/toasts'

type Phase = 'phone' | 'pending' | 'success' | 'failed'

export function MpesaModal({
  open,
  onClose,
  order,
  onPaid,
}: {
  open: boolean
  onClose: () => void
  order: Order | null
  onPaid: (o: Order) => void
}) {
  const branding = useBranding((s) => s.branding)
  const [phase, setPhase] = useState<Phase>('phone')
  const [phone, setPhone] = useState('')
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [elapsed, setElapsed] = useState(0)
  const [current, setCurrent] = useState<Order | null>(order)
  const pollRef = useRef<number | null>(null)
  const timerRef = useRef<number | null>(null)

  // Reset when a new order arrives.
  useEffect(() => {
    if (order) {
      setCurrent(order)
      const pay = order.payments[order.payments.length - 1]
      const stkActive = order.status === 'PENDING' && pay?.status === 'PENDING' && !!pay?.checkoutRequestId?.startsWith?.('ws_CO')
      setPhase(order.status === 'PAID' ? 'success' : stkActive ? 'pending' : 'phone')
      setPhone(pay?.phone?.replace(/^254/, '0') || '')
      setCode('')
      setError('')
      setElapsed(0)
    }
    return () => {
      if (pollRef.current) window.clearInterval(pollRef.current)
      if (timerRef.current) window.clearInterval(timerRef.current)
    }
  }, [order])

  // Poll the order while STK is pending (the backend sweeper completes it
  // via stkpushquery — no webhook needed on a LAN box).
  useEffect(() => {
    if (phase !== 'pending' || !current) return
    pollRef.current = window.setInterval(async () => {
      try {
        const o = await api.get<Order>(`/api/v1/orders/${current.id}`)
        setCurrent(o)
        const pay = o.payments[o.payments.length - 1]
        if (o.status === 'PAID') {
          setPhase('success')
          onPaid(o)
          stopTimers()
        } else if (pay?.status === 'FAILED') {
          setPhase('failed')
          setError(pay.resultDesc || 'STK push failed or timed out')
          stopTimers()
        }
      } catch {
        /* transient network error — keep polling */
      }
    }, 2000)
    timerRef.current = window.setInterval(() => setElapsed((e) => e + 1), 1000)
    return () => {
      if (pollRef.current) window.clearInterval(pollRef.current)
      if (timerRef.current) window.clearInterval(timerRef.current)
    }
  }, [phase, current?.id])

  const stopTimers = () => {
    if (pollRef.current) window.clearInterval(pollRef.current)
    if (timerRef.current) window.clearInterval(timerRef.current)
  }

  const pushStk = async () => {
    if (!current || busy) return
    setBusy(true)
    setError('')
    try {
      const o = await api.post<Order>(`/api/v1/orders/${current.id}/stkpush`, { phone })
      setCurrent(o)
      const pay = o.payments[o.payments.length - 1]
      if (pay?.status === 'FAILED') {
        setPhase('failed')
        setError(pay.resultDesc || 'STK push failed')
      } else {
        setPhase('pending')
        setElapsed(0)
      }
    } catch (err: any) {
      setError(err?.message || 'Could not send STK push')
    } finally {
      setBusy(false)
    }
  }

  const submitManual = async () => {
    if (!current || busy) return
    setBusy(true)
    setError('')
    try {
      const o = await api.post<Order>(`/api/v1/orders/${current.id}/manual`, {
        receiptCode: code.trim().toUpperCase(),
      })
      setCurrent(o)
      setPhase('success')
      onPaid(o)
      toast.success('Payment confirmed', `Receipt ${code.trim().toUpperCase()}`)
    } catch (err: any) {
      setError(err?.message || 'Invalid receipt code')
    } finally {
      setBusy(false)
    }
  }

  if (!current) return null
  const pay = current.payments[current.payments.length - 1]
  const till = branding.till_number || branding.paybill_number

  return (
    <Modal
      open={open}
      onClose={() => { stopTimers(); onClose() }}
      title={`M-Pesa — ${current.number}`}
      footer={
        phase === 'success' ? (
          <Button variant="primary" onClick={() => { stopTimers(); onClose() }}>
            Done — print receipt
          </Button>
        ) : undefined
      }
    >
      {/* Amount header */}
      <div className="flex items-baseline justify-between mb-4">
        <span className="text-ink-muted text-sm font-semibold">Amount due</span>
        <span className="text-[40px] leading-none font-black text-ink tabular">{formatMoney(current.totalCents)}</span>
      </div>

      {phase === 'phone' && (
        <div className="space-y-4">
          <Field label="Customer M-Pesa phone" hint="Safaricom sends a payment prompt to this number.">
            <Input
              value={phone}
              onChange={(e) => setPhone(e.target.value)}
              inputMode="tel"
              placeholder="07XX XXX XXX"
              autoFocus
              onKeyDown={(e) => e.key === 'Enter' && pushStk()}
            />
          </Field>
          {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
          <Button variant="primary" size="lg" className="w-full" onClick={pushStk} disabled={busy || !phone.trim()}>
            {busy ? <Spinner className="border-t-brand-ink" /> : 'Send payment request'}
          </Button>
          {till && (
            <div className="bg-surface-muted border-2 border-line rounded-input p-3">
              <p className="text-[13px] font-bold text-ink">No prompt? Pay manually:</p>
              <p className="text-[13px] text-ink-muted mt-1">
                Send {formatMoney(current.totalCents)} to{' '}
                <strong className="text-ink">{branding.paybill_number ? 'Paybill ' + branding.paybill_number : 'Till ' + till}</strong>
                {branding.paybill_number && <> (Account: <strong className="text-ink">{current.number}</strong>)</>}
                , then enter the receipt code below.
              </p>
            </div>
          )}
          <ManualEntry code={code} setCode={setCode} busy={busy} onSubmit={submitManual} error={error} />
        </div>
      )}

      {phase === 'pending' && (
        <div className="space-y-5 text-center py-2">
          <div className="flex justify-center">
            <span className="relative flex w-16 h-16 items-center justify-center">
              <span className="absolute inset-0 rounded-full bg-pending-bg border-2 border-pending-text/40 anim-pulse-dot" aria-hidden />
              <span className="text-3xl" aria-hidden>📲</span>
            </span>
          </div>
          <div>
            <p className="font-bold text-ink text-lg">Waiting for the customer…</p>
            <p className="text-ink-muted text-sm mt-1">
              An M-Pesa prompt was sent to — ask them to enter their PIN:
            </p>
            <p className="mt-2 inline-block tabular font-black text-xl text-ink bg-surface-muted border-2 border-line-strong rounded-input px-4 py-2">
              {pay?.phone || phone}
            </p>
          </div>
          {/* progress ~180s (backend timeout is 3 min) */}
          <div className="h-2 bg-surface-muted rounded-pill overflow-hidden border border-line" aria-hidden>
            <div
              className="h-full bg-pending-text transition-[width] duration-1000 ease-linear"
              style={{ width: `${Math.min(100, (elapsed / 180) * 100)}%` }}
            />
          </div>
          <p className="text-ink-subtle text-xs tabular">
            {Math.max(0, 180 - elapsed)}s left · auto-checks every 2s
          </p>
          <details className="text-left bg-surface-muted border-2 border-line rounded-input p-3">
            <summary className="text-[13px] font-bold text-ink cursor-pointer">
              Customer paid at the till themselves? Enter receipt code manually
            </summary>
            <div className="pt-3">
              <ManualEntry code={code} setCode={setCode} busy={busy} onSubmit={submitManual} error={error} plain />
            </div>
          </details>
          <Button variant="ghost" onClick={() => { stopTimers(); setPhase('failed'); setError('Cancelled by cashier') }}>
            Cancel this request
          </Button>
        </div>
      )}

      {phase === 'success' && (
        <div className="space-y-4 text-center py-3">
          <div className="w-16 h-16 mx-auto rounded-full bg-paid-bg border-2 border-paid-text flex items-center justify-center text-3xl" aria-hidden>
            ✓
          </div>
          <div>
            <p className="font-black text-ink text-xl">Paid</p>
            <p className="text-ink-muted text-sm mt-1">
              {pay?.mpesaReceipt ? (
                <>M-Pesa receipt <strong className="text-ink tabular">{pay.mpesaReceipt}</strong></>
              ) : (
                'Payment confirmed'
              )}
            </p>
          </div>
          <div className="flex justify-center">
            <StatusPill status={current.discrepancy ? 'danger' : 'paid'} label={current.discrepancy ? 'Amount discrepancy — review' : 'Payment complete'} />
          </div>
        </div>
      )}

      {phase === 'failed' && (
        <div className="space-y-4">
          <div className="flex items-start gap-3 bg-danger-bg border-2 border-danger-text/30 rounded-input p-3">
            <span className="text-xl" aria-hidden>⚠</span>
            <div>
              <p className="font-bold text-danger-text text-sm">STK push didn't complete</p>
              <p className="text-[13px] text-ink-muted mt-0.5">{error || pay?.resultDesc || 'The customer may have cancelled or the request timed out.'}</p>
            </div>
          </div>
          <Button variant="secondary" className="w-full" onClick={() => setPhase('phone')}>
            ↻ Retry with a phone number
          </Button>
          <ManualEntry code={code} setCode={setCode} busy={busy} onSubmit={submitManual} error={error} highlight />
        </div>
      )}
    </Modal>
  )
}

function ManualEntry({
  code,
  setCode,
  busy,
  onSubmit,
  error,
  highlight,
  plain,
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
    <div className={plain ? '' : highlight ? 'pt-2 border-t-2 border-line mt-2' : ''}>
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
