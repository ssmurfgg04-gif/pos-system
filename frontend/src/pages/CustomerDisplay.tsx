// Customer-facing display (/display). Renders ONLY what the till window
// publishes over the same-origin channel (see lib/customerDisplay.ts): no
// login, no API calls, no settings. Built to be read from across a counter:
// big type, high contrast, the amount and where to pay it front and centre.

import { useEffect, useState } from 'react'
import { CheckCircle2, Smartphone, ShoppingBag } from 'lucide-react'
import { DisplayState, PAID_HOLD_MS, subscribeDisplay } from '../lib/customerDisplay'

export function CustomerDisplay() {
  const [state, setState] = useState<DisplayState | null>(null)

  useEffect(() => {
    document.title = 'Customer display'
    return subscribeDisplay(setState)
  }, [])

  // The thank-you screen returns to the welcome on its own, so the next
  // customer never sees the last one's total.
  useEffect(() => {
    if (state?.kind !== 'paid') return
    const t = window.setTimeout(() => setState({ kind: 'idle', brand: state.brand }), PAID_HOLD_MS)
    return () => window.clearTimeout(t)
  }, [state])

  const brand = state?.brand
  return (
    <div
      className="min-h-screen flex flex-col bg-surface-muted text-ink select-none"
      style={brand?.color ? ({ ['--color-brand' as string]: brand.color } as React.CSSProperties) : undefined}
    >
      <header className="flex items-center gap-4 px-8 py-5 bg-brand text-white">
        {brand?.logoUrl && (
          <img
            src={brand.logoUrl}
            alt=""
            className="h-12 w-12 rounded-input object-contain bg-white/95 p-1"
            onError={(e) => { (e.currentTarget as HTMLImageElement).style.display = 'none' }}
          />
        )}
        <p className="text-[28px] font-extrabold tracking-tight truncate">{brand?.storeName || 'Welcome'}</p>
      </header>

      <main className="flex-1 min-h-0 flex">
        {!state && <Waiting />}
        {state?.kind === 'idle' && <Welcome />}
        {state?.kind === 'cart' && <Cart s={state} />}
        {state?.kind === 'pay' && <Pay s={state} />}
        {state?.kind === 'paid' && <Paid s={state} />}
      </main>
    </div>
  )
}

function Waiting() {
  return (
    <div className="m-auto text-center px-8">
      <p className="text-[22px] font-semibold text-ink-muted">Waiting for the till…</p>
      <p className="text-[15px] text-ink-subtle mt-2">Keep the till window open; this screen follows it automatically.</p>
    </div>
  )
}

function Welcome() {
  return (
    <div className="m-auto text-center px-8">
      <ShoppingBag className="mx-auto text-brand" size={72} strokeWidth={1.75} aria-hidden />
      <p className="text-[56px] font-extrabold mt-4 leading-none">Karibu!</p>
      <p className="text-[24px] text-ink-muted mt-3">Welcome</p>
    </div>
  )
}

function Cart({ s }: { s: Extract<DisplayState, { kind: 'cart' }> }) {
  return (
    <div className="flex-1 grid grid-cols-1 md:grid-cols-[1fr_minmax(320px,40%)] min-h-0">
      <ul className="overflow-y-auto px-8 py-6 space-y-3" aria-label="Items">
        {s.lines.map((l, i) => (
          <li key={i} className="flex items-baseline gap-4 text-[24px]">
            <span className="font-bold tabular text-ink-muted w-14 shrink-0 text-right">{l.qty}×</span>
            <span className="flex-1 min-w-0 truncate font-semibold">{l.name}</span>
            <span className="font-bold tabular shrink-0">{l.total}</span>
          </li>
        ))}
      </ul>
      <aside className="bg-surface border-t md:border-t-0 md:border-l border-line px-8 py-8 flex flex-col justify-center gap-4">
        {s.discount && (
          <p className="flex justify-between text-[22px] text-paid-text font-semibold">
            <span>Discount</span><span className="tabular">−{s.discount}</span>
          </p>
        )}
        <div>
          <p className="text-[18px] uppercase tracking-widest text-ink-muted font-bold">Total · {s.itemCount} item{s.itemCount === 1 ? '' : 's'}</p>
          <p className="text-[clamp(36px,5.2vw,64px)] leading-none font-extrabold tabular whitespace-nowrap mt-2" data-testid="display-total">{s.total}</p>
        </div>
        {s.received && (
          <p className="flex justify-between text-[22px] font-semibold">
            <span>Cash</span><span className="tabular">{s.received}</span>
          </p>
        )}
        {s.change && (
          <p className="flex justify-between text-[30px] font-extrabold text-paid-text">
            <span>Change</span><span className="tabular">{s.change}</span>
          </p>
        )}
      </aside>
    </div>
  )
}

function Pay({ s }: { s: Extract<DisplayState, { kind: 'pay' }> }) {
  return (
    <div className="m-auto text-center px-8 max-w-3xl">
      <p className="text-[22px] uppercase tracking-widest font-bold text-ink-muted">Lipa na M-Pesa · Pay with M-Pesa</p>
      <p className="text-[clamp(48px,8.5vw,88px)] leading-none font-extrabold tabular whitespace-nowrap mt-4" data-testid="display-amount">{s.amount}</p>
      {s.stkSent ? (
        <div className="mt-8 inline-flex items-center gap-4 rounded-card bg-surface border-2 border-brand px-8 py-5">
          <Smartphone className="text-brand shrink-0" size={44} strokeWidth={2} aria-hidden />
          <p className="text-[26px] font-bold text-left leading-snug">
            Check your phone and enter your M-Pesa PIN
          </p>
        </div>
      ) : s.payTo ? (
        <div className="mt-8 rounded-card bg-surface border-2 border-brand px-10 py-6 inline-block">
          <p className="text-[20px] text-ink-muted font-semibold">Pay to</p>
          <p className="text-[56px] leading-tight font-extrabold tabular" data-testid="display-payto">{s.payTo}</p>
          {s.account && (
            <p className="text-[26px] font-bold mt-2">
              Account <span className="tabular">{s.account}</span>
            </p>
          )}
        </div>
      ) : null}
      <p className="text-[20px] text-ink-muted mt-6">Please pay exactly this amount.</p>
    </div>
  )
}

function Paid({ s }: { s: Extract<DisplayState, { kind: 'paid' }> }) {
  return (
    <div className="m-auto text-center px-8">
      <CheckCircle2 className="mx-auto text-paid-text" size={96} strokeWidth={2} aria-hidden />
      <p className="text-[64px] font-extrabold leading-none mt-4">Asante!</p>
      <p className="text-[26px] text-ink-muted mt-3">Thank you · Paid {s.total}</p>
      {s.change && (
        <p className="text-[44px] font-extrabold text-paid-text mt-6 tabular">Change {s.change}</p>
      )}
    </div>
  )
}
