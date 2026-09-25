// StockCountPanel — physical stock counts ("stocktake"). Open a session
// (snapshots every stock-tracked product), type what is on the shelf, then
// close with a report only or apply the counted quantities as the new
// system stock. Variance (units + value at cost) is colored red/green.
//
// Gated like the Go route group: suppliers.manage for everything (viewing,
// opening, counting, closing) — the same permission the Suppliers page
// uses for stock management. Edits are only possible while status=OPEN.

import { useEffect, useState } from 'react'
import { api, StockCount, StockCountLine } from '../lib/api'
import { Button, Card, EmptyState, Field, Input, Modal, Spinner, StatusPill, Table, Textarea } from './ui'
import { centsToAmount, formatMoney } from '../lib/money'
import { toast } from '../stores/toasts'
import { ClipboardList, ClipboardCheck, Plus } from 'lucide-react'

export function StockCountPanel({ onChanged }: { onChanged?: () => void }) {
  const [counts, setCounts] = useState<StockCount[] | null>(null)
  const [newOpen, setNewOpen] = useState(false)
  const [openId, setOpenId] = useState<number | null>(null)

  const load = async () => {
    try {
      const r = await api.get<{ counts: StockCount[] }>('/api/v1/stock-counts?limit=50')
      setCounts(Array.isArray(r) ? (r as unknown as StockCount[]) : r?.counts ?? [])
    } catch (e: any) {
      toast.error('Could not load stock counts', e?.message)
      setCounts([])
    }
  }
  useEffect(() => { load() }, [])

  return (
    <>
      <Card
        title="Stock counts"
        sub="Count what is on the shelf, then close with a report — or write the counted stock back"
        actions={<Button variant="primary" size="sm" onClick={() => setNewOpen(true)}><Plus size={14} strokeWidth={2.5} aria-hidden />New count</Button>}
        pad={false}
      >
        {!counts ? (
          <div className="py-12 flex justify-center"><Spinner /></div>
        ) : counts.length === 0 ? (
          <EmptyState
            icon={<ClipboardList size={24} strokeWidth={2.25} />}
            title="No stock counts yet"
            body="Open a count session, walk the shelves, and record what you find."
            action={<Button variant="primary" size="sm" onClick={() => setNewOpen(true)}>Start a count</Button>}
          />
        ) : (
          <Table head={['Count', 'Status', 'Opened', 'Closed', 'Counted', 'Variance', 'By', '']}>
            {counts.map((c) => {
              const surplus = c.varianceUnits > 0
              const shrink = c.varianceUnits < 0
              const closed = c.status !== 'OPEN'
              return (
                <tr key={c.id}>
                  <td className="px-3 py-2.5">
                    <p className="font-bold text-ink text-[13px] tabular">{c.number}</p>
                    {c.note && <p className="text-[11px] text-ink-subtle max-w-44 truncate" title={c.note}>{c.note}</p>}
                  </td>
                  <td className="px-3 py-2.5">
                    <StatusPill
                      status={c.status === 'OPEN' ? 'progress' : c.status === 'DONE' ? 'paid' : 'void'}
                      label={c.status === 'OPEN' ? 'Open' : c.status === 'DONE' ? 'Closed' : 'Cancelled'}
                    />
                  </td>
                  <td className="px-3 py-2.5 text-[12px] text-ink-muted whitespace-nowrap">{c.openedAt ? new Date(c.openedAt).toLocaleString() : '—'}</td>
                  <td className="px-3 py-2.5 text-[12px] text-ink-muted whitespace-nowrap">{c.closedAt ? new Date(c.closedAt).toLocaleString() : '—'}</td>
                  <td className="px-3 py-2.5 tabular text-[13px] font-semibold text-ink">{c.linesCounted}/{c.linesTotal}</td>
                  <td className={`px-3 py-2.5 text-[13px] font-bold tabular ${!closed ? 'text-ink-subtle' : shrink ? 'text-danger-text' : surplus ? 'text-paid-text' : 'text-ink-muted'}`}>
                    {closed
                      ? `${c.varianceUnits > 0 ? '+' : ''}${c.varianceUnits} · ${c.varianceValueCents > 0 ? '+' : ''}${centsToAmount(c.varianceValueCents)}`
                      : '—'}
                  </td>
                  <td className="px-3 py-2.5 text-[12px] text-ink-muted">{c.countedByName || '—'}</td>
                  <td className="px-3 py-2.5 text-right whitespace-nowrap">
                    <Button size="sm" variant="ghost" onClick={() => setOpenId(c.id)}>{closed ? 'View' : 'Count'}</Button>
                  </td>
                </tr>
              )
            })}
          </Table>
        )}
      </Card>

      <NewCountModal
        open={newOpen}
        onClose={() => setNewOpen(false)}
        onCreated={(c) => {
          setNewOpen(false)
          setOpenId(c.id)
          load()
        }}
      />

      {openId !== null && (
        <CountDetail
          id={openId}
          onClose={() => setOpenId(null)}
          onChanged={() => { load(); onChanged?.() }}
        />
      )}
    </>
  )
}

