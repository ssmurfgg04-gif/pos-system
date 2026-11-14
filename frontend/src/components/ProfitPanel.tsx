// ProfitPanel — v1.10 "what did I actually make?" ladder for the daily and
// monthly reports. Reads straight down like a shop book:
//   Sales − VAT − buying price = Profit;  − expenses − write-offs = you keep.
import { navigate } from '../lib/router'
import { AlertTriangle, TrendingUp } from 'lucide-react'
import { formatMoney } from '../lib/money'

export interface ProfitFigures {
  salesCents: number
  salesVatCents?: number
  cogsCents?: number
  profitCents?: number
  netProfitCents?: number
  expensesCents?: number
  disposalValueCents?: number
  disposalCount?: number
  uncostedLines?: number
  uncostedSalesCents?: number
  returnsProfitCents?: number
}

export function ProfitPanel({ f, period }: { f: ProfitFigures; period: 'day' | 'month' }) {
  if (f.profitCents === undefined) return null
  const vat = f.salesVatCents ?? 0
  const exVat = f.salesCents - vat
  const margin = exVat > 0 ? Math.round((f.profitCents / exVat) * 1000) / 10 : 0
  const net = f.netProfitCents ?? f.profitCents
  const row = (label: string, cents: number, sign: '−' | '' = '−', hint?: string) => (
    <div className="flex items-baseline justify-between gap-3 py-1 text-[13px]">
      <span className="text-ink-muted">
        {sign && <span className="inline-block w-3 font-bold">{sign}</span>}
        {label}
        {hint && <span className="text-ink-subtle text-[11px]"> · {hint}</span>}
      </span>
      <span className="tabular font-semibold text-ink">{formatMoney(cents)}</span>
    </div>
  )
  return (
    <section className="mt-4 rounded-card border-2 border-line bg-surface p-4" aria-label="Profit">
      <div className="flex items-center gap-2 mb-2">
        <TrendingUp size={16} strokeWidth={2.5} className="text-paid-text" aria-hidden />
        <h3 className="font-bold text-ink">Profit {period === 'day' ? 'today' : 'this month'}</h3>
        <span className="ml-auto text-[11px] text-ink-subtle">paid sales only</span>
      </div>
      <div className="divide-y divide-line">
        <div>
          {row('Sales', f.salesCents, '')}
          {vat > 0 && row('VAT in those sales', vat, '−', "KRA's money")}
          {row('Buying price of what was sold', f.cogsCents ?? 0)}
          {(f.returnsProfitCents ?? 0) !== 0 && row('Profit given back on returns', f.returnsProfitCents ?? 0)}
        </div>
        <div className="flex items-baseline justify-between gap-3 py-2">
          <span className="font-bold text-ink">Profit on goods</span>
          <span className={`tabular font-black text-lg ${f.profitCents >= 0 ? 'text-paid-text' : 'text-danger-text'}`}>
            {formatMoney(f.profitCents)} <span className="text-[12px] font-bold text-ink-muted">({margin.toFixed(1)}%)</span>
          </span>
        </div>
        <div>
          {row('Expenses', f.expensesCents ?? 0)}
          {(f.disposalValueCents ?? 0) > 0 && row(`Stock written off (${f.disposalCount ?? 0})`, f.disposalValueCents ?? 0)}
        </div>
        <div className="flex items-baseline justify-between gap-3 pt-2">
          <span className="font-bold text-ink">What you keep</span>
          <span className={`tabular font-black text-xl ${net >= 0 ? 'text-paid-text' : 'text-danger-text'}`}>{formatMoney(net)}</span>
        </div>
      </div>
      {(f.uncostedLines ?? 0) > 0 && (
        <div role="note" className="mt-3 flex gap-2 rounded-input border border-pending-text/40 bg-pending-bg p-2.5 text-[12.5px] text-ink">
          <AlertTriangle size={15} strokeWidth={2.5} className="text-pending-text shrink-0 mt-0.5" aria-hidden />
          <p>
            <b>{f.uncostedLines} sold item{f.uncostedLines === 1 ? ' has' : 's have'} no buying price</b>, so{' '}
            {formatMoney(f.uncostedSalesCents ?? 0)} of sales is counted with no cost and profit looks bigger than it is.{' '}
            <button type="button" onClick={() => navigate('/inventory')} className="font-bold underline">Add buying prices in Inventory</button>.
          </p>
        </div>
      )}
    </section>
  )
}
