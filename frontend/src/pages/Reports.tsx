// Reports — daily summary cards, 7-day CSS bar chart (no chart lib —
// keeps the bundle tiny and works offline), payment split, top products;
// plus the Monthly (KRA) tab: one calendar month of VAT figures for the
// accountant's return, downloadable as CSV.

import { useEffect, useState } from 'react'
import { api, downloadFile, DailySummary, ExpiryBatchRow, ExpiryReport, MonthlySummary } from '../lib/api'
import { Button, Card, EmptyState, Input, Spinner, StatusPill, Tabs } from '../components/ui'
import { centsToAmount, formatMoney } from '../lib/money'
import { fmtQty } from '../lib/qty'
import { todayLocal, thisMonthLocal } from '../lib/katime'
import { toast } from '../stores/toasts'
import { BarChart3, Banknote, Smartphone, CreditCard, Wallet, Download, FileSpreadsheet, Undo2, ReceiptText, CalendarClock } from 'lucide-react'

// Local (EAT) date strings — toISOString() is UTC and put Nairobi's
// 00:00-03:00 sales on yesterday's report.
const today = todayLocal
const thisMonth = thisMonthLocal

/**
 * v1.6.3 net-of-returns sales for the "Money out / money back" card.
 * The server sends netSalesCents; older backends omit it and the same
 * figure derives client-side (sales − returns).
 */
export function netSalesOf(s: { salesCents: number; returnsCents?: number; netSalesCents?: number }): number {
  if (s.netSalesCents !== undefined) return s.netSalesCents
  return s.salesCents - (s.returnsCents ?? 0)
}

/**
 * v1.8.0: the daily summary reports shelf stock in milli-units
 * (stockQtyMilli); older backends send the legacy whole-unit stockUnits
 * field. Both stay optional in the shared type — read the milli one first.
 */
function shelfUnitsOf(s: DailySummary): string | null {
  const milli = (s as DailySummary & { stockQtyMilli?: number }).stockQtyMilli
  if (milli !== undefined) return fmtQty(milli)
  if (s.stockUnits !== undefined) return String(s.stockUnits)
  return null
}

export function Reports() {
  const [view, setView] = useState<'daily' | 'monthly'>('daily')
  return (
    <div className="space-y-4">
      <Tabs
        tabs={[
          { key: 'daily' as const, label: 'Daily', icon: <BarChart3 size={15} strokeWidth={2.25} aria-hidden /> },
          { key: 'monthly' as const, label: 'Monthly, KRA returns', icon: <FileSpreadsheet size={15} strokeWidth={2.25} aria-hidden /> },
        ]}
        value={view}
        onChange={setView}
      />
      {view === 'daily' ? <Daily /> : <Monthly />}
    </div>
  )
}

