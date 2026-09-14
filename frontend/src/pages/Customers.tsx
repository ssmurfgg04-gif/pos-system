// Customers — tabs & credit. A customer with a credit limit can take goods
// now and pay later; the ledger is the audit trail (charges, payments,
// adjustments, loyalty). Cashiers see balances and take payments;
// managing customers needs customers.manage.

import { useEffect, useState } from 'react'
import { api, Customer, LedgerEntry } from '../lib/api'
import { useAuth } from '../stores/auth'
import { formatMoney } from '../lib/money'
import { Button, Card, EmptyState, Field, Input, Modal, Spinner, StatusPill, Table } from '../components/ui'
import { toast } from '../stores/toasts'
import { BookUser } from 'lucide-react'

export function Customers() {
  const { can } = useAuth()
  const manage = can('customers.manage')
  const [customers, setCustomers] = useState<Customer[] | null>(null)
  const [search, setSearch] = useState('')
  const [editing, setEditing] = useState<Customer | 'new' | null>(null)
  const [ledgerFor, setLedgerFor] = useState<Customer | null>(null)
  const [ledger, setLedger] = useState<LedgerEntry[] | null>(null)
  const [payFor, setPayFor] = useState<Customer | null>(null)

  const load = async (q = search) => {
    try {
      setCustomers(await api.get<Customer[]>(`/api/v1/customers?search=${encodeURIComponent(q)}`))
    } catch (e: any) {
      toast.error('Load failed', e?.message)
    }
  }
  useEffect(() => { load('') }, [])
  useEffect(() => {
    const t = window.setTimeout(() => load(), 250)
    return () => window.clearTimeout(t)
  }, [search])

  const openLedger = async (c: Customer) => {
    setLedgerFor(c)
    setLedger(null)
    try {
      setLedger(await api.get<LedgerEntry[]>(`/api/v1/customers/${c.id}/ledger`))
    } catch (e: any) {
      toast.error('Ledger failed', e?.message)
    }
  }

  return (
    <div className="space-y-4">
      <Card
        title="Customers"
        sub="Tabs, credit limits, loyalty — money owed lives here, not in anyone's head"
        actions={manage ? <Button variant="primary" size="sm" onClick={() => setEditing('new')}>+ Customer</Button> : undefined}
        pad={false}
      >
        <div className="px-4 py-3">
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Name or phone…" />
        </div>
        {!customers ? (
          <div className="py-12 flex justify-center"><Spinner /></div>
        ) : customers.length === 0 ? (
          <EmptyState icon={<BookUser size={24} strokeWidth={2.25} />} title="No customers" />
        ) : (
          <Table head={['Customer', 'Balance', 'Limit', 'Loyalty', 'Status', '']}>
            {customers.map((c) => (
              <tr key={c.id} className={c.active ? '' : 'opacity-50'}>
                <td className="px-3 py-2.5">
                  <p className="font-bold text-ink text-[13px]">{c.name}</p>
                  <p className="text-[11px] text-ink-subtle">{c.phone || 'no phone'}</p>
                </td>
                <td className={`px-3 py-2.5 text-[13px] font-bold ${c.balanceCents > 0 ? 'text-danger-text' : 'text-ink-muted'}`}>
                  {formatMoney(c.balanceCents)}
                </td>
                <td className="px-3 py-2.5 text-[13px] text-ink-muted">
                  {c.creditLimitCents > 0 ? formatMoney(c.creditLimitCents) : 'cash only'}
                </td>
                <td className="px-3 py-2.5 text-[13px] text-ink-muted">{c.loyaltyPoints} pts</td>
                <td className="px-3 py-2.5">
                  <StatusPill status={c.active ? 'paid' : 'void'} label={c.active ? 'Active' : 'Inactive'} />
                </td>
                <td className="px-3 py-2.5 text-right whitespace-nowrap">
                  <span className="inline-flex items-center gap-1">
                    <Button size="sm" variant="ghost" onClick={() => openLedger(c)}>Ledger</Button>
                    {c.balanceCents > 0 && <Button size="sm" variant="ghost" onClick={() => setPayFor(c)}>Pay</Button>}
                    {manage && <Button size="sm" variant="ghost" onClick={() => setEditing(c)}>Edit</Button>}
                  </span>
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Card>

      <Modal open={editing !== null} onClose={() => setEditing(null)} title={editing === 'new' ? 'New customer' : 'Edit customer'}>
        {editing && <CustomerForm initial={editing === 'new' ? null : editing} onDone={() => { setEditing(null); load() }} />}
      </Modal>

      <Modal open={ledgerFor !== null} onClose={() => { setLedgerFor(null); setLedger(null) }} title={ledgerFor ? `Ledger — ${ledgerFor.name}` : 'Ledger'} size="lg">
        {!ledger ? (
          <div className="py-8 flex justify-center"><Spinner /></div>
        ) : ledger.length === 0 ? (
          <EmptyState icon={<BookUser size={24} strokeWidth={2.25} />} title="No entries yet" />
        ) : (
          <Table head={['When', 'Kind', 'Amount', 'Points', 'Note']}>
            {ledger.map((e) => (
              <tr key={e.id}>
                <td className="px-3 py-2 text-[12px] text-ink-subtle">{e.createdAt}</td>
                <td className="px-3 py-2 text-[12px] font-bold text-ink capitalize">{e.kind}</td>
                <td className={`px-3 py-2 text-[12px] font-bold ${e.amountCents > 0 ? 'text-danger-text' : 'text-paid-text'}`}>
                  {e.amountCents > 0 ? '+' : ''}{formatMoney(e.amountCents)}
                </td>
                <td className="px-3 py-2 text-[12px] text-ink-muted">{e.pointsDelta > 0 ? `+${e.pointsDelta}` : '—'}</td>
                <td className="px-3 py-2 text-[12px] text-ink-muted max-w-48 truncate">{e.note}</td>
              </tr>
            ))}
          </Table>
        )}
      </Modal>

      <Modal open={payFor !== null} onClose={() => setPayFor(null)} title={payFor ? `Take payment — ${payFor.name}` : 'Take payment'}>
        {payFor && <PaymentForm customer={payFor} onDone={() => { setPayFor(null); load() }} />}
      </Modal>
    </div>
  )
}

function CustomerForm({ initial, onDone }: { initial: Customer | null; onDone: () => void }) {
  const [name, setName] = useState(initial?.name || '')
  const [phone, setPhone] = useState(initial?.phone || '')
  const [limit, setLimit] = useState(initial ? String(initial.creditLimitCents / 100) : '')
  const [active, setActive] = useState(initial?.active ?? true)
  const [busy, setBusy] = useState(false)

  const save = async () => {
    if (!name.trim()) {
      toast.error('Name required')
      return
    }
    setBusy(true)
    try {
      const body = {
        name: name.trim(),
        phone: phone.trim(),
        creditLimitCents: Math.max(0, Math.round(Number(limit || '0') * 100)),
        ...(initial ? { active } : {}),
      }
      if (initial) {
        await api.put(`/api/v1/customers/${initial.id}`, body)
        toast.success('Customer updated')
      } else {
        await api.post('/api/v1/customers', body)
        toast.success('Customer added')
      }
      onDone()
    } catch (e: any) {
      toast.error('Save failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-3">
      <Field label="Name">
        <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus placeholder="e.g. Mama Mboga" />
      </Field>
      <Field label="Phone">
        <Input value={phone} onChange={(e) => setPhone(e.target.value)} inputMode="tel" placeholder="07XX XXX XXX" />
      </Field>
      <Field label="Credit limit (KES)" hint="0 means cash only — no tab">
        <Input value={limit} onChange={(e) => setLimit(e.target.value.replace(/[^\d.]/g, ''))} inputMode="decimal" placeholder="0" />
      </Field>
      {initial && (
        <label className="flex items-center gap-2 text-[13px] font-semibold text-ink">
          <input type="checkbox" checked={active} onChange={(e) => setActive(e.target.checked)} className="w-5 h-5" />
          Active
        </label>
      )}
      <Button variant="primary" size="lg" className="w-full" onClick={save} disabled={busy}>
        {busy ? <Spinner /> : initial ? 'Save' : 'Add customer'}
      </Button>
    </div>
  )
}

function PaymentForm({ customer, onDone }: { customer: Customer; onDone: () => void }) {
  const [amount, setAmount] = useState('')
  const [busy, setBusy] = useState(false)

  const pay = async () => {
    const cents = Math.round(Number(amount || '0') * 100)
    if (!(cents > 0)) {
      toast.error('Enter an amount')
      return
    }
    setBusy(true)
    try {
      await api.post(`/api/v1/customers/${customer.id}/payments`, { amountCents: cents })
      toast.success('Payment recorded', formatMoney(cents))
      onDone()
    } catch (e: any) {
      toast.error('Payment failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-3">
      <p className="text-[13px] text-ink-muted">
        Owes <strong className="text-danger-text">{formatMoney(customer.balanceCents)}</strong>
      </p>
      <Field label="Amount (KES)">
        <Input value={amount} onChange={(e) => setAmount(e.target.value.replace(/[^\d.]/g, ''))} inputMode="decimal" autoFocus placeholder="0.00" />
      </Field>
      <Button variant="primary" size="lg" className="w-full" onClick={pay} disabled={busy}>
        {busy ? <Spinner /> : 'Record payment'}
      </Button>
    </div>
  )
}
