// Parked (held) sales drawer — slides in from the right over the POS
// screen. Lists every parked cart with its reference name, item count,
// total estimate, who parked it and when; Resume rehydrates the cart
// (parent does the matching) then deletes the hold; Discard deletes only.

import { useEffect, useState } from 'react'
import { api, HeldSale } from '../lib/api'
import { Button, EmptyState, Spinner } from './ui'
import { formatMoneyCompact } from '../lib/money'
import { toast } from '../stores/toasts'
import { X, Archive, Play, Trash2 } from 'lucide-react'

function when(iso: string): string {
  const d = new Date(iso)
  if (isNaN(d.getTime())) return iso
  return d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

export function HeldSalesDrawer({
  open,
  onClose,
  onResume,
  onChanged,
}: {
  open: boolean
  onClose: () => void
  /** Rehydrate the cart from a held sale (throws to keep the hold). */
  onResume: (sale: HeldSale) => Promise<void>
  /** Parent refreshes the header count after any change. */
  onChanged: () => void
}) {
  const [sales, setSales] = useState<HeldSale[] | null>(null)
  const [busyId, setBusyId] = useState<number | null>(null)

  const load = async () => {
    try {
      const list = await api.get<HeldSale[]>('/api/v1/held-sales')
      setSales(Array.isArray(list) ? list : [])
    } catch (e: any) {
      setSales([])
      toast.error('Could not load parked sales', e?.message)
    }
  }

  useEffect(() => {
    if (open) {
      setSales(null)
      load()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [open, onClose])

  if (!open) return null

  const discard = async (s: HeldSale) => {
    setBusyId(s.id)
    try {
      await api.del(`/api/v1/held-sales/${s.id}`)
      setSales((list) => (list ? list.filter((x) => x.id !== s.id) : list))
      onChanged()
      toast.success('Parked sale discarded', s.refName)
    } catch (e: any) {
      toast.error('Could not discard', e?.message)
    } finally {
      setBusyId(null)
    }
  }

  const resume = async (s: HeldSale) => {
    setBusyId(s.id)
    try {
      await onResume(s)
      // Only after the cart is back does the hold disappear.
      await api.del(`/api/v1/held-sales/${s.id}`)
      setSales((list) => (list ? list.filter((x) => x.id !== s.id) : list))
      onChanged()
      onClose()
      toast.success('Sale resumed', s.refName)
    } catch (e: any) {
      toast.error('Could not resume', e?.message)
    } finally {
      setBusyId(null)
    }
  }

  const itemCount = (s: HeldSale) => s.items.reduce((n, i) => n + i.qty, 0)
  const estimate = (s: HeldSale) => s.items.reduce((sum, i) => sum + (i.unitPriceCents ?? 0) * i.qty, 0)

  return (
    <div className="fixed inset-0 z-50" role="dialog" aria-modal="true" aria-label="Parked sales">
      <div className="absolute inset-0 bg-black/60 anim-backdrop" onClick={onClose} aria-hidden />
      <aside className="absolute right-0 top-0 h-full w-full max-w-md bg-surface border-l-2 border-line-strong shadow-brutal flex flex-col anim-modal">
        <header className="flex items-center justify-between gap-3 px-5 pt-4 pb-3 border-b-2 border-line">
          <h2 className="text-lg font-bold text-ink flex items-center gap-2">
            <Archive size={18} strokeWidth={2.5} aria-hidden />
            Parked sales
          </h2>
          <button
            onClick={onClose}
            aria-label="Close parked sales"
            className="min-w-11 min-h-11 -mr-2 flex items-center justify-center text-ink-muted hover:text-ink rounded-input hover:bg-surface-muted"
          >
            <X size={20} strokeWidth={2.5} aria-hidden />
          </button>
        </header>

        <div className="flex-1 overflow-y-auto px-4 py-4 space-y-3">
          {!sales ? (
            <div className="py-16 flex justify-center"><Spinner className="w-7 h-7 border-4" /></div>
          ) : sales.length === 0 ? (
            <EmptyState
              icon={<Archive size={24} strokeWidth={2.25} />}
              title="Nothing parked"
              body="Use Park for later on a busy till to hold a cart while serving the next customer."
            />
          ) : (
            sales.map((s) => (
              <div key={s.id} className="border-2 border-line-strong rounded-card shadow-brutal-sm p-3 bg-surface">
                <div className="flex items-start justify-between gap-2">
                  <div className="min-w-0">
                    <p className="font-bold text-[13px] text-ink leading-snug truncate">{s.refName}</p>
                    <p className="text-[11px] text-ink-subtle mt-0.5">
                      {s.customerName ? `${s.customerName} · ` : ''}{s.createdByName || '—'} · {when(s.createdAt)}
                    </p>
                  </div>
                  <span className="font-black text-ink tabular text-[15px] whitespace-nowrap">≈ {formatMoneyCompact(estimate(s))}</span>
                </div>
                <p className="text-[12px] text-ink-muted mt-1">{itemCount(s)} item{itemCount(s) === 1 ? '' : 's'}</p>
                <div className="flex gap-2 mt-2.5">
                  <Button size="sm" variant="primary" className="flex-1" onClick={() => resume(s)} disabled={busyId === s.id}>
                    {busyId === s.id ? <Spinner className="border-t-brand-ink w-4 h-4" /> : <Play size={14} strokeWidth={2.5} aria-hidden />}
                    Resume
                  </Button>
                  <Button size="sm" variant="danger" onClick={() => discard(s)} disabled={busyId === s.id} title="Discard parked sale">
                    <Trash2 size={14} strokeWidth={2.5} aria-hidden />
                    Discard
                  </Button>
                </div>
              </div>
            ))
          )}
        </div>
      </aside>
    </div>
  )
}
