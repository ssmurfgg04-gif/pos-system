// Orders — searchable history with a detail drawer, void with reason,
// manual receipt completion for pending M-Pesa orders, receipt print link.
// Voided orders surface their reason; discounts, redeemed points and
// Paystack payments (card / M-Money via Paystack) render in the detail.

import { useEffect, useMemo, useState } from 'react'
import { api, DesignJob, GiftCard, Order, OrderEvent, WhatsAppContact } from '../lib/api'
import { useAuth } from '../stores/auth'
import { Button, Card, EmptyState, Input, Modal, Spinner, StatusPill, Table, Tabs, Textarea, Field } from '../components/ui'
import { ReceiptModal } from '../components/Receipt'
import { useBranding } from '../stores/branding'
import { centsToAmount, formatMoney } from '../lib/money'
import { toast } from '../stores/toasts'
import { ReceiptText, Banknote, Smartphone, AlertTriangle, Printer, BookUser, CreditCard, Wallet, MessageCircle, Briefcase, History } from 'lucide-react'

// Newer orders carry checkout extras the shared Order type doesn't declare
// yet — optional locally so the shared type stays untouched.
type OrderDetail = Order & {
  discountCents?: number
  discountLabel?: string
  pointsRedeemed?: number
}

export function PaymentLabel({ method }: { method?: string }) {
  return method === 'cash' ? (
    <span className="inline-flex items-center gap-1.5"><Banknote size={14} strokeWidth={2.25} aria-hidden />Cash</span>
  ) : method === 'mpesa' ? (
    <span className="inline-flex items-center gap-1.5"><Smartphone size={14} strokeWidth={2.25} aria-hidden />M-Pesa</span>
  ) : method === 'account' ? (
    <span className="inline-flex items-center gap-1.5"><BookUser size={14} strokeWidth={2.25} aria-hidden />Tab</span>
  ) : method === 'paystack' ? (
    <span className="inline-flex items-center gap-1.5"><CreditCard size={14} strokeWidth={2.25} aria-hidden />Card / M-Money</span>
  ) : method === 'credit' ? (
    <span className="inline-flex items-center gap-1.5"><Wallet size={14} strokeWidth={2.25} aria-hidden />Store credit</span>
  ) : (
    <span>—</span>
  )
}

function isTabOrder(o: Order) {
  return o.payments.some((p) => p.method === 'account')
}

