// Expenses — money out of the till: rent, power, airtime, salaries…
// (v1.6.3.) Every booked expense feeds the reports' "money out" figures,
// so the day's true cash position reads next to the takings. Amounts are
// integer cents on the wire — the KES input parses through money.ts.
//
// The route is gated by 'expenses.view' (App.tsx); recording needs
// 'expenses.manage', which is why the button is permission-conditional.

import { useEffect, useState } from 'react'
import { api, Expense, ExpenseType } from '../lib/api'
import { useAuth } from '../stores/auth'
import { Button, Card, EmptyState, Field, Input, Modal, MoneyInput, Select, Spinner, Table, Textarea } from '../components/ui'
import { centsToAmount, formatMoney } from '../lib/money'
import { todayLocal } from '../lib/katime'
import { toast } from '../stores/toasts'
import { Wallet, Plus, Banknote, Smartphone, Landmark } from 'lucide-react'
import { fmtDateTime } from '../lib/when'

function MethodLabel({ method }: { method: string }) {
  return method === 'MPESA' ? (
    <span className="inline-flex items-center gap-1.5"><Smartphone size={14} strokeWidth={2.25} aria-hidden />M-Pesa</span>
  ) : method === 'BANK' ? (
    <span className="inline-flex items-center gap-1.5"><Landmark size={14} strokeWidth={2.25} aria-hidden />Bank</span>
  ) : (
    <span className="inline-flex items-center gap-1.5"><Banknote size={14} strokeWidth={2.25} aria-hidden />Cash</span>
  )
}

export function Expenses() {
  const canManage = useAuth((s) => !!s.user?.permissions.includes('expenses.manage'))
  const [rows, setRows] = useState<Expense[] | null>(null)
  const [types, setTypes] = useState<ExpenseType[]>([])
  const [date, setDate] = useState('')
  const [recording, setRecording] = useState(false)

  const load = async () => {
    try {
      setRows(await api.get<Expense[]>(`/api/v1/expenses${date ? `?date=${encodeURIComponent(date)}` : ''}`))
    } catch (e: any) {
      toast.error('Load failed', e?.message)
    }
  }
  useEffect(() => { load() }, [date])

  useEffect(() => {
    api.get<ExpenseType[]>('/api/v1/expenses/types')
      .then(setTypes)
      .catch(() => setTypes([]))
  }, [])

  // Summary line: total for whatever range the filter shows.
  const totalCents = (rows ?? []).reduce((s, r) => s + r.amountCents, 0)

  return (
    <div className="space-y-4">
      <Card
        title="Expenses"
        sub={rows
          ? `${rows.length} shown · total ${formatMoney(totalCents)}${date ? ` on ${date}` : ' across recent entries'}`
          : 'Loading…'}
        actions={
          <div className="flex gap-2 items-center">
            <div className="w-40"><Input type="date" value={date} onChange={(e) => setDate(e.target.value)} aria-label="Filter by date" /></div>
            {date && (
              <Button size="md" variant="ghost" onClick={() => setDate('')} title="Show all recent expenses">All recent</Button>
            )}
            {canManage && (
              <Button size="md" variant="primary" onClick={() => setRecording(true)}>
                <Plus size={15} strokeWidth={2.5} aria-hidden />
                Record expense
              </Button>
            )}
          </div>
        }
        pad={false}
      >
        {!rows ? (
          <div className="py-12 flex justify-center"><Spinner /></div>
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<Wallet size={24} strokeWidth={2.25} />}
            title="No expenses"
            body={date
              ? `Nothing booked on ${date} — clear the date filter to see recent entries.`
              : 'Rent, power, airtime, salaries — book them here so the reports show true profit.'}
          />
        ) : (
          <Table head={['Number', 'Date', 'Type', 'Amount', 'Method', 'Reference', 'Note', 'By', 'Created']}>
            {rows.map((e) => (
              <tr key={e.id}>
                <td className="px-3 py-2.5 font-bold text-ink text-[13px] tabular">{e.number}</td>
                <td className="px-3 py-2.5 text-[13px] text-ink-muted tabular">{e.spentOn}</td>
                <td className="px-3 py-2.5 font-semibold text-ink text-[13px]">{e.typeName}</td>
                <td className="px-3 py-2.5 font-bold tabular text-ink">{centsToAmount(e.amountCents)}</td>
                <td className="px-3 py-2.5 text-[13px] text-ink-muted"><MethodLabel method={e.method} /></td>
                <td className="px-3 py-2.5 text-[13px] text-ink-muted">{e.reference || '—'}</td>
                <td className="px-3 py-2.5 text-[13px] text-ink-muted max-w-52 truncate" title={e.note}>{e.note || '—'}</td>
                <td className="px-3 py-2.5 text-[13px] text-ink-muted">{e.createdByName}</td>
                <td className="px-3 py-2.5 text-[11px] text-ink-subtle">{fmtDateTime(e.createdAt)}</td>
              </tr>
            ))}
          </Table>
        )}
      </Card>

      {recording && (
        <ExpenseModal
          types={types.filter((t) => t.isActive)}
          onClose={() => setRecording(false)}
          onDone={() => { setRecording(false); load() }}
        />
      )}
    </div>
  )
}

