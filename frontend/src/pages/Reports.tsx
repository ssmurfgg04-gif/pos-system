// Reports — daily summary cards, 7-day CSS bar chart (no chart lib —
// keeps the bundle tiny and works offline), payment split, top products;
// plus the Monthly (KRA) tab: one calendar month of VAT figures for the
// accountant's return, downloadable as CSV.

import { useEffect, useState } from 'react'
import { api, downloadFile, DailySummary, MonthlySummary } from '../lib/api'
import { Button, Card, EmptyState, Input, Spinner, StatusPill, Tabs } from '../components/ui'
import { centsToAmount, formatMoney } from '../lib/money'
import { toast } from '../stores/toasts'
import { BarChart3, Banknote, Smartphone, Download, FileSpreadsheet } from 'lucide-react'

const today = () => new Date().toISOString().slice(0, 10)
const thisMonth = () => new Date().toISOString().slice(0, 7)

export function Reports() {
  const [view, setView] = useState<'daily' | 'monthly'>('daily')
  return (
    <div className="space-y-4">
      <Tabs
        tabs={[
          { key: 'daily' as const, label: 'Daily', icon: <BarChart3 size={15} strokeWidth={2.25} aria-hidden /> },
          { key: 'monthly' as const, label: 'Monthly — KRA returns', icon: <FileSpreadsheet size={15} strokeWidth={2.25} aria-hidden /> },
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

  const load = async (d: string) => {
    setLoading(true)
    try {
      setData(await api.get<DailySummary>(`/api/v1/reports/daily?date=${d}`))
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => { load(date) }, [date])

  const max = Math.max(1, ...(data?.series.map((s) => s.salesCents) ?? [1]))

  return (
    <Card
      title="Daily report"
      actions={
        <div className="flex gap-2 items-center">
          <Input type="date" value={date} max={today()} onChange={(e) => setDate(e.target.value)} className="w-40" />
          <Button size="sm" variant="secondary" onClick={() => setDate(today())}>Today</Button>
        </div>
      }
    >
      {loading && !data ? (
        <div className="py-12 flex justify-center"><Spinner /></div>
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

          {/* Payment split */}
          <div className="mt-4">
            <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">Payment split</p>
            <div className="flex h-9 border-2 border-line-strong rounded-input overflow-hidden font-bold text-[13px]">
              {data.cashCents > 0 && (
                <div className="bg-surface-muted flex items-center justify-center tabular gap-1.5" style={{ width: pct(data.cashCents, data.cashCents + data.mpesaCents) }}>
                  <Banknote size={13} strokeWidth={2.5} aria-hidden />
                  {centsToAmount(data.cashCents)}
                </div>
              )}
              {data.mpesaCents > 0 && (
                <div className="bg-paid-bg text-paid-text flex items-center justify-center tabular gap-1.5" style={{ width: pct(data.mpesaCents, data.cashCents + data.mpesaCents) }}>
                  <Smartphone size={13} strokeWidth={2.5} aria-hidden />
                  {centsToAmount(data.mpesaCents)}
                </div>
              )}
            </div>
          </div>

          {/* 7-day bar chart (pure CSS) */}
          <div className="mt-5">
            <p className="text-[12px] uppercase font-bold text-ink-muted mb-2">Last 7 days</p>
            <div className="flex items-end gap-2 border-b-2 border-line pb-px" role="img" aria-label="Sales per day for the last 7 days">
              {data.series.map((s) => (
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
        </>
      )}
    </Card>
  )
}

function Monthly() {
  const [month, setMonth] = useState(thisMonth())
  const [data, setData] = useState<MonthlySummary | null>(null)
  const [loading, setLoading] = useState(false)
  const [downloading, setDownloading] = useState(false)

  const load = async (m: string) => {
    setLoading(true)
    try {
      setData(await api.get<MonthlySummary>(`/api/v1/reports/monthly?month=${m}`))
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

  const max = Math.max(1, ...(data?.series.map((s) => s.salesCents) ?? [1]))

  return (
    <Card
      title="Monthly VAT return"
      sub="VAT figures for the accountant — attach the CSV to your iTax return"
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

          {/* Per-day bars for the month */}
          <div className="mt-5">
            <p className="text-[12px] uppercase font-bold text-ink-muted mb-2">Daily sales — {month}</p>
            <div className="flex items-end gap-[3px] h-24 border-b-2 border-line pb-px" role="img" aria-label={`Sales per day during ${month}`}>
              {data.series.map((s) => (
                <div
                  key={s.date}
                  className="flex-1 min-w-[3px] rounded-t-[2px] bg-brand/85 border-t-2 border-line-strong transition-[height]"
                  style={{ height: `${Math.max(2, (s.salesCents / max) * 100)}%` }}
                  title={`${s.date}: ${centsToAmount(s.salesCents)} (${s.orders} orders)`}
                />
              ))}
            </div>
            <div className="flex justify-between mt-1">
              <span className="text-[10px] text-ink-subtle">{data.series[0]?.date?.slice(5)}</span>
              <span className="text-[10px] text-ink-subtle">{data.series[data.series.length - 1]?.date?.slice(5)}</span>
            </div>
          </div>

          <div className="mt-4 flex flex-wrap gap-2">
            {data.ordersVoided > 0 && <StatusPill status="void" label={`${data.ordersVoided} voided orders (not in totals)`} />}
            {data.discrepancies > 0 && <StatusPill status="danger" label={`${data.discrepancies} amount discrepancies`} />}
            {data.ordersVoided === 0 && data.discrepancies === 0 && (
              <StatusPill status="paid" label="Clean month — no voids or discrepancies" />
            )}
          </div>
        </>
      )}
    </Card>
  )
}

function pct(part: number, whole: number) {
  if (whole <= 0) return 0
  return `${Math.max(12, Math.round((part / whole) * 100))}%`
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