function NewCountModal({ open, onClose, onCreated }: { open: boolean; onClose: () => void; onCreated: (c: StockCount) => void }) {
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    if (open) { setNote(''); setError('') }
  }, [open])

  const create = async () => {
    setBusy(true)
    setError('')
    try {
      const c = await api.post<StockCount>('/api/v1/stock-counts', { note: note.trim() })
      toast.success('Count opened', `${c.number} — every tracked product is on the sheet`)
      onCreated(c)
    } catch (e: any) {
      setError(e?.message || 'Could not open the count')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title="New stock count" size="sm" footer={
      <>
        <Button variant="ghost" onClick={onClose} disabled={busy}>Cancel</Button>
        <Button variant="primary" onClick={create} disabled={busy}>
          {busy ? <Spinner className="border-t-brand-ink" /> : 'Open count'}
        </Button>
      </>
    }>
      <div className="space-y-3">
        <p className="text-[13px] text-ink-muted">
          Snapshots every stock-tracked product (gift cards excluded — they never track stock). Count at your own pace;
          sales keep working while the count is open.
        </p>
        <Field label="Note (optional)" hint="e.g. “Month-end shelf count — back room”">
          <Textarea value={note} onChange={(e) => setNote(e.target.value)} autoFocus />
        </Field>
        {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
      </div>
    </Modal>
  )
}

function CountDetail({ id, onClose, onChanged }: { id: number; onClose: () => void; onChanged: () => void }) {
  const [count, setCount] = useState<StockCount | null>(null)
  const [lines, setLines] = useState<StockCountLine[] | null>(null)
  const [drafts, setDrafts] = useState<Record<number, string>>({})
  const [saving, setSaving] = useState<number | null>(null)
  const [confirmApply, setConfirmApply] = useState(false)
  const [closing, setClosing] = useState(false)

  const load = async () => {
    try {
      const r = await api.get<{ count: StockCount; lines: StockCountLine[] }>(`/api/v1/stock-counts/${id}`)
      setCount(r.count)
      setLines(r.lines ?? [])
      setDrafts((prev) => {
        const next: Record<number, string> = { ...prev }
        for (const l of r.lines ?? []) {
          if (next[l.productId] === undefined) next[l.productId] = l.countedQty === null ? '' : String(l.countedQty)
        }
        return next
      })
    } catch (e: any) {
      toast.error('Could not load the count', e?.message)
      onClose()
    }
  }
  useEffect(() => { load() /* eslint-disable-line react-hooks/exhaustive-deps */ }, [id])

  const saveLine = async (line: StockCountLine) => {
    if (!count || count.status !== 'OPEN') return
    const raw = (drafts[line.productId] ?? '').trim()
    const digits = raw.replace(/[^\d]/g, '')
    const value: number | null = raw === '' || digits === '' ? null : Math.min(parseInt(digits, 10), 1_000_000)
    const current = line.countedQty
    if (value === current) return
    setSaving(line.productId)
    try {
      await api.put(`/api/v1/stock-counts/${id}/lines`, { productId: line.productId, countedQty: value })
      await load()
    } catch (e: any) {
      toast.error('Could not save the count', e?.message)
      await load()
    } finally {
      setSaving(null)
    }
  }

  const complete = async (apply: boolean) => {
    if (!count) return
    setClosing(true)
    try {
      const done = await api.post<StockCount>(`/api/v1/stock-counts/${id}/complete`, { apply })
      setCount(done)
      setConfirmApply(false)
      toast.success(
        apply ? 'Counted stock applied' : 'Count closed',
        `${done.number} — variance ${done.varianceUnits > 0 ? '+' : ''}${done.varianceUnits} units · ${formatMoney(done.varianceValueCents)}`,
      )
      onChanged()
      await load()
    } catch (e: any) {
      toast.error('Could not close the count', e?.message)
    } finally {
      setClosing(false)
    }
  }

  if (!count || !lines) {
    return (
      <Modal open onClose={onClose} title="Stock count" size="xl">
        <div className="py-10 flex justify-center"><Spinner className="w-7 h-7 border-4" /></div>
      </Modal>
    )
  }

  const open = count.status === 'OPEN'
  const pct = count.linesTotal > 0 ? Math.round((count.linesCounted / count.linesTotal) * 100) : 0
  const shrink = count.varianceUnits < 0
  const surplus = count.varianceUnits > 0

  return (
    <Modal
      open
      onClose={onClose}
      title={`Count ${count.number}${count.note ? ` — ${count.note}` : ''}`}
      size="xl"
      footer={
        open ? (
          <>
            <Button variant="ghost" onClick={onClose}>Keep counting later</Button>
            <Button variant="secondary" onClick={() => complete(false)} disabled={closing}>
              {closing ? <Spinner className="border-t-brand-ink" /> : <ClipboardList size={15} strokeWidth={2.5} aria-hidden />}
              Close report only
            </Button>
            <Button variant="primary" onClick={() => setConfirmApply(true)} disabled={closing}>
              <ClipboardCheck size={15} strokeWidth={2.5} aria-hidden />
              Apply counted stock
            </Button>
          </>
        ) : (
          <Button variant="primary" onClick={onClose}>Close</Button>
        )
      }
    >
      <div className="space-y-3">
        <div className="flex flex-wrap items-center gap-2">
          <StatusPill status={open ? 'progress' : count.status === 'DONE' ? 'paid' : 'void'} label={open ? 'Open' : count.status === 'DONE' ? 'Closed' : 'Cancelled'} />
          <span className="text-[12px] text-ink-muted">
            Opened {count.openedAt ? new Date(count.openedAt).toLocaleString() : '—'} by {count.countedByName || '—'}
            {count.closedAt ? ` · closed ${new Date(count.closedAt).toLocaleString()}` : ''}
          </span>
        </div>

        {/* Progress: counted / total */}
        <div>
          <div className="flex justify-between text-[12px] font-bold text-ink-muted mb-1">
            <span>{count.linesCounted} of {count.linesTotal} lines counted</span>
            <span className="tabular">{pct}%</span>
          </div>
          <div className="h-3 bg-surface-muted rounded-pill overflow-hidden border-2 border-line" aria-hidden>
            <div className="h-full bg-paid-text transition-[width] duration-300" style={{ width: `${pct}%` }} />
          </div>
        </div>

        {!open && count.status === 'DONE' && (
          <div className={`rounded-input border-2 p-3 ${shrink ? 'bg-danger-bg border-danger-text/30' : surplus ? 'bg-paid-bg border-paid-text/30' : 'bg-surface-muted border-line'}`}>
            <p className="text-[13px] font-bold text-ink">Variance summary</p>
            <p className={`text-sm font-bold tabular mt-0.5 ${shrink ? 'text-danger-text' : surplus ? 'text-paid-text' : 'text-ink-muted'}`}>
              {count.varianceUnits > 0 ? '+' : ''}{count.varianceUnits} units · {count.varianceValueCents > 0 ? '+' : ''}{formatMoney(count.varianceValueCents)} at cost
            </p>
            <p className="text-[12px] text-ink-muted mt-0.5">
              Negative = shrinkage (counted less than the system believed). Report only — system stock was left untouched.
            </p>
          </div>
        )}

        <Table head={['SKU', 'Product', 'Expected', 'Counted', 'Difference']}>
          {lines.map((l) => {
            const raw = (drafts[l.productId] ?? '').trim()
            const n = raw === '' ? null : parseInt(raw.replace(/[^\d]/g, ''), 10)
            const diff = open && n !== null && !isNaN(n) ? n - l.systemQty : l.countedQty !== null ? l.countedQty - l.systemQty : null
            return (
              <tr key={l.id}>
                <td className="px-3 py-2 font-mono text-[12px] text-ink-muted">{l.sku || '—'}</td>
                <td className="px-3 py-2 font-semibold text-ink text-[13px]">{l.name}</td>
                <td className="px-3 py-2 tabular text-[13px] text-ink-muted">{l.expectedQty}</td>
                <td className="px-3 py-2">
                  <Input
                    value={drafts[l.productId] ?? ''}
                    onChange={(e) => setDrafts((d) => ({ ...d, [l.productId]: e.target.value.replace(/[^\d]/g, '') }))}
                    onBlur={() => saveLine(l)}
                    onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); (e.target as HTMLInputElement).blur() } }}
                    disabled={!open}
                    inputMode="numeric"
                    placeholder="—"
                    aria-label={`Counted quantity for ${l.name}`}
                    className={`w-24 text-center tabular font-bold ${open ? '' : 'opacity-70'}`}
                  />
                  {saving === l.productId && <span className="ml-2 inline-block align-middle"><Spinner className="w-4 h-4" /></span>}
                </td>
                <td className={`px-3 py-2 tabular text-[13px] font-bold ${diff === null ? 'text-ink-subtle' : diff > 0 ? 'text-paid-text' : diff < 0 ? 'text-danger-text' : 'text-ink-muted'}`}>
                  {diff === null ? '—' : `${diff > 0 ? '+' : ''}${diff}`}
                </td>
              </tr>
            )
          })}
        </Table>
        {open && <p className="text-[11px] text-ink-subtle">Type the physical quantity — it saves when you leave the field or press Enter. Clear a field to un-count that line.</p>}
        {!open && <p className="text-[11px] text-ink-subtle">This count is closed — the sheet is read-only.</p>}
      </div>

      {confirmApply && (
        <Modal open onClose={() => setConfirmApply(false)} title="Apply counted stock?" size="sm" footer={
          <>
            <Button variant="ghost" onClick={() => setConfirmApply(false)} disabled={closing}>Cancel</Button>
            <Button variant="danger" onClick={() => complete(true)} disabled={closing}>
              {closing ? <Spinner className="border-t-white" /> : 'Apply to system stock'}
            </Button>
          </>
        }>
          <div className="space-y-3">
            <p className="text-sm text-ink-muted">
              This <strong className="text-ink">overwrites system stock</strong> with the counted quantities for every
              counted line. Lines you did not count are left alone. This cannot be undone automatically — prefer
              “Close report only” if you just need the paperwork.
            </p>
          </div>
        </Modal>
      )}
    </Modal>
  )
}
