// LEDGER component kit — neubrutalist touch-first primitives.
// Spec notes: primary = brand bg + slate-900 ink (7.04:1, never white);
// press = translate + shadow collapse; targets ≥44px; status pills always
// carry text (never color-only); toasts bottom-center above modals.

import React, { useEffect, useRef } from 'react'
import { useToasts } from '../stores/toasts'
import { useNet } from '../offline/heartbeat'
import { X, Check, AlertTriangle, Info, Package } from 'lucide-react'

// ---- Button ----

type ButtonVariant = 'primary' | 'secondary' | 'danger' | 'ghost'
type ButtonSize = 'sm' | 'md' | 'lg'

const buttonBase =
  'inline-flex items-center justify-center gap-2 font-semibold select-none ' +
  'border-2 border-line-strong transition-[transform,box-shadow] duration-75 ' +
  'active:translate-x-[2px] active:translate-y-[2px] active:shadow-none ' +
  'disabled:opacity-40 disabled:pointer-events-none rounded-input'

const buttonVariants: Record<ButtonVariant, string> = {
  primary: 'bg-brand text-brand-ink shadow-brutal-brand hover:brightness-105',
  secondary: 'bg-surface text-ink shadow-brutal hover:bg-surface-muted',
  danger: 'bg-danger text-white shadow-brutal-danger hover:brightness-105',
  ghost: 'bg-transparent text-ink-muted border-transparent hover:bg-surface-muted hover:text-ink shadow-none',
}

const buttonSizes: Record<ButtonSize, string> = {
  sm: 'min-h-9 px-3 text-sm',
  md: 'min-h-11 px-4 text-[15px]',
  lg: 'min-h-14 px-6 text-lg',
}

export function Button({
  variant = 'secondary',
  size = 'md',
  className = '',
  type = 'button',
  ...rest
}: React.ButtonHTMLAttributes<HTMLButtonElement> & { variant?: ButtonVariant; size?: ButtonSize }) {
  return (
    <button
      type={type}
      className={`${buttonBase} ${buttonVariants[variant]} ${buttonSizes[size]} ${className}`}
      {...rest}
    />
  )
}

// ---- Card ----

export function Card({
  title,
  sub,
  actions,
  children,
  className = '',
  pad = true,
}: {
  title?: React.ReactNode
  sub?: React.ReactNode
  actions?: React.ReactNode
  children?: React.ReactNode
  className?: string
  pad?: boolean
}) {
  return (
    <section className={`bg-surface border-2 border-line-strong rounded-card shadow-brutal ${className}`}>
      {(title || actions) && (
        <header className="flex items-start justify-between gap-3 px-4 pt-4 pb-2 sm:px-5">
          <div>
            <h2 className="text-base font-bold text-ink leading-tight">{title}</h2>
            {sub && <p className="text-[13px] text-ink-muted mt-0.5">{sub}</p>}
          </div>
          {actions && <div className="flex items-center gap-2 shrink-0">{actions}</div>}
        </header>
      )}
      <div className={pad ? 'px-4 pb-4 sm:px-5 sm:pb-5' : ''}>{children}</div>
    </section>
  )
}

// ---- Modal ----

type ModalSize = 'sm' | 'md' | 'lg' | 'xl' | 'full'

const modalSizes: Record<ModalSize, string> = {
  sm: 'max-w-sm',
  md: 'max-w-lg',
  lg: 'max-w-2xl',
  xl: 'max-w-4xl',
  full: 'max-w-6xl',
}

export function Modal({
  open,
  onClose,
  title,
  size = 'md',
  children,
  footer,
}: {
  open: boolean
  onClose: () => void
  title?: React.ReactNode
  size?: ModalSize
  children: React.ReactNode
  footer?: React.ReactNode
}) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null
  return (
    <div
      className="fixed inset-0 z-50 flex items-end sm:items-center justify-center p-0 sm:p-6"
      role="dialog"
      aria-modal="true"
    >
      <div className="absolute inset-0 bg-black/60 anim-backdrop" onClick={onClose} aria-hidden />
      <div
        ref={ref}
        className={`relative w-full ${modalSizes[size]} bg-surface border-2 border-line-strong rounded-card shadow-brutal anim-modal max-h-[92vh] flex flex-col`}
      >
        {title && (
          <header className="flex items-center justify-between gap-3 px-5 pt-4 pb-3 border-b-2 border-line">
            <h2 className="text-lg font-bold text-ink">{title}</h2>
            <button
              onClick={onClose}
              aria-label="Close"
              className="min-w-11 min-h-11 -mr-2 flex items-center justify-center text-ink-muted hover:text-ink rounded-input hover:bg-surface-muted"
            >
              <X size={20} strokeWidth={2.5} aria-hidden />
            </button>
          </header>
        )}
        <div className="px-5 py-4 overflow-y-auto">{children}</div>
        {footer && <footer className="px-5 py-4 border-t-2 border-line flex flex-wrap justify-end gap-3 bg-surface-muted/50">{footer}</footer>}
      </div>
    </div>
  )
}