function Daily() {
  const [date, setDate] = useState(today())
  const [data, setData] = useState<DailySummary | null>(null)
  const [loading, setLoading] = useState(false)
  // Multi-store (P6): when the account belongs to more than one shop,
  // offer the consolidated all-shops view with per-shop rows.
  const [shops, setShops] = useState<{ id: string; name: string }[] | null>(null)
  const [allShops, setAllShops] = useState(false)

  useEffect(() => {
    api.get<{ id: string; name: string }[]>('/api/v1/shops').then((rows) => {
      if (rows && rows.length > 1) setShops(rows)
    }).catch(() => undefined)
  }, [])

  const [loadError, setLoadError] = useState('')
  const load = async (d: string) => {
    setLoading(true)
    setLoadError('')
    try {
      setData(await api.get<DailySummary>(`/api/v1/reports/daily?date=${d}${allShops ? '&shop=all' : ''}`))
    } catch (e: any) {
      // A failed request must never masquerade as "No sales on <date>" —
      // an owner reading that during an outage panics. Keep the last good
      // data on screen and say what actually happened.
      setLoadError(e?.message || 'Could not load the report, check the connection and retry')
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => { load(date) }, [date, allShops])

  const series = data?.series ?? [] // consolidated (all-shops) payloads carry no per-day series
  const max = Math.max(1, ...series.map((s) => s.salesCents), 1)

  return (
    <>
      <Card
        title="Daily report"
        sub={data?.consolidated ? 'All shops combined' : undefined}
        actions={
          <div className="flex gap-2 items-center">
            {shops && shops.length > 1 && (
              <label className="flex items-center gap-1.5 text-[12.5px] font-semibold text-ink cursor-pointer select-none">
                <input
                  type="checkbox"
                  checked={allShops}
                  onChange={(e) => setAllShops(e.target.checked)}
                  className="w-4 h-4 accent-[#0047AB]"
                />
                All shops
              </label>
            )}
            <Input type="date" value={date} max={today()} onChange={(e) => setDate(e.target.value)} className="w-40" />
            <Button size="sm" variant="secondary" onClick={() => setDate(today())}>Today</Button>
          </div>
        }
      >
      {loading && !data ? (
        <div className="py-12 flex justify-center"><Spinner /></div>
      ) : loadError ? (
        <EmptyState icon={<BarChart3 size={24} strokeWidth={2.25} />} title="Report could not load" body={loadError} />
      ) : !data || data.ordersPaid + data.ordersOpen + data.ordersVoided === 0 ? (
        <EmptyState icon={<BarChart3 size={24} strokeWidth={2.25} />} title={`No sales on ${date}`} body="Pick another day or start selling." />
      ) : (
        <>
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
            <Stat label="Sales" value={formatMoney(data.salesCents)} big />
            <Stat label="Orders paid" value={String(data.ordersPaid)} />
            <Stat label="Average order" value={formatMoney(data.avgOrderCents)} />
            <Stat label="Discrepancies" value={String(data.discrepancies)} warn={data.discrepancies > 0} />
          </div>

          {/* v1.6.3 — money out / money back: refunds and booked expenses
              between the takings and the shelf, so the day's true cash
              position reads top to bottom. Optional fields: older backends
              omit them, so every row renders only when present. */}
          {(data.netSalesCents !== undefined || data.returnsCents !== undefined || data.expensesCents !== undefined) && (
            <div className="mt-4">
              <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">Money out / money back</p>
              <div className="border-2 border-line rounded-input overflow-hidden text-[13px]">
                {data.netSalesCents !== undefined && (
                  <div className="flex items-center gap-3 px-3 py-2 bg-surface">
                    <span className="text-ink-muted flex-1">Net sales (takings − refunds)</span>
                    <span className="font-black tabular text-ink">{formatMoney(netSalesOf(data))}</span>
                  </div>
                )}
                {data.returnsCents !== undefined && (
                  <div className="flex items-center gap-3 px-3 py-2 bg-surface-muted">
                    <Undo2 size={13} strokeWidth={2.5} className="text-ink-muted shrink-0" aria-hidden />
                    <span className="text-ink-muted flex-1">Returns{data.returnsCount !== undefined ? ` (${data.returnsCount})` : ''}</span>
                    <span className="font-bold tabular text-ink">{formatMoney(data.returnsCents)}</span>
                  </div>
                )}
                {data.expensesCents !== undefined && (
                  <div className="flex items-center gap-3 px-3 py-2 bg-surface">
                    <ReceiptText size={13} strokeWidth={2.5} className="text-ink-muted shrink-0" aria-hidden />
                    <span className="text-ink-muted flex-1">Expenses booked</span>
                    <span className={`font-bold tabular ${data.expensesCents > 0 ? 'text-danger-text' : 'text-ink'}`}>
                      {formatMoney(data.expensesCents)}
                    </span>
                  </div>
                )}
              </div>
            </div>
          )}

          {/* v1.7.0 — the day over cost: line cost snapshots turn the
              takings into gross margin the shopkeeper can trust. */}
          {data.cogsCents !== undefined && (
            <div className="mt-4">
              <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">Gross margin (paid sales)</p>
              <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
                <Stat label="Cost of goods" value={formatMoney(data.cogsCents)} />
                <Stat label="Gross profit" value={formatMoney(data.grossProfitCents ?? 0)} />
                <Stat label="Margin" value={`${(data.marginPct ?? 0).toFixed(1)}%`} />
                {(data.disposalCount ?? 0) > 0 && (
                  <Stat label={`Write-offs (${data.disposalCount})`} value={formatMoney(data.disposalValueCents ?? 0)} warn />
                )}
              </div>
            </div>
          )}

          {/* v1.6.3 — shelf: what the stock room is worth + low-stock alerts. */}
          {(data.stockValueCents !== undefined || data.lowStockCount !== undefined) && (
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mt-3">
              {data.stockValueCents !== undefined && (
                <Stat
                  label={`Stock value${shelfUnitsOf(data) ? ` · ${shelfUnitsOf(data)} units` : ''}`}
                  value={formatMoney(data.stockValueCents)}
                />
              )}
              {data.lowStockCount !== undefined && (
                <Stat label="Low stock alerts" value={String(data.lowStockCount)} warn={data.lowStockCount > 0} />
              )}
            </div>
          )}

          {data.consolidated && data.shops && data.shops.length > 0 && (
            <div className="mt-4">
              <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">By shop</p>
              <div className="border-2 border-line rounded-input overflow-hidden">
                <table className="w-full text-[13px]">
                  <thead>
                    <tr className="bg-surface-muted text-left">
                      <th className="px-3 py-2 font-bold text-ink-muted">Shop</th>
                      <th className="px-3 py-2 font-bold text-ink-muted text-right">Sales</th>
                      <th className="px-3 py-2 font-bold text-ink-muted text-right">Paid</th>
                      <th className="px-3 py-2 font-bold text-ink-muted text-right">Voids</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.shops.map((sh) => (
                      <tr key={sh.id}>
                        <td className="px-3 py-2 font-semibold text-ink">{sh.name}</td>
                        <td className="px-3 py-2 text-right tabular text-ink">{formatMoney(sh.salesCents)}</td>
                        <td className="px-3 py-2 text-right tabular text-ink-muted">{sh.ordersPaid}</td>
                        <td className="px-3 py-2 text-right tabular text-ink-muted">{sh.ordersVoided ?? 0}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}

          {/* Payment split */}
          <div className="mt-4">
            <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">Payment split</p>
            {data.cashCents + data.mpesaCents + data.paystackCents + data.creditCents === 0 ? (
              <p className="text-sm text-ink-muted bg-surface-muted border-2 border-line rounded-input px-3 py-2">
                No completed payments yet today, the split appears with the first paid order.
              </p>
            ) : (
              <div className="flex h-9 border-2 border-line-strong rounded-input overflow-hidden font-bold text-[13px]">
                {data.cashCents > 0 && (
                  <div className="bg-surface-muted flex items-center justify-center tabular gap-1.5" style={{ width: pct(data.cashCents, data.cashCents + data.mpesaCents + data.paystackCents + data.creditCents) }}>
                    <Banknote size={13} strokeWidth={2.5} aria-hidden />
                    {centsToAmount(data.cashCents)}
                  </div>
                )}
                {data.mpesaCents > 0 && (
                  <div className="bg-paid-bg text-paid-text flex items-center justify-center tabular gap-1.5" style={{ width: pct(data.mpesaCents, data.cashCents + data.mpesaCents + data.paystackCents + data.creditCents) }}>
                    <Smartphone size={13} strokeWidth={2.5} aria-hidden />
                    {centsToAmount(data.mpesaCents)}
                  </div>
                )}
                {data.paystackCents > 0 && (
                  <div className="bg-brand/30 text-ink flex items-center justify-center tabular gap-1.5" style={{ width: pct(data.paystackCents, data.cashCents + data.mpesaCents + data.paystackCents + data.creditCents) }}>
                    <CreditCard size={13} strokeWidth={2.5} aria-hidden />
                    {centsToAmount(data.paystackCents)}
                  </div>
                )}
                {data.creditCents > 0 && (
                  <div className="bg-pending-bg text-pending-text flex items-center justify-center tabular gap-1.5" style={{ width: pct(data.creditCents, data.cashCents + data.mpesaCents + data.paystackCents + data.creditCents) }}>
                    <Wallet size={13} strokeWidth={2.5} aria-hidden />
                    {centsToAmount(data.creditCents)}
                  </div>
                )}
              </div>
            )}
          </div>

          {/* 7-day bar chart (pure CSS) */}
          <div className="mt-5">
            <p className="text-[12px] uppercase font-bold text-ink-muted mb-2">Last 7 days</p>
            <div className="flex items-end gap-2 border-b-2 border-line pb-px" role="img" aria-label="Sales per day for the last 7 days">
              {series.map((s) => (
                <div key={s.date} className="flex-1 flex flex-col items-center gap-1 group h-32">
                  <span className="text-[10px] font-bold text-ink-muted tabular opacity-0 group-hover:opacity-100 transition-opacity">
                    {centsToAmount(s.salesCents)}
                  </span>
                  <div
                    className={`w-full rounded-t-[4px] border-2 border-line-strong transition-[height] ${
                      s.date === date ? 'bg-brand' : 'bg-surface-muted'
                    }`}
                    style={{ height: `${Math.max(3, (s.salesCents / max) * 100)}%` }}
                    title={`${s.date}: ${centsToAmount(s.salesCents)} (${s.orders} orders)`}
                  />
                  <span className="text-[10px] text-ink-subtle">{s.date.slice(5)}</span>
                </div>
              ))}
            </div>
          </div>

          {/* Per-staff sales (P5): who sold what today. */}
          {!!data.perStaff && data.perStaff.length > 0 && (
            <div className="mt-5">
              <p className="text-[12px] uppercase font-bold text-ink-muted mb-2">Per staff</p>
              <div className="border-2 border-line rounded-input overflow-hidden">
                {data.perStaff.map((st, i) => (
                  <div key={st.userId} className={`flex items-center gap-3 px-3 py-2 text-[13px] ${i % 2 ? 'bg-surface-muted' : 'bg-surface'}`}>
                    <span className="font-semibold text-ink flex-1 truncate">{st.name}</span>
                    <span className="text-ink-muted tabular">{st.orders} order{st.orders === 1 ? '' : 's'}</span>
                    <span className="font-bold tabular text-ink min-w-24 text-right">{centsToAmount(st.salesCents)}</span>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Best sellers — the SQL already ran for this on every load;
              it just never reached the screen. Owners reorder from this. */}
          {!!data.topProducts && data.topProducts.length > 0 && (
            <div className="mt-5">
              <p className="text-[12px] uppercase font-bold text-ink-muted mb-2">Top products today</p>
              <div className="border-2 border-line rounded-input overflow-hidden">
                {data.topProducts.map((tp, i) => (
                  <div key={`${tp.productId}-${i}`} className={`flex items-center gap-3 px-3 py-2 text-[13px] ${i % 2 ? 'bg-surface-muted' : 'bg-surface'}`}>
                    <span className="w-5 text-ink-subtle tabular font-bold">{i + 1}</span>
                    <span className="font-semibold text-ink flex-1 truncate">{tp.name}</span>
                    <span className="text-ink-muted tabular">{fmtQty(tp.qtyMilli)} sold</span>
                    {tp.profitCents !== undefined && (
                      <span className={`tabular text-[12px] font-bold ${tp.profitCents >= 0 ? 'text-paid-text' : 'text-danger-text'}`} title="Sales − cost of goods">
                        {tp.profitCents >= 0 ? '+' : ''}{centsToAmount(tp.profitCents)}
                      </span>
                    )}
                    <span className="font-bold tabular text-ink min-w-24 text-right">{centsToAmount(tp.salesCents)}</span>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Sales by originating till (v19 device attribution). */}
          {!!data.byTill && data.byTill.length > 0 && (
            <div className="mt-5">
              <p className="text-[12px] uppercase font-bold text-ink-muted mb-2">By till</p>
              <div className="border-2 border-line rounded-input overflow-hidden">
                {data.byTill.map((t, i) => (
                  <div key={t.deviceId || 'legacy'} className={`flex items-center gap-3 px-3 py-2 text-[13px] ${i % 2 ? 'bg-surface-muted' : 'bg-surface'}`}>
                    <span className="font-semibold text-ink flex-1 truncate">{t.deviceId || 'Unattributed (pre-update)'}</span>
                    <span className="text-ink-muted tabular">{t.orders} order{t.orders === 1 ? '' : 's'}</span>
                    <span className="font-bold tabular text-ink min-w-24 text-right">{centsToAmount(t.salesCents)}</span>
                  </div>
                ))}
              </div>
            </div>
          )}

          {!!data.newCustomers && data.newCustomers > 0 && (
            <p className="mt-3 text-[12.5px] text-ink-muted">
              <span className="font-bold text-ink">{data.newCustomers}</span> new customer{data.newCustomers === 1 ? '' : 's'} captured today.
            </p>
          )}
        </>
      )}
      </Card>
      <ExpirySection />
    </>
  )
}

// ExpirySection (v1.8.0) — batch/expiry watchlist: every dated lot holding
// stock whose expiry falls inside the window, split into already-expired
// and expiring-soon, with the value at risk at cost. days filter mirrors
// the Go query param (7/30/60/90).
function ExpirySection() {
  const [days, setDays] = useState(30)
  const [data, setData] = useState<ExpiryReport | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let alive = true
    setError('')
    api.get<ExpiryReport>(`/api/v1/reports/expiry?days=${days}`)
      .then((r) => { if (alive) setData(r) })
      .catch((e: any) => {
        if (!alive) return
        setError(e?.message || 'Could not load the expiry report')
        setData(null)
      })
    return () => { alive = false }
  }, [days])

  const expired = data?.expired ?? []
  const soon = data?.soon ?? []

  return (
    <Card
      title="Expiry"
      sub="Dated lots at risk, value at cost — pull and write them off before they sell"
      actions={
        <div className="flex gap-1.5" role="group" aria-label="Expiry window">
          {[7, 30, 60, 90].map((d) => (
            <button
              key={d}
              onClick={() => setDays(d)}
              aria-pressed={days === d}
              className={`min-h-9 px-3.5 rounded-pill border font-semibold text-[13px] transition-colors ${
                days === d ? 'bg-brand border-brand text-white' : 'bg-surface text-ink-muted border-line hover:text-ink hover:border-line-strong'
              }`}
            >
              {d}d
            </button>
          ))}
        </div>
      }
    >
      {error ? (
        <EmptyState icon={<CalendarClock size={24} strokeWidth={2.25} />} title="Expiry report could not load" body={error} />
      ) : !data ? (
        <div className="py-10 flex justify-center"><Spinner /></div>
      ) : expired.length === 0 && soon.length === 0 ? (
        <EmptyState
          icon={<CalendarClock size={24} strokeWidth={2.25} />}
          title="Nothing at risk"
          body={`No expired lots and nothing expiring within ${days} days.`}
        />
      ) : (
        <div className="space-y-4">
          <ExpiryTable title={`Expired (${expired.length})`} rows={expired} emptyNote="No expired lots." />
          <ExpiryTable title={`Expiring soon, within ${days} days (${soon.length})`} rows={soon} emptyNote="Nothing expiring in the window." />
          <p className="text-[13px] font-bold text-ink">
            Total value at risk:{' '}
            <span className="tabular text-danger-text">{formatMoney(data.valueCents)}</span>
            {' '}at cost across {expired.length + soon.length} lot{(expired.length + soon.length) === 1 ? '' : 's'}.
          </p>
        </div>
      )}
    </Card>
  )
}

function ExpiryTable({ title, rows, emptyNote }: { title: string; rows: ExpiryBatchRow[]; emptyNote: string }) {
  return (
    <div>
      <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">{title}</p>
      {rows.length === 0 ? (
        <p className="text-sm text-ink-muted bg-surface-muted border-2 border-line rounded-input px-3 py-2">{emptyNote}</p>
      ) : (
        <div className="border-2 border-line rounded-input overflow-hidden">
          <table className="w-full text-[13px]">
            <thead>
              <tr className="bg-surface-muted text-left">
                <th className="px-3 py-2 font-bold text-ink-muted">Product</th>
                <th className="px-3 py-2 font-bold text-ink-muted">Batch</th>
                <th className="px-3 py-2 font-bold text-ink-muted">Expiry</th>
                <th className="px-3 py-2 font-bold text-ink-muted text-right">Remaining</th>
                <th className="px-3 py-2 font-bold text-ink-muted text-right">Value</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const past = !!r.expiryDate && Date.parse(r.expiryDate + 'T00:00:00') < Date.parse(new Date().toISOString().slice(0, 10) + 'T00:00:00')
                return (
                  <tr key={r.batchId} className="border-t border-line">
                    <td className="px-3 py-2">
                      <p className="font-semibold text-ink">{r.name}</p>
                      <p className="text-[11px] text-ink-subtle">{r.sku}</p>
                    </td>
                    <td className="px-3 py-2 text-ink-muted">{r.batchNo || '—'}</td>
                    <td className="px-3 py-2">
                      <span
                        className={`inline-flex items-center px-1.5 py-0.5 rounded-pill text-[11px] font-bold tabular whitespace-nowrap ${
                          past ? 'bg-danger-bg text-danger-text' : 'bg-pending-bg text-pending-text'
                        }`}
                      >
                        {r.expiryDate || '—'}
                      </span>
                    </td>
                    <td className="px-3 py-2 text-right tabular font-bold text-ink">{fmtQty(r.qtyRemainingMilli)}</td>
                    <td className="px-3 py-2 text-right tabular text-ink">{centsToAmount(r.valueCents)}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function Monthly() {
  const [month, setMonth] = useState(thisMonth())
  const [data, setData] = useState<MonthlySummary | null>(null)
  const [loading, setLoading] = useState(false)
  const [downloading, setDownloading] = useState(false)

  const [loadError, setLoadError] = useState('')
  const load = async (m: string) => {
    setLoading(true)
    setLoadError('')
    try {
      setData(await api.get<MonthlySummary>(`/api/v1/reports/monthly?month=${m}`))
    } catch (e: any) {
      setLoadError(e?.message || 'Could not load the report, check the connection and retry')
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => { load(month) }, [month])

  const download = async () => {
    setDownloading(true)
    try {
      await downloadFile(`/api/v1/reports/monthly.csv?month=${month}`, `kra-return-${month}.csv`)
      toast.success('CSV downloaded', `kra-return-${month}.csv`)
    } catch (e: any) {
      toast.error('Download failed', e?.message)
    } finally {
      setDownloading(false)
    }
  }

  const mseries = data?.series ?? []
  const max = Math.max(1, ...mseries.map((s) => s.salesCents), 1)

  return (
    <Card
      title="Monthly VAT return"
      sub="VAT figures for the accountant, attach the CSV to your iTax return"
      actions={
        <div className="flex gap-2 items-center">
          <Input type="month" value={month} max={thisMonth()} onChange={(e) => setMonth(e.target.value)} className="w-40" />
          <Button size="sm" variant="secondary" onClick={download} disabled={downloading || !data || data.ordersPaid === 0}>
            {downloading ? <Spinner className="border-t-line-strong" /> : <Download size={14} strokeWidth={2.5} aria-hidden />}
            CSV
          </Button>
        </div>
      }
    >
      {loading && !data ? (
        <div className="py-12 flex justify-center"><Spinner /></div>
      ) : loadError ? (
        <EmptyState icon={<FileSpreadsheet size={24} strokeWidth={2.25} />} title="Report could not load" body={loadError} />
      ) : !data || data.ordersPaid + data.ordersVoided === 0 ? (
        <EmptyState icon={<FileSpreadsheet size={24} strokeWidth={2.25} />} title={`No sales in ${month}`} body="Pick another month or start selling." />
      ) : (
        <>
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
            <Stat label="Gross sales (incl. VAT)" value={formatMoney(data.grossCents)} big />
            <Stat label="Taxable value (nett)" value={formatMoney(data.nettCents)} />
            <Stat label={`VAT collected (${data.taxPercent}%)`} value={formatMoney(data.vatCents)} highlight />
            <Stat label="Transactions" value={String(data.ordersPaid)} />
          </div>
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mt-3">
            <Stat label="Average transaction" value={formatMoney(data.avgOrderCents)} />
            <Stat label="Cash takings" value={formatMoney(data.cashCents)} />
            <Stat label="M-Pesa takings" value={formatMoney(data.mpesaCents)} />
            <Stat label="Discrepancies" value={String(data.discrepancies)} warn={data.discrepancies > 0} />
          </div>

          {/* v1.6.3 — money out of the month: booked expenses + refunds. */}
          {(data.expensesCents !== undefined || data.returnsCents !== undefined) && (
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mt-3">
              {data.returnsCents !== undefined && <Stat label="Returns" value={formatMoney(data.returnsCents)} />}
              {data.expensesCents !== undefined && <Stat label="Expenses" value={formatMoney(data.expensesCents)} warn={data.expensesCents > 0} />}
            </div>
          )}

          {/* v1.7.0 — the month over cost: gross margin + write-offs. */}
          {data.cogsCents !== undefined && (
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 mt-3">
              <Stat label="Cost of goods" value={formatMoney(data.cogsCents)} />
              <Stat label="Gross profit" value={formatMoney(data.grossProfitCents ?? 0)} />
              <Stat label="Margin" value={`${(data.marginPct ?? 0).toFixed(1)}%`} />
              {data.disposalValueCents !== undefined && data.disposalValueCents > 0 && (
                <Stat label={`Write-offs (${data.disposalCount})`} value={formatMoney(data.disposalValueCents)} warn />
              )}
            </div>
          )}

          {/* Per-day bars for the month */}
          <div className="mt-5">
            <p className="text-[12px] uppercase font-bold text-ink-muted mb-2">Daily sales, {month}</p>
            <div className="flex items-end gap-[3px] h-24 border-b-2 border-line pb-px" role="img" aria-label={`Sales per day during ${month}`}>
              {mseries.map((s) => (
                <div
                  key={s.date}
                  className="flex-1 min-w-[3px] rounded-t-[2px] bg-brand border-t-2 border-line-strong transition-[height]"
                  style={{ height: `${Math.max(2, (s.salesCents / max) * 100)}%` }}
                  title={`${s.date}: ${centsToAmount(s.salesCents)} (${s.orders} orders)`}
                />
              ))}
            </div>
            <div className="flex justify-between mt-1">
              <span className="text-[10px] text-ink-subtle">{mseries[0]?.date?.slice(5)}</span>
              <span className="text-[10px] text-ink-subtle">{mseries[mseries.length - 1]?.date?.slice(5)}</span>
            </div>
          </div>

          <div className="mt-4 flex flex-wrap gap-2">
            {data.ordersVoided > 0 && <StatusPill status="void" label={`${data.ordersVoided} voided orders (not in totals)`} />}
            {data.discrepancies > 0 && <StatusPill status="danger" label={`${data.discrepancies} amount discrepancies`} />}
            {data.ordersVoided === 0 && data.discrepancies === 0 && (
              <StatusPill status="paid" label="Clean month, no voids or discrepancies" />
            )}
          </div>
        </>
      )}
    </Card>
  )
}

function pct(part: number, whole: number) {
  // Proportional on purpose: the old 12% floor meant four ~10% legs
  // rendered a 48%-full bar and a 90/10 split read as 90/12.
  if (whole <= 0) return 0
  return `${Math.max(2, Math.round((part / whole) * 100))}%` // 2% keeps a sliver visible for hover
}

function Stat({ label, value, big, warn, highlight }: { label: string; value: string; big?: boolean; warn?: boolean; highlight?: boolean }) {
  return (
    <div className={`border-2 rounded-input p-3 ${
      warn ? 'bg-danger-bg border-danger-text/40'
      : highlight ? 'bg-paid-bg border-paid-text/40'
      : 'bg-surface-muted border-line'
    }`}>
      <p className="text-[11px] uppercase font-bold text-ink-muted">{label}</p>
      <p className={`font-black tabular mt-0.5 ${highlight ? 'text-paid-text' : 'text-ink'} ${big ? 'text-xl' : 'text-lg'}`}>{value}</p>
    </div>
  )
}
