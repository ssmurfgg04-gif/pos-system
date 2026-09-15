// Suppliers — supplier records, purchase orders (receive posts stock with
// weighted-average cost), and stock takes (count vs expected + variance).
// Viewing needs suppliers.view; everything mutating needs suppliers.manage.

import { useEffect, useState } from 'react'
import { api, Supplier, PurchaseOrder, StockTake, Product } from '../lib/api'
import { useAuth } from '../stores/auth'
import { formatMoney } from '../lib/money'
import { Button, Card, EmptyState, Field, Input, Modal, Select, Spinner, StatusPill, Table, Tabs, Textarea } from '../components/ui'
import { toast } from '../stores/toasts'
import { Truck, X } from 'lucide-react'

export function Suppliers() {
  const manage = useAuth((s) => !!s.user?.permissions.includes('suppliers.manage'))
  const [tab, setTab] = useState<'suppliers' | 'orders' | 'takes'>('suppliers')

  return (
    <div className="space-y-4">
      <Tabs
        tabs={[
          { key: 'suppliers' as const, label: 'Suppliers', icon: <Truck size={15} strokeWidth={2.25} aria-hidden /> },
          { key: 'orders' as const, label: 'Purchase orders' },
          { key: 'takes' as const, label: 'Stock takes' },
        ]}
        value={tab}
        onChange={setTab}
      />
      {tab === 'suppliers' && <SupplierList manage={manage} />}
      {tab === 'orders' && <POList manage={manage} />}
      {tab === 'takes' && <TakeList manage={manage} />}
    </div>
  )
}

function SupplierList({ manage }: { manage: boolean }) {
  const [suppliers, setSuppliers] = useState<Supplier[] | null>(null)
  const [search, setSearch] = useState('')
  const [editing, setEditing] = useState<Supplier | 'new' | null>(null)

  const load = async (q = search) => {
    try {
      setSuppliers(await api.get<Supplier[]>(`/api/v1/suppliers?search=${encodeURIComponent(q)}`))
    } catch (e: any) {
      toast.error('Load failed', e?.message)
    }
  }
  useEffect(() => { load('') }, [])
  useEffect(() => {
    const t = window.setTimeout(() => load(), 250)
    return () => window.clearTimeout(t)
  }, [search])

  return (
    <Card
      title="Suppliers"
      sub="Who you buy stock from"
      actions={manage ? <Button variant="primary" size="sm" onClick={() => setEditing('new')}>+ Supplier</Button> : undefined}
      pad={false}
    >
      <div className="px-4 py-3">
        <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Name or phone…" />
      </div>
      {!suppliers ? (
        <div className="py-12 flex justify-center"><Spinner /></div>
      ) : suppliers.length === 0 ? (
        <EmptyState icon={<Truck size={24} strokeWidth={2.25} />} title="No suppliers" />
      ) : (
        <Table head={['Supplier', 'Contact', 'Status', '']}>
          {suppliers.map((s) => (
            <tr key={s.id} className={s.active ? '' : 'opacity-50'}>
              <td className="px-3 py-2.5">
                <p className="font-bold text-ink text-[13px]">{s.name}</p>
                <p className="text-[11px] text-ink-subtle">{s.address || 'no address'}</p>
              </td>
              <td className="px-3 py-2.5 text-[13px] text-ink-muted">{s.phone || s.email || '—'}</td>
              <td className="px-3 py-2.5">
                <StatusPill status={s.active ? 'paid' : 'void'} label={s.active ? 'Active' : 'Inactive'} />
              </td>
              <td className="px-3 py-2.5 text-right">
                {manage && <Button size="sm" variant="ghost" onClick={() => setEditing(s)}>Edit</Button>}
              </td>
            </tr>
          ))}
        </Table>
      )}
      <Modal open={editing !== null} onClose={() => setEditing(null)} title={editing === 'new' ? 'New supplier' : 'Edit supplier'}>
        {editing && <SupplierForm initial={editing === 'new' ? null : editing} onDone={() => { setEditing(null); load() }} />}
      </Modal>
    </Card>
  )
}

