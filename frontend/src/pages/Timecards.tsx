// Timecards (v1.12) — everyone clocks themselves in and out; managers
// (timecards.manage) see everyone's hours, fix missed punches (with a
// reason that is kept) and export the payroll sheet.

import { useEffect, useState } from 'react'
import { api, downloadFile } from '../lib/api'
import { useAuth } from '../stores/auth'
import { Button, Card, EmptyState, Field, Input, Modal, MoneyInput, Select, Spinner, StatusPill, Table, Tabs } from '../components/ui'
import { formatMoney } from '../lib/money'
import { todayLocal } from '../lib/katime'
import { toast } from '../stores/toasts'
import { Clock, Coffee, LogIn, LogOut, Plus, Download, Pencil, Ban, Play } from 'lucide-react'
import { TimecardReport, TimeEntry, fromLocalInput, hm, toLocalInput } from '../lib/v112'

const weekAgo = () => {
  const d = new Date(todayLocal() + 'T00:00:00+03:00')
  d.setDate(d.getDate() - 6)
  return new Date(d.getTime() + 3 * 3600_000).toISOString().slice(0, 10)
}

const when = (s: string) => {
  if (!s) return '—'
  const d = new Date(s)
  return isNaN(d.getTime()) ? s : d.toLocaleString('en-KE', { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', timeZone: 'Africa/Nairobi' })
}

export function Timecards() {
  const canManage = useAuth((s) => !!s.user?.permissions.includes('timecards.manage'))
  const [tab, setTab] = useState<'me' | 'team'>('me')
  return (
    <div className="space-y-4">
      {canManage && (
        <Tabs
          tabs={[{ key: 'me', label: 'My time', icon: <Clock size={15} aria-hidden /> }, { key: 'team', label: 'Team timecards' }]}
          value={tab}
          onChange={setTab}
        />
      )}
      {tab === 'me' || !canManage ? <MyClock /> : <TeamTimecards />}
    </div>
  )
}

function MyClock() {
  const [open, setOpen] = useState<TimeEntry | null>(null)
  const [rep, setRep] = useState<TimecardReport | null>(null)
  const [busy, setBusy] = useState(false)
  const load = async () => {
    try {
      const r = await api.get<{ open: TimeEntry | null; report: TimecardReport }>(`/api/v1/timeclock/me?from=${weekAgo()}&to=${todayLocal()}`)
      setOpen(r.open)
      setRep(r.report)
    } catch (e: any) {
      toast.error('Could not load your time', e?.message)
    }
  }
  useEffect(() => { load() }, [])
  // live counter while clocked in
  const [, tick] = useState(0)
  useEffect(() => {
    if (!open) return
    const t = setInterval(() => tick((n) => n + 1), 30_000)
    return () => clearInterval(t)
  }, [open])

  const punch = async (action: 'in' | 'out' | 'break' | 'resume') => {
    setBusy(true)
    try {
      await api.post('/api/v1/timeclock/me', { action })
      toast.success({ in: 'Clocked in', out: 'Clocked out', break: 'On a break', resume: 'Back from break' }[action])
      await load()
    } catch (e: any) {
      toast.error('Could not punch', e?.message)
    } finally {
      setBusy(false)
    }
  }
  const me = rep?.people[0]
  const liveSeconds = open
    ? Math.max(0, (Date.now() - new Date(open.clockIn).getTime()) / 1000 - open.breakSeconds -
      (open.onBreak ? (Date.now() - new Date(open.breakStarted).getTime()) / 1000 : 0))
    : 0

  return (
    <>
      <Card title="Time clock" sub={open ? (open.onBreak ? 'On a break' : `Clocked in since ${when(open.clockIn)}`) : 'You are clocked out'}>
        <div className="flex flex-wrap items-center gap-3">
          <div className="text-4xl font-bold tabular text-ink mr-4" aria-live="polite">{open ? hm(liveSeconds) : '0:00'}</div>
          {!open && (
            <Button variant="primary" size="lg" disabled={busy} onClick={() => punch('in')}><LogIn size={18} aria-hidden />Clock in</Button>
          )}
          {open && !open.onBreak && (
            <Button size="lg" disabled={busy} onClick={() => punch('break')}><Coffee size={18} aria-hidden />Start break</Button>
          )}
          {open?.onBreak && (
            <Button size="lg" variant="primary" disabled={busy} onClick={() => punch('resume')}><Play size={18} aria-hidden />End break</Button>
          )}
          {open && (
            <Button size="lg" variant="danger" disabled={busy} onClick={() => punch('out')}><LogOut size={18} aria-hidden />Clock out</Button>
          )}
        </div>
        <p className="text-xs text-ink-subtle mt-3">You can also clock in or out on the PIN screen without signing in.</p>
      </Card>
      <Card title="Last 7 days" sub={me ? `${hm(me.workedSeconds)} hours over ${me.daysWorked} day(s)` : 'No time recorded yet'} pad={false}>
        {!rep ? <div className="py-10 flex justify-center"><Spinner /></div> : rep.entries.length === 0 ? (
          <EmptyState icon={<Clock size={24} />} title="No time yet" body="Clock in when you start work." />
        ) : (
          <Table head={['In', 'Out', 'Break', 'Hours', '']}>
            {rep.entries.map((e) => (
              <tr key={e.id}>
                <td className="px-3 py-2.5 text-[13px] tabular">{when(e.clockIn)}</td>
                <td className="px-3 py-2.5 text-[13px] tabular">{e.open ? <StatusPill status="progress" label="Clocked in" /> : when(e.clockOut)}</td>
                <td className="px-3 py-2.5 text-[13px] tabular">{Math.round(e.breakSeconds / 60)} min</td>
                <td className="px-3 py-2.5 font-bold tabular">{hm(e.workedSeconds)}</td>
                <td className="px-3 py-2.5 text-[12px] text-ink-subtle">{e.editedBy ? `edited by ${e.editedBy}: ${e.editReason}` : ''}</td>
              </tr>
            ))}
          </Table>
        )}
      </Card>
    </>
  )
}

function TeamTimecards() {
  const [from, setFrom] = useState(weekAgo())
  const [to, setTo] = useState(todayLocal())
  const [rep, setRep] = useState<TimecardReport | null>(null)
  const [editing, setEditing] = useState<TimeEntry | 'new' | null>(null)
  const [rateFor, setRateFor] = useState<{ userId: number; name: string; cents: number } | null>(null)
  const load = async () => {
    try {
      setRep(await api.get<TimecardReport>(`/api/v1/timecards?from=${from}&to=${to}`))
    } catch (e: any) {
      toast.error('Could not load timecards', e?.message)
    }
  }
  useEffect(() => { load() }, [from, to])

  const voidEntry = async (e: TimeEntry) => {
    const reason = window.prompt(`Void ${e.fullName}'s entry of ${when(e.clockIn)}? Say why:`)
    if (!reason?.trim()) return
    try {
      await api.post(`/api/v1/timecards/${e.id}/void`, { reason })
      toast.success('Entry voided')
      load()
    } catch (err: any) {
      toast.error('Could not void', err?.message)
    }
  }

  return (
    <>
      <Card
        title="Team timecards"
        sub={rep ? `${hm(rep.totalSeconds)} hours · pay ${formatMoney(rep.totalPayCents)} · overtime after ${rep.weeklyHours} h a week at ${rep.overtimeRate}x` : 'Loading…'}
        actions={
          <div className="flex flex-wrap gap-2 items-center">
            <div className="w-36"><Input type="date" value={from} onChange={(e) => setFrom(e.target.value)} aria-label="From" /></div>
            <div className="w-36"><Input type="date" value={to} onChange={(e) => setTo(e.target.value)} aria-label="To" /></div>
            <Button onClick={() => downloadFile(`/api/v1/timecards.csv?from=${from}&to=${to}`, `timecards-${from}-${to}.csv`).catch((e) => toast.error('Download failed', e?.message))}>
              <Download size={15} aria-hidden />Payroll CSV
            </Button>
            <Button variant="primary" onClick={() => setEditing('new')}><Plus size={15} aria-hidden />Add missed time</Button>
          </div>
        }
        pad={false}
      >
        {!rep ? <div className="py-10 flex justify-center"><Spinner /></div> : rep.people.length === 0 ? (
          <EmptyState icon={<Clock size={24} />} title="No time recorded" body="Staff clock in from the PIN screen or the Time page." />
        ) : (
          <Table head={['Person', 'Days', 'Hours', 'Normal', 'Overtime', 'Rate / hour', 'Pay', '']}>
            {rep.people.map((p) => (
              <tr key={p.username}>
                <td className="px-3 py-2.5 font-semibold">{p.fullName}{p.openEntries > 0 && <span className="ml-2"><StatusPill status="progress" label="In" /></span>}</td>
                <td className="px-3 py-2.5 tabular">{p.daysWorked}</td>
                <td className="px-3 py-2.5 font-bold tabular">{hm(p.workedSeconds)}</td>
                <td className="px-3 py-2.5 tabular">{hm(p.normalSeconds)}</td>
                <td className="px-3 py-2.5 tabular">{p.overtimeSeconds > 0 ? hm(p.overtimeSeconds) : '—'}</td>
                <td className="px-3 py-2.5 tabular">{p.hourlyRateCents ? formatMoney(p.hourlyRateCents) : 'not set'}</td>
                <td className="px-3 py-2.5 font-bold tabular">{p.hourlyRateCents ? formatMoney(p.payCents) : '—'}</td>
                <td className="px-3 py-2.5">
                  {p.userId > 0 && (
                    <Button size="sm" variant="ghost" onClick={() => setRateFor({ userId: p.userId, name: p.fullName, cents: p.hourlyRateCents })}>Set rate</Button>
                  )}
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Card>
      {rep && rep.entries.length > 0 && (
        <Card title="Entries" sub="Every change keeps who made it and why." pad={false}>
          <Table head={['Person', 'In', 'Out', 'Break', 'Hours', 'Note', '']}>
            {rep.entries.map((e) => (
              <tr key={e.id} className={e.status === 'void' ? 'opacity-50 line-through' : ''}>
                <td className="px-3 py-2.5 font-semibold text-[13px]">{e.fullName}</td>
                <td className="px-3 py-2.5 text-[13px] tabular">{when(e.clockIn)}</td>
                <td className="px-3 py-2.5 text-[13px] tabular">{e.open ? <StatusPill status="progress" label="Clocked in" /> : when(e.clockOut)}</td>
                <td className="px-3 py-2.5 text-[13px] tabular">{Math.round(e.breakSeconds / 60)} min</td>
                <td className="px-3 py-2.5 font-bold tabular">{hm(e.workedSeconds)}</td>
                <td className="px-3 py-2.5 text-[12px] text-ink-muted max-w-64">
                  {e.flag && <span className="block text-danger-text font-semibold">{e.flag}</span>}
                  {e.note}{e.editedBy && <span className="block text-ink-subtle">edited by {e.editedBy}: {e.editReason}</span>}
                </td>
                <td className="px-3 py-2.5 whitespace-nowrap">
                  {e.status === 'active' && (
                    <>
                      <Button size="sm" variant="ghost" onClick={() => setEditing(e)} aria-label="Edit"><Pencil size={14} aria-hidden /></Button>
                      <Button size="sm" variant="ghost" onClick={() => voidEntry(e)} aria-label="Void"><Ban size={14} aria-hidden /></Button>
                    </>
                  )}
                </td>
              </tr>
            ))}
          </Table>
        </Card>
      )}
      {editing && <EntryModal entry={editing === 'new' ? null : editing} onClose={() => setEditing(null)} onDone={() => { setEditing(null); load() }} />}
      {rateFor && <RateModal {...rateFor} onClose={() => setRateFor(null)} onDone={() => { setRateFor(null); load() }} />}
    </>
  )
}

function EntryModal({ entry, onClose, onDone }: { entry: TimeEntry | null; onClose: () => void; onDone: () => void }) {
  const [users, setUsers] = useState<{ id: number; fullName: string; username: string }[]>([])
  const [userId, setUserId] = useState(entry?.userId ?? 0)
  const [clockIn, setClockIn] = useState(toLocalInput(entry?.clockIn ?? ''))
  const [clockOut, setClockOut] = useState(toLocalInput(entry?.clockOut ?? ''))
  const [breakMinutes, setBreak] = useState(Math.round((entry?.breakSeconds ?? 0) / 60))
  const [note, setNote] = useState(entry?.note ?? '')
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => {
    if (entry) return
    type U = { id: number; fullName: string; username: string }
    // the staff list needs users.manage; the PIN-pad list is open to all
    api.get<U[]>('/api/v1/users')
      .catch(() => api.get<U[]>('/api/v1/auth/pin-users'))
      .then((u) => {
        setUsers(u)
        if (u[0]) setUserId(u[0].id)
      })
      .catch(() => setUsers([]))
  }, [entry])
  const save = async () => {
    setBusy(true)
    setError('')
    const body = { clockIn: fromLocalInput(clockIn), clockOut: fromLocalInput(clockOut), breakMinutes, note, reason }
    try {
      if (entry) await api.put(`/api/v1/timecards/${entry.id}`, body)
      else await api.post('/api/v1/timecards', { ...body, userId })
      toast.success('Saved')
      onDone()
    } catch (e: any) {
      setError(e?.message || 'Could not save')
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal open onClose={onClose} title={entry ? `Correct ${entry.fullName}'s time` : 'Add missed time'}
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" disabled={busy || !clockIn || !reason.trim()} onClick={save}>Save</Button></>}>
      <div className="space-y-3">
        {!entry && (
          <Field label="Person">
            <Select value={userId} onChange={(e) => setUserId(Number(e.target.value))}>
              {users.map((u) => <option key={u.id} value={u.id}>{u.fullName || u.username}</option>)}
            </Select>
          </Field>
        )}
        <div className="grid grid-cols-2 gap-3">
          <Field label="Clock in"><Input type="datetime-local" value={clockIn} onChange={(e) => setClockIn(e.target.value)} /></Field>
          <Field label="Clock out" hint="Leave empty if still at work"><Input type="datetime-local" value={clockOut} onChange={(e) => setClockOut(e.target.value)} /></Field>
        </div>
        <Field label="Break (minutes)"><Input type="number" min={0} value={breakMinutes} onChange={(e) => setBreak(Math.max(0, Number(e.target.value) || 0))} /></Field>
        <Field label="Note"><Input value={note} onChange={(e) => setNote(e.target.value)} maxLength={200} /></Field>
        <Field label="Reason for the change" hint="Kept with the entry and in the audit log"><Input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={200} /></Field>
        {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
      </div>
    </Modal>
  )
}

function RateModal({ userId, name, cents, onClose, onDone }: { userId: number; name: string; cents: number; onClose: () => void; onDone: () => void }) {
  const [value, setValue] = useState(cents)
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    try {
      await api.put(`/api/v1/timecard-rates/${userId}`, { hourlyRateCents: value })
      toast.success('Rate saved')
      onDone()
    } catch (e: any) {
      toast.error('Could not save', e?.message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal open onClose={onClose} title={`Hourly rate for ${name}`} size="sm"
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" disabled={busy} onClick={save}>Save</Button></>}>
      <Field label="KES per hour" hint="Used to estimate pay on the timecard. 0 = not set.">
        <MoneyInput value={value} onCents={setValue} />
      </Field>
    </Modal>
  )
}