export function Orders() {
  const canVoid = useAuth((s) => !!s.user?.permissions.includes('pos.void'))
  const canManual = useAuth((s) => !!s.user?.permissions.includes('payments.manual'))
  const canSell = useAuth((s) => !!s.user?.permissions.includes('pos.sell'))
  const canAssign = useAuth((s) => !!s.user?.permissions.includes('orders.assign'))
  const canNotify = useAuth((s) => !!s.user?.permissions.includes('orders.notify'))
  const [settleFor, setSettleFor] = useState<Order | null>(null)
  const [orders, setOrders] = useState<Order[] | null>(null)
  const [status, setStatus] = useState<'all' | 'PAID' | 'PENDING' | 'VOIDED'>('all')
  const [search, setSearch] = useState('')
  const [selected, setSelected] = useState<Order | null>(null)
  const [voiding, setVoiding] = useState<Order | null>(null)
  const [manualFor, setManualFor] = useState<Order | null>(null)
  const [discrepancyOnly, setDiscrepancyOnly] = useState(false)
  const [receiptFor, setReceiptFor] = useState<Order | null>(null)
  const [checkingId, setCheckingId] = useState<number | null>(null)

  // Re-verify a stuck PENDING order's charges with the provider — the
  // recovery path for money that arrived after the till stopped watching
  // (late M-Pesa PIN entry). Completes the sale exactly-once when paid.
  const recheck = async (o: Order) => {
    if (checkingId) return
    setCheckingId(o.id)
    try {
      const r = await api.post<{ order: Order; message: string }>(`/api/v1/orders/${o.id}/recheck`, {})
      if (r?.order?.status === 'PAID') {
        toast.success('Payment confirmed', `${r.order.number} — the money arrived, order is now paid`)
        load()
      } else {
        toast.info('No payment yet', r?.message || 'No confirmed payment for this order yet.')
      }
    } catch (e: any) {
      toast.error('Check failed', e?.message || 'Could not re-check the payment')
    } finally {
      setCheckingId(null)
    }
  }

  const load = async () => {
    try {
      const q = new URLSearchParams({ limit: '100' })
      if (status !== 'all') q.set('status', status)
      if (search.trim()) q.set('search', search.trim())
      setOrders(await api.get<Order[]>(`/api/v1/orders?${q}`))
    } catch (e: any) {
      toast.error('Load failed', e?.message)
    }
  }
  useEffect(() => { load() }, [status])

  const shown = useMemo(
    () => (orders ?? []).filter((o) => !discrepancyOnly || o.discrepancy),
    [orders, discrepancyOnly],
  )

  return (
    <div className="space-y-4">
      <Card
        title="Orders"
        sub={orders ? `${shown.length} shown` : 'Loading…'}
        actions={
          <label className="flex items-center gap-2 text-[13px] font-semibold text-ink-muted cursor-pointer">
            <input type="checkbox" checked={discrepancyOnly} onChange={(e) => setDiscrepancyOnly(e.target.checked)} className="w-4.5 h-4.5 accent-[#10B981]" />
            Discrepancies only
          </label>
        }
        pad={false}
      >
        <div className="px-4 py-3 flex flex-wrap gap-2">
          <Tabs
            tabs={[
              { key: 'all' as const, label: 'All' },
              { key: 'PAID' as const, label: 'Paid' },
              { key: 'PENDING' as const, label: 'Pending' },
              { key: 'VOIDED' as const, label: 'Voided' },
            ]}
            value={status}
            onChange={setStatus}
          />
          <form className="flex-1 min-w-44" onSubmit={(e) => { e.preventDefault(); load() }}>
            <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Order #, customer, receipt code, phone…" />
          </form>
          <Button size="md" variant="secondary" onClick={load}>Search</Button>
        </div>

        {!orders ? (
          <div className="py-12 flex justify-center"><Spinner /></div>
        ) : shown.length === 0 ? (
          <EmptyState icon={<ReceiptText size={24} strokeWidth={2.25} />} title="No orders" body="Sales will appear here." />
        ) : (
          <Table head={['Order', 'Status', 'Items', 'Total', 'Payment', 'Cashier', '']}>
            {shown.map((o) => {
              const pay = o.payments[o.payments.length - 1]
              return (
                <tr key={o.id} className={o.discrepancy ? 'bg-danger-bg' : ''}>
                  <td className="px-3 py-2.5">
                    <p className="font-bold text-ink text-[13px] tabular">{o.number}</p>
                    <p className="text-[11px] text-ink-subtle">{new Date(o.createdAt).toLocaleString()}</p>
                  </td>
                  <td className="px-3 py-2.5">
                    <StatusPill
                      status={o.status === 'PAID' ? 'paid' : o.status === 'PENDING' ? 'pending' : 'void'}
                      label={o.discrepancy ? (o.status === 'PAID' ? 'Paid — check amount' : 'Pending — check amount') : undefined}
                    />
                    {o.status === 'VOIDED' && o.voidReason && (
                      <p className="mt-1 text-[11px] font-bold text-void-text max-w-44 truncate" title={o.voidReason}>
                        Void reason: {o.voidReason}
                      </p>
                    )}
                  </td>
                  <td className="px-3 py-2.5 tabular text-ink-muted">{o.items.reduce((n, i) => n + i.qty, 0)}</td>
                  <td className="px-3 py-2.5 font-bold tabular text-ink">{centsToAmount(o.totalCents)}</td>
                  <td className="px-3 py-2.5 text-[13px] text-ink-muted">
                    {pay ? (
                      <span>
                        <PaymentLabel method={pay.method} />
                        {pay.method === 'mpesa' && pay.mpesaReceipt ? <> · <span className="font-mono text-[12px] font-semibold text-ink" title="M-Pesa receipt code">{pay.mpesaReceipt}</span></> : null}
                        {pay.method === 'paystack' && pay.mpesaReceipt ? <> · <span className="font-mono text-[12px] font-semibold text-ink" title="Paystack reference">{pay.mpesaReceipt}</span></> : null}
                      </span>
                    ) : '—'}
                  </td>
                  <td className="px-3 py-2.5 text-[13px] text-ink-muted">{o.cashierName}</td>
                  <td className="px-3 py-2.5 text-right">
                    <div className="flex items-center justify-end gap-1">
                      {o.status === 'PENDING' && canSell && (
                        <Button
                          size="sm"
                          variant="secondary"
                          disabled={checkingId === o.id}
                          onClick={() => recheck(o)}
                          title="Re-verify the charge with Paystack / M-Pesa — completes the sale if the money arrived"
                        >
                          {checkingId === o.id ? <Spinner /> : 'Check payment'}
                        </Button>
                      )}
                      <Button size="sm" variant="ghost" onClick={() => setSelected(o)}>View</Button>
                    </div>
                  </td>
                </tr>
              )
            })}
          </Table>
        )}
      </Card>

      {selected && (
        <OrderDrawer
          order={selected}
          onClose={() => setSelected(null)}
          onVoid={canVoid ? () => { setVoiding(selected); setSelected(null) } : undefined}
          onManual={canManual && selected.status === 'PENDING' ? () => { setManualFor(selected); setSelected(null) } : undefined}
          onSettle={canSell && selected.status === 'PENDING' && isTabOrder(selected) ? () => { setSettleFor(selected); setSelected(null) } : undefined}
          onRecheck={canSell && selected.status === 'PENDING' ? () => recheck(selected) : undefined}
          checking={checkingId === selected.id}
          canAssign={canAssign}
          canNotify={canNotify}
          onChanged={load}
        />
      )}

      {settleFor && (
        <SettleModal
          order={settleFor}
          onClose={() => setSettleFor(null)}
          onDone={() => { setSettleFor(null); load() }}
        />
      )}

      {voiding && <VoidModal order={voiding} onClose={() => setVoiding(null)} onDone={() => { setVoiding(null); load() }} />}

      {manualFor && (
        <ManualModal
          order={manualFor}
          onClose={() => setManualFor(null)}
          onDone={() => { setManualFor(null); load() }}
        />
      )}

      {receiptFor && (
        <ReceiptModal
          open={!!receiptFor}
          order={receiptFor}
          branding={useBranding.getState().branding}
          onClose={() => setReceiptFor(null)}
        />
      )}
    </div>
  )
}