function SupplierForm({ initial, onDone }: { initial: Supplier | null; onDone: () => void }) {
  const [name, setName] = useState(initial?.name || '')
  const [phone, setPhone] = useState(initial?.phone || '')
  const [email, setEmail] = useState(initial?.email || '')
  const [address, setAddress] = useState(initial?.address || '')
  const [notes, setNotes] = useState(initial?.notes || '')
  const [active, setActive] = useState(initial?.active ?? true)
  const [busy, setBusy] = useState(false)

  const save = async () => {
    if (!name.trim()) {
      toast.error('Name required')
      return
    }
    setBusy(true)
    try {
      const body = { name: name.trim(), phone: phone.trim(), email: email.trim(), address: address.trim(), notes: notes.trim(), ...(initial ? { active } : {}) }
      if (initial) {
        await api.put(`/api/v1/suppliers/${initial.id}`, body)
        toast.success('Supplier updated')
      } else {
        await api.post('/api/v1/suppliers', body)
        toast.success('Supplier added')
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
      <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} autoFocus placeholder="e.g. Nairobi Wholesalers" /></Field>
      <Field label="Phone"><Input value={phone} onChange={(e) => setPhone(e.target.value)} inputMode="tel" placeholder="07XX XXX XXX" /></Field>
      <Field label="Email"><Input value={email} onChange={(e) => setEmail(e.target.value)} inputMode="email" placeholder="orders@example.com" /></Field>
      <Field label="Address"><Input value={address} onChange={(e) => setAddress(e.target.value)} placeholder="Shop, street, town" /></Field>
      <Field label="Notes"><Textarea value={notes} onChange={(e) => setNotes(e.target.value)} placeholder="Delivery days, terms…" /></Field>
      {initial && (
        <label className="flex items-center gap-2 text-[13px] font-semibold text-ink">
          <input type="checkbox" checked={active} onChange={(e) => setActive(e.target.checked)} className="w-5 h-5" />
          Active
        </label>
      )}
      <Button variant="primary" size="lg" className="w-full" onClick={save} disabled={busy}>
        {busy ? <Spinner /> : initial ? 'Save' : 'Add supplier'}
      </Button>
    </div>
  )
}

function POList({ manage }: { manage: boolean }) {
  const [orders, setOrders] = useState<PurchaseOrder[] | null>(null)
  const [creating, setCreating] = useState(false)
  const [viewing, setViewing] = useState<PurchaseOrder | null>(null)

  const load = async () => {
    try {
      setOrders(await api.get<PurchaseOrder[]>('/api/v1/purchase-orders'))
    } catch (e: any) {
      toast.error('Load failed', e?.message)
    }
  }
  useEffect(() => { load() }, [])

  const receive = async (o: PurchaseOrder) => {
    try {
      await api.post(`/api/v1/purchase-orders/${o.id}/receive`, {})
      toast.success('Stock received', `${o.number} — quantities added, costs averaged`)
      load()
      setViewing(null)
    } catch (e: any) {
      toast.error('Receive failed', e?.message)
    }
  }

  return (
    <Card
      title="Purchase orders"
      sub="Receive posts stock + averages cost"
      actions={manage ? <Button variant="primary" size="sm" onClick={() => setCreating(true)}>+ Order</Button> : undefined}
      pad={false}
    >
      {!orders ? (
        <div className="py-12 flex justify-center"><Spinner /></div>
      ) : orders.length === 0 ? (
        <EmptyState icon={<Truck size={24} strokeWidth={2.25} />} title="No purchase orders" />
      ) : (
        <Table head={['Order', 'Status', 'Lines', 'Total cost', '']}>
          {orders.map((o) => (
            <tr key={o.id}>
              <td className="px-3 py-2.5">
                <p className="font-bold text-ink text-[13px] tabular">{o.number}</p>
                <p className="text-[11px] text-ink-subtle">{o.supplierName} · {new Date(o.createdAt).toLocaleDateString()}</p>
              </td>
              <td className="px-3 py-2.5">
                <StatusPill status={o.status === 'RECEIVED' ? 'paid' : o.status === 'PENDING' ? 'pending' : 'void'} label={o.status} />
              </td>
              <td className="px-3 py-2.5 tabular text-ink-muted">{o.items.length}</td>
              <td className="px-3 py-2.5 font-bold tabular text-ink">{formatMoney(o.subtotalCents)}</td>
              <td className="px-3 py-2.5 text-right whitespace-nowrap">
                <span className="inline-flex items-center gap-1">
                  <Button size="sm" variant="ghost" onClick={() => setViewing(o)}>View</Button>
                  {manage && o.status === 'PENDING' && <Button size="sm" variant="primary" onClick={() => receive(o)}>Receive</Button>}
                </span>
              </td>
            </tr>
          ))}
        </Table>
      )}
      <Modal open={creating} onClose={() => setCreating(false)} title="New purchase order" size="lg">
        <POForm onDone={() => { setCreating(false); load() }} />
      </Modal>
      <Modal open={viewing !== null} onClose={() => setViewing(null)} title={viewing ? `Order ${viewing.number}` : 'Order'} size="lg"
        footer={viewing && manage && viewing.status === 'PENDING'
          ? <><Button variant="ghost" onClick={() => setViewing(null)}>Close</Button><Button variant="primary" onClick={() => receive(viewing)}>Receive stock</Button></>
          : undefined}
      >
        {viewing && (
          <Table head={['Item', 'Qty', 'Unit cost', 'Line total']}>
            {viewing.items.map((i) => (
              <tr key={i.id}>
                <td className="px-3 py-2"><p className="font-semibold text-ink text-[13px]">{i.name}</p><p className="text-[11px] text-ink-subtle">{i.sku}</p></td>
                <td className="px-3 py-2 tabular">{i.qty}</td>
                <td className="px-3 py-2 tabular text-ink-muted">{formatMoney(i.costCents)}</td>
                <td className="px-3 py-2 tabular font-semibold">{formatMoney(i.lineTotalCents)}</td>
              </tr>
            ))}
          </Table>
        )}
      </Modal>
    </Card>
  )
}

function POForm({ onDone }: { onDone: () => void }) {
  const [suppliers, setSuppliers] = useState<Supplier[]>([])
  const [products, setProducts] = useState<Product[]>([])
  const [supplierId, setSupplierId] = useState('')
  const [note, setNote] = useState('')
  const [lines, setLines] = useState<{ productId: string; qty: string; cost: string }[]>([{ productId: '', qty: '', cost: '' }])
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    (async () => {
      try {
        const [s, p] = await Promise.all([
          api.get<Supplier[]>('/api/v1/suppliers?search='),
          api.get<Product[]>('/api/v1/products'),
        ])
        setSuppliers(s.filter((x) => x.active))
        setProducts(p.filter((x) => x.active))
      } catch (e: any) {
        toast.error('Load failed', e?.message)
      }
    })()
  }, [])

  const setLine = (i: number, patch: Partial<{ productId: string; qty: string; cost: string }>) =>
    setLines((ls) => ls.map((l, j) => (j === i ? { ...l, ...patch } : l)))

  const save = async () => {
    const items = lines
      .filter((l) => l.productId && Number(l.qty) > 0)
      .map((l) => {
        const prod = products.find((p) => p.id === Number(l.productId))
        return { productId: Number(l.productId), qty: Math.round(Number(l.qty)), costCents: Math.max(0, Math.round(Number(l.cost || prod?.costCents || 0) * 100)) }
      })
    if (!supplierId || items.length === 0) {
      toast.error('Pick a supplier and at least one line with quantity')
      return
    }
    setBusy(true)
    try {
      await api.post('/api/v1/purchase-orders', { supplierId: Number(supplierId), items, note: note.trim() })
      toast.success('Purchase order created')
      onDone()
    } catch (e: any) {
      toast.error('Save failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-3">
      <Field label="Supplier">
        <Select value={supplierId} onChange={(e) => setSupplierId(e.target.value)}>
          <option value="">Choose…</option>
          {suppliers.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
        </Select>
      </Field>
      {lines.map((l, i) => (
        <div key={i} className="grid grid-cols-[1fr_72px_110px_auto] gap-2 items-end">
          <Field label={i === 0 ? 'Product' : ''}>
            <Select value={l.productId} onChange={(e) => setLine(i, { productId: e.target.value, cost: String((products.find((p) => p.id === Number(e.target.value))?.costCents ?? 0) / 100) })}>
              <option value="">Choose…</option>
              {products.map((p) => <option key={p.id} value={p.id}>{p.name} ({formatMoney(p.costCents)})</option>)}
            </Select>
          </Field>
          <Field label={i === 0 ? 'Qty' : ''}>
            <Input value={l.qty} onChange={(e) => setLine(i, { qty: e.target.value.replace(/[^\d]/g, '') })} inputMode="numeric" placeholder="0" />
          </Field>
          <Field label={i === 0 ? 'Unit cost' : ''}>
            <Input value={l.cost} onChange={(e) => setLine(i, { cost: e.target.value.replace(/[^\d.]/g, '') })} inputMode="decimal" placeholder="0.00" />
          </Field>
          <Button size="sm" variant="ghost" onClick={() => setLines((ls) => ls.filter((_, j) => j !== i))} aria-label="Remove line"><X size={15} strokeWidth={2.25} aria-hidden /></Button>
        </div>
      ))}
      <Button size="sm" variant="secondary" onClick={() => setLines((ls) => [...ls, { productId: '', qty: '', cost: '' }])}>+ Line</Button>
      <Field label="Note (optional)"><Input value={note} onChange={(e) => setNote(e.target.value)} placeholder="Invoice no, delivery date…" /></Field>
      <Button variant="primary" size="lg" className="w-full" onClick={save} disabled={busy}>
        {busy ? <Spinner /> : 'Create order'}
      </Button>
    </div>
  )
}

function TakeList({ manage }: { manage: boolean }) {
  const [takes, setTakes] = useState<StockTake[] | null>(null)
  const [viewing, setViewing] = useState<StockTake | null>(null)
  const [counts, setCounts] = useState<Record<number, string>>({})
  const [busy, setBusy] = useState(false)

  const load = async () => {
    try {
      setTakes(await api.get<StockTake[]>('/api/v1/stock-takes'))
    } catch (e: any) {
      toast.error('Load failed', e?.message)
    }
  }
  useEffect(() => { load() }, [])

  const openTake = async (t: StockTake) => {
    try {
      const full = await api.get<StockTake>(`/api/v1/stock-takes/${t.id}`)
      setViewing(full)
      const c: Record<number, string> = {}
      full.items.forEach((i) => { c[i.productId] = String(i.countedQty) })
      setCounts(c)
    } catch (e: any) {
      toast.error('Load failed', e?.message)
    }
  }

  const create = async () => {
    try {
      const t = await api.post<StockTake>('/api/v1/stock-takes', { note: '' })
      toast.success('Count started', `${t.number} — ${t.items.length} lines`)
      load()
      openTake(t)
    } catch (e: any) {
      toast.error('Create failed', e?.message)
    }
  }

  const saveCounts = async () => {
    if (!viewing) return
    const payload: Record<string, number> = {}
    viewing.items.forEach((i) => {
      const v = counts[i.productId]
      payload[String(i.productId)] = v === undefined || v === '' ? i.countedQty : Math.max(0, Math.round(Number(v)))
    })
    setBusy(true)
    try {
      const updated = await api.post<StockTake>(`/api/v1/stock-takes/${viewing.id}/count`, { counts: payload })
      setViewing(updated)
      const c: Record<number, string> = {}
      updated.items.forEach((i) => { c[i.productId] = String(i.countedQty) })
      setCounts(c)
      toast.success('Counts saved')
    } catch (e: any) {
      toast.error('Save failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  const apply = async () => {
    if (!viewing) return
    setBusy(true)
    try {
      await saveCountsSilent()
      await api.post(`/api/v1/stock-takes/${viewing.id}/apply`, {})
      toast.success('Stock updated', 'Counted quantities are now live stock')
      setViewing(null)
      load()
    } catch (e: any) {
      toast.error('Apply failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  const saveCountsSilent = async () => {
    if (!viewing) return
    const payload: Record<string, number> = {}
    viewing.items.forEach((i) => {
      const v = counts[i.productId]
      payload[String(i.productId)] = v === undefined || v === '' ? i.countedQty : Math.max(0, Math.round(Number(v)))
    })
    const updated = await api.post<StockTake>(`/api/v1/stock-takes/${viewing.id}/count`, { counts: payload })
    setViewing(updated)
  }

  return (
    <Card
      title="Stock takes"
      sub="Count the shelves, apply the truth"
      actions={manage ? <Button variant="primary" size="sm" onClick={create}>+ Start count</Button> : undefined}
      pad={false}
    >
      {!takes ? (
        <div className="py-12 flex justify-center"><Spinner /></div>
      ) : takes.length === 0 ? (
        <EmptyState icon={<Truck size={24} strokeWidth={2.25} />} title="No stock takes" />
      ) : (
        <Table head={['Take', 'Status', 'Lines', '']}>
          {takes.map((t) => (
            <tr key={t.id}>
              <td className="px-3 py-2.5">
                <p className="font-bold text-ink text-[13px] tabular">{t.number}</p>
                <p className="text-[11px] text-ink-subtle">{new Date(t.createdAt).toLocaleDateString()}</p>
              </td>
              <td className="px-3 py-2.5">
                <StatusPill status={t.status === 'APPLIED' ? 'paid' : t.status === 'OPEN' ? 'pending' : 'void'} label={t.status} />
              </td>
              <td className="px-3 py-2.5 tabular text-ink-muted">{t.itemCount}</td>
              <td className="px-3 py-2.5 text-right">
                <Button size="sm" variant="ghost" onClick={() => openTake(t)}>Open</Button>
              </td>
            </tr>
          ))}
        </Table>
      )}
      <Modal open={viewing !== null} onClose={() => setViewing(null)} title={viewing ? `Count ${viewing.number}` : 'Count'} size="lg"
        footer={viewing && manage && viewing.status === 'OPEN'
          ? <><Button variant="ghost" onClick={() => setViewing(null)}>Close</Button><Button variant="secondary" onClick={saveCounts} disabled={busy}>{busy ? <Spinner /> : 'Save counts'}</Button><Button variant="primary" onClick={apply} disabled={busy}>Apply to stock</Button></>
          : undefined}
      >
        {viewing && (
          <Table head={['Item', 'Expected', 'Counted', 'Variance']}>
            {viewing.items.map((i) => {
              const counted = counts[i.productId] === undefined || counts[i.productId] === '' ? i.countedQty : Number(counts[i.productId])
              const variance = counted - i.expectedQty
              return (
                <tr key={i.id}>
                  <td className="px-3 py-2"><p className="font-semibold text-ink text-[13px]">{i.name}</p><p className="text-[11px] text-ink-subtle">{i.sku}</p></td>
                  <td className="px-3 py-2 tabular text-ink-muted">{i.expectedQty}</td>
                  <td className="px-3 py-2">
                    {manage && viewing.status === 'OPEN' ? (
                      <Input value={counts[i.productId] ?? ''} onChange={(e) => setCounts((c) => ({ ...c, [i.productId]: e.target.value.replace(/[^\d]/g, '') }))} inputMode="numeric" className="w-20" />
                    ) : (
                      <span className="tabular">{i.countedQty}</span>
                    )}
                  </td>
                  <td className={`px-3 py-2 tabular font-bold ${variance === 0 ? 'text-ink-muted' : variance > 0 ? 'text-paid-text' : 'text-danger-text'}`}>
                    {variance === 0 ? '—' : variance > 0 ? `+${variance}` : variance}
                  </td>
                </tr>
              )
            })}
          </Table>
        )}
      </Modal>
    </Card>
  )
}

