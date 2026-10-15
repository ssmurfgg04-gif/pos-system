// Quotes — the quotation pipeline. Creative Divine quotes branding,
// printing and cyber packages before the work exists; until now those
// offers lived in WhatsApp threads. A quote is a document the whole team
// sees (it syncs like orders): draft → sent → accepted/declined, and
// "Move to till" parks its lines as a held sale so checkout does the
// money through the normal POS path. Quotes never touch stock or money
// directly — they describe the offer.

import { useEffect, useMemo, useRef, useState } from 'react'
import {
  api,
  Quote,
  QuoteItem,
  QuoteInput,
  QuoteStatus,
  Product,
  Customer,
} from '../lib/api'
import { useAuth } from '../stores/auth'
import { useBranding } from '../stores/branding'
import {
  Button, Card, EmptyState, Field, Input, Modal, MoneyInput,
  Spinner, StatusPill, Table, Textarea,
} from '../components/ui'
import { toast } from '../stores/toasts'
import {
  FileText, Plus, Printer, Search, Trash2, Send, CircleCheck, CircleX,
  ShoppingCart, Pencil, X,
} from 'lucide-react'

const STATUS_FILTERS: { key: string; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'draft', label: 'Drafts' },
  { key: 'sent', label: 'Sent' },
  { key: 'accepted', label: 'Accepted' },
  { key: 'declined', label: 'Declined' },
  { key: 'converted', label: 'On a till' },
]

function quotePill(s: QuoteStatus): { status: Parameters<typeof StatusPill>[0]['status']; label: string } {
  switch (s) {
    case 'draft': return { status: 'info', label: 'Draft' }
    case 'sent': return { status: 'pending', label: 'Sent' }
    case 'accepted': return { status: 'paid', label: 'Accepted' }
    case 'declined': return { status: 'void', label: 'Declined' }
    case 'converted': return { status: 'delivered', label: 'On a till' }
  }
}

const money = (c: number) => (c / 100).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })

