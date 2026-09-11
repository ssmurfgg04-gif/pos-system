// Reports — daily summary cards, 7-day CSS bar chart (no chart lib —
// keeps the bundle tiny and works offline), payment split, top products.

import { useEffect, useState } from 'react'
import { api, DailySummary } from '../lib/api'
import { Button, Card, EmptyState, Input, Spinner, StatusPill, Table } from '../components/ui'
import { centsToAmount, formatMoney } from '../lib/money'

const today = () => new Date().toISOString().slice(0, 10)

export function Reports() {
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
    <div className="space-y-4">
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
          <EmptyState icon="📊" title={`No sales on ${date}`} body="Pick another day or start selling." />
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
                  <div className="bg-surface-muted flex items-center justify-center tabular" style={{ width: pct(data.cashCents, data.cashCents + data.mpesaCents) }}>
                    💵 {centsToAmount(data.cashCents)}
                  </div>
                )}
                {data.mpesaCents > 0 && (
                  <div className="bg-paid-bg text-paid-text flex items-center justify-center tabular" style={{ width: pct(data.mpesaCents, data.cashCents + data.mpesaCents) }}>
                    📱 {centsToAmount(data.mpesaCents)}
                  </div>
                )}
              </div>
            </div>

            {/* 7-day bar chart (pure CSS) */}
            <div className="mt-5">
              <p className="text-[12px] uppercase font-bold text-ink-muted mb-2">Last 7 days</p>
              <div className="flex items-end gap-2 h-32" role="img" aria-label="Sales per day for the last 7 days">
                {data.series.map((s) => (
                  <div key={s.date} className="flex-1 flex flex-col items-center gap-1 group">
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

      {data && data.topProducts.length > 0 && (
        <Card title="Top products" pad={false}>
          <Table head={['#', 'Product', 'Qty sold', 'Sales']}>
            {data.topProducts.map((p, i) => (
              <tr key={p.productId}>
                <td className="px-3 py-2.5 tabular text-ink-subtle w-8">{i + 1}</td>
                <td className="px-3 py-2.5 font-semibold text-ink text-[13px]">{p.name}</td>
                <td className="px-3 py-2.5 tabular font-bold">{p.qty}</td>
                <td className="px-3 py-2.5 tabular font-semibold">{centsToAmount(p.salesCents)}</td>
              </tr>
            ))}
          </Table>
        </Card>
      )}

      {data && (data.ordersOpen > 0 || data.ordersVoided > 0 || data.discrepancies > 0) && (
        <Card title="Attention">
          <div className="flex flex-wrap gap-2">
            {data.ordersOpen > 0 && <StatusPill status="pending" label={`${data.ordersOpen} unpaid pending orders`} />}
            {data.ordersVoided > 0 && <StatusPill status="void" label={`${data.ordersVoided} voided`} />}
            {data.discrepancies > 0 && <StatusPill status="danger" label={`${data.discrepancies} amount discrepancies`} />}
          </div>
        </Card>
      )}
    </div>
  )
}

function pct(part: number, whole: number) {
  if (whole <= 0) return 0
  return `${Math.max(12, Math.round((part / whole) * 100))}%`
}

function Stat({ label, value, big, warn }: { label: string; value: string; big?: boolean; warn?: boolean }) {
  return (
    <div className={`border-2 rounded-input p-3 ${warn ? 'bg-danger-bg border-danger-text/40' : 'bg-surface-muted border-line'}`}>
      <p className="text-[11px] uppercase font-bold text-ink-muted">{label}</p>
      <p className={`font-black tabular text-ink mt-0.5 ${big ? 'text-xl' : 'text-lg'}`}>{value}</p>
    </div>
  )
}
