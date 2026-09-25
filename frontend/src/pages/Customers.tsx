// Customers — tabs, credit limits & store credit. A customer with a credit
// limit can take goods now and pay later; store credit is prepaid money the
// shop owes them (topped up here, redeemed at checkout). The ledger is the
// audit trail (charges, payments, adjustments, loyalty, credit top-ups and
// redemptions). Cashiers see balances and take payments; managing customers
// needs customers.manage, topping up credit needs credit.manage.

import { useEffect, useState } from 'react'
import { api, Customer, LedgerEntry } from '../lib/api'
import { useAuth } from '../stores/auth'
import { formatMoney } from '../lib/money'
import { Button, Card, EmptyState, Field, Input, Modal, Spinner, StatusPill, Table, Textarea } from '../components/ui'
import { toast } from '../stores/toasts'
import { BookUser, Ticket } from 'lucide-react'

// Older backends may not send the prepaid balance yet — optional locally so
// the shared Customer type stays untouched.
type CustomerRow = Customer & { storeCreditCents?: number }

// Ledger kind labels; credit entries get their own colours (green = money
// the shop owes the customer went UP, amber = redeemed at checkout).
const KIND_META: Record<LedgerEntry['kind'], { label: string; cls?: string }> = {
  charge: { label: 'Charge' },
  payment: { label: 'Payment' },
  adjustment: { label: 'Adjustment' },
  loyalty: { label: 'Loyalty' },
  credit_topup: { label: 'Credit top-up', cls: 'bg-paid-bg text-paid-text' },
  credit_redeem: { label: 'Credit redeemed', cls: 'bg-pending-bg text-pending-text' },
}