function OrderDrawer({
  order,
  onClose,
  onVoid,
  onManual,
  onSettle,
  onRecheck,
  checking,
  canAssign,
  canNotify,
  onChanged,
}: {
  order: Order
  onClose: () => void
  onVoid?: () => void
  onManual?: () => void
  onSettle?: () => void
  onRecheck?: () => void
  checking?: boolean
  canAssign?: boolean
  canNotify?: boolean
  onChanged?: () => void
}) {
  const [live, setLive] = useState<OrderDetail>(order)
  const [events, setEvents] = useState<OrderEvent[]>([])
  const [jobModal, setJobModal] = useState(false)
  const [contact, setContact] = useState<WhatsAppContact | null>(null)
  const refreshTimeline = async () => {
    try {
      setEvents(await api.get<OrderEvent[]>(`/api/v1/orders/${order.id}/events`))
    } catch { /* timeline unavailable */ }
  }
  const [receiptOpen, setReceiptOpen] = useState(false)
  // Gift cards mint on paid sales — fetch them lazily, only when this order
  // plausibly contains a gift-card line and only for PAID orders. Fails
  // soft: an old backend just shows no chips.
  const [giftCards, setGiftCards] = useState<GiftCard[] | null>(null)
  const branding = useBranding((s) => s.branding)
  const looksGiftCard = live.items.some((i) => /^gc-/i.test(i.sku) || /gift card/i.test(i.name))
  useEffect(() => {
    setGiftCards(null)
    if (live.status !== 'PAID' || !looksGiftCard) return
    let alive = true
    api.get<{ cards: GiftCard[] }>(`/api/v1/gift-cards?orderId=${order.id}`)
      .then((r) => { if (alive) setGiftCards(Array.isArray(r) ? (r as unknown as GiftCard[]) : r?.cards ?? []) })
      .catch(() => { if (alive) setGiftCards([]) })
    return () => { alive = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [order.id, live.status, looksGiftCard])
  useEffect(() => {
    refreshTimeline()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [order.id])
  useEffect(() => {
    const t = setInterval(async () => {
      try {
        setLive(await api.get<OrderDetail>(`/api/v1/orders/${order.id}`))
      } catch { /* ignore */ }
    }, 3000)
    return () => clearInterval(t)
  }, [order.id])

  return (
    <Modal
      open
      onClose={onClose}
      title={`Order ${live.number}`}
      footer={
        <>
          {onRecheck && live.status === 'PENDING' && (
            <Button variant="secondary" onClick={onRecheck} disabled={checking} title="Re-verify the charge with Paystack / M-Pesa — completes the sale if the money arrived">
              {checking ? <Spinner /> : 'Check payment'}
            </Button>
          )}
          {onManual && live.status === 'PENDING' && (
            <Button variant="secondary" onClick={onManual}>Enter receipt code</Button>
          )}
          {onSettle && live.status === 'PENDING' && isTabOrder(live) && (
            <Button variant="primary" onClick={onSettle}>Settle tab</Button>
          )}
          <Button variant="secondary" onClick={() => setReceiptOpen(true)}>
            <Printer size={15} strokeWidth={2.25} aria-hidden />
            Receipt
          </Button>
          {onVoid && live.status !== 'VOIDED' && (
            <Button variant="danger" onClick={onVoid}>Void order</Button>
          )}
          <Button variant="primary" onClick={onClose}>Close</Button>
        </>
      }
    >
      <div className="flex items-center gap-2 mb-3">
        <StatusPill status={live.status === 'PAID' ? 'paid' : live.status === 'PENDING' ? 'pending' : 'void'} />
        {live.discrepancy && <StatusPill status="danger" label="Amount discrepancy" />}
        <span className="text-ink-subtle text-[12px] ml-auto">{new Date(live.createdAt).toLocaleString()}</span>
      </div>

      <Table head={['Item', 'Qty', 'Unit', 'Total']}>
        {live.items.map((i) => (
          <tr key={i.id}>
            <td className="px-3 py-2">
              <p className="font-semibold text-ink text-[13px]">{i.name}</p>
              <p className="text-[11px] text-ink-subtle">{i.sku}</p>
            </td>
            <td className="px-3 py-2 tabular">{i.qty}</td>
            <td className="px-3 py-2 tabular text-ink-muted">{centsToAmount(i.unitPriceCents)}</td>
            <td className="px-3 py-2 tabular font-semibold">{centsToAmount(i.lineTotalCents)}</td>
          </tr>
        ))}
      </Table>

      <div className="mt-3 space-y-1 text-sm">
        <div className="flex justify-between"><span className="text-ink-muted">Subtotal</span><span className="tabular font-semibold">{centsToAmount(live.subtotalCents)}</span></div>
        {!!live.discountCents && live.discountCents > 0 && (
          <div className="flex justify-between">
            <span className="text-ink-muted">Discount{live.discountLabel ? ` — ${live.discountLabel}` : ''}</span>
            <span className="tabular font-semibold text-pending-text">−{centsToAmount(live.discountCents)}</span>
          </div>
        )}
        {!!live.pointsRedeemed && live.pointsRedeemed > 0 && (
          <div className="flex justify-between">
            <span className="text-ink-muted">Points redeemed</span>
            <span className="tabular font-semibold text-pending-text">{live.pointsRedeemed} pts</span>
          </div>
        )}
        <div className="flex justify-between"><span className="text-ink-muted">Tax</span><span className="tabular font-semibold">{centsToAmount(live.taxCents)}</span></div>
        <div className="flex justify-between border-t-2 border-line pt-1">
          <span className="font-bold">Total</span>
          <span className="font-black tabular text-lg">{formatMoney(live.totalCents)}</span>
        </div>
      </div>

      {live.payments.map((p) => {
        const failed = p.status !== 'COMPLETED' && p.status !== 'PENDING' && p.status !== 'VOIDED'
        return (
          <div key={p.id} className="mt-3 border-2 border-line rounded-input p-3 text-[13px]">
            <div className="flex justify-between">
              <span className="font-bold"><PaymentLabel method={p.method} /> <span className="text-ink-subtle font-normal">({p.mode || '—'})</span></span>
              <StatusPill status={p.status === 'COMPLETED' ? 'paid' : p.status === 'PENDING' ? 'pending' : p.status === 'VOIDED' ? 'void' : 'danger'} label={p.status} />
            </div>
            <div className="mt-1 grid grid-cols-2 gap-x-3 text-ink-muted">
              <span>Amount: <span className="tabular text-ink font-semibold">{centsToAmount(p.amountCents)}</span></span>
              {p.phone && <span>Phone: <span className="tabular text-ink">{p.phone}</span></span>}
              {p.email && <span className="truncate" title={p.email}>Email: <span className="text-ink">{p.email}</span></span>}
              {p.mpesaReceipt && (
                <span>
                  {p.method === 'paystack' ? 'Ref: ' : 'Receipt: '}
                  <span className="tabular text-ink font-bold">{p.mpesaReceipt}</span>
                </span>
              )}
              {p.discrepancy && <span className="text-danger-text font-bold col-span-2 inline-flex items-center gap-1.5"><AlertTriangle size={13} strokeWidth={2.5} aria-hidden />Paid amount mismatch</span>}
              {p.resultDesc && (
                <span className={`col-span-2 truncate ${failed ? 'text-danger-text font-bold' : 'text-ink-subtle'}`}>
                  {failed ? `Result: ${p.resultDesc}` : p.resultDesc}
                </span>
              )}
            </div>
          </div>
        )
      })}

      {giftCards && giftCards.length > 0 && (
        <div className="mt-3">
          <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">Gift cards minted</p>
          <div className="flex flex-wrap gap-2">
            {giftCards.map((g) => (
              <span
                key={g.id}
                className="inline-flex items-center gap-2 px-2.5 py-1 rounded-pill border-2 border-line-strong bg-surface text-[12px] font-bold"
                title={g.status === 'ACTIVE' ? 'Redeemable — credit the code to a customer from their profile' : 'Already redeemed to store credit'}
              >
                <span className="font-mono tracking-wider text-ink">{g.code}</span>
                <span className="text-ink-muted tabular">{formatMoney(g.initialCents)}</span>
                <StatusPill status={g.status === 'ACTIVE' ? 'paid' : 'void'} label={g.status === 'ACTIVE' ? 'Active' : 'Redeemed'} />
              </span>
            ))}
          </div>
        </div>
      )}

      {live.voidReason && (
        <div className="mt-3 flex items-start gap-2 text-[13px] font-bold text-void-text bg-void-bg border-2 border-void-text/30 rounded-input p-3">
          <AlertTriangle size={15} strokeWidth={2.5} className="shrink-0 mt-0.5" aria-hidden />
          <span>
            Voided{live.voidedAt ? ` ${new Date(live.voidedAt).toLocaleString()}` : ''} — reason: {live.voidReason}
          </span>
        </div>
      )}

      {canNotify && live.status === 'PAID' && (
        <Button
          variant="primary"
          size="md"
          className="w-full mt-3"
          onClick={async () => {
            try {
              setContact(await api.post<WhatsAppContact>(`/api/v1/orders/${live.id}/notify`))
            } catch (e: any) {
              toast.error('Could not prepare the message', e?.message)
            }
          }}
        >
          <MessageCircle size={15} strokeWidth={2.5} aria-hidden />
          Contact customer on WhatsApp
        </Button>
      )}

      <div className="mt-4 border-t-2 border-line pt-3">
        <div className="flex items-center justify-between mb-2">
          <p className="text-[12px] uppercase font-bold text-ink-muted flex items-center gap-1.5">
            <Briefcase size={13} strokeWidth={2.5} aria-hidden />
            Fulfilment jobs
          </p>
          {canAssign && live.status === 'PAID' && (
            <Button size="sm" variant="secondary" onClick={() => setJobModal(true)}>+ Create job</Button>
          )}
        </div>
        {(live.jobs ?? []).length === 0 ? (
          <p className="text-[12px] text-ink-subtle">
            No jobs yet{canAssign && live.status === 'PAID' ? ' — create one to route this sale to a designer or the branding team.' : '.'}
          </p>
        ) : (
          <ul className="space-y-1.5">
            {(live.jobs ?? []).map((j: DesignJob) => (
              <li key={j.id} className="flex items-center gap-2 text-[13px] bg-surface-muted border border-line rounded-input px-2.5 py-1.5">
                <StatusPill
                  status={j.status === 'delivered' ? 'paid' : j.status === 'ready' ? 'info' : j.status === 'in_progress' ? 'pending' : 'void'}
                  label={j.status.replace('_', ' ')}
                />
                <span className="font-semibold text-ink truncate flex-1">{j.title}</span>
                <span className="text-ink-subtle text-[12px]">{j.assigneeName || 'unassigned'}</span>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="mt-4 border-t-2 border-line pt-3">
        <p className="text-[12px] uppercase font-bold text-ink-muted mb-2 flex items-center gap-1.5">
          <History size={13} strokeWidth={2.5} aria-hidden />
          Activity
        </p>
        {events.length === 0 ? (
          <p className="text-[12px] text-ink-subtle">Job moves, WhatsApp contacts and notes will appear here.</p>
        ) : (
          <ol className="space-y-1.5 max-h-44 overflow-y-auto pr-1">
            {events.map((e) => (
              <li key={e.id} className="text-[12.5px] flex gap-2">
                <span className="text-ink-subtle tabular shrink-0">{new Date(e.createdAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</span>
                <span className="text-ink-muted flex-1">
                  {e.kind === 'whatsapp' && <MessageCircle size={12} strokeWidth={2.5} className="inline mr-1 text-paid-text" aria-hidden />}
                  {e.message}
                  {e.username && <span className="text-ink-subtle"> — {e.username}</span>}
                </span>
              </li>
            ))}
          </ol>
        )}
      </div>

      {contact && (
        <Modal open onClose={() => setContact(null)} title="Contact customer on WhatsApp" size="sm"
          footer={<Button variant="primary" onClick={() => setContact(null)}>Close</Button>}>
          <div className="space-y-3">
            <p className="text-[13px] text-ink-muted">
              Ready for pickup — message to <span className="font-bold text-ink">{contact.phone}</span>:
            </p>
            <p className="bg-surface-muted border border-line rounded-input px-3 py-2.5 text-[13px] text-ink select-all">
              {contact.message}
            </p>
            <p className="text-[11.5px] text-ink-subtle">
              WhatsApp opens with this draft prefilled — review it and press send there. The contact attempt is
              already on the activity timeline; the app never sends automatically.
            </p>
            <Button variant="primary" size="lg" className="w-full"
              onClick={() => {
                window.open(contact.url, '_blank', 'noopener')
                toast.success('WhatsApp opened', 'Send the message from there.')
                refreshTimeline()
              }}
            >
              <MessageCircle size={16} strokeWidth={2.5} aria-hidden className="mr-2" />
              Open WhatsApp
            </Button>
          </div>
        </Modal>
      )}

      {jobModal && (
        <JobFromOrderModal
          order={live}
          onClose={() => setJobModal(false)}
          onDone={async () => {
            setJobModal(false)
            setLive(await api.get<OrderDetail>(`/api/v1/orders/${order.id}`))
            refreshTimeline()
            onChanged?.()
          }}
        />
      )}

      {receiptOpen && (
        <ReceiptModal open order={live} branding={branding} onClose={() => setReceiptOpen(false)} />
      )}
    </Modal>
  )
}

// CreateJobFromOrderModal (orders.assign): turn a paid sale into a tracked
// job with a deadline, priority and assignee — the sale → design handoff.
function JobFromOrderModal({ order, onClose, onDone }: { order: Order; onClose: () => void; onDone: () => void }) {
  const [title, setTitle] = useState('')
  const [deadline, setDeadline] = useState('')
  const [priority, setPriority] = useState('normal')
  const [assigneeId, setAssigneeId] = useState(0)
  const [staff, setStaff] = useState<{ id: number; fullName: string; roleName: string }[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    api.get<typeof staff>('/api/v1/assignees')
      .then(setStaff)
      .catch(() => { /* team list unavailable — unassigned is fine */ })
  }, [])

  const go = async () => {
    setBusy(true)
    setError('')
    try {
      await api.post(`/api/v1/orders/${order.id}/jobs`, {
        title: title.trim() || `Job for ${order.number}`,
        deadline,
        priority,
        assigneeId,
      })
      toast.success('Job created', `${order.number} routed to the production board`)
      onDone()
    } catch (e: any) {
      setError(e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={`Create job from ${order.number}`} size="sm" footer={
      <>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={go} disabled={busy}>
          {busy ? <Spinner className="border-t-brand-ink" /> : 'Create & route'}
        </Button>
      </>
    }>
      <div className="space-y-3">
        <Field label="Job title" hint="Defaults to the order number.">
          <Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder={`e.g. Print run for ${order.customerName || 'customer'}`} autoFocus />
        </Field>
        <div className="grid grid-cols-2 gap-3">
          <Field label="Deadline">
            <Input type="date" value={deadline} onChange={(e) => setDeadline(e.target.value)} />
          </Field>
          <Field label="Priority">
            <select value={priority} onChange={(e) => setPriority(e.target.value)}
              className="w-full min-h-10 px-3 bg-surface-muted border border-line rounded-input text-ink text-[13px]">
              <option value="normal">Normal</option>
              <option value="high">High</option>
              <option value="low">Low</option>
            </select>
          </Field>
        </div>
        <Field label="Assign to">
          <select value={assigneeId} onChange={(e) => setAssigneeId(Number(e.target.value))}
            className="w-full min-h-10 px-3 bg-surface-muted border border-line rounded-input text-ink text-[13px]">
            <option value={0}>Unassigned (queue)</option>
            {staff.map((m) => (
              <option key={m.id} value={m.id}>{m.fullName || String(m.id)}{m.roleName ? ` — ${m.roleName}` : ''}</option>
            ))}
          </select>
        </Field>
        {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
      </div>
    </Modal>
  )
}

function SettleModal({ order, onClose, onDone }: { order: Order; onClose: () => void; onDone: () => void }) {
  const [method, setMethod] = useState<'cash' | 'mpesa'>('cash')
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const valid = method === 'cash' || /^[A-Z0-9]{10}$/.test(code)

  const go = async () => {
    setBusy(true)
    setError('')
    try {
      await api.post(`/api/v1/orders/${order.id}/settle`, method === 'cash' ? { method } : { method, receiptCode: code })
      toast.success('Tab settled', `${order.number} — ${formatMoney(order.totalCents)}`)
      onDone()
    } catch (e: any) {
      setError(e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={`Settle tab — ${order.number}`} size="sm" footer={
      <>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={go} disabled={busy || !valid}>Settle {formatMoney(order.totalCents)}</Button>
      </>
    }>
      <p className="text-[13px] text-ink-muted mb-3">
        Owed <strong className="text-danger-text">{formatMoney(order.totalCents)}</strong> — settling marks the order paid and clears the tab.
      </p>
      <Tabs
        tabs={[
          { key: 'cash' as const, label: 'Cash', icon: <Banknote size={15} strokeWidth={2.25} aria-hidden /> },
          { key: 'mpesa' as const, label: 'M-Pesa receipt', icon: <Smartphone size={15} strokeWidth={2.25} aria-hidden /> },
        ]}
        value={method}
        onChange={setMethod}
      />
      {method === 'mpesa' && (
        <div className="mt-3">
          <Field label="M-Pesa receipt code" hint="10 characters, as printed on their confirmation SMS.">
            <Input value={code} autoFocus onChange={(e) => setCode(e.target.value.toUpperCase().replace(/[^A-Z0-9]/g, '').slice(0, 10))} placeholder="AAAAAAAAAA" className="font-mono" />
          </Field>
        </div>
      )}
      {error && <p role="alert" className="text-danger-text text-sm font-semibold mt-2">{error}</p>}
    </Modal>
  )
}

function VoidModal({ order, onClose, onDone }: { order: Order; onClose: () => void; onDone: () => void }) {
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const go = async () => {
    setBusy(true)
    setError('')
    try {
      await api.post(`/api/v1/orders/${order.id}/void`, { reason })
      toast.success('Order voided', `${order.number} — stock restored`)
      onDone()
    } catch (e: any) {
      setError(e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={`Void ${order.number}?`} size="sm" footer={
      <>
        <Button variant="ghost" onClick={onClose}>Keep order</Button>
        <Button variant="danger" onClick={go} disabled={busy || !reason.trim()}>
          {busy ? <Spinner /> : 'Void & restore stock'}
        </Button>
      </>
    }>
      <Field label="Reason (required)" hint="Recorded in the audit log.">
        <Textarea value={reason} onChange={(e) => setReason(e.target.value)} autoFocus />
      </Field>
      {error && <p role="alert" className="text-danger-text text-sm font-semibold mt-2">{error}</p>}
    </Modal>
  )
}

function ManualModal({ order, onClose, onDone }: { order: Order; onClose: () => void; onDone: () => void }) {
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const valid = /^[A-Z0-9]{10}$/.test(code)

  const go = async () => {
    setBusy(true)
    setError('')
    try {
      await api.post(`/api/v1/orders/${order.id}/manual`, { receiptCode: code })
      toast.success('Payment confirmed', `${order.number} — receipt ${code}`)
      onDone()
    } catch (e: any) {
      setError(e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={`Receipt code — ${order.number}`} size="sm" footer={
      <>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={go} disabled={busy || !valid}>Confirm payment</Button>
      </>
    }>
      <p className="text-sm text-ink-muted mb-3">
        Customer paid {formatMoney(order.totalCents)} to the till/paybill themselves. Type the 10-character
        code from their M-Pesa SMS.
      </p>
      <Input
        value={code}
        onChange={(e) => setCode(e.target.value.toUpperCase().replace(/[^A-Z0-9]/g, '').slice(0, 10))}
        placeholder="NLJ7RT61SV"
        className="tabular tracking-widest font-bold text-center text-lg"
        autoFocus
      />
      {error && <p role="alert" className="text-danger-text text-sm font-semibold mt-2">{error}</p>}
    </Modal>
  )
}
