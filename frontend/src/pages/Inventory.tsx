// Inventory — products & categories CRUD, stock adjust, CSV import/export.
// products.manage gates mutations; the page itself needs products.view.
// Also hosts the category manager (add / inline rename / delete with the
// 409 "still has products" guard) and product photo upload (≤2 MB raster
// images via the public /products/:id/image route, thumbnails in the list).

import { useEffect, useMemo, useRef, useState } from 'react'
import { api, downloadFile, isDemoSync, productImageUrl, Category, Product } from '../lib/api'
import { demoProductImageUrl } from '../demo/backend'
import { useAuth } from '../stores/auth'
import { Button, Card, EmptyState, Field, Input, Modal, MoneyInput, Select, Spinner, Table, Tabs } from '../components/ui'
import { centsToAmount } from '../lib/money'
import { toast } from '../stores/toasts'
import { Package, Upload, Download, Plus, PackagePlus, Image as ImageIcon, Pencil, Trash2 } from 'lucide-react'

/** <img> src for a product photo. On a static/demo host the /image route
 * doesn't exist, so the demo backend's in-memory data URL is used instead. */
function productPhotoSrc(p: Product): string {
  if (isDemoSync()) return demoProductImageUrl(p.id)
  return productImageUrl(p)
}

/** Small lazy thumbnail with an icon placeholder when no photo exists. */
function ProductThumb({ p }: { p: Product }) {
  const [failed, setFailed] = useState(false)
  const src = productPhotoSrc(p)
  if (!src || failed) {
    return (
      <span className="w-10 h-10 shrink-0 rounded-input border-2 border-line bg-surface-muted flex items-center justify-center text-ink-subtle" aria-hidden>
        <ImageIcon size={16} strokeWidth={2.25} />
      </span>
    )
  }
  return (
    <img
      src={src}
      alt=""
      loading="lazy"
      onError={() => setFailed(true)}
      className="w-10 h-10 shrink-0 rounded-input border-2 border-line object-cover bg-surface-muted"
    />
  )
}

