// SplitTenderEditor — mixed-tender builder for the charge modal: one leg
// per payment method (cash / M-Pesa / card / store credit), a live
// "remaining" readout that must reach exactly 0, and the backend's rule
// baked into the UI: at most ONE asynchronous leg (M-Pesa or Paystack) —
// the second async option is disabled once one exists. Cash and store
// credit settle instantly at the till.
//
// Standalone by design: ChargeModal owns the legs state and only mounts
// this when the cashier opts into split payment.

import { SplitLeg } from '../lib/api'
import { splitRemaining, formatMoney, normalizePhoneKe } from '../lib/money'
import { Input, MoneyInput, Select } from './ui'
import { Plus, X, AlertTriangle } from 'lucide-react'

const METHODS: SplitLeg['method'][] = ['cash', 'mpesa', 'paystack', 'credit']

const METHOD_LABEL: Record<SplitLeg['method'], string> = {
  cash: 'Cash',
  mpesa: 'M-Pesa',
  paystack: 'Card / M-M',
  credit: 'Store credit',
}

export function isAsyncLeg(l: SplitLeg): boolean {
  return l.method === 'mpesa' || l.method === 'paystack'
}

export function SplitTenderEditor({
  legs,
  onChange,
  totalCents,
  creditEnabled,
  paystackReady,
  creditReady,
}: {
  legs: SplitLeg[]
  onChange: (legs: SplitLeg[]) => void
  totalCents: number
  creditEnabled: boolean
  paystackReady: boolean
  /** A customer with store credit is attached to the checkout. */
  creditReady: boolean
}) {
  const remaining = splitRemaining(totalCents, legs)
  const asyncIdx = legs.findIndex(isAsyncLeg)

  const update = (i: number, patch: Partial<SplitLeg>) => {
    onChange(legs.map((l, j) => (j === i ? { ...l, ...patch } : l)))
  }

  const remove = (i: number) => {
    onChange(legs.filter((_, j) => j !== i))
  }

  const addLeg = () => {
    onChange([...legs, { method: 'cash', amountCents: 0 }])
  }

  // Options for one row: capability-gated, and the second async method is
  // disabled once an async leg exists elsewhere (server rejects > 1).
  const optionsFor = (i: number): SplitLeg['method'][] =>
    METHODS.filter((m) => {
      if (m === 'credit' && !creditEnabled) return false
      if (m === 'paystack' && !paystackReady) return false
      if (isAsyncLeg({ method: m, amountCents: 0 }) && asyncIdx >= 0 && asyncIdx !== i) return false
      return true
    })

  const remainingState = remaining === 0 ? 'exact' : remaining > 0 ? 'under' : 'over'

  return (
    <div className="space-y-2">
      {legs.map((leg, i) => {
        const blocked = !optionsFor(i).includes(leg.method)
        return (
          <div key={i} className="border-2 border-line rounded-input p-2.5 bg-surface space-y-2">
            <div className="flex gap-2 items-center">
              <Select
                value={leg.method}
                onChange={(e) => update(i, { method: e.target.value as SplitLeg['method'] })}
                className="w-40 shrink-0"
                aria-label={`Payment method ${i + 1}`}
              >
                {blocked && <option value={leg.method}>{METHOD_LABEL[leg.method]}</option>}
                {optionsFor(i).map((m) => (
                  <option key={m} value={m}>{METHOD_LABEL[m]}</option>
                ))}
              </Select>
              <MoneyInput
                value={leg.amountCents}
                onCents={(c) => update(i, { amountCents: c })}
                placeholder="0.00"
                className="flex-1 font-bold"
                aria-label={`Amount for ${METHOD_LABEL[leg.method]} ${i + 1}`}
              />
              {legs.length > 1 && (
                <button
                  type="button"
                  onClick={() => remove(i)}
                  aria-label={`Remove ${METHOD_LABEL[leg.method]} payment`}
                  className="w-11 h-11 shrink-0 flex items-center justify-center border-2 border-line rounded-input text-ink-muted hover:text-danger-text hover:border-danger-text/40 active:translate-y-[1px]"
                >
                  <X size={16} strokeWidth={2.5} aria-hidden />
                </button>
              )}
            </div>
            {leg.method === 'mpesa' && (
              <Input
                value={leg.phone || ''}
                onChange={(e) => update(i, { phone: e.target.value })}
                inputMode="tel"
                placeholder="Customer M-Pesa phone — 07XX XXX XXX"
                aria-label="M-Pesa phone for this leg"
              />
            )}
            {leg.method === 'mpesa' && leg.phone && !normalizePhoneKe(leg.phone) && (
              <p className="text-[11px] text-danger-text font-semibold">Not a valid Safaricom number yet.</p>
            )}
            {leg.method === 'paystack' && (
              <Input
                type="email"
                value={leg.email || ''}
                onChange={(e) => update(i, { email: e.target.value })}
                inputMode="email"
                placeholder="Customer email (optional — Paystack receipt)"
                aria-label="Email for the card payment leg"
              />
            )}
            {leg.method === 'credit' && !creditReady && (
              <p className="text-[11px] text-danger-text font-semibold flex items-center gap-1">
                <AlertTriangle size={12} strokeWidth={2.5} aria-hidden />
                Pick the customer below — store credit pays from their wallet.
              </p>
            )}
          </div>
        )
      })}

      <button
        type="button"
        onClick={addLeg}
        className="min-h-11 px-3 text-[13px] font-bold bg-surface-muted border-2 border-line rounded-input hover:border-line-strong active:translate-y-[1px] inline-flex items-center gap-1.5 text-ink"
      >
        <Plus size={14} strokeWidth={2.75} aria-hidden />
        Add payment
      </button>

      <div
        role="status"
        className={`rounded-input border-2 p-3 flex justify-between items-baseline ${
          remainingState === 'over'
            ? 'bg-danger-bg border-danger-text/30 text-danger-text'
            : remainingState === 'under'
              ? 'bg-pending-bg border-pending-text/30 text-pending-text'
              : 'bg-paid-bg border-paid-text/30 text-paid-text'
        }`}
      >
        <span className="font-bold text-sm">
          {remainingState === 'over' ? 'Split is over the total' : remainingState === 'under' ? 'Still to allocate' : 'Split covers the total'}
        </span>
        <span className="font-black text-2xl tabular">
          {remainingState === 'exact' ? formatMoney(totalCents) : formatMoney(Math.abs(remaining))}
        </span>
      </div>

      <p className="text-[11px] text-ink-subtle">
        Cash and store credit settle now. One M-Pesa or card payment can stay pending — a second one is not allowed.
      </p>
    </div>
  )
}
