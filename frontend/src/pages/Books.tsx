// The books (v1.12) — one balanced journal entry per day, ready to import
// into QuickBooks Online or Xero. Account names (QuickBooks matches by
// name) and codes (Xero matches by code) are editable to match the
// accountant's chart of accounts.

import { useEffect, useState } from 'react'
import { api, downloadFile } from '../lib/api'
import { Button, Card, EmptyState, Input, Spinner, Table } from '../components/ui'
import { formatMoney } from '../lib/money'
import { todayLocal } from '../lib/katime'
import { toast } from '../stores/toasts'
import { BookOpen, Download, Save } from 'lucide-react'
import { AccountingAccount, JournalDay } from '../lib/v112'

const monthStart = () => todayLocal().slice(0, 8) + '01'

export function Books() {
  const [from, setFrom] = useState(monthStart())
  const [to, setTo] = useState(todayLocal())
  const [days, setDays] = useState<JournalDay[] | null>(null)
  const [error, setError] = useState('')
  const [open, setOpen] = useState<string>('')
  const [accounts, setAccounts] = useState<AccountingAccount[] | null>(null)
  const [saving, setSaving] = useState(false)

  const load = async () => {
    setError('')
    try {
      const j = await api.get<{ days: JournalDay[] }>(`/api/v1/accounting/journal?from=${from}&to=${to}`)
      setDays(j.days)
    } catch (e: any) {
      setDays([])
      setError(e?.message || 'Could not build the journal')
    }
  }
  useEffect(() => { load() }, [from, to])
  useEffect(() => { api.get<AccountingAccount[]>('/api/v1/accounting/accounts').then(setAccounts).catch(() => setAccounts([])) }, [])

  const exportAs = (format: 'quickbooks' | 'xero' | 'detail') =>
    downloadFile(`/api/v1/accounting/export.csv?from=${from}&to=${to}&format=${format}`, `ledgerpos-${format}-${from}-${to}.csv`)
      .catch((e) => toast.error('Export failed', e?.message))

  const saveAccounts = async () => {
    if (!accounts) return
    setSaving(true)
    try {
      const body: Record<string, { name: string; code: string }> = {}
      accounts.forEach((a) => { body[a.key] = { name: a.name, code: a.code } })
      setAccounts(await api.put<AccountingAccount[]>('/api/v1/accounting/accounts', body))
      toast.success('Accounts saved')
      load()
    } catch (e: any) {
      toast.error('Could not save', e?.message)
    } finally {
      setSaving(false)
    }
  }

  const rounding = (days ?? []).reduce((s, d) => s + Math.abs(d.roundingCents), 0)

  return (
    <div className="space-y-4">
      <Card
        title="The books"
        sub="One balanced entry per day: sales, VAT, payments, returns, cost of goods, expenses and customer accounts."
        actions={
          <div className="flex flex-wrap gap-2 items-center">
            <div className="w-36"><Input type="date" value={from} onChange={(e) => setFrom(e.target.value)} aria-label="From" /></div>
            <div className="w-36"><Input type="date" value={to} onChange={(e) => setTo(e.target.value)} aria-label="To" /></div>
          </div>
        }
      >
        <div className="flex flex-wrap gap-2">
          <Button variant="primary" onClick={() => exportAs('quickbooks')}><Download size={15} aria-hidden />QuickBooks CSV</Button>
          <Button variant="primary" onClick={() => exportAs('xero')}><Download size={15} aria-hidden />Xero CSV</Button>
          <Button onClick={() => exportAs('detail')}><Download size={15} aria-hidden />Detailed CSV</Button>
        </div>
        <p className="text-xs text-ink-subtle mt-3">
          QuickBooks Online: Settings → Import data → Journal entries. Xero: Accounting → Manual journals → Import.
          Export days that are finished: a sale voided later changes its day.
        </p>
        {rounding > 0 && (
          <p className="text-sm text-pending-text font-semibold mt-2">
            {formatMoney(rounding)} went to Rounding: some sales were recorded as paid with a different amount than their total. Check those days.
          </p>
        )}
      </Card>

      <Card title="Daily entries" pad={false}>
        {!days ? <div className="py-10 flex justify-center"><Spinner /></div> : error ? (
          <p className="p-4 text-danger-text font-semibold">{error}</p>
        ) : days.length === 0 ? (
          <EmptyState icon={<BookOpen size={24} />} title="Nothing to post" body="No sales, expenses or stock changes in these dates." />
        ) : (
          <div className="divide-y divide-line">
            {days.map((d) => (
              <div key={d.date}>
                <button className="w-full flex justify-between px-4 py-3 hover:bg-surface-muted text-left" onClick={() => setOpen(open === d.date ? '' : d.date)} aria-expanded={open === d.date}>
                  <span className="font-semibold">{d.date} <span className="text-ink-subtle text-xs ml-2">{d.number}</span></span>
                  <span className="tabular text-sm">{formatMoney(d.debitCents)} <span className="text-ink-subtle">balanced</span></span>
                </button>
                {open === d.date && (
                  <Table head={['Account', 'Code', 'Debit', 'Credit', 'Memo']}>
                    {d.lines.map((l, i) => (
                      <tr key={i}>
                        <td className="px-3 py-2 font-semibold text-[13px]">{l.name}</td>
                        <td className="px-3 py-2 text-[12px] text-ink-muted">{l.code}</td>
                        <td className="px-3 py-2 tabular">{l.debitCents ? formatMoney(l.debitCents) : ''}</td>
                        <td className="px-3 py-2 tabular">{l.creditCents ? formatMoney(l.creditCents) : ''}</td>
                        <td className="px-3 py-2 text-[12px] text-ink-muted">{l.memo}</td>
                      </tr>
                    ))}
                  </Table>
                )}
              </div>
            ))}
          </div>
        )}
      </Card>

      <Card
        title="Accounts"
        sub="Match these to your accountant's chart of accounts. QuickBooks uses the name, Xero uses the code."
        actions={<Button variant="primary" disabled={saving || !accounts} onClick={saveAccounts}><Save size={15} aria-hidden />Save</Button>}
        pad={false}
      >
        {!accounts ? <div className="py-10 flex justify-center"><Spinner /></div> : (
          <Table head={['What', 'QuickBooks account name', 'Xero code']}>
            {accounts.map((a, i) => (
              <tr key={a.key}>
                <td className="px-3 py-2 text-[13px] font-semibold">{a.label}</td>
                <td className="px-3 py-2"><Input value={a.name} maxLength={100} onChange={(e) => setAccounts(accounts.map((x, j) => j === i ? { ...x, name: e.target.value } : x))} /></td>
                <td className="px-3 py-2 w-32"><Input value={a.code} maxLength={10} onChange={(e) => setAccounts(accounts.map((x, j) => j === i ? { ...x, code: e.target.value } : x))} /></td>
              </tr>
            ))}
          </Table>
        )}
      </Card>
    </div>
  )
}