// ---- Inputs ----

export function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="block text-[13px] font-semibold text-ink-muted mb-1">{label}</span>
      {children}
      {hint && <span className="block text-xs text-ink-subtle mt-1">{hint}</span>}
    </label>
  )
}

const inputCls =
  'w-full min-h-11 px-3 bg-surface border-2 border-line-strong rounded-input text-ink ' +
  'placeholder:text-ink-subtle focus:outline-none focus-visible:outline-2 ' +
  'focus-visible:outline-brand focus-visible:-outline-offset-0 disabled:bg-surface-muted'

export const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(
  function Input(props, ref) {
    return <input ref={ref} {...props} className={`${inputCls} ${props.className || ''}`} />
  },
)

export function Select(props: React.SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={`${inputCls} ${props.className || ''}`} />
}

export function Textarea(props: React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...props} className={`${inputCls} min-h-20 py-2 ${props.className || ''}`} />
}

/** Money input: cents value in/out, grouped display, numeric keyboard. */
export function MoneyInput({
  value,
  onCents,
  className = '',
  ...rest
}: Omit<React.InputHTMLAttributes<HTMLInputElement>, 'value' | 'onChange'> & {
  value: number
  onCents: (cents: number) => void
}) {
  const display = value === 0 ? '' : (value / 100).toFixed(2)
  return (
    <input
      {...rest}
      inputMode="decimal"
      className={`${inputCls} tabular text-right ${className}`}
      value={display}
      onChange={(e) => {
        const raw = e.target.value.replace(/[^0-9.]/g, '')
        const [w = '0', f = ''] = raw.split('.')
        const frac = (f + '00').slice(0, 2)
        onCents(Math.max(0, Number(w || '0') * 100 + Number(frac)))
      }}
    />
  )
}

// ---- StatusPill (text + dot, never color-only) ----

export type PillStatus = 'paid' | 'pending' | 'void' | 'danger' | 'info' | 'queued' | 'progress' | 'ready' | 'delivered'

const pillMap: Record<PillStatus, { label: string; cls: string }> = {
  paid: { label: 'Paid', cls: 'bg-paid-bg text-paid-text' },
  pending: { label: 'Pending', cls: 'bg-pending-bg text-pending-text' },
  void: { label: 'Voided', cls: 'bg-void-bg text-void-text' },
  danger: { label: 'Failed', cls: 'bg-danger-bg text-danger-text' },
  info: { label: 'Info', cls: 'bg-info-bg text-info-text' },
  queued: { label: 'Queued', cls: 'bg-void-bg text-void-text' },
  progress: { label: 'In progress', cls: 'bg-pending-bg text-pending-text' },
  ready: { label: 'Ready', cls: 'bg-paid-bg text-paid-text' },
  delivered: { label: 'Delivered', cls: 'bg-void-bg text-void-text' },
}

const pillDot: Record<PillStatus, string> = {
  paid: 'bg-paid-text', pending: 'bg-pending-text', void: 'bg-void-text',
  danger: 'bg-danger-text', info: 'bg-info-text', queued: 'bg-void-text',
  progress: 'bg-pending-text', ready: 'bg-paid-text', delivered: 'bg-void-text',
}

export function StatusPill({ status, label }: { status: PillStatus; label?: string }) {
  const m = pillMap[status]
  return (
    <span
      className={`inline-flex items-center gap-1.5 px-2.5 py-0.5 rounded-pill text-xs font-bold whitespace-nowrap ${m.cls}`}
    >
      <span className={`w-1.5 h-1.5 rounded-full ${pillDot[status]}`} aria-hidden />
      {label ?? m.label}
    </span>
  )
}

// ---- Table ----

