// Shifts — open with float, close counting the drawer, variance coloring.

import { useEffect, useState } from 'react'
import { api, Shift } from '../lib/api'
import { useAuth } from '../stores/auth'
import { Button, Card, EmptyState, Field, MoneyInput, Modal, Spinner, StatusPill, Table, Tabs } from '../components/ui'
import { centsToAmount, formatMoney } from '../lib/money'
import { toast } from '../stores/toasts'
import { onWsEvent } from '../ws/client'
import { Coins } from 'lucide-react'

export function Shifts() {
  const { user } = useAuth()
  const [tab, setTab] = useState<'mine' | 'all'>('mine')
  const [current, setCurrent] = useState<Shift | null>(null)
  const [history, setHistory] = useState<Shift[] | null>(null)
  const [closing, setClosing] = useState<Shift | null>(null)
  const [opening, setOpening] = useState(false)

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

  const shown = history ?? []

  return (
    <div className="space-y-4">
      <Card
        title="Cash drawer shift"
        sub={current ? `Opened ${new Date(current.openedAt).toLocaleTimeString()}` : 'No open shift'}
        actions={
          current ? (
            <Button variant="danger" size="sm" onClick={() => setClosing(current)}>Close shift</Button>
          ) : (
            <Button variant="primary" size="sm" onClick={() => setOpening(true)}>Open shift</Button>
          )
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
          <Table head={['When', 'Who', 'Float', 'Expected', 'Counted', 'Variance', 'Status']}>
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