export function Customers() {
  const { can } = useAuth()
  const manage = can('customers.manage')
  const canCredit = can('credit.manage')
  const [customers, setCustomers] = useState<CustomerRow[] | null>(null)
  const [search, setSearch] = useState('')
  const [editing, setEditing] = useState<CustomerRow | 'new' | null>(null)
  const [ledgerFor, setLedgerFor] = useState<CustomerRow | null>(null)
  const [ledger, setLedger] = useState<LedgerEntry[] | null>(null)
  const [payFor, setPayFor] = useState<CustomerRow | null>(null)
  const [creditFor, setCreditFor] = useState<CustomerRow | null>(null)
  const [redeemFor, setRedeemFor] = useState<CustomerRow | null>(null)

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
          <Table head={['Customer', 'Balance', 'Store credit', 'Limit', 'Loyalty', 'Status', '']}>
            {customers.map((c) => (
              <tr key={c.id} className={c.active ? '' : 'opacity-50'}>
                <td className="px-3 py-2.5">
                  <p className="font-bold text-ink text-[13px]">{c.name}</p>
                  <p className="text-[11px] text-ink-subtle">{c.phone || 'no phone'}</p>
                </td>
                <td className={`px-3 py-2.5 text-[13px] font-bold ${c.balanceCents > 0 ? 'text-danger-text' : 'text-ink-muted'}`}>
                  {formatMoney(c.balanceCents)}
                </td>
                <td className={`px-3 py-2.5 text-[13px] font-bold ${(c.storeCreditCents ?? 0) > 0 ? 'text-paid-text' : 'text-ink-subtle'}`}>
                  {c.storeCreditCents !== undefined ? formatMoney(c.storeCreditCents) : '—'}
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
                    {canCredit && <Button size="sm" variant="ghost" onClick={() => setCreditFor(c)}>Top up credit</Button>}
                    {canCredit && <Button size="sm" variant="ghost" onClick={() => setRedeemFor(c)}>Redeem gift card</Button>}
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
            {ledger.map((e) => {
              const kind = KIND_META[e.kind] ?? { label: e.kind }
              const isCredit = e.kind === 'credit_topup' || e.kind === 'credit_redeem'
              return (
                <tr key={e.id}>
                  <td className="px-3 py-2 text-[12px] text-ink-subtle">{e.createdAt}</td>
                  <td className="px-3 py-2">
                    {kind.cls ? (
                      <span className={`inline-flex items-center px-2 py-0.5 rounded-pill text-[11px] font-bold ${kind.cls}`}>
                        {kind.label}
                      </span>
                    ) : (
                      <span className="text-[12px] font-bold text-ink">{kind.label}</span>
                    )}
                  </td>
                  <td className={`px-3 py-2 text-[12px] font-bold ${isCredit ? kind.cls : e.amountCents > 0 ? 'text-danger-text' : 'text-paid-text'}`}>
                    {e.amountCents > 0 ? '+' : ''}{formatMoney(e.amountCents)}
                  </td>
                  <td className="px-3 py-2 text-[12px] text-ink-muted">{e.pointsDelta > 0 ? `+${e.pointsDelta}` : '—'}</td>
                  <td className="px-3 py-2 text-[12px] text-ink-muted max-w-48 truncate">{e.note}</td>
                </tr>
              )
            })}
          </Table>
        )}
      </Modal>

      <Modal open={payFor !== null} onClose={() => setPayFor(null)} title={payFor ? `Take payment — ${payFor.name}` : 'Take payment'}>
        {payFor && <PaymentForm customer={payFor} onDone={() => { setPayFor(null); load() }} />}
      </Modal>

      <Modal
        open={creditFor !== null}
        onClose={() => setCreditFor(null)}
        title={creditFor ? `Top up credit — ${creditFor.name}` : 'Top up credit'}
        size="sm"
      >
        {creditFor && <CreditTopUpForm customer={creditFor} onDone={() => { setCreditFor(null); load() }} />}
      </Modal>

      <Modal
        open={redeemFor !== null}
        onClose={() => setRedeemFor(null)}
        title="Redeem gift card"
        size="sm"
      >
        {redeemFor && (
          <GiftCardRedeemForm
            initial={redeemFor}
            onDone={() => {
              setRedeemFor(null)
              load()
              // A minted card lands as store credit — keep the open ledger honest.
              if (ledgerFor) openLedger(ledgerFor)
            }}
          />
        )}
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

// Prepaid store credit: money the customer has paid up front that the shop
// owes them. Needs credit.manage. The server records a credit_topup ledger
// entry and returns the updated customer.
function CreditTopUpForm({ customer, onDone }: { customer: CustomerRow; onDone: () => void }) {
  const [amount, setAmount] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)

  const topUp = async () => {
    const cents = Math.round(Number(amount || '0') * 100)
    if (!(cents > 0)) {
      toast.error('Enter an amount')
      return
    }
    setBusy(true)
    try {
      await api.post<Customer>(`/api/v1/customers/${customer.id}/credit-topup`, { amountCents: cents, note: note.trim() })
      toast.success('Credit topped up', `${customer.name} — ${formatMoney(cents)}`)
      onDone()
    } catch (e: any) {
      toast.error('Top-up failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-3">
      <p className="text-[13px] text-ink-muted">
        Store credit is prepaid money the shop owes the customer — they spend it at checkout.
        {customer.storeCreditCents !== undefined && (
          <> Current balance: <strong className="text-paid-text">{formatMoney(customer.storeCreditCents)}</strong>.</>
        )}
      </p>
      <Field label="Amount received (KES)">
        <Input value={amount} onChange={(e) => setAmount(e.target.value.replace(/[^\d.]/g, ''))} inputMode="decimal" autoFocus placeholder="0.00" />
      </Field>
      <Field label="Note (optional)" hint="Printed in the ledger, e.g. cash received, refund issued as credit.">
        <Textarea value={note} onChange={(e) => setNote(e.target.value)} />
      </Field>
      <Button variant="primary" size="lg" className="w-full" onClick={topUp} disabled={busy}>
        {busy ? <Spinner /> : 'Top up credit'}
      </Button>
    </div>
  )
}

// Gift-card redemption (credit.manage, like the top-up): converts a minted
// code into the customer's prepaid store credit. The server matches codes
// case-insensitively and returns the updated customer — the credited amount
// is the store-credit delta.
function GiftCardRedeemForm({ initial, onDone }: { initial: CustomerRow; onDone: () => void }) {
  const [code, setCode] = useState('')
  const [customer, setCustomer] = useState<CustomerRow | null>(initial)
  const [query, setQuery] = useState(initial.name)
  const [options, setOptions] = useState<CustomerRow[]>([])
  const [busy, setBusy] = useState(false)

  // Customer picker search (same debounce pattern as checkout).
  useEffect(() => {
    const q = query.trim()
    if (!q || customer) return
    const t = window.setTimeout(async () => {
      try {
        setOptions(await api.get<CustomerRow[]>(`/api/v1/customers?search=${encodeURIComponent(q)}`))
      } catch {
        /* offline — the picker needs the server */
      }
    }, 250)
    return () => window.clearTimeout(t)
  }, [query, customer])

  const redeem = async () => {
    const trimmed = code.trim()
    if (!customer) {
      toast.error('Pick a customer')
      return
    }
    if (!trimmed) {
      toast.error('Enter the gift card code')
      return
    }
    setBusy(true)
    try {
      const r = await api.post<{ customer: CustomerRow }>('/api/v1/gift-cards/redeem', {
        code: trimmed,
        customerId: customer.id,
      })
      const updated = r?.customer
      const credited = updated && updated.storeCreditCents !== undefined
        ? updated.storeCreditCents - (customer.storeCreditCents ?? 0)
        : 0
      toast.success(
        'Gift card redeemed',
        credited > 0
          ? `${trimmed.toUpperCase()} — ${formatMoney(credited)} credited to ${updated?.name ?? customer.name}`
          : `${customer.name} — store credit now ${formatMoney(updated?.storeCreditCents ?? 0)}`,
      )
      onDone()
    } catch (e: any) {
      toast.error('Redeem failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-3">
      <p className="text-[13px] text-ink-muted">
        The card's remaining value becomes store credit for the customer — one ledger entry, spendable at checkout.
      </p>
      <Field label="Gift card code" hint="Printed on the card, e.g. GC-KQ7P-2MX9. Case doesn't matter.">
        <Input
          value={code}
          onChange={(e) => setCode(e.target.value.toUpperCase())}
          placeholder="GC-XXXX-XXXX"
          className="font-mono uppercase tracking-wider font-bold"
          autoFocus
          onKeyDown={(e) => e.key === 'Enter' && redeem()}
        />
      </Field>
      <Field label="Customer">
        <Input
          value={customer ? `${customer.name}${customer.phone ? ` · ${customer.phone}` : ''}` : query}
          onChange={(e) => { setCustomer(null); setQuery(e.target.value) }}
          placeholder="Type a name or phone…"
        />
      </Field>
      {!customer && options.length > 0 && (
        <div className="border-2 border-line rounded-input overflow-hidden">
          {options.slice(0, 6).map((c) => (
            <button
              key={c.id}
              type="button"
              onClick={() => { setCustomer(c); setOptions([]) }}
              className="w-full flex items-center justify-between gap-2 px-3 py-2.5 text-left hover:bg-surface-muted active:bg-surface-muted"
            >
              <span>
                <span className="block text-[13px] font-bold text-ink">{c.name}</span>
                <span className="block text-[11px] text-ink-subtle">{c.phone || 'no phone'}</span>
              </span>
              <span className="text-[12px] font-bold text-ink-muted">
                {c.storeCreditCents !== undefined ? `${formatMoney(c.storeCreditCents)} credit` : ''}
              </span>
            </button>
          ))}
        </div>
      )}
      {!customer && !options.length && (
        <p className="text-[11px] text-ink-subtle flex items-center gap-1.5">
          <Ticket size={13} strokeWidth={2.25} aria-hidden />
          Search above and pick who receives the credit.
        </p>
      )}
      <Button variant="primary" size="lg" className="w-full" onClick={redeem} disabled={busy || !customer || !code.trim()}>
        {busy ? <Spinner /> : 'Redeem to store credit'}
      </Button>
    </div>
  )
}