export function Inventory() {
  const canManage = useAuth((s) => !!s.user?.permissions.includes('products.manage'))
  const [tab, setTab] = useState<'products' | 'categories'>('products')
  const [products, setProducts] = useState<Product[] | null>(null)
  const [categories, setCategories] = useState<Category[]>([])
  const [search, setSearch] = useState('')
  const [catFilter, setCatFilter] = useState('all')
  const [editing, setEditing] = useState<Product | 'new' | null>(null)
  const [stockFor, setStockFor] = useState<Product | null>(null)
  const [newCat, setNewCat] = useState('')
  const [addingCat, setAddingCat] = useState(false)
  const [editCatId, setEditCatId] = useState<number | null>(null)
  const [editCatName, setEditCatName] = useState('')
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

  // ---- Category manager (products.manage) ----
  const addCategory = async () => {
    const name = newCat.trim()
    if (!name || addingCat) return
    setAddingCat(true)
    try {
      await api.post('/api/v1/categories', { name })
      toast.success('Category added', name)
      setNewCat('')
      load()
    } catch (e: any) {
      toast.error('Could not add category', e?.message)
    } finally {
      setAddingCat(false)
    }
  }

  const renameCategory = async (id: number) => {
    const name = editCatName.trim()
    if (!name) return
    try {
      await api.put(`/api/v1/categories/${id}`, { name })
      toast.success('Category renamed', name)
      setEditCatId(null)
      load()
    } catch (e: any) {
      toast.error('Rename failed', e?.message)
    }
  }

  const deleteCategory = async (c: Category) => {
    try {
      await api.del(`/api/v1/categories/${c.id}`)
      toast.success('Category deleted', c.name)
      load()
    } catch (e: any) {
      if (e?.status === 409) {
        toast.error('Category still has products', 'Move or reassign its products first.')
      } else {
        toast.error('Delete failed', e?.message)
      }
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
                <Button variant="secondary" size="sm" onClick={() => fileRef.current?.click()}>
                  <Upload size={14} strokeWidth={2.5} aria-hidden />
                  Import CSV
                </Button>
                <input
                  ref={fileRef}
                  type="file"
                  accept=".csv,text/csv"
                  className="hidden"
                  onChange={(e) => e.target.files?.[0] && doImport(e.target.files[0])}
                />
              </label>
              <Button
                variant="secondary"
                size="sm"
                onClick={async () => {
                  try {
                    await downloadFile('/api/v1/products/export', 'products.csv')
                    toast.success('Export downloaded', 'products.csv')
                  } catch (e: any) {
                    toast.error('Export failed', e?.message)
                  }
                }}
              >
                <Download size={14} strokeWidth={2.5} aria-hidden />
                Export
              </Button>
              <Button variant="primary" size="sm" onClick={() => setEditing('new')}>
                <Plus size={14} strokeWidth={2.5} aria-hidden />
                Product
              </Button>
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
            <EmptyState icon={<Package size={24} strokeWidth={2.25} />} title="No products" body={canManage ? 'Create your first product.' : 'Nothing matches the filter.'} />
          ) : (
            <Table head={['Product', 'Category', 'Price', 'Stock', ...(canManage ? [''] : [])]}>
              {filtered.map((p) => (
                <tr key={p.id} className={p.active ? '' : 'opacity-50'}>
                  <td className="px-3 py-2.5">
                    <div className="flex items-center gap-2.5">
                      <ProductThumb p={p} />
                      <div className="min-w-0">
                        <p className="font-bold text-ink text-[13px] leading-tight">{p.name}</p>
                        <p className="text-[11px] text-ink-subtle">{p.sku}{p.barcode ? ` · ${p.barcode}` : ''}</p>
                      </div>
                    </div>
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
                      <span className="inline-flex items-center gap-1">
                        {p.trackStock && (
                          <Button size="sm" variant="ghost" onClick={() => setStockFor(p)} title="Receive / adjust stock">
                            <PackagePlus size={14} strokeWidth={2.5} aria-hidden />
                            Stock
                          </Button>
                        )}
                        <Button size="sm" variant="ghost" onClick={() => setEditing(p)}>Edit</Button>
                      </span>
                    </td>
                  )}
                </tr>
              ))}
            </Table>
          )
        ) : (
          <>
            {canManage && (
              <div className="px-4 py-3 border-b-2 border-line flex flex-wrap gap-2">
                <Input
                  value={newCat}
                  onChange={(e) => setNewCat(e.target.value)}
                  onKeyDown={(e) => e.key === 'Enter' && addCategory()}
                  placeholder="New category name"
                  className="flex-1 min-w-44"
                />
                <Button variant="primary" size="sm" onClick={addCategory} disabled={addingCat || !newCat.trim()}>
                  {addingCat ? <Spinner className="w-4 h-4 border-t-brand-ink" /> : <Plus size={14} strokeWidth={2.5} aria-hidden />}
                  Add category
                </Button>
              </div>
            )}
            <Table head={['Category', 'Slug', 'Products', ...(canManage ? [''] : [])]}>
              {categories.map((c) => (
                <tr key={c.id}>
                  <td className="px-3 py-2.5 font-bold text-ink text-[13px]">
                    {editCatId === c.id ? (
                      <span className="flex items-center gap-1.5">
                        <Input
                          value={editCatName}
                          onChange={(e) => setEditCatName(e.target.value)}
                          onKeyDown={(e) => e.key === 'Enter' && renameCategory(c.id)}
                          className="max-w-52"
                          autoFocus
                        />
                        <Button size="sm" variant="primary" onClick={() => renameCategory(c.id)} disabled={!editCatName.trim()}>
                          Save
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => setEditCatId(null)}>
                          Cancel
                        </Button>
                      </span>
                    ) : c.name}
                  </td>
                  <td className="px-3 py-2.5 text-ink-muted text-[13px] font-mono">{c.slug}</td>
                  <td className="px-3 py-2.5 tabular text-ink">{c.productCount ?? 0}</td>
                  {canManage && (
                    <td className="px-3 py-2.5 text-right whitespace-nowrap">
                      <span className="inline-flex items-center gap-1">
                        <Button size="sm" variant="ghost" onClick={() => { setEditCatId(c.id); setEditCatName(c.name) }} disabled={editCatId === c.id}>
                          <Pencil size={14} strokeWidth={2.5} aria-hidden />
                          Rename
                        </Button>
                        <Button size="sm" variant="ghost" className="text-danger-text hover:bg-danger-bg" onClick={() => deleteCategory(c)}>
                          <Trash2 size={14} strokeWidth={2.5} aria-hidden />
                          Delete
                        </Button>
                      </span>
                    </td>
                  )}
                </tr>
              ))}
            </Table>
          </>
        )}
      </Card>

      {editing && <ProductModal product={editing === 'new' ? null : editing} categories={categories} onClose={() => setEditing(null)} onSaved={load} />}

      {stockFor && <StockModal product={stockFor} onClose={() => setStockFor(null)} onSaved={load} />}
    </div>
  )
}