// ExpenseModal (expenses.manage): pick a type, key the KES amount
// (MoneyInput keeps integer cents in state), say how it was paid and when.
function ExpenseModal({ types, onClose, onDone }: { types: ExpenseType[]; onClose: () => void; onDone: () => void }) {
  const [expenseTypeId, setExpenseTypeId] = useState(types[0]?.id ?? 0)
  const [amountCents, setAmountCents] = useState(0)
  const [method, setMethod] = useState<'CASH' | 'MPESA' | 'BANK'>('CASH')
  const [reference, setReference] = useState('')
  const [note, setNote] = useState('')
  const [spentOn, setSpentOn] = useState(todayLocal())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const valid = types.length > 0 && expenseTypeId > 0 && amountCents > 0 && /^\d{4}-\d{2}-\d{2}$/.test(spentOn)

  const go = async () => {
    setBusy(true)
    setError('')
    try {
      const r = await api.post<Expense>('/api/v1/expenses', {
        expenseTypeId,
        amountCents,
        method,
        reference: reference.trim(),
        note: note.trim(),
        spentOn,
      })
      toast.success('Expense recorded', `${r.number}, ${formatMoney(r.amountCents)} paid ${r.method === 'MPESA' ? 'via M-Pesa' : r.method === 'BANK' ? 'from the bank' : 'in cash'}`)
      onDone()
    } catch (e: any) {
      setError(e?.message || 'Could not record the expense')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="Record expense" size="sm" footer={
      <>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={go} disabled={busy || !valid}>
          {busy ? <Spinner /> : 'Record expense'}
        </Button>
      </>
    }>
      <div className="space-y-3">
        {types.length === 0 ? (
          <p role="alert" className="text-danger-text text-sm font-semibold">
            Expense types could not be loaded — close this dialog and retry.
          </p>
        ) : (
          <>
            <Field label="Type">
              <Select value={expenseTypeId} onChange={(e) => setExpenseTypeId(Number(e.target.value))} autoFocus>
                {types.map((t) => (
                  <option key={t.id} value={t.id}>{t.name}</option>
                ))}
              </Select>
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Amount (KES)">
                <MoneyInput value={amountCents} onCents={setAmountCents} placeholder="0.00" />
              </Field>
              <Field label="Method">
                <Select value={method} onChange={(e) => setMethod(e.target.value as 'CASH' | 'MPESA' | 'BANK')}>
                  <option value="CASH">Cash</option>
                  <option value="MPESA">M-Pesa</option>
                  <option value="BANK">Bank</option>
                </Select>
              </Field>
            </div>
            <Field label="Reference" hint="Receipt / invoice number where there is one.">
              <Input value={reference} onChange={(e) => setReference(e.target.value)} placeholder="e.g. KPLC-88213" />
            </Field>
            <Field label="Note">
              <Textarea value={note} onChange={(e) => setNote(e.target.value)} placeholder="What was this for?" />
            </Field>
            <Field label="Date spent">
              <Input type="date" value={spentOn} max={todayLocal()} onChange={(e) => setSpentOn(e.target.value)} />
            </Field>
            {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
          </>
        )}
      </div>
    </Modal>
  )
}
