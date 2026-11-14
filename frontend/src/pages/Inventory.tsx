// Inventory — products & categories CRUD, stock adjust, CSV import/export.
// products.manage gates mutations; the page itself needs products.view.
// Also hosts the category manager (add / inline rename / delete with the
// 409 "still has products" guard) and product photo upload (≤2 MB raster
// images via the public /products/:id/image route, thumbnails in the list).

import { useEffect, useMemo, useRef, useState } from 'react'
import { api, downloadFile, Category, Product, ProductBatch, StockMovement } from '../lib/api'
import { productPhotoCandidates } from '../lib/photos'
import { ProductPhoto } from '../components/ProductPhoto'
import { useAuth } from '../stores/auth'
import { useBranding } from '../stores/branding'
import { Button, Card, EmptyState, Field, Input, Modal, MoneyInput, Select, Spinner, StatusPill, Table, Tabs } from '../components/ui'
import { StockCountPanel } from '../components/StockCount'
import { printBarcodeLabels } from '../lib/labels'
import { centsToAmount, formatMoney } from '../lib/money'
import { UNITS, fmtQty, isFractionUnit, parseQtyInput, unitLabel } from '../lib/qty'
import { normalizeProductImage, PHOTO_ACCEPT } from '../lib/photo'
import { toast } from '../stores/toasts'
import { unitProfit } from '../lib/profit'
import { lowStockAt } from '../lib/stock'
import { Package, Upload, Download, Plus, PackagePlus, Image as ImageIcon, Pencil, Trash2, Printer, History, PackageMinus, Layers, TriangleAlert } from 'lucide-react'
import { fmtDateTime } from '../lib/when'
import { ClassPicker, TAX_TYPES } from '../components/EtimsPanel'

/** Editor preview: the product's CURRENT best photo (uploaded first, then
 *  the demo's bundled fallback) — unlike the thumbnails this must NOT walk
 *  the whole chain; the cashier is editing what the server actually has. */
function editorPhotoSrc(p: Product): string {
  return productPhotoCandidates(p)[0] ?? ''
}

/** Thumbnail via the shared ProductPhoto chain (uploaded → bundled →
 *  monogram) — the inventory list shows exactly what the POS shows. */
function ProductThumb({ p }: { p: Product }) {
  return (
    <span className="w-10 h-10 shrink-0 rounded-input border-2 border-line bg-surface-muted overflow-hidden flex" aria-hidden>
      <ProductPhoto p={p} />
    </span>
  )
}

/** v1.8.0 expiry badge tone for a YYYY-MM-DD date ('' = undated). */
function expiryTone(d: string): 'danger' | 'warn' | 'none' {
  if (!d) return 'none'
  const today = new Date()
  today.setHours(0, 0, 0, 0)
  const diff = Math.round((Date.parse(d + 'T00:00:00') - today.getTime()) / 864e5)
  if (diff < 0) return 'danger'
  if (diff <= 30) return 'warn'
  return 'none'
}

function ExpiryCell({ d }: { d: string }) {
  const tone = expiryTone(d)
  return (
    <span
      className={`inline-flex items-center gap-1 px-1.5 py-0.5 rounded-pill text-[11px] font-bold tabular whitespace-nowrap ${
        tone === 'danger' ? 'bg-danger-bg text-danger-text' : tone === 'warn' ? 'bg-pending-bg text-pending-text' : 'text-ink-muted'
      }`}
    >
      {tone === 'danger' && <TriangleAlert size={11} strokeWidth={2.5} aria-hidden />}
      {d || '—'}
    </span>
  )
}

// Stock-ledger kinds → badge colours (semantic tokens: gray = neutral,
// red = stock out the door, green = stock back on the shelf, blue = manual
// adjustment, amber = count session).
const MOVEMENT_META: Record<string, { label: string; cls: string }> = {
  OPENING: { label: 'Opening', cls: 'bg-void-bg text-void-text' },
  SALE: { label: 'Sale', cls: 'bg-danger-bg text-danger-text' },
  VOID_RESTORE: { label: 'Void restored', cls: 'bg-paid-bg text-paid-text' },
  ADJUSTMENT: { label: 'Adjustment', cls: 'bg-info-bg text-info-text' },
  COUNT: { label: 'Count', cls: 'bg-pending-bg text-pending-text' },
  PO_RECEIVE: { label: 'PO received', cls: 'bg-paid-bg text-paid-text' },
  RETURN_RESTOCK: { label: 'Return restock', cls: 'bg-paid-bg text-paid-text' },
}