// StockModal — receive stock / adjust with a reason (the admin's daily
// "add stock" flow, without opening the full product editor).
function StockModal({ product, onClose, onSaved }: { product: Product; onClose: () => void; onSaved: () => void }) {
  const [delta, setDelta] = useState('')
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const n = parseInt(delta, 10)
  const valid = !isNaN(n) && n !== 0

  const save = async () => {
    setBusy(true)
    setError('')
    try {
      await api.post(`/api/v1/products/${product.id}/adjust-stock`, { delta: n, reason: reason.trim() || undefined })
      toast.success('Stock updated', `${product.name}: ${product.stockQty} → ${Math.max(0, product.stockQty + n)}`)
      onSaved()
      onClose()
    } catch (e: any) {
      setError(e?.message || 'Adjust failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={`Stock — ${product.name}`} size="sm" footer={
      <>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={save} disabled={busy || !valid}>
          {busy ? <Spinner className="border-t-brand-ink" /> : 'Apply adjustment'}
        </Button>
      </>
    }>
      <div className="space-y-3">
        <p className="text-sm text-ink-muted">Current stock: <strong className="text-ink tabular">{product.stockQty}</strong></p>
        <Field label="Quantity change" hint="Positive receives stock (e.g. 20), negative corrects overshoots (e.g. -3).">
          <Input
            value={delta}
            onChange={(e) => setDelta(e.target.value.replace(/[^\d-]/g, ''))}
            inputMode="numeric"
            placeholder="e.g. 20"
            className="tabular text-lg font-bold"
            autoFocus
          />
        </Field>
        <Field label="Reason (optional)" hint="Recorded in the audit log — e.g. “received from supplier”">
          <Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Received from supplier" />
        </Field>
        {valid && (
          <p className="text-[13px] text-ink-muted">
            New stock level: <strong className={`tabular ${n > 0 ? 'text-paid-text' : 'text-danger-text'}`}>{Math.max(0, product.stockQty + n)}</strong>
          </p>
        )}
        {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
      </div>
    </Modal>
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
  // Product photos: existing products upload straight away; new products
  // keep the file until create succeeds, then upload to the fresh id.
  const [pendingPhoto, setPendingPhoto] = useState<File | null>(null)
  const [photoBusy, setPhotoBusy] = useState(false)
  const [previewSrc, setPreviewSrc] = useState(product ? productPhotoSrc(product) : '')
  const [hasPhoto, setHasPhoto] = useState<boolean | null>(product ? null : false)

  const save = async () => {
    setBusy(true)
    setError('')
    try {
      let savedId = product?.id ?? 0
      if (product) {
        await api.put(`/api/v1/products/${product.id}`, form)
        toast.success('Product updated', form.name)
      } else {
        const created = await api.post<Product>('/api/v1/products', form)
        savedId = created?.id ?? 0
        toast.success('Product created', form.name)
      }
      if (pendingPhoto && savedId) {
        try {
          const fd = new FormData()
          fd.append('file', pendingPhoto)
          await api.form(`/api/v1/products/${savedId}/image`, fd)
          toast.success('Photo uploaded', form.name)
        } catch (e: any) {
          // The product itself is saved — surface the photo failure softly.
          toast.error('Photo upload failed', e?.message)
        }
      }
      onSaved()
      onClose()
    } catch (e: any) {
      setError(e?.message || 'Save failed')
    } finally {
      setBusy(false)
    }
  }

  const pickPhoto = async (f: File | undefined) => {
    if (!f) return
    if (f.size > 2 * 1024 * 1024) {
      toast.error('Image too large', 'Maximum 2 MB (png, jpeg, webp, gif)')
      return
    }
    if (!product) {
      setPendingPhoto(f)
      return
    }
    setPhotoBusy(true)
    try {
      const fd = new FormData()
      fd.append('file', f)
      const res = await api.form<{ imageUrl: string }>(`/api/v1/products/${product.id}/image`, fd)
      setPreviewSrc(res.imageUrl) // demo returns the data URL; real a cache-busted path
      setHasPhoto(true)
      toast.success('Photo updated', product.name)
      onSaved()
    } catch (e: any) {
      toast.error('Photo upload failed', e?.message)
    } finally {
      setPhotoBusy(false)
    }
  }

  const removePhoto = async () => {
    if (!product) {
      setPendingPhoto(null)
      return
    }
    setPhotoBusy(true)
    try {
      await api.del(`/api/v1/products/${product.id}/image`)
      setPreviewSrc('')
      setHasPhoto(false)
      toast.success('Photo removed', product.name)
      onSaved()
    } catch (e: any) {
      toast.error('Could not remove photo', e?.message)
    } finally {
      setPhotoBusy(false)
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
          <Field
            label="Photo"
            hint="PNG, JPEG, WebP or GIF — max 2 MB. Shows on the till and inventory."
          >
            <div className="flex gap-3 items-start">
              <div className="w-28 h-28 shrink-0 rounded-input border-2 border-line-strong bg-surface-muted flex items-center justify-center overflow-hidden" aria-hidden>
                {previewSrc && hasPhoto !== false ? (
                  <img
                    src={previewSrc}
                    alt={`Photo of ${form.name || 'product'}`}
                    className="w-full h-full object-contain"
                    onError={() => setHasPhoto(false)}
                    onLoad={() => setHasPhoto(true)}
                  />
                ) : (
                  <ImageIcon size={28} strokeWidth={2} className="text-ink-subtle" />
                )}
              </div>
              <div className="flex-1 min-w-0 space-y-2">
                {product ? (
                  <div className="flex flex-wrap items-center gap-2">
                    <label>
                      <Button size="sm" variant="secondary" disabled={photoBusy}>
                        <Upload size={14} strokeWidth={2.5} aria-hidden />
                        {hasPhoto ? 'Replace photo' : 'Upload photo'}
                      </Button>
                      <input
                        type="file"
                        accept="image/png,image/jpeg,image/webp,image/gif"
                        className="hidden"
                        onChange={(e) => { pickPhoto(e.target.files?.[0]); e.currentTarget.value = '' }}
                      />
                    </label>
                    {hasPhoto && (
                      <Button size="sm" variant="ghost" className="text-danger-text hover:bg-danger-bg" onClick={removePhoto} disabled={photoBusy}>
                        <Trash2 size={14} strokeWidth={2.5} aria-hidden />
                        Remove
                      </Button>
                    )}
                    {photoBusy && <Spinner />}
                  </div>
                ) : (
                  <div className="flex flex-wrap items-center gap-2">
                    <label>
                      <Button size="sm" variant="secondary">
                        <Upload size={14} strokeWidth={2.5} aria-hidden />
                        Choose photo
                      </Button>
                      <input
                        type="file"
                        accept="image/png,image/jpeg,image/webp,image/gif"
                        className="hidden"
                        onChange={(e) => {
                          const f = e.target.files?.[0]
                          if (f && f.size > 2 * 1024 * 1024) {
                            toast.error('Image too large', 'Maximum 2 MB (png, jpeg, webp, gif)')
                            e.currentTarget.value = ''
                            return
                          }
                          setPendingPhoto(f ?? null)
                          e.currentTarget.value = ''
                        }}
                      />
                    </label>
                    {pendingPhoto && <span className="text-[12px] text-ink-muted truncate max-w-44">{pendingPhoto.name}</span>}
                  </div>
                )}
                <p className="text-[11px] text-ink-subtle">
                  {product ? 'Photos are public (like the brand logo) and served cache-busted.' : 'The photo uploads right after the product is created.'}
                </p>
              </div>
            </div>
          </Field>
        </div>
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