export function Quotes() {
  const { user } = useAuth()
  const canManage = user?.permissions.includes('quotes.manage') ?? false
  const canDiscount = user?.permissions.includes('payments.apply_discount') ?? false
  const [quotes, setQuotes] = useState<Quote[] | null>(null)
  const [status, setStatus] = useState('all')
  const [q, setQ] = useState('')
  const [error, setError] = useState('')
  const [editing, setEditing] = useState<Quote | null>(null)   // null = closed, {}-ish for new
  const [creating, setCreating] = useState(false)
  const [detail, setDetail] = useState<Quote | null>(null)
  const [printing, setPrinting] = useState<Quote | null>(null)
  const [rowBusy, setRowBusy] = useState(0)

  // ONE debounced loader over both filters, with a request-sequence guard so
  // a slow earlier response can never overwrite a newer one (the review
  // caught status-chip clicks racing the search debounce).
  const loadSeq = useRef(0)
  const load = async (statusArg: string, qArg: string) => {
    const seq = ++loadSeq.current
    try {
      const params = new URLSearchParams()
      if (statusArg !== 'all') params.set('status', statusArg)
      if (qArg.trim()) params.set('q', qArg.trim())
      const rows = await api.get<Quote[]>(`/api/v1/quotes?${params.toString()}`)
      if (seq === loadSeq.current) {
        setQuotes(rows)
        setError('')
      }
    } catch (e: any) {
      if (seq === loadSeq.current) {
        setQuotes([])
        setError(e?.message || 'Could not load quotations')
      }
    }
  }
  useEffect(() => {
    const t = setTimeout(() => load(status, q), 220)
    return () => clearTimeout(t)
  }, [status, q])

  const setStatusOf = async (id: number, next: QuoteStatus) => {
    setRowBusy(id)
    try {
      const updated = await api.post<Quote>(`/api/v1/quotes/${id}/status`, { status: next })
      toast.success(`Quote ${updated.quoteNo} ${next === 'sent' ? 'marked sent' : next}`)
      setDetail((d) => (d?.id === id ? updated : d))
      await load(status, q)
    } catch (e: any) {
      toast.error('Could not update the quote', e?.message)
    } finally {
      setRowBusy(0)
    }
  }

  const convert = async (id: number) => {
    setRowBusy(id)
    try {
      const held = await api.post<{ id: number }>(`/api/v1/quotes/${id}/convert`)
      toast.success('Parked on this till', 'Open the POS → Held sales → resume, then check out as usual.')
      setDetail(null)
      await load(status, q)
      return held
    } catch (e: any) {
      toast.error('Could not move the quote to a till', e?.message)
      return null
    } finally {
      setRowBusy(0)
    }
  }

  return (
    <div className="p-4 sm:p-5 space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h1 className="text-lg font-bold text-ink flex items-center gap-2">
            <FileText size={19} strokeWidth={2.25} aria-hidden /> Quotations
          </h1>
          <p className="text-[12.5px] text-ink-muted mt-0.5">
            Priced offers for custom work, send, track, and move the yes to a till.
          </p>
        </div>
        {canManage && (
          <Button variant="primary" onClick={() => { setCreating(true); setEditing({} as Quote) }}>
            <Plus size={15} strokeWidth={2.5} aria-hidden /> New quotation
          </Button>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-2">
        {STATUS_FILTERS.map((f) => (
          <button
            key={f.key}
            onClick={() => setStatus(f.key)}
            className={`min-h-9 px-3 rounded-input border-2 text-[12.5px] font-bold transition-colors ${
              status === f.key ? 'bg-brand border-brand-strong text-brand-ink' : 'bg-surface border-line text-ink-muted hover:text-ink hover:border-line-strong'
            }`}
          >
            {f.label}
          </button>
        ))}
        <div className="relative ml-auto w-56">
          <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-ink-subtle" aria-hidden />
          <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search number, customer, phone" className="pl-8" />
        </div>
      </div>

      {error && (
        <p role="alert" className="text-danger-text text-sm font-semibold bg-danger-bg border border-danger-text/30 rounded-input px-3 py-2">
          {error}
        </p>
      )}

      {quotes === null ? (
        <div className="py-16 flex justify-center"><Spinner /></div>
      ) : quotes.length === 0 ? (
        <Card>
          <EmptyState
            icon={<FileText size={24} strokeWidth={2.25} />}
            title={q || status !== 'all' ? 'No quotations match' : 'No quotations yet'}
            body={q || status !== 'all'
              ? 'Try a different search or filter.'
              : canManage
                ? 'Create the first quotation, pick the customer, add the products, print or WhatsApp it.'
                : 'Ask the front desk to create one, you will see it here the moment it exists.'}
            action={canManage && !q && status === 'all' ? (
              <Button variant="primary" onClick={() => { setCreating(true); setEditing({} as Quote) }}>
                <Plus size={15} strokeWidth={2.5} aria-hidden /> New quotation
              </Button>
            ) : undefined}
          />
        </Card>
      ) : (
        <Card className="overflow-x-auto">
          <Table head={['Quote', 'Customer', 'Lines', 'Total', 'Valid until', 'Status', 'By', '']}>
            {quotes.map((qy) => {
              const pill = quotePill(qy.status)
              return (
                <tr key={qy.id} className="cursor-pointer hover:bg-surface-muted" onClick={() => setDetail(qy)}>
                  <td className="px-3 py-2 font-mono text-[12.5px] font-bold text-ink">{qy.quoteNo}</td>
                  <td className="px-3 py-2 text-[13px] text-ink">
                    {qy.customerName}
                    {qy.customerPhone && <span className="block text-[11px] text-ink-muted">{qy.customerPhone}</span>}
                  </td>
                  <td className="px-3 py-2 text-[12.5px] text-ink-muted tabular">{qy.items.length}</td>
                  <td className="px-3 py-2 text-[13px] font-bold text-ink tabular">{money(qy.totalCents)}</td>
                  <td className="px-3 py-2 text-[12px] text-ink-muted tabular">
                    {qy.validUntil || '-'}
                    {qy.validUntil && new Date(qy.validUntil + 'T23:59:59') < new Date() && qy.status !== 'converted' && (
                      <span className="block text-[11px] font-bold text-pending-text">expired</span>
                    )}
                  </td>
                  <td className="px-3 py-2"><StatusPill status={pill.status} label={pill.label} /></td>
                  <td className="px-3 py-2 text-[12px] text-ink-muted">{qy.createdByName}</td>
                  <td className="px-3 py-2 text-right">
                    <button
                      onClick={(e) => { e.stopPropagation(); setPrinting(qy) }}
                      className="inline-flex items-center gap-1 text-[12px] font-semibold text-ink-muted hover:text-ink"
                      title="Print / save as PDF"
                    >
                      <Printer size={14} strokeWidth={2.25} aria-hidden /> Print
                    </button>
                  </td>
                </tr>
              )
            })}
          </Table>
        </Card>
      )}

      {(creating || (editing !== null && !creating)) && editing && (
        <QuoteEditor
          quote={creating ? null : editing}
          canOverridePrice={user?.permissions.includes('payments.override_price') ?? false}
          canDiscount={canDiscount}
          onClose={() => { setCreating(false); setEditing(null) }}
          onSaved={(saved) => {
            setCreating(false); setEditing(null)
            toast.success(`Quote ${saved.quoteNo} saved`)
            // Keep the detail modal in sync when the edit came from it
            // (stale totals behind the editor confused clerks).
            setDetail((d) => (d?.id === saved.id ? saved : d))
            load(status, q)
          }}
        />
      )}

      {detail && (
        <QuoteDetail
          quote={detail}
          canManage={canManage}
          rowBusy={rowBusy}
          onStatus={(s) => setStatusOf(detail.id, s)}
          onConvert={() => convert(detail.id)}
          onEdit={() => { setEditing(detail); setCreating(false) }}
          onClose={() => setDetail(null)}
        />
      )}

      {printing && <QuotePrint quote={printing} onClose={() => setPrinting(null)} />}
    </div>
  )
}

// ---- Editor (create / edit draft-or-sent) ----

function QuoteEditor({
  quote,
  canOverridePrice,
  canDiscount,
  onClose,
  onSaved,
}: {
  quote: Quote | null
  canOverridePrice: boolean
  canDiscount: boolean
  onClose: () => void
  onSaved: (q: Quote) => void
}) {
  const branding = useBranding((s) => s.branding)
  const [customerId, setCustomerId] = useState(quote?.customerId ?? 0)
  const [customerName, setCustomerName] = useState(quote?.customerName ?? '')
  const [customerPhone, setCustomerPhone] = useState(quote?.customerPhone ?? '')
  const [items, setItems] = useState<QuoteItem[]>(quote?.items ?? [])
  const [discountCents, setDiscountCents] = useState(quote?.discountCents ?? 0)
  const [discountLabel, setDiscountLabel] = useState(quote?.discountLabel ?? '')
  const [notes, setNotes] = useState(quote?.notes ?? '')
  const [validUntil, setValidUntil] = useState(quote?.validUntil ?? defaultValidUntil())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  // Catalog + customer pickers.
  const [products, setProducts] = useState<Product[]>([])
  const [prodQ, setProdQ] = useState('')
  const [custQ, setCustQ] = useState('')
  const [custResults, setCustResults] = useState<Customer[]>([])
  const custDebounce = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(() => {
    api.get<Product[]>('/api/v1/products').then(setProducts).catch(() => setProducts([]))
  }, [])
  useEffect(() => {
    if (custDebounce.current) clearTimeout(custDebounce.current)
    if (!custQ.trim()) { setCustResults([]); return }
    custDebounce.current = setTimeout(() => {
      api.get<Customer[]>(`/api/v1/customers?q=${encodeURIComponent(custQ.trim())}`)
        .then(setCustResults).catch(() => setCustResults([]))
    }, 250)
  }, [custQ])

  const addProduct = (p: Product) => {
    setItems((cur) => {
      const i = cur.findIndex((it) => it.productId === p.id)
      if (i >= 0) {
        const next = [...cur]
        next[i] = { ...next[i], qty: next[i].qty + 1, lineTotalCents: (next[i].qty + 1) * next[i].unitPriceCents }
        return next
      }
      return [...cur, { productId: p.id, sku: p.sku, name: p.name, qty: 1, unitPriceCents: p.priceCents, lineTotalCents: p.priceCents }]
    })
    setProdQ('')
  }

  const setLine = (idx: number, patch: Partial<QuoteItem>) => {
    setItems((cur) => cur.map((it, i) => {
      if (i !== idx) return it
      const next = { ...it, ...patch }
      next.lineTotalCents = next.unitPriceCents * Math.max(0, next.qty)
      return next
    }))
  }

  const subtotal = items.reduce((a, it) => a + it.lineTotalCents, 0)
  const total = Math.max(0, subtotal - discountCents)

  const pickCustomer = (c: Customer) => {
    setCustomerId(c.id); setCustomerName(c.name); setCustomerPhone(c.phone); setCustQ(''); setCustResults([])
  }

  const save = async () => {
    if (busy) return
    setError('')
    if (items.length === 0) { setError('Add at least one product line'); return }
    if (!customerId && !customerName.trim()) { setError('Pick a customer or type a name'); return }
    const payload: QuoteInput = {
      customerId, customerName: customerName.trim(), customerPhone: customerPhone.trim(),
      items, discountCents, discountLabel: discountLabel.trim(), notes: notes.trim(), validUntil,
    }
    setBusy(true)
    try {
      const saved = quote?.id
        ? await api.put<Quote>(`/api/v1/quotes/${quote.id}`, payload)
        : await api.post<Quote>('/api/v1/quotes', payload)
      onSaved(saved)
    } catch (e: any) {
      setError(e?.message || 'Could not save the quotation')
    } finally {
      setBusy(false)
    }
  }

  const filteredProducts = useMemo(() => {
    const needle = prodQ.trim().toLowerCase()
    const base = products.filter((p) => p.active)
    if (!needle) return base.slice(0, 8)
    return base.filter((p) =>
      p.name.toLowerCase().includes(needle) ||
      p.sku.toLowerCase().includes(needle) ||
      (p.barcode || '').toLowerCase().includes(needle)
    ).slice(0, 8)
  }, [products, prodQ])

  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={quote?.id ? `Edit ${quote.quoteNo}` : 'New quotation'}
      footer={
        <div className="flex items-center justify-between gap-2 w-full">
          <span className="text-[12px] text-ink-muted">
            {items.length} line{items.length === 1 ? '' : 's'} · Total{' '}
            <span className="font-bold text-ink tabular">{branding.currency_symbol ? branding.currency_symbol + ' ' : ''}{money(total)}</span>
          </span>
          <span className="flex gap-2">
            <Button variant="ghost" onClick={onClose}>Cancel</Button>
            <Button variant="primary" onClick={save} disabled={busy}>
              {busy ? <Spinner className="border-t-white" /> : (quote?.id ? 'Save changes' : 'Create quotation')}
            </Button>
          </span>
        </div>
      }
    >
      <div className="space-y-4">
        <div className="grid sm:grid-cols-2 gap-3">
          <Field label="Customer" hint={customerId ? 'Saved customer' : 'Type a name for a walk-in quote'}>
            {customerId ? (
              <div className="flex items-center gap-2 min-h-11 rounded-input border-2 border-line bg-surface-muted px-3">
                <span className="text-sm font-semibold text-ink truncate">{customerName}</span>
                <span className="text-[12px] text-ink-muted">{customerPhone}</span>
                <button onClick={() => { setCustomerId(0); setCustomerName(''); setCustomerPhone('') }}
                  className="ml-auto text-ink-muted hover:text-ink" title="Clear">
                  <X size={14} strokeWidth={2.5} aria-hidden />
                </button>
              </div>
            ) : (
              <>
                <Input value={customerName} onChange={(e) => setCustomerName(e.target.value)} placeholder="Customer name" />
                {custResults.length > 0 && (
                  <div className="mt-1 border-2 border-line rounded-input bg-surface divide-y divide-line max-h-44 overflow-y-auto">
                    {custResults.map((c) => (
                      <button key={c.id} onClick={() => pickCustomer(c)}
                        className="w-full text-left px-3 py-2 hover:bg-surface-muted text-[13px]">
                        <span className="font-semibold text-ink">{c.name}</span>
                        <span className="ml-2 text-[11.5px] text-ink-muted">{c.phone}</span>
                      </button>
                    ))}
                  </div>
                )}
              </>
            )}
          </Field>
          <Field label="Phone (optional, syncs the customer)">
            <Input value={customerPhone} onChange={(e) => setCustomerPhone(e.target.value)} placeholder="e.g. 0712 345 678" />
          </Field>
        </div>

        <Field label="Find another customer by phone or name">
          <Input value={custQ} onChange={(e) => setCustQ(e.target.value)} placeholder="Search saved customers…" />
        </Field>

        <div>
          <p className="text-[12px] font-bold text-ink-muted uppercase tracking-wide mb-1.5">Line items</p>
          <div className="flex gap-2 mb-2">
            <Input
              value={prodQ}
              onChange={(e) => setProdQ(e.target.value)}
              placeholder="Search products to add, name, SKU or barcode"
            />
          </div>
          {prodQ.trim() && (
            <div className="border-2 border-line rounded-input bg-surface divide-y divide-line max-h-52 overflow-y-auto mb-2">
              {filteredProducts.length === 0 && (
                <p className="px-3 py-2.5 text-[12.5px] text-ink-muted">No products match “{prodQ}”.</p>
              )}
              {filteredProducts.map((p) => (
                <button key={p.id} onClick={() => addProduct(p)}
                  className="w-full flex items-center justify-between gap-2 text-left px-3 py-2 hover:bg-surface-muted">
                  <span className="min-w-0">
                    <span className="block text-[13px] font-semibold text-ink truncate">{p.name}</span>
                    <span className="block text-[11px] text-ink-muted">{p.sku} · stock {p.stockQty}</span>
                  </span>
                  <span className="text-[12.5px] font-bold text-ink tabular shrink-0">{money(p.priceCents)}</span>
                </button>
              ))}
            </div>
          )}
          {items.length === 0 ? (
            <p className="text-[12.5px] text-ink-muted border-2 border-dashed border-line rounded-input px-3 py-4 text-center">
              Search above and tap a product to add it.
            </p>
          ) : (
            <div className="border-2 border-line rounded-input divide-y divide-line">
              {items.map((it, idx) => (
                <div key={`${it.productId}-${idx}`} className="flex flex-wrap items-center gap-2 px-2.5 py-2">
                  <div className="min-w-0 flex-1">
                    <p className="text-[13px] font-semibold text-ink truncate">{it.name}</p>
                    <p className="text-[11px] text-ink-muted">{it.sku}</p>
                  </div>
                  <label className="flex items-center gap-1 text-[11px] text-ink-muted">
                    Qty
                    <input
                      type="number" min={1} max={9999}
                      value={it.qty}
                      onChange={(e) => setLine(idx, { qty: Math.max(1, Math.round(Number(e.target.value) || 1)) })}
                      className="w-16 min-h-9 rounded-input border-2 border-line bg-surface px-2 text-[13px] text-ink tabular"
                    />
                  </label>
                  <label className="flex items-center gap-1 text-[11px] text-ink-muted">
                    Price
                    <MoneyInput value={it.unitPriceCents} onCents={(c) => setLine(idx, { unitPriceCents: c })}
                      className="w-28" readOnly={!canOverridePrice}
                      title={canOverridePrice ? 'Line price' : 'Price override needs the payments.override_price permission'} />
                  </label>
                  <span className="w-24 text-right text-[13px] font-bold text-ink tabular">{money(it.lineTotalCents)}</span>
                  <button onClick={() => setItems((cur) => cur.filter((_, i) => i !== idx))}
                    className="text-ink-muted hover:text-danger-text" title="Remove line">
                    <Trash2 size={14} strokeWidth={2.25} aria-hidden />
                  </button>
                </div>
              ))}
              <div className="flex flex-wrap items-center justify-end gap-x-5 gap-y-1 px-3 py-2 bg-surface-muted text-[12.5px]">
                <span className="text-ink-muted">Subtotal <span className="font-bold text-ink tabular">{money(subtotal)}</span></span>
                {discountCents > 0 && <span className="text-ink-muted">Discount <span className="font-bold text-ink tabular">−{money(discountCents)}</span></span>}
                <span className="text-ink-muted">Total <span className="font-bold text-ink tabular">{money(total)}</span></span>
              </div>
            </div>
          )}
        </div>

        <div className="grid sm:grid-cols-3 gap-3">
          <Field label="Discount" hint={canDiscount ? undefined : 'Needs the apply-discount permission'}>
            <MoneyInput value={discountCents} onCents={setDiscountCents} readOnly={!canDiscount}
              className={canDiscount ? '' : 'opacity-60'}
              title={canDiscount ? 'Order-level discount' : 'Applying a discount needs payments.apply_discount'} />
          </Field>
          <Field label="Discount label">
            <Input value={discountLabel} onChange={(e) => setDiscountLabel(e.target.value)} placeholder="e.g. negotiated" />
          </Field>
          <Field label="Valid until">
            <Input type="date" value={validUntil} onChange={(e) => setValidUntil(e.target.value)} />
          </Field>
        </div>

        <Field label="Notes (shown on the printed quote)">
          <Textarea value={notes} onChange={(e) => setNotes(e.target.value)} rows={3}
            placeholder="Delivery timeline, revisions included, payment terms…" />
        </Field>

        {error && (
          <p role="alert" className="text-danger-text text-sm font-semibold bg-danger-bg border border-danger-text/30 rounded-input px-3 py-2">
            {error}
          </p>
        )}
      </div>
    </Modal>
  )
}

function defaultValidUntil(): string {
  const d = new Date(Date.now() + 14 * 864e5)
  return d.toISOString().slice(0, 10)
}

// ---- Detail drawer ----

function QuoteDetail({
  quote, canManage, rowBusy, onStatus, onConvert, onEdit, onClose,
}: {
  quote: Quote
  canManage: boolean
  rowBusy: number
  onStatus: (s: QuoteStatus) => void
  onConvert: () => void
  onEdit: () => void
  onClose: () => void
}) {
  const pill = quotePill(quote.status)
  const editable = canManage && (quote.status === 'draft' || quote.status === 'sent')
  return (
    <Modal open onClose={onClose} size="md" title={
      <span className="flex items-center gap-2">
        <span className="font-mono">{quote.quoteNo}</span>
        <StatusPill status={pill.status} label={pill.label} />
      </span>
    }>
      <div className="space-y-4">
        <div className="grid grid-cols-2 gap-3 text-[13px]">
          <div>
            <p className="text-[11px] font-bold text-ink-muted uppercase">Customer</p>
            <p className="font-semibold text-ink">{quote.customerName}</p>
            {quote.customerPhone && <p className="text-ink-muted">{quote.customerPhone}</p>}
          </div>
          <div>
            <p className="text-[11px] font-bold text-ink-muted uppercase">Created</p>
            <p className="text-ink">{new Date(quote.createdAt).toLocaleString()}</p>
            <p className="text-[12px] text-ink-muted">by {quote.createdByName}</p>
          </div>
        </div>

        <div className="border-2 border-line rounded-input divide-y divide-line">
          {quote.items.map((it, i) => (
            <div key={i} className="flex items-center justify-between gap-3 px-3 py-2 text-[13px]">
              <span className="min-w-0">
                <span className="block font-semibold text-ink truncate">{it.name}</span>
                <span className="text-[11px] text-ink-muted">{it.qty} × {money(it.unitPriceCents)}</span>
              </span>
              <span className="font-bold text-ink tabular shrink-0">{money(it.lineTotalCents)}</span>
            </div>
          ))}
          <div className="px-3 py-2 bg-surface-muted text-[12.5px] space-y-0.5">
            <p className="flex justify-between text-ink-muted"><span>Subtotal</span><span className="tabular">{money(quote.subtotalCents)}</span></p>
            {quote.discountCents > 0 && (
              <p className="flex justify-between text-ink-muted">
                <span>Discount{quote.discountLabel ? ` (${quote.discountLabel})` : ''}</span>
                <span className="tabular">−{money(quote.discountCents)}</span>
              </p>
            )}
            <p className="flex justify-between font-bold text-ink"><span>Total</span><span className="tabular">{money(quote.totalCents)}</span></p>
          </div>
        </div>

        {(quote.notes || quote.validUntil) && (
          <div className="text-[13px] space-y-1">
            {quote.validUntil && (
              <p className="text-ink-muted">Valid until <span className="font-bold text-ink">{quote.validUntil}</span></p>
            )}
            {quote.notes && <p className="text-ink-muted whitespace-pre-wrap">{quote.notes}</p>}
          </div>
        )}

        {quote.status === 'converted' && (
          <p className="text-[12.5px] font-semibold text-paid-text bg-paid-bg border border-paid-text/30 rounded-input px-3 py-2">
            Moved to a till as parked sale #{quote.convertedHeldSaleId}. Resume it from the POS Held-sales panel and check out as usual.
          </p>
        )}

        {canManage && (
          <div className="flex flex-wrap gap-2 border-t-2 border-line pt-3">
            {quote.status === 'draft' && (
              <Button variant="secondary" size="sm" onClick={() => onStatus('sent')} disabled={rowBusy === quote.id}>
                {rowBusy === quote.id ? <Spinner className="border-t-brand-ink" /> : <Send size={14} strokeWidth={2.5} aria-hidden />}
                Mark sent
              </Button>
            )}
            {(quote.status === 'draft' || quote.status === 'sent') && (
              <>
                <Button variant="secondary" size="sm" onClick={() => onStatus('accepted')} disabled={rowBusy === quote.id}>
                  {rowBusy === quote.id ? <Spinner className="border-t-brand-ink" /> : <CircleCheck size={14} strokeWidth={2.5} aria-hidden />}
                  Accepted
                </Button>
                <Button variant="ghost" size="sm" onClick={() => onStatus('declined')} disabled={rowBusy === quote.id}>
                  <CircleX size={14} strokeWidth={2.5} aria-hidden /> Declined
                </Button>
              </>
            )}
            {(quote.status === 'draft' || quote.status === 'sent' || quote.status === 'accepted') && (
              <Button variant="primary" size="sm" onClick={onConvert} disabled={rowBusy === quote.id}
                title="Parks the lines in this till's Held sales">
                {rowBusy === quote.id ? <Spinner className="border-t-brand-ink" /> : <ShoppingCart size={14} strokeWidth={2.5} aria-hidden />}
                Move to till
              </Button>
            )}
            {editable && (
              <Button variant="ghost" size="sm" onClick={onEdit}>
                <Pencil size={14} strokeWidth={2.25} aria-hidden /> Edit
              </Button>
            )}
          </div>
        )}
      </div>
    </Modal>
  )
}

// ---- Print view (A4 offer; browser print → save as PDF) ----

function QuotePrint({ quote, onClose }: { quote: Quote; onClose: () => void }) {
  const branding = useBranding((s) => s.branding)
  const kraPin = branding.kra_pin || ''
  const cur = branding.currency_symbol || ''
  useEffect(() => {
    const prev = document.title
    document.title = `${quote.quoteNo}, ${quote.customerName}`
    return () => { document.title = prev }
  }, [quote])
  // Toggle the isolation class the print stylesheet targets; without it,
  // window.print() output the whole app (the review's H-1).
  const doPrint = () => {
    document.body.classList.add('printing-quote')
    const cleanup = () => {
      document.body.classList.remove('printing-quote')
      window.removeEventListener('afterprint', cleanup)
    }
    window.addEventListener('afterprint', cleanup)
    window.print()
    // Safety net for browsers that skip afterprint on cancel.
    window.setTimeout(cleanup, 2000)
  }
  return (
    <Modal open onClose={onClose} size="md" title={`Print ${quote.quoteNo}`}
      footer={
        <div className="flex gap-2 w-full justify-end">
          <Button variant="ghost" onClick={onClose}>Close</Button>
          <Button variant="primary" onClick={doPrint}>
            <Printer size={15} strokeWidth={2.5} aria-hidden /> Print / Save PDF
          </Button>
        </div>
      }
    >
      <div id="quote-print-root" className="bg-white text-black p-5 rounded-input border border-line text-[13px]">
        <div className="flex items-start justify-between gap-4 border-b-2 border-black/10 pb-3 mb-3">
          <div className="flex items-center gap-3">
            {branding.brand_logo_url ? (
              <img src={branding.brand_logo_url} alt="" className="w-12 h-12 object-contain" />
            ) : (
              <div className="w-12 h-12 rounded bg-black text-white flex items-center justify-center font-black text-xl">
                {(branding.store_name || 'Q').slice(0, 1).toUpperCase()}
              </div>
            )}
            <div>
              <p className="font-black text-base leading-tight">{branding.store_name || branding.app_name}</p>
              {kraPin && <p className="text-[11.5px] text-black/70">KRA PIN: {kraPin}</p>}
            </div>
          </div>
          <div className="text-right">
            <p className="font-black text-lg tracking-tight">QUOTATION</p>
            <p className="font-mono text-[12px]">{quote.quoteNo}</p>
            <p className="text-[11.5px] text-black/70">{new Date(quote.createdAt).toLocaleDateString()}</p>
          </div>
        </div>
        <div className="mb-3">
          <p className="text-[11px] uppercase tracking-wide text-black/60 font-bold">Prepared for</p>
          <p className="font-bold">{quote.customerName}</p>
          {quote.customerPhone && <p className="text-black/70">{quote.customerPhone}</p>}
        </div>
        <table className="w-full border-collapse mb-3">
          <thead>
            <tr className="border-b-2 border-black/20 text-left text-[11px] uppercase tracking-wide text-black/60">
              <th className="py-1.5">Item</th>
              <th className="py-1.5 text-right">Qty</th>
              <th className="py-1.5 text-right">Unit price</th>
              <th className="py-1.5 text-right">Total</th>
            </tr>
          </thead>
          <tbody>
            {quote.items.map((it, i) => (
              <tr key={i} className="border-b border-black/10">
                <td className="py-1.5 font-semibold">{it.name}</td>
                <td className="py-1.5 text-right tabular">{it.qty}</td>
                <td className="py-1.5 text-right tabular">{cur}{money(it.unitPriceCents)}</td>
                <td className="py-1.5 text-right tabular font-bold">{cur}{money(it.lineTotalCents)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        <div className="flex justify-end mb-3">
          <div className="w-56 text-[12.5px] space-y-1">
            <p className="flex justify-between text-black/70"><span>Subtotal</span><span className="tabular">{cur}{money(quote.subtotalCents)}</span></p>
            {quote.discountCents > 0 && (
              <p className="flex justify-between text-black/70">
                <span>Discount{quote.discountLabel ? ` (${quote.discountLabel})` : ''}</span>
                <span className="tabular">−{cur}{money(quote.discountCents)}</span>
              </p>
            )}
            <p className="flex justify-between font-black text-sm border-t-2 border-black/20 pt-1">
              <span>Total</span><span className="tabular">{cur}{money(quote.totalCents)}</span>
            </p>
          </div>
        </div>
        {(quote.notes || quote.validUntil) && (
          <div className="border-t border-black/10 pt-2 text-[11.5px] text-black/80 space-y-1">
            {quote.validUntil && <p><span className="font-bold">Valid until:</span> {quote.validUntil}</p>}
            {quote.notes && <p className="whitespace-pre-wrap">{quote.notes}</p>}
          </div>
        )}
      </div>
    </Modal>
  )
}