export function Inventory() {
  const canManage = useAuth((s) => !!s.user?.permissions.includes('products.manage'))
  // Stock counts ride the same permission the Go route group uses
  // (suppliers.manage — the Suppliers page's stock-management gate).
  const canStocktake = useAuth((s) => !!s.user?.permissions.includes('suppliers.manage'))
  const [tab, setTab] = useState<'products' | 'categories' | 'stockcount'>('products')
  const [products, setProducts] = useState<Product[] | null>(null)
  const [categories, setCategories] = useState<Category[]>([])
  const [search, setSearch] = useState('')
  const [catFilter, setCatFilter] = useState('all')
  const [editing, setEditing] = useState<Product | 'new' | null>(null)
  const [stockFor, setStockFor] = useState<Product | null>(null)
  const [disposeFor, setDisposeFor] = useState<Product | null>(null)
  // v1.8.0 batch/expiry lots — the per-product lots panel (read + goods-in + recall).
  const [batchesFor, setBatchesFor] = useState<Product | null>(null)
  // v1.6.3 stock ledger — the per-product movement history (read-only).
  const [historyFor, setHistoryFor] = useState<Product | null>(null)
  const [movements, setMovements] = useState<StockMovement[] | null>(null)
  const [newCat, setNewCat] = useState('')
  const [addingCat, setAddingCat] = useState(false)
  const [editCatId, setEditCatId] = useState<number | null>(null)
  const [editCatName, setEditCatName] = useState('')
  const [labelPicks, setLabelPicks] = useState<number[]>([])
  const [showArchived, setShowArchived] = useState(false)
  const [removing, setRemoving] = useState<number | null>(null)
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
      if (!showArchived && !p.active) return false
      if (catFilter !== 'all' && p.categoryId !== Number(catFilter)) return false
      if (q && !p.name.toLowerCase().includes(q) && !p.sku.toLowerCase().includes(q) && !p.barcode.includes(q)) return false
      return true
    })
  }, [products, search, catFilter, showArchived])

  // ---- Product removal (products.manage) — soft archive, restorable ----
  // Archived products vanish from the POS immediately but keep their order
  // history intact; restore brings them back with stock and pricing.
  const removeProduct = async (p: Product) => {
    if (removing) return
    if (!window.confirm(`Remove “${p.name}” from the POS?\n\nIt disappears from the sell screen right away. Past sales keep their history, and you can restore it anytime with “Show archived”.`)) return
    setRemoving(p.id)
    try {
      await api.del(`/api/v1/products/${p.id}`)
      toast.success('Product removed', `${p.name} is archived, restore it anytime.`)
      load()
    } catch (e: any) {
      toast.error('Remove failed', e?.message)
    } finally {
      setRemoving(null)
    }
  }

  const restoreProduct = async (p: Product) => {
    if (removing) return
    setRemoving(p.id)
    try {
      // UpdateProduct is a FULL replace: anything omitted resets (stock
      // to 0, tracking on, barcode cleared). Send the archived row's
      // values so a restore brings the product back exactly as it was.
      await api.put(`/api/v1/products/${p.id}`, {
        name: p.name,
        categoryId: p.categoryId,
        priceCents: p.priceCents,
        costCents: p.costCents,
        stockQtyMilli: p.stockQtyMilli,
        trackStock: p.trackStock,
        barcode: p.barcode || '',
        // v1.6.3 fields — a full replace would silently reset them to 0.
        wholesalePriceCents: p.wholesalePriceCents ?? 0,
        lowStockUnits: p.lowStockUnits ?? 0,
        // v1.8.0 fields — same full-replace rule.
        unit: p.unit ?? 'pc',
        allowFraction: p.allowFraction ?? false,
        trackBatches: p.trackBatches ?? false,
        active: true,
      })
      toast.success('Product restored', p.name)
      load()
    } catch (e: any) {
      toast.error('Restore failed', e?.message)
    } finally {
      setRemoving(null)
    }
  }

  const openHistory = async (p: Product) => {
    setHistoryFor(p)
    setMovements(null)
    try {
      setMovements(await api.get<StockMovement[]>(`/api/v1/products/${p.id}/movements?limit=100`))
    } catch (e: any) {
      toast.error('History failed', e?.message)
    }
  }

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

  // ---- Barcode label printing (products.manage) ----
  const doPrintLabels = () => {
    const picks = (products ?? []).filter((p) => labelPicks.includes(p.id))
    if (picks.length === 0) return
    const n = printBarcodeLabels(picks, useBranding.getState().branding.store_name || '')
    if (n === 0) {
      toast.error('Popup blocked', 'Allow popups for this page to print labels.')
    } else {
      toast.success('Labels ready', `${n} label${n === 1 ? '' : 's'}, 50×30 mm, use the print dialog`)
    }
  }

  const togglePick = (id: number, on: boolean) => {
    setLabelPicks((picks) => (on ? [...new Set([...picks, id])] : picks.filter((x) => x !== id)))
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
              <Button
                variant="secondary"
                size="sm"
                onClick={doPrintLabels}
                disabled={labelPicks.length === 0}
                title={labelPicks.length ? 'Print price labels with Code128 barcodes' : 'Tick products in the list below to print their price labels'}
              >
                <Printer size={14} strokeWidth={2.5} aria-hidden />
                Print labels{labelPicks.length ? ` (${labelPicks.length})` : ''}
              </Button>
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
              ...(canStocktake ? [{ key: 'stockcount' as const, label: 'Stock count' }] : []),
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
              <label className="inline-flex items-center gap-1.5 text-[12.5px] font-semibold text-ink-muted cursor-pointer select-none whitespace-nowrap">
                <input
                  type="checkbox"
                  className="w-4 h-4 accent-[#0047AB]"
                  checked={showArchived}
                  onChange={(e) => setShowArchived(e.target.checked)}
                />
                Show archived{products ? ` (${products.filter((p) => !p.active).length})` : ''}
              </label>
            </>
          )}
          {tab === 'stockcount' && (
            <p className="text-[12px] text-ink-subtle">Count sessions, variance and apply-to-stock, nothing leaves the building uncounted.</p>
          )}
        </div>

        {tab === 'stockcount' ? (
          <StockCountPanel onChanged={load} />
        ) : tab === 'products' ? (
          !products ? (
            <div className="py-12 flex justify-center"><Spinner /></div>
          ) : filtered.length === 0 ? (
            <EmptyState icon={<Package size={24} strokeWidth={2.25} />} title="No products" body={canManage ? 'Create your first product.' : 'Nothing matches the filter.'} />
          ) : (
            <Table head={[...(canManage ? [<input
              key="selall"
              type="checkbox"
              aria-label="Select all products for label printing"
              className="w-4 h-4 accent-[#10B981]"
              checked={filtered.length > 0 && filtered.every((p) => labelPicks.includes(p.id))}
              onChange={(e) => setLabelPicks(e.target.checked ? filtered.map((p) => p.id) : [])}
            />] : []), 'Product', 'Category', 'Price', 'Stock', ...(canManage ? [''] : [])]}>
              {filtered.map((p) => (
                <tr key={p.id} className={p.active ? '' : 'opacity-50'}>
                  {canManage && (
                    <td className="px-3 py-2.5 w-8">
                      <input
                        type="checkbox"
                        aria-label={`Select ${p.name} for label printing`}
                        className="w-4 h-4 accent-[#10B981]"
                        checked={labelPicks.includes(p.id)}
                        onChange={(e) => togglePick(p.id, e.target.checked)}
                      />
                    </td>
                  )}
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
                      <span
                        className={`font-bold tabular text-[13px] ${p.stockQtyMilli <= 0 ? 'text-danger-text' : p.stockQtyMilli <= lowStockAt(p) * 1000 ? 'text-pending-text' : 'text-ink'}`}
                        title={(p.lowStockUnits ?? 0) > 0 ? `Low-stock alert at ${lowStockAt(p)} ${unitLabel(p.unit)} (product setting)` : undefined}
                      >
                        {fmtQty(p.stockQtyMilli)}{p.unit ? ` ${p.unit}` : ''}
                      </span>
                    ) : (
                      <span className="text-ink-subtle text-[12px]">service</span>
                    )}
                  </td>
                  {canManage && (
                    <td className="px-3 py-2.5 text-right whitespace-nowrap">
                      <span className="inline-flex items-center gap-1">
                        {p.active && (
                          <Button size="sm" variant="ghost" onClick={() => setEditing(p)}>
                            <Pencil size={14} strokeWidth={2.5} aria-hidden />
                            Edit
                          </Button>
                        )}
                        <Button size="sm" variant="ghost" onClick={() => openHistory(p)} title="Stock ledger — sales, adjustments, counts">
                          <History size={14} strokeWidth={2.5} aria-hidden />
                          History
                        </Button>
                        {p.trackStock && p.active && (
                          <Button size="sm" variant="ghost" onClick={() => setStockFor(p)} title="Receive / adjust stock">
                            <PackagePlus size={14} strokeWidth={2.5} aria-hidden />
                            Stock
                          </Button>
                        )}
                        {p.trackStock && p.active && (
                          <Button size="sm" variant="ghost" onClick={() => setDisposeFor(p)} title="Record a loss — expired, spoilt, lost or damaged">
                            <PackageMinus size={14} strokeWidth={2.5} aria-hidden />
                            Loss
                          </Button>
                        )}
                        {p.trackBatches && p.active && (
                          <Button size="sm" variant="ghost" onClick={() => setBatchesFor(p)} title="Batches and expiry lots">
                            <Layers size={14} strokeWidth={2.5} aria-hidden />
                            Batches
                          </Button>
                        )}
                        {p.active ? (
                          <Button
                            size="sm"
                            variant="ghost"
                            className="text-danger-text hover:bg-danger-bg"
                            onClick={() => removeProduct(p)}
                            disabled={removing === p.id}
                            title="Remove from the POS (archived, restorable)"
                          >
                            {removing === p.id ? <Spinner className="w-4 h-4 border-t-danger-text" /> : <Trash2 size={14} strokeWidth={2.5} aria-hidden />}
                            Remove
                          </Button>
                        ) : (
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => restoreProduct(p)}
                            disabled={removing === p.id}
                            title="Put this product back on the POS"
                          >
                            Restore
                          </Button>
                        )}
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
      {disposeFor && <DisposeModal product={disposeFor} onClose={() => setDisposeFor(null)} onSaved={load} />}
      {batchesFor && <BatchesModal product={batchesFor} onClose={() => setBatchesFor(null)} onChanged={load} />}

      {/* v1.6.3 stock ledger — same shape as the Customers page's ledger
          modal: spinner → empty state → table. Rendered newest-first. */}
      <Modal
        open={historyFor !== null}
        onClose={() => { setHistoryFor(null); setMovements(null) }}
        title={historyFor ? `Stock history, ${historyFor.name}` : 'Stock history'}
        size="lg"
      >
        {!movements ? (
          <div className="py-8 flex justify-center"><Spinner /></div>
        ) : movements.length === 0 ? (
          <EmptyState icon={<History size={24} strokeWidth={2.25} />} title="No stock movements yet" body="Sales, adjustments and count sessions for this product will appear here." />
        ) : (
          <Table head={['When', 'Kind', 'Change', 'Balance', 'Reference', 'Note', 'By']}>
            {[...movements]
              .sort((a, b) => b.createdAt.localeCompare(a.createdAt) || b.id - a.id)
              .map((m) => {
                const meta = MOVEMENT_META[m.kind] ?? { label: m.kind, cls: 'bg-void-bg text-void-text' }
                return (
                  <tr key={m.id}>
                    <td className="px-3 py-2 text-[12px] text-ink-subtle whitespace-nowrap">{fmtDateTime(m.createdAt)}</td>
                    <td className="px-3 py-2">
                      <span className={`inline-flex items-center px-2 py-0.5 rounded-pill text-[11px] font-bold whitespace-nowrap ${meta.cls}`}>
                        {meta.label}
                      </span>
                    </td>
                    <td className={`px-3 py-2 text-[12px] font-bold tabular ${m.deltaMilli > 0 ? 'text-paid-text' : m.deltaMilli < 0 ? 'text-danger-text' : 'text-ink-muted'}`}>
                      {m.deltaMilli > 0 ? `+${fmtQty(m.deltaMilli)}` : fmtQty(m.deltaMilli)}
                    </td>
                    <td className="px-3 py-2 text-[12px] font-bold tabular text-ink">{fmtQty(m.balanceAfterMilli)}</td>
                    <td className="px-3 py-2 text-[12px] text-ink-muted max-w-36 truncate">{m.reference || '—'}</td>
                    <td className="px-3 py-2 text-[12px] text-ink-muted max-w-48 truncate">{m.note || '—'}</td>
                    <td className="px-3 py-2 text-[12px] text-ink-muted whitespace-nowrap">{m.username}</td>
                  </tr>
                )
              })}
          </Table>
        )}
      </Modal>
    </div>
  )
}

// StockModal — receive stock / adjust with a reason (the admin's daily
// "add stock" flow, without opening the full product editor).
// v1.8.0: the input is in UNITS (decimals for weighed products) and the
// wire takes deltaMilli — a whole-unit adjust stays a whole-unit delta.
function StockModal({ product, onClose, onSaved }: { product: Product; onClose: () => void; onSaved: () => void }) {
  const [delta, setDelta] = useState('')
  const [reason, setReason] = useState('')
  // v1.6.3: a free-text note rides the adjustment into the stock ledger
  // (separate from the reason — the ledger prints both).
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  // Optional leading minus (a correction), magnitude parsed by the shared
  // milli parser; 0 stays out (the endpoint rejects zero deltas).
  const deltaMilli = (() => {
    const t = delta.trim()
    if (t === '' || t === '-') return null
    const neg = t.startsWith('-')
    const mag = parseQtyInput(neg ? t.slice(1) : t)
    return mag === null ? null : neg ? -mag : mag
  })()
  const valid = deltaMilli !== null && deltaMilli !== 0
  const newStockMilli = valid ? Math.max(0, product.stockQtyMilli + (deltaMilli as number)) : 0

  const save = async () => {
    if (deltaMilli === null) return
    setBusy(true)
    setError('')
    try {
      await api.post(`/api/v1/products/${product.id}/adjust-stock`, {
        deltaMilli,
        reason: reason.trim() || undefined,
        note: note.trim() || undefined,
      })
      toast.success('Stock updated', `${product.name}: ${fmtQty(product.stockQtyMilli)} → ${fmtQty(newStockMilli)}`)
      onSaved()
      onClose()
    } catch (e: any) {
      setError(e?.message || 'Adjust failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={`Stock, ${product.name}`} size="sm" footer={
      <>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={save} disabled={busy || !valid}>
          {busy ? <Spinner className="border-t-brand-ink" /> : 'Apply adjustment'}
        </Button>
      </>
    }>
      <div className="space-y-3">
        <p className="text-sm text-ink-muted">Current stock: <strong className="text-ink tabular">{fmtQty(product.stockQtyMilli)}{product.unit ? ` ${product.unit}` : ''}</strong></p>
        <Field
          label="Quantity change"
          hint={product.allowFraction
            ? `Positive receives (${unitLabel(product.unit)}s, decimals OK), negative corrects overshoots (e.g. -0.5).`
            : 'Positive receives stock (e.g. 20), negative corrects overshoots (e.g. -3).'}
        >
          <Input
            value={delta}
            onChange={(e) => setDelta(e.target.value.replace(/[^\d.-]/g, ''))}
            inputMode="decimal"
            placeholder="e.g. 20"
            className="tabular text-lg font-bold"
            autoFocus
          />
        </Field>
        <Field label="Reason (optional)" hint="Recorded in the audit log, e.g. “received from supplier”">
          <Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Received from supplier" />
        </Field>
        <Field label="Note (optional)" hint="Printed in the stock ledger, e.g. “damaged in transit”">
          <Input value={note} onChange={(e) => setNote(e.target.value)} />
        </Field>
        {valid && (
          <p className="text-[13px] text-ink-muted">
            New stock level: <strong className={`tabular ${deltaMilli! > 0 ? 'text-paid-text' : 'text-danger-text'}`}>{fmtQty(newStockMilli)}{product.unit ? ` ${product.unit}` : ''}</strong>
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
    // v1.6.3: wholesale tier price (0 = same as retail) and the per-product
    // low-stock threshold (0 = shop default). Persisted on PUT/POST below.
    wholesalePriceCents: product?.wholesalePriceCents ?? 0,
    lowStockUnits: product?.lowStockUnits ?? 0,
    // v1.8.0: stock is typed in UNITS (decimals for weighed goods) and the
    // wire takes stockQtyMilli; unit/allowFraction/trackBatches ride along.
    stockUnits: product ? fmtQty(product.stockQtyMilli) : '0',
    unit: (product?.unit ?? 'pc') as string,
    allowFraction: product?.allowFraction ?? false,
    trackBatches: product?.trackBatches ?? false,
    // v1.10 KRA eTIMS ('' = shop default from Settings → eTIMS)
    etimsTaxType: product?.etimsTaxType ?? '',
    etimsClsCd: product?.etimsClsCd ?? '',
    trackStock: product?.trackStock ?? true,
    isGiftCard: product?.isGiftCard ?? false,
    active: product?.active ?? true,
  })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // Product photos: existing products upload straight away; new products
  // keep the file until create succeeds, then upload to the fresh id.
  const [pendingPhoto, setPendingPhoto] = useState<File | null>(null)
  const [photoBusy, setPhotoBusy] = useState(false)
  const [previewSrc, setPreviewSrc] = useState(product ? editorPhotoSrc(product) : '')
  const [hasPhoto, setHasPhoto] = useState<boolean | null>(product ? null : false)
  // File pickers are opened EXPLICITLY via refs: wrapping the Button in a
  // <label> looked right but a click on a native <button> inside a label
  // never reaches the input (HTML activation behavior) — the "Choose photo"
  // dialog silently never opened (the Windows complaint).
  const newPhotoInputRef = useRef<HTMLInputElement>(null)
  const editPhotoInputRef = useRef<HTMLInputElement>(null)

  const save = async () => {
    setBusy(true)
    setError('')
    try {
      let savedId = product?.id ?? 0
      // Units text → milli: empty/0 stays 0; anything else rides the shared
      // milli parser (decimals only make sense when allowFraction is on,
      // but the parser accepts them either way — the server validates).
      const unitsText = form.stockUnits.trim()
      const stockMilli = unitsText === '' || Number(unitsText) === 0
        ? 0
        : parseQtyInput(unitsText)
      if (stockMilli === null) {
        setBusy(false)
        setError('Stock quantity must look like 12 or 0.5 (max 3 decimals).')
        return
      }
      // Gift cards sell value, not shelf stock — trackStock is forced off
      // here and the backend forces it too.
      const payload = {
        name: form.name,
        sku: form.sku,
        barcode: form.barcode,
        categoryId: form.categoryId,
        priceCents: form.priceCents,
        costCents: form.costCents,
        wholesalePriceCents: form.wholesalePriceCents,
        lowStockUnits: form.lowStockUnits,
        stockQtyMilli: stockMilli,
        unit: form.unit,
        allowFraction: form.allowFraction,
        trackBatches: form.trackBatches,
        trackStock: form.isGiftCard ? false : form.trackStock,
        isGiftCard: form.isGiftCard,
        active: form.active,
        etimsTaxType: form.etimsTaxType,
        etimsClsCd: form.etimsClsCd,
      }
      if (product) {
        await api.put(`/api/v1/products/${product.id}`, payload)
        toast.success('Product updated', form.name)
      } else {
        const created = await api.post<Product>('/api/v1/products', payload)
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
      // Raw backend validator dumps ("Key: 'x' Error:Field validation…") mean
      // nothing to a shop attendant — translate the common cases.
      const raw = e?.message || 'Save failed'
      const friendly = /Field validation.*'(\w+)'/.exec(raw)
      const field = friendly?.[1] || ''
      const known: Record<string, string> = {
        Name: 'Product name is required.',
        CategoryID: 'Please pick a category.',
        PriceCents: 'Price must be zero or more.',
        CostCents: 'Cost must be zero or more.',
      }
      if (/products\.sku/i.test(raw) && /(unique|duplicate)/i.test(raw)) {
        // Raw "UNIQUE constraint failed: products.sku (2067)" means nothing
        // to a shop attendant — say what to do instead.
        setError('That SKU is already in use. Change it, or clear the SKU field to auto-generate a new one.')
      } else {
        setError(known[field] || raw)
      }
    } finally {
      setBusy(false)
    }
  }

  const pickPhoto = async (f: File | undefined) => {
    if (!f) return
    if (f.size > 8 * 1024 * 1024) {
      // normalizeProductImage compresses big phone photos down to the
      // server's 2 MB cap; this hard stop only fires when even that
      // cannot help (an 8 MB+ source image).
      toast.error('Image too large', 'Please pick a photo under 8 MB')
      return
    }
    if (!product) {
      setPendingPhoto(await normalizeProductImage(f))
      return
    }
    setPhotoBusy(true)
    try {
      const up = await normalizeProductImage(f)
      const fd = new FormData()
      fd.append('file', up)
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
    <Modal open onClose={onClose} title={product ? `Edit, ${product.name}` : 'New product'} footer={
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
            hint="PNG, JPEG, WebP or GIF. Large photos are compressed automatically. Shows on the till and inventory."
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
                    <Button size="sm" variant="secondary" disabled={photoBusy}
                      onClick={() => editPhotoInputRef.current?.click()}>
                      <Upload size={14} strokeWidth={2.5} aria-hidden />
                      {hasPhoto ? 'Replace photo' : 'Upload photo'}
                    </Button>
                    <input
                      ref={editPhotoInputRef}
                      type="file"
                      accept={PHOTO_ACCEPT}
                      className="hidden"
                      onChange={(e) => {
                        const input = e.currentTarget
                        const f = input.files?.[0]
                        input.value = ''
                        if (f) pickPhoto(f)
                      }}
                    />
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
                    <Button size="sm" variant="secondary" onClick={() => newPhotoInputRef.current?.click()}>
                      <Upload size={14} strokeWidth={2.5} aria-hidden />
                      Choose photo
                    </Button>
                    <input
                      ref={newPhotoInputRef}
                      type="file"
                      accept={PHOTO_ACCEPT}
                      className="hidden"
                      onChange={async (e) => {
                        const input = e.currentTarget
                        const f = input.files?.[0]
                        input.value = ''
                        if (!f) return
                        if (f.size > 8 * 1024 * 1024) {
                          toast.error('Image too large', 'Please pick a photo under 8 MB')
                          return
                        }
                        setPendingPhoto(await normalizeProductImage(f))
                      }}
                    />
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
            {categories.length === 0 && <option value={0}>Uncategorised</option>}
            {categories.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
          </Select>
        </Field>
        <Field label="Stock quantity" hint={form.allowFraction ? `Decimals OK (${unitLabel(form.unit)}s, e.g. 0.5)` : 'Whole units on the shelf'}>
          <Input
            type="number"
            value={form.stockUnits}
            onChange={(e) => setForm({ ...form, stockUnits: e.target.value.replace(/[^\d.]/g, '') })}
            inputMode="decimal"
            step={form.allowFraction ? 0.001 : 1}
            min={0}
          />
        </Field>
        <Field label="Selling price">
          <MoneyInput value={form.priceCents} onCents={(c) => setForm({ ...form, priceCents: c })} />
        </Field>
        <Field label="Buying price" hint="What you pay the supplier for one. Profit = selling price - VAT - this">
          <MoneyInput value={form.costCents} onCents={(c) => setForm({ ...form, costCents: c })} />
          <UnitProfitHint priceCents={form.priceCents} costCents={form.costCents} />
        </Field>
        <EtimsProductFields
          taxType={form.etimsTaxType}
          clsCd={form.etimsClsCd}
          onChange={(v) => setForm({ ...form, ...v })}
        />
        <Field label="Wholesale price (KES)" hint="0 = same as retail; wholesale customers auto-get this price at the till">
          <MoneyInput value={form.wholesalePriceCents} onCents={(c) => setForm({ ...form, wholesalePriceCents: c })} />
        </Field>
        <Field label="Low stock alert (units)" hint="0 = shop default">
          <Input
            type="number"
            value={form.lowStockUnits}
            onChange={(e) => setForm({ ...form, lowStockUnits: Number(e.target.value) })}
            inputMode="numeric"
          />
        </Field>
        <label className="flex items-center gap-2 min-h-11 text-sm font-semibold text-ink">
          <input
            type="checkbox"
            checked={form.trackStock}
            disabled={form.isGiftCard}
            onChange={(e) => setForm({ ...form, trackStock: e.target.checked })}
            className="w-5 h-5 accent-[#10B981]"
          />
          Track stock
        </label>
        <Field label="Unit" hint="How the till counts and displays this product">
          <Select
            value={form.unit}
            onChange={(e) => setForm({ ...form, unit: e.target.value, ...(isFractionUnit(e.target.value) ? { allowFraction: true } : {}) })}
          >
            {UNITS.map((u) => <option key={u} value={u}>{u}</option>)}
          </Select>
        </Field>
        <label className="flex items-center gap-2 min-h-11 text-sm font-semibold text-ink">
          <input
            type="checkbox"
            checked={form.allowFraction}
            onChange={(e) => setForm({ ...form, allowFraction: e.target.checked })}
            className="w-5 h-5 accent-[#10B981]"
          />
          Allow fractional quantities
        </label>
        <label className="flex items-center gap-2 min-h-11 text-sm font-semibold text-ink">
          <input
            type="checkbox"
            checked={form.trackBatches}
            onChange={(e) => setForm({ ...form, trackBatches: e.target.checked })}
            className="w-5 h-5 accent-[#10B981]"
          />
          Track batches / expiry
        </label>
        <label className="flex items-center gap-2 min-h-11 text-sm font-semibold text-ink">
          <input
            type="checkbox"
            checked={form.isGiftCard}
            onChange={(e) => setForm({ ...form, isGiftCard: e.target.checked, ...(e.target.checked ? { trackStock: false } : {}) })}
            className="w-5 h-5 accent-[#10B981]"
          />
          Gift card
        </label>
        <label className="flex items-center gap-2 min-h-11 text-sm font-semibold text-ink">
          <input type="checkbox" checked={form.active} onChange={(e) => setForm({ ...form, active: e.target.checked })} className="w-5 h-5 accent-[#10B981]" />
          Active (sellable)
        </label>
        {form.isGiftCard && (
          <p className="sm:col-span-2 text-[11px] text-ink-subtle -mt-2">
            Sells value, mints a redeemable code, never tracks stock. One code per unit is issued when the sale is paid;
            codes are redeemed into a customer's store credit from their profile.
          </p>
        )}
        {form.trackBatches && (
          <p className="sm:col-span-2 text-[11px] text-ink-subtle -mt-2">
            Every receipt becomes a dated lot: goods-in captures batch number + expiry, sales consume the
            earliest-expiring lot first, and the Reports page lists anything expired or expiring soon.
          </p>
        )}
        {error && <p role="alert" className="sm:col-span-2 text-danger-text text-sm font-semibold">{error}</p>}
      </div>
    </Modal>
  )
}


// DisposeModal (v1.7.0) — structured loss recording, the friend's SmartPOS
// disposal module: expired / spoilt / lost / damaged / other. The deduction
// is guarded (never below zero) and lands in the stock ledger as DISPOSAL
// with the loss value computable from product cost in the reports.
// v1.8.0: the wire takes qtyMilli (input in units, decimals for weighed
// goods) and a batch-tracked product can name the specific lot written off.
const DISPOSE_REASONS = [
  { value: 'expired', label: 'Expired' },
  { value: 'spoilt', label: 'Spoilt / damaged goods' },
  { value: 'lost', label: 'Lost / missing' },
  { value: 'damaged', label: 'Physically damaged' },
  { value: 'other', label: 'Other' },
] as const

function DisposeModal({ product, onClose, onSaved }: { product: Product; onClose: () => void; onSaved: () => void }) {
  const [qty, setQty] = useState('')
  const [reason, setReason] = useState('expired')
  const [note, setNote] = useState('')
  const [batchId, setBatchId] = useState(0)
  const [batches, setBatches] = useState<ProductBatch[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  // Batch-tracked lots: only ACTIVE lots with stock left can take a write-off.
  useEffect(() => {
    if (!product.trackBatches) return
    let alive = true
    api.get<ProductBatch[]>(`/api/v1/products/${product.id}/batches`)
      .then((list) => {
        if (!alive) return
        const usable = (Array.isArray(list) ? list : []).filter((b) => b.status === 'ACTIVE' && b.qtyRemainingMilli > 0)
        setBatches(usable)
      })
      .catch(() => { if (alive) setBatches([]) })
    return () => { alive = false }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [product.id])

  const qtyMilli = parseQtyInput(qty)
  const valid =
    qtyMilli !== null &&
    (!product.trackStock || qtyMilli <= product.stockQtyMilli)

  const save = async () => {
    if (qtyMilli === null) return
    setBusy(true)
    setError('')
    try {
      await api.post(`/api/v1/products/${product.id}/dispose`, {
        qtyMilli,
        reason,
        note: note.trim() || undefined,
        ...(product.trackBatches && batchId > 0 ? { batchId } : {}),
      })
      toast.success('Loss recorded', `${product.name}: ${fmtQty(qtyMilli)} ${unitLabel(product.unit)} (${reason})`)
      onSaved()
      onClose()
    } catch (e: any) {
      setError(e?.message || 'Disposal failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={`Record loss, ${product.name}`} size="sm" footer={
      <>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={save} disabled={busy || !valid}>
          {busy ? <Spinner className="border-t-brand-ink" /> : 'Record loss'}
        </Button>
      </>
    }>
      <div className="space-y-3">
        <p className="text-sm text-ink-muted">
          Current stock: <strong className="text-ink tabular">{fmtQty(product.stockQtyMilli)}{product.unit ? ` ${product.unit}` : ''}</strong>
          {(product.costCents ?? 0) > 0 && qtyMilli !== null && (
            <> · loss value ≈ <strong className="text-danger-text tabular">{centsToAmount(Math.round(product.costCents * qtyMilli / 1000))}</strong></>
          )}
        </p>
        <Field label={product.allowFraction ? `Quantity written off (${unitLabel(product.unit)}s, decimals OK)` : 'Quantity written off'}>
          <Input value={qty} onChange={(e) => setQty(e.target.value.replace(/[^\d.]/g, ''))} inputMode="decimal" autoFocus placeholder="0" />
        </Field>
        {product.trackBatches && (
          <Field label="Batch (optional)" hint="Name the exact lot written off when it matters for expiry.">
            <Select value={batchId} onChange={(e) => setBatchId(Number(e.target.value))}>
              <option value={0}>{batches === null ? 'Loading lots…' : 'No specific lot'}</option>
              {(batches ?? []).map((b) => (
                <option key={b.id} value={b.id}>
                  {`${b.batchNo || '(no batch)'} · exp ${b.expiryDate || '—'} · ${fmtQty(b.qtyRemainingMilli)} left`}
                </option>
              ))}
            </Select>
          </Field>
        )}
        <Field label="Reason" hint="Shows in the disposal report so losses can be explained, not guessed.">
          <Select value={reason} onChange={(e) => setReason(e.target.value)}>
            {DISPOSE_REASONS.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
          </Select>
        </Field>
        <Field label="Note (optional)" hint="e.g. fridge died overnight, torn packaging, suspected theft.">
          <Input value={note} onChange={(e) => setNote(e.target.value)} />
        </Field>
        {error && <p className="text-[12px] font-bold text-danger-text">{error}</p>}
      </div>
    </Modal>
  )
}

// BatchesModal (v1.8.0) — the per-product lots panel for batch-tracked
// products: list (ACTIVE first, FEFO order as the server returns them),
// goods-in (batchNo, expiry date, whole units, cost) and recall of an
// ACTIVE lot with a reason. Expiry renders red once past, amber within
// 30 days.
function BatchesModal({ product, onClose, onChanged }: { product: Product; onClose: () => void; onChanged: () => void }) {
  const [batches, setBatches] = useState<ProductBatch[] | null>(null)
  const [adding, setAdding] = useState(false)
  const [batchNo, setBatchNo] = useState('')
  const [expiryDate, setExpiryDate] = useState('')
  const [qtyUnits, setQtyUnits] = useState('')
  const [cost, setCost] = useState(String((product.costCents ?? 0) / 100))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // Recall flow: the reason is required — capture it inline instead of a
  // browser prompt (same audit rule as the void-reason modal).
  const [recallFor, setRecallFor] = useState<ProductBatch | null>(null)
  const [recallReason, setRecallReason] = useState('')

  const load = async () => {
    try {
      const list = await api.get<ProductBatch[]>(`/api/v1/products/${product.id}/batches`)
      setBatches(Array.isArray(list) ? list : [])
    } catch (e: any) {
      toast.error('Could not load batches', e?.message)
      setBatches([])
    }
  }
  useEffect(() => { load() /* eslint-disable-line react-hooks/exhaustive-deps */ }, [product.id])

  const goodsIn = async () => {
    if (busy) return
    const units = Number(qtyUnits)
    if (!Number.isFinite(units) || units <= 0 || !Number.isInteger(units)) {
      setError('Goods-in quantity must be a whole number of units (lots are received whole).')
      return
    }
    setBusy(true)
    setError('')
    try {
      const costCents = cost.trim() !== '' ? Math.max(0, Math.round(Number(cost) * 100)) : 0
      await api.post<ProductBatch>(`/api/v1/products/${product.id}/batches`, {
        batchNo: batchNo.trim(),
        expiryDate,
        qtyMilli: Math.round(units) * 1000,
        costCents,
        supplierId: 0,
        note: '',
      })
      toast.success('Batch received', `${batchNo.trim() || 'New lot'}, ${units} ${unitLabel(product.unit)}`)
      setAdding(false)
      setBatchNo('')
      setExpiryDate('')
      setQtyUnits('')
      await load()
      onChanged()
    } catch (e: any) {
      setError(e?.message || 'Could not record the batch')
    } finally {
      setBusy(false)
    }
  }

  const recall = async () => {
    if (!recallFor || busy) return
    if (!recallReason.trim()) {
      setError('A recall reason is required.')
      return
    }
    setBusy(true)
    setError('')
    try {
      const b = await api.post<ProductBatch>(`/api/v1/batches/${recallFor.id}/recall`, { reason: recallReason.trim() })
      toast.success('Batch recalled', `${b.batchNo || 'Lot'} pulled off the shelf, remaining stock written off`)
      setRecallFor(null)
      setRecallReason('')
      await load()
      onChanged()
    } catch (e: any) {
      setError(e?.message || 'Recall failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title={`Batches, ${product.name}`} size="lg">
      <div className="space-y-3">
        <p className="text-[13px] text-ink-muted">
          Lots sell earliest-expiry-first (FEFO). Current stock:{' '}
          <strong className="text-ink tabular">{fmtQty(product.stockQtyMilli)}{product.unit ? ` ${product.unit}` : ''}</strong>
        </p>

        {!batches ? (
          <div className="py-8 flex justify-center"><Spinner /></div>
        ) : batches.length === 0 ? (
          <EmptyState icon={<Layers size={24} strokeWidth={2.25} />} title="No batches yet" body="Record the first goods-in below to start tracking lots and expiry." />
        ) : (
          <Table head={['Batch', 'Expiry', 'Received', 'Remaining', 'Unit cost', 'Status', '']}>
            {batches.map((b) => (
              <tr key={b.id} className={b.status === 'RECALLED' ? 'opacity-60' : ''}>
                <td className="px-3 py-2">
                  <p className="font-semibold text-ink text-[13px]">{b.batchNo || '(no batch)'}</p>
                  {b.reference && <p className="text-[11px] text-ink-subtle max-w-40 truncate">{b.reference}</p>}
                </td>
                <td className="px-3 py-2"><ExpiryCell d={b.expiryDate} /></td>
                <td className="px-3 py-2 tabular text-[13px] text-ink-muted">{fmtQty(b.qtyReceivedMilli)}</td>
                <td className="px-3 py-2 tabular text-[13px] font-bold text-ink">{fmtQty(b.qtyRemainingMilli)}{product.unit ? ` ${product.unit}` : ''}</td>
                <td className="px-3 py-2 tabular text-[13px] text-ink-muted">{formatMoney(b.costCents)}</td>
                <td className="px-3 py-2">
                  <StatusPill
                    status={b.status === 'ACTIVE' ? 'paid' : b.status === 'RECALLED' ? 'danger' : 'void'}
                    label={b.status === 'ACTIVE' ? 'Active' : b.status === 'RECALLED' ? 'Recalled' : 'Exhausted'}
                  />
                </td>
                <td className="px-3 py-2 text-right">
                  {b.status === 'ACTIVE' && (
                    <Button
                      size="sm"
                      variant="ghost"
                      className="text-danger-text hover:bg-danger-bg"
                      onClick={() => { setRecallFor(b); setRecallReason('') }}
                      title="Pull this lot off the shelf — remaining stock is written off"
                    >
                      Recall
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </Table>
        )}

        {recallFor && (
          <div className="border-2 border-danger-text/40 bg-danger-bg rounded-input p-3 space-y-2">
            <p className="text-[13px] font-bold text-danger-text">Recall lot {recallFor.batchNo || '(no batch)'} — {fmtQty(recallFor.qtyRemainingMilli)} {unitLabel(product.unit)} will be written off</p>
            <div className="flex gap-2 items-end">
              <div className="flex-1">
                <Input
                  value={recallReason}
                  onChange={(e) => setRecallReason(e.target.value)}
                  placeholder="Reason, e.g. supplier recall notice RC-2291"
                  autoFocus
                />
              </div>
              <Button size="sm" variant="ghost" onClick={() => setRecallFor(null)} disabled={busy}>Cancel</Button>
              <Button size="sm" variant="danger" onClick={recall} disabled={busy || !recallReason.trim()}>
                {busy ? <Spinner className="w-4 h-4 border-t-danger-text" /> : 'Recall lot'}
              </Button>
            </div>
          </div>
        )}

        {!adding ? (
          <Button variant="secondary" size="sm" onClick={() => { setAdding(true); setError('') }}>
            <Plus size={14} strokeWidth={2.5} aria-hidden />
            Add batch
          </Button>
        ) : (
          <div className="border-2 border-line rounded-input p-3 space-y-2.5 bg-surface-muted">
            <p className="text-[13px] font-bold text-ink">Goods-in — new lot</p>
            <div className="grid sm:grid-cols-2 gap-2.5">
              <Field label="Batch number (optional)">
                <Input value={batchNo} onChange={(e) => setBatchNo(e.target.value)} placeholder="e.g. LOT-2409B" />
              </Field>
              <Field label="Expiry date (optional)">
                <Input type="date" value={expiryDate} onChange={(e) => setExpiryDate(e.target.value)} />
              </Field>
              <Field label="Quantity (whole units)" hint="Lots are received whole, never weighed in">
                <Input value={qtyUnits} onChange={(e) => setQtyUnits(e.target.value.replace(/\D/g, ''))} inputMode="numeric" placeholder="0" />
              </Field>
              <Field label="Unit cost (KES)" hint="0 = use the product's cost">
                <Input value={cost} onChange={(e) => setCost(e.target.value.replace(/[^\d.]/g, ''))} inputMode="decimal" placeholder="0.00" />
              </Field>
            </div>
            <div className="flex gap-2 justify-end">
              <Button size="sm" variant="ghost" onClick={() => { setAdding(false); setError('') }} disabled={busy}>Cancel</Button>
              <Button size="sm" variant="primary" onClick={goodsIn} disabled={busy || qtyUnits.trim() === ''}>
                {busy ? <Spinner className="w-4 h-4 border-t-brand-ink" /> : 'Receive batch'}
              </Button>
            </div>
          </div>
        )}

        {error && <p role="alert" className="text-[12px] font-bold text-danger-text">{error}</p>}
      </div>
    </Modal>
  )
}

// v1.10 — live "you make KES X on each" under the buying price.
function UnitProfitHint({ priceCents, costCents }: { priceCents: number; costCents: number }) {
  const b = useBranding((st) => st.branding)
  if (!costCents) return <p className="mt-1 text-[12px] text-pending-text">No buying price: sales of this item will show as unknown profit.</p>
  const u = unitProfit(priceCents, costCents, Number(b.tax_percent) || 0, !!b.tax_included)
  if (!u) return null
  return (
    <p className={`mt-1 text-[12px] font-semibold ${u.profitCents >= 0 ? 'text-paid-text' : 'text-danger-text'}`}>
      {u.profitCents >= 0 ? 'You make ' : 'You LOSE '}{formatMoney(Math.abs(u.profitCents))} on each ({u.marginPct.toFixed(1)}%){Number(b.tax_percent) > 0 && b.tax_included ? ', after VAT' : ''}
    </p>
  )
}

// v1.10 — KRA eTIMS per product: blank = the shop default (Settings → eTIMS).
function EtimsProductFields({ taxType, clsCd, onChange }: {
  taxType: string; clsCd: string; onChange: (v: { etimsTaxType?: string; etimsClsCd?: string }) => void
}) {
  const [picking, setPicking] = useState(false)
  return (
    <div className="sm:col-span-2 rounded-input border border-line p-3 space-y-2">
      <p className="text-[13px] font-bold text-ink">KRA eTIMS <span className="font-normal text-ink-subtle">(blank = shop default)</span></p>
      <div className="grid sm:grid-cols-2 gap-3">
        <Field label="Tax type">
          <Select value={taxType} onChange={(e) => onChange({ etimsTaxType: e.target.value })}>
            <option value="">Shop default</option>
            {TAX_TYPES.map((t) => <option key={t.code} value={t.code}>{t.label}</option>)}
          </Select>
        </Field>
        <div>
          <span className="block text-[13px] font-semibold text-ink-muted mb-1">Item class</span>
          <div className="flex items-center gap-2 min-h-11">
            <span className="font-mono text-[13px]">{clsCd || 'Shop default'}</span>
            <Button size="sm" variant="ghost" onClick={() => setPicking((p) => !p)}>{picking ? 'Close' : 'Change'}</Button>
            {clsCd && <Button size="sm" variant="ghost" onClick={() => onChange({ etimsClsCd: '' })}>Use default</Button>}
          </div>
        </div>
      </div>
      {picking && <ClassPicker onPick={(c) => { onChange({ etimsClsCd: c.code }); setPicking(false) }} />}
    </div>
  )
}
