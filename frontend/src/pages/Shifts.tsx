// Shifts — open with float, close counting the drawer, variance coloring,
// and the X/Z cash-session report (X = live totals for the open shift,
// Z = the frozen close-out with counted cash and signed-off variance).

import { useEffect, useState } from 'react'
import { api, Shift, ShiftReport } from '../lib/api'
import { useAuth } from '../stores/auth'
import { Button, Card, EmptyState, Field, MoneyInput, Modal, Spinner, StatusPill, Table, Tabs } from '../components/ui'
import { centsToAmount, formatMoney } from '../lib/money'
import { toast } from '../stores/toasts'
import { onWsEvent } from '../ws/client'
import { Coins, FileText } from 'lucide-react'

export function Shifts() {
  const { user } = useAuth()
  const [tab, setTab] = useState<'mine' | 'all'>('mine')
  const [current, setCurrent] = useState<Shift | null>(null)
  const [history, setHistory] = useState<Shift[] | null>(null)
  const [closing, setClosing] = useState<Shift | null>(null)
  const [opening, setOpening] = useState(false)
  const [report, setReport] = useState<{ rep: ShiftReport | null; loading: boolean } | null>(null)

  const load = async () => {
    try {
      const mine = await api.get<Shift | null>('/api/v1/shifts/current')
      setCurrent(mine)
      const all = await api.get<Shift[]>('/api/v1/shifts')
      setHistory(all)
    } catch (e: any) {
      toast.error('Load failed', e?.message)
    }
  }
  useEffect(() => {
    load()
    return onWsEvent('SHIFT_UPDATED', () => load())
  }, [])

  const openReport = async (shiftId?: number) => {
    setReport({ rep: null, loading: true })
    try {
      const q = shiftId ? `?shift=${shiftId}` : ''
      const rep = await api.get<ShiftReport>(`/api/v1/shifts/report${q}`)
      setReport({ rep, loading: false })
    } catch (e: any) {
      setReport(null)
      toast.error('Report failed', e?.message)
    }
  }

  const shown = history ?? []

  return (
    <div className="space-y-4">
      <Card
        title="Cash drawer shift"
        sub={current ? `Opened ${new Date(current.openedAt).toLocaleTimeString()}` : 'No open shift'}
        actions={
          <div className="flex items-center gap-2">
            {current && (
              <Button variant="ghost" size="sm" onClick={() => openReport()} title="X report — live totals so far">
                <FileText size={14} strokeWidth={2.5} aria-hidden /> X report
              </Button>
            )}
            {current ? (
              <Button variant="danger" size="sm" onClick={() => setClosing(current)}>Close shift</Button>
            ) : (
              <Button variant="primary" size="sm" onClick={() => setOpening(true)}>Open shift</Button>
            )}
          </div>
        }
      >
        {current ? (
          <div className="grid grid-cols-3 gap-3">
            <Stat label="Opening float" value={formatMoney(current.openingFloatCents)} />
            <Stat label="Counted at close" value="—" />
            <Stat label="Variance" value="—" />
          </div>
        ) : (
          <EmptyState
            icon={<Coins size={24} strokeWidth={2.25} />}
            title="Drawer is closed"
            body="Open a shift when you start taking cash so end-of-day reconciliation works."
          />
        )}
      </Card>

      <Card title="History" pad={false} actions={
        <Tabs
          tabs={[
            { key: 'mine' as const, label: 'Mine' },
            { key: 'all' as const, label: 'Everyone' },
          ]}
          value={tab}
          onChange={setTab}
        />
      }>
        {!history ? (
          <div className="py-12 flex justify-center"><Spinner /></div>
        ) : (
          <Table head={['When', 'Who', 'Float', 'Expected', 'Counted', 'Variance', 'Status', 'Z']}>
            {(tab === 'mine' ? shown.filter((s) => s.userId === user?.id) : shown).map((s) => (
              <tr key={s.id}>
                <td className="px-3 py-2.5 text-[13px] text-ink-muted">
                  {new Date(s.openedAt).toLocaleDateString()}
                  <span className="text-ink-subtle block text-[11px]">
                    {new Date(s.openedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
                    {s.closedAt && ` → ${new Date(s.closedAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`}
                  </span>
                </td>
                <td className="px-3 py-2.5 text-[13px] font-semibold text-ink">{s.userName}</td>
                <td className="px-3 py-2.5 tabular text-ink-muted">{centsToAmount(s.openingFloatCents)}</td>
                <td className="px-3 py-2.5 tabular text-ink-muted">{s.closedAt ? centsToAmount(s.expectedCents) : '—'}</td>
                <td className="px-3 py-2.5 tabular text-ink-muted">{s.closedAt ? centsToAmount(s.countedCents) : '—'}</td>
                <td className="px-3 py-2.5">
                  {s.closedAt ? (
                    <span className={`font-bold tabular text-[13px] ${s.varianceCents === 0 ? 'text-paid-text' : Math.abs(s.varianceCents) > 10000 ? 'text-danger-text' : 'text-pending-text'}`}>
                      {s.varianceCents > 0 ? '+' : ''}{centsToAmount(s.varianceCents)}
                    </span>
                  ) : (
                    <span className="text-ink-subtle">—</span>
                  )}
                </td>
                <td className="px-3 py-2.5">
                  {s.closedAt ? <StatusPill status="void" label="Closed" /> : <StatusPill status="pending" label="Open" />}
                </td>
                <td className="px-3 py-2.5">
                  <button
                    onClick={() => openReport(s.id)}
                    className="text-[12px] font-bold text-brand hover:underline"
                    title={s.closedAt ? 'Z report — frozen close-out' : 'X report — live totals'}
                  >
                    {s.closedAt ? 'Z' : 'X'}
                  </button>
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Card>

      {opening && (
        <OpenModal onClose={() => setOpening(false)} onDone={() => { setOpening(false); load() }} />
      )}
      {closing && (
        <CloseModal shift={closing} onClose={() => setClosing(null)} onDone={() => { setClosing(null); load() }} />
      )}
      {report && (
        <ShiftReportModal
          report={report.rep}
          loading={report.loading}
          onClose={() => setReport(null)}
        />
      )}
    </div>
  )
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="bg-surface-muted border-2 border-line rounded-input p-3 text-center">
      <p className="text-[11px] uppercase font-bold text-ink-muted">{label}</p>
      <p className="font-black text-ink tabular text-lg mt-0.5">{value}</p>
    </div>
  )
}

// ---- X/Z cash-session report (P6): sales by tender, drawer expectation,
// counted cash and variance. The title says X while the shift is open
// (nothing is final) and Z once it's closed (the signed-off close-out).
function ShiftReportModal({ report, loading, onClose }: { report: ShiftReport | null; loading: boolean; onClose: () => void }) {
  return (
    <Modal open onClose={onClose} title={report && !report.open ? 'Z report — shift close-out' : 'X report — shift so far'} size="sm">
      {loading || !report ? (
        <div className="py-10 flex justify-center"><Spinner /></div>
      ) : (
        <div className="space-y-3 text-[13px]">
          <div className="flex items-center justify-between text-ink-muted">
            <span className="font-semibold">{report.shift.userName}</span>
            <span className="text-[12px]">
              {new Date(report.shift.openedAt).toLocaleString()}
              {report.shift.closedAt && <> → {new Date(report.shift.closedAt).toLocaleTimeString()}</>}
            </span>
          </div>
          <div className="grid grid-cols-3 gap-2">
            <Stat label="Orders" value={String(report.ordersCount)} />
            <Stat label="Voids" value={String(report.voidsCount)} />
            <Stat label="Gross" value={formatMoney(report.grossCents)} />
          </div>
          <div className="border border-line rounded-input divide-y divide-line bg-surface">
            <Row2 label="Cash" value={formatMoney(report.cashCents)} />
            <Row2 label="M-Pesa" value={formatMoney(report.mpesaCents)} />
            <Row2 label="Card / Paystack" value={formatMoney(report.paystackCents)} />
            <Row2 label="Store credit" value={formatMoney(report.creditCents)} />
            {report.otherCents > 0 && <Row2 label="Other tenders" value={formatMoney(report.otherCents)} />}
          </div>
          <div className="border border-line rounded-input divide-y divide-line bg-surface-muted">
            <Row2 label="Opening float" value={formatMoney(report.shift.openingFloatCents)} />
            <Row2 label={report.open ? 'Cash expected NOW' : 'Cash expected'} value={formatMoney(report.expectedCashCents)} bold />
            {!report.open && <Row2 label="Counted" value={formatMoney(report.countedCents)} bold />}
            {!report.open && (
              <Row2
                label="Variance"
                value={`${report.varianceCents > 0 ? '+' : ''}${formatMoney(report.varianceCents)}`}
                bold
                tone={report.varianceCents === 0 ? 'paid' : Math.abs(report.varianceCents) > 10000 ? 'danger' : 'pending'}
              />
            )}
          </div>
          {report.open && (
            <p className="text-[11.5px] text-ink-subtle">
              X report — nothing is final yet. Count the drawer when you close; the Z report freezes these numbers.
            </p>
          )}
        </div>
      )}
    </Modal>
  )
}

function Row2({ label, value, bold, tone }: { label: string; value: string; bold?: boolean; tone?: 'paid' | 'danger' | 'pending' }) {
  const color = tone === 'paid' ? 'text-paid-text' : tone === 'danger' ? 'text-danger-text' : tone === 'pending' ? 'text-pending-text' : 'text-ink'
  return (
    <div className="flex justify-between px-3 py-1.5">
      <span className="text-ink-muted">{label}</span>
      <span className={`tabular ${bold ? 'font-bold' : 'font-semibold'} ${color}`}>{value}</span>
    </div>
  )
}

function OpenModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [float, setFloat] = useState(0)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const go = async () => {
    setBusy(true)
    setError('')
    try {
      await api.post('/api/v1/shifts/open', { openingFloatCents: float })
      toast.success('Shift opened', `Float ${formatMoney(float)}`)
      onDone()
    } catch (e: any) {
      setError(e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Open shift" size="sm" footer={
      <>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={go} disabled={busy}>
          {busy ? <Spinner className="border-t-brand-ink" /> : 'Open with this float'}
        </Button>
      </>
    }>
      <Field label="Opening float (cash in drawer)" hint="Count the drawer before you start.">
        <MoneyInput value={float} onCents={setFloat} autoFocus />
      </Field>
      {error && <p role="alert" className="text-danger-text text-sm font-semibold mt-2">{error}</p>}
    </Modal>
  )
}

function CloseModal({ shift, onClose, onDone }: { shift: Shift; onClose: () => void; onDone: () => void }) {
  const [counted, setCounted] = useState(0)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const go = async () => {
    setBusy(true)
    setError('')
    try {
      const closed = await api.post<Shift>('/api/v1/shifts/close', { countedCents: counted })
      const v = closed.varianceCents
      toast.success('Shift closed', v === 0 ? 'Perfect count — zero variance' : `Variance ${v > 0 ? '+' : ''}${centsToAmount(v)}`)
      onDone()
    } catch (e: any) {
      setError(e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Close shift" size="sm" footer={
      <>
        <Button variant="ghost" onClick={onClose}>Keep open</Button>
        <Button variant="primary" onClick={go} disabled={busy}>
          {busy ? <Spinner className="border-t-brand-ink" /> : 'Close shift'}
        </Button>
      </>
    }>
      <Field label="Counted cash in drawer" hint={`Opened with ${formatMoney(shift.openingFloatCents)}. Expected is computed from cash sales.`}>
        <MoneyInput value={counted} onCents={setCounted} autoFocus />
      </Field>
      {error && <p role="alert" className="text-danger-text text-sm font-semibold mt-2">{error}</p>}
    </Modal>
  )
}