export function Table({ head, children, className = '' }: { head: React.ReactNode[]; children: React.ReactNode; className?: string }) {
  return (
    <div className={`overflow-x-auto ${className}`}>
      <table className="w-full text-sm border-collapse">
        <thead>
          <tr className="border-b-2 border-line-strong bg-surface-muted sticky top-0 z-10">
            {head.map((h, i) => (
              <th key={i} className="text-left px-3 py-2.5 text-[12px] uppercase tracking-wide font-bold text-ink-muted whitespace-nowrap">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-line">{children}</tbody>
      </table>
    </div>
  )
}

// ---- Tabs ----

export function Tabs<T extends string>({ tabs, value, onChange }: { tabs: { key: T; label: string; icon?: React.ReactNode }[]; value: T; onChange: (t: T) => void }) {
  return (
    <div role="tablist" className="flex gap-1 p-1 bg-surface-muted border-2 border-line rounded-input overflow-x-auto">
      {tabs.map((t) => (
        <button
          key={t.key}
          role="tab"
          aria-selected={value === t.key}
          onClick={() => onChange(t.key)}
          className={`min-h-9 px-3.5 rounded-[5px] text-sm font-semibold whitespace-nowrap transition-colors inline-flex items-center gap-1.5 ${
            value === t.key ? 'bg-surface text-ink border-2 border-line-strong shadow-brutal-sm' : 'text-ink-muted hover:text-ink'
          }`}
        >
          {t.icon}
          {t.label}
        </button>
      ))}
    </div>
  )
}

// ---- Empty / Spinner ----

export function EmptyState({ icon, title, body, action }: { icon?: React.ReactNode; title: string; body?: string; action?: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center py-12 px-6 text-center">
      <div className="w-14 h-14 rounded-card border-2 border-line-strong bg-surface-muted flex items-center justify-center text-ink-muted mb-3" aria-hidden>
        {icon ?? <Package size={24} strokeWidth={2.25} />}
      </div>
      <h3 className="font-bold text-ink">{title}</h3>
      {body && <p className="text-sm text-ink-muted mt-1 max-w-sm">{body}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}

export function Spinner({ className = '' }: { className?: string }) {
  return (
    <span
      className={`inline-block w-5 h-5 border-[3px] border-line border-t-line-strong rounded-full anim-spin ${className}`}
      role="status"
      aria-label="Loading"
    />
  )
}

// ---- Keypad (3×4, POS PIN entry) ----

export function Keypad({ onDigit, onBack, onClear }: { onDigit: (d: string) => void; onBack: () => void; onClear?: () => void }) {
  const keys = ['1', '2', '3', '4', '5', '6', '7', '8', '9', onClear ? 'C' : '', '0', '⌫']
  return (
    <div className="grid grid-cols-3 gap-2 max-w-[260px] mx-auto">
      {keys.map((k, i) =>
        k === '' ? (
          <span key={i} />
        ) : (
          <button
            key={i}
            onClick={() => (k === '⌫' ? onBack() : k === 'C' ? onClear?.() : onDigit(k))}
            className="min-h-14 min-w-14 text-xl font-bold bg-surface border-2 border-line-strong rounded-input shadow-brutal-sm
              active:translate-x-[2px] active:translate-y-[2px] active:shadow-none transition-transform duration-75"
            aria-label={k === '⌫' ? 'Backspace' : k === 'C' ? 'Clear' : k}
          >
            {k}
          </button>
        ),
      )}
    </div>
  )
}

// ---- Toasts (bottom-center, z-60 above modals) ----

export function ToastHost() {
  const { toasts, dismiss } = useToasts()
  if (toasts.length === 0) return null
  return (
    <div className="fixed bottom-5 left-1/2 -translate-x-1/2 z-[60] flex flex-col gap-2 items-center w-full max-w-md px-4" aria-live="polite">
      {toasts.map((t) => (
        <div
          key={t.id}
          role="status"
          className={`w-full flex items-start gap-3 px-4 py-3 border-2 border-line-strong rounded-card shadow-brutal-sm anim-toast bg-surface ${
            t.kind === 'success' ? 'border-l-paid-text border-l-8' : t.kind === 'error' ? 'border-l-danger-text border-l-8' : ''
          }`}
          onClick={() => dismiss(t.id)}
        >
          <span className={`shrink-0 leading-none mt-0.5 ${t.kind === 'success' ? 'text-paid-text' : t.kind === 'error' ? 'text-danger-text' : 'text-info-text'}`} aria-hidden>
            {t.kind === 'success' ? <Check size={18} strokeWidth={2.5} /> : t.kind === 'error' ? <AlertTriangle size={18} strokeWidth={2.5} /> : <Info size={18} strokeWidth={2.5} />}
          </span>
          <div className="min-w-0">
            <p className="font-bold text-ink text-sm">{t.title}</p>
            {t.body && <p className="text-[13px] text-ink-muted break-words">{t.body}</p>}
          </div>
        </div>
      ))}
    </div>
  )
}

// ---- Offline banner (app shell top, under the topbar) ----

export function OfflineBanner() {
  const { online, pending } = useNet()
  if (online && pending === 0) return null
  return (
    <div
      role="status"
      className={`px-4 py-2.5 text-[13px] font-bold flex items-center gap-2 border-b-2 ${
        online
          ? 'bg-pending-bg text-pending-text border-pending-text/40'
          : 'bg-pending-bg text-pending-text border-pending-text/40'
      }`}
    >
      <span className={`w-2 h-2 rounded-full ${online ? 'bg-pending-text anim-pulse-dot' : 'bg-danger anim-pulse-dot'}`} aria-hidden />
      {online
        ? `Back online — ${pending} sale${pending === 1 ? '' : 's'} queued for sync…`
        : `Offline mode — sales are saved locally${pending > 0 ? ` (${pending} queued)` : ''}`}
    </div>
  )
}
