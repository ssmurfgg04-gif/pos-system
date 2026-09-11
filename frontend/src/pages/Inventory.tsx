// Inventory — products & categories CRUD, stock adjust, CSV import/export.
// products.manage gates mutations; the page itself needs products.view.

import { useEffect, useMemo, useRef, useState } from 'react'
import { api, Category, Product } from '../lib/api'
import { useAuth } from '../stores/auth'
import { Button, Card, EmptyState, Field, Input, Modal, MoneyInput, Select, Spinner, Table, Tabs } from '../components/ui'
import { centsToAmount } from '../lib/money'
import { toast } from '../stores/toasts'

export function Inventory() {
  const canManage = useAuth((s) => !!s.user?.permissions.includes('products.manage'))
  const [tab, setTab] = useState<'products' | 'categories'>('products')
  const [products, setProducts] = useState<Product[] | null>(null)
  const [categories, setCategories] = useState<Category[]>([])
  const [search, setSearch] = useState('')
  const [catFilter, setCatFilter] = useState('all')
  const [editing, setEditing] = useState<Product | 'new' | null>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  const load = async () => {
    try {
      const [p, c] = await Promise.all([
        api.get<Product[]>('/api/v1/products'),
        api.get<Category[]>('/api/v1/categories'),
      ])
      setProducts(p)
      setCategories(c)
    } catch (e: any) {
      toast.error('Load failed', e?.message)
    }
  }
  useEffect(() => { load() }, [])

  const filtered = useMemo(() => {
    if (!products) return []
    const q = search.trim().toLowerCase()
    return products.filter((p) => {
      if (catFilter !== 'all' && p.categoryId !== Number(catFilter)) return false
      if (q && !p.name.toLowerCase().includes(q) && !p.sku.toLowerCase().includes(q) && !p.barcode.includes(q)) return false
      return true
    })
  }, [products, search, catFilter])

  const doImport = async (file: File) => {
    const form = new FormData()
    form.append('file', file)
    try {
      const res = await api.form<{ created: number; updated: number }>('/api/v1/products/import', form)
      toast.success('Import complete', `${res.created} created, ${res.updated} updated`)
      load()
    } catch (e: any) {
      toast.error('Import failed', e?.message)
    }
  }

  return (
    <div className="space-y-4">
      <Card
        title="Inventory"
        sub={products ? `${products.filter((p) => p.active).length} active products` : 'Loading…'}
        actions={
          canManage && (
            <>
              <label className="hidden sm:inline-flex">
                <Button variant="secondary" size="sm" onClick={() => fileRef.current?.click()}>↑ Import CSV</Button>
                <input
                  ref={fileRef}
                  type="file"
                  accept=".csv,text/csv"
                  className="hidden"
                  onChange={(e) => e.target.files?.[0] && doImport(e.target.files[0])}
                />
              </label>
              <a href="/api/v1/products/export" className="hidden sm:inline-flex">
                <Button variant="secondary" size="sm">↓ Export</Button>
              </a>
              <Button variant="primary" size="sm" onClick={() => setEditing('new')}>+ Product</Button>
            </>
          )
        }
        pad={false}
      >
        <div className="px-4 py-3 flex flex-wrap gap-2 items-center">
          <Tabs
            tabs={[
              { key: 'products' as const, label: 'Products' },
              { key: 'categories' as const, label: 'Categories' },
            ]}
            value={tab}
            onChange={setTab}
          />
          {tab === 'products' && (
            <>
              <Input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search name / SKU / barcode"
                className="flex-1 min-w-44"
              />
              <Select value={catFilter} onChange={(e) => setCatFilter(e.target.value)} className="w-44">
                <option value="all">All categories</option>
                {categories.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
              </Select>
            </>
          )}
        </div>

        {tab === 'products' ? (
          !products ? (
            <div className="py-12 flex justify-center"><Spinner /></div>
          ) : filtered.length === 0 ? (
            <EmptyState icon="📦" title="No products" body={canManage ? 'Create your first product.' : 'Nothing matches the filter.'} />
          ) : (
            <Table head={['Product', 'Category', 'Price', 'Stock', ...(canManage ? [''] : [])]}>
              {filtered.map((p) => (
                <tr key={p.id} className={p.active ? '' : 'opacity-50'}>
                  <td className="px-3 py-2.5">
                    <p className="font-bold text-ink text-[13px] leading-tight">{p.name}</p>
                    <p className="text-[11px] text-ink-subtle">{p.sku}{p.barcode ? ` · ${p.barcode}` : ''}</p>
                  </td>
                  <td className="px-3 py-2.5 text-ink-muted text-[13px]">{p.categoryName}</td>
                  <td className="px-3 py-2.5 font-semibold tabular text-ink">{centsToAmount(p.priceCents)}</td>
                  <td className="px-3 py-2.5">
                    {p.trackStock ? (
                      <span className={`font-bold tabular text-[13px] ${p.stockQty <= 0 ? 'text-danger-text' : p.stockQty <= 5 ? 'text-pending-text' : 'text-ink'}`}>
                        {p.stockQty}
                      </span>
                    ) : (
                      <span className="text-ink-subtle text-[12px]">service</span>
                    )}
                  </td>
                  {canManage && (
                    <td className="px-3 py-2.5 text-right whitespace-nowrap">
                      <Button size="sm" variant="ghost" onClick={() => setEditing(p)}>Edit</Button>
                    </td>
                  )}
                </tr>
              ))}
            </Table>
          )
        ) : (
          <Table head={['Category', 'Slug', 'Products', ...(canManage ? [''] : [])]}>
            {categories.map((c) => (
              <tr key={c.id}>
                <td className="px-3 py-2.5 font-bold text-ink text-[13px]">{c.name}</td>
                <td className="px-3 py-2.5 text-ink-muted text-[13px] font-mono">{c.slug}</td>
                <td className="px-3 py-2.5 tabular text-ink">{c.productCount ?? 0}</td>
                {canManage && <td className="px-3 py-2.5"><CategoryEditor category={c} onSaved={load} /></td>}
              </tr>
            ))}
          </Table>
        )}
      </Card>

      {editing && <ProductModal product={editing === 'new' ? null : editing} categories={categories} onClose={() => setEditing(null)} onSaved={load} />}
    </div>
  )
}

function ProductModal({
  product,
  categories,
  onClose,
  onSaved,
}: {
  product: Product | null
  categories: Category[]
  onClose: () => void
  onSaved: () => void
}) {
  const [form, setForm] = useState({
    name: product?.name ?? '',
    sku: product?.sku ?? '',
    barcode: product?.barcode ?? '',
    categoryId: product?.categoryId ?? categories[0]?.id ?? 0,
    priceCents: product?.priceCents ?? 0,
    costCents: product?.costCents ?? 0,
    stockQty: product?.stockQty ?? 0,
    trackStock: product?.trackStock ?? true,
    active: product?.active ?? true,
  })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const save = async () => {
    setBusy(true)
    setError('')
    try {
      if (product) {
        await api.put(`/api/v1/products/${product.id}`, form)
        toast.success('Product updated', form.name)
      } else {
        await api.post('/api/v1/products', form)
        toast.success('Product created', form.name)
      }
      onSaved()
      onClose()
    } catch (e: any) {
      setError(e?.message || 'Save failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={product ? `Edit — ${product.name}` : 'New product'} footer={
      <>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={save} disabled={busy || !form.name.trim()}>
          {busy ? <Spinner className="border-t-brand-ink" /> : product ? 'Save changes' : 'Create product'}
        </Button>
      </>
    }>
      <div className="grid sm:grid-cols-2 gap-3">
        <div className="sm:col-span-2">
          <Field label="Name">
            <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} autoFocus required />
          </Field>
        </div>
        <Field label="SKU" hint="Unique. Auto-generated when blank.">
          <Input value={form.sku} onChange={(e) => setForm({ ...form, sku: e.target.value })} />
        </Field>
        <Field label="Barcode" hint="EAN-13 or any scanner code">
          <Input value={form.barcode} onChange={(e) => setForm({ ...form, barcode: e.target.value })} inputMode="numeric" />
        </Field>
        <Field label="Category">
          <Select value={form.categoryId} onChange={(e) => setForm({ ...form, categoryId: Number(e.target.value) })}>
            {categories.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
          </Select>
        </Field>
        <Field label="Stock quantity">
          <Input
            type="number"
            value={form.stockQty}
            onChange={(e) => setForm({ ...form, stockQty: Number(e.target.value) })}
            inputMode="numeric"
          />
        </Field>
        <Field label="Selling price">
          <MoneyInput value={form.priceCents} onCents={(c) => setForm({ ...form, priceCents: c })} />
        </Field>
        <Field label="Cost price" hint="Used for margin reports">
          <MoneyInput value={form.costCents} onCents={(c) => setForm({ ...form, costCents: c })} />
        </Field>
        <label className="flex items-center gap-2 min-h-11 text-sm font-semibold text-ink">
          <input type="checkbox" checked={form.trackStock} onChange={(e) => setForm({ ...form, trackStock: e.target.checked })} className="w-5 h-5 accent-[#10B981]" />
          Track stock
        </label>
        <label className="flex items-center gap-2 min-h-11 text-sm font-semibold text-ink">
          <input type="checkbox" checked={form.active} onChange={(e) => setForm({ ...form, active: e.target.checked })} className="w-5 h-5 accent-[#10B981]" />
          Active (sellable)
        </label>
        {error && <p role="alert" className="sm:col-span-2 text-danger-text text-sm font-semibold">{error}</p>}
      </div>
    </Modal>
  )
}

function CategoryEditor({ category, onSaved }: { category: Category; onSaved: () => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState(category.name)
  const [error, setError] = useState('')

  const save = async () => {
    try {
      await api.put(`/api/v1/categories/${category.id}`, { name })
      toast.success('Category renamed', name)
      setOpen(false)
      onSaved()
    } catch (e: any) {
      setError(e?.message)
    }
  }

  const remove = async () => {
    try {
      await api.del(`/api/v1/categories/${category.id}`)
      toast.success('Category deleted')
      onSaved()
    } catch (e: any) {
      setError(e?.message)
    }
  }

  return (
    <Modal open={open} onClose={() => setOpen(false)} title="Category" size="sm" footer={
      <>
        <Button variant="danger" size="sm" onClick={remove}>Delete</Button>
        <Button variant="primary" size="sm" onClick={save} disabled={!name.trim()}>Rename</Button>
      </>
    }>
      {open && (
        <>
          <Field label="Name">
            <Input value={name} onChange={(e) => setName(e.target.value)} />
          </Field>
          {error && <p role="alert" className="text-danger-text text-sm font-semibold mt-2">{error}</p>}
        </>
      )}
      <div className="hidden">{category.slug}</div>
    </Modal>
  )
}
