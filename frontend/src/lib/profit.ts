// profit.ts — v1.10 "sell minus buying price", one rule for the whole app.
//
//   profit      = what the customer paid - the VAT inside it - buying price
//   net profit  = profit - expenses - stock written off
//
// VAT is KRA's money, so it never counts as the shop's profit. A line with
// no buying price can't be costed: it is reported as "unknown", never as
// 100% profit.

export interface ProfitLine {
  qtyMilli: number
  lineTotalCents: number
  /** unit buying price frozen on the sale line (0 = not recorded) */
  costCents?: number
}

export interface SaleProfit {
  /** null when any line has no buying price: the figure would be inflated */
  profitCents: number | null
  costCents: number
  uncostedLines: number
  marginPct: number | null
}

export function saleProfit(totalCents: number, taxCents: number, lines: ProfitLine[]): SaleProfit {
  let cost = 0
  let uncosted = 0
  for (const l of lines) {
    const c = l.costCents ?? 0
    if (c <= 0) uncosted++
    else cost += Math.round((c * l.qtyMilli) / 1000)
  }
  const exVat = totalCents - taxCents
  const profit = uncosted > 0 ? null : exVat - cost
  return {
    profitCents: profit,
    costCents: cost,
    uncostedLines: uncosted,
    marginPct: profit === null || exVat <= 0 ? null : Math.round((profit / exVat) * 1000) / 10,
  }
}

/** Per-item profit while editing a product: selling price (VAT-inclusive
 *  when the shop prices that way) against the buying price. */
export function unitProfit(priceCents: number, costCents: number, taxPercent: number, taxIncluded: boolean) {
  if (costCents <= 0 || priceCents <= 0) return null
  const exVat = taxIncluded && taxPercent > 0 ? Math.round(priceCents / (1 + taxPercent / 100)) : priceCents
  const profit = exVat - costCents
  return { profitCents: profit, marginPct: exVat > 0 ? Math.round((profit / exVat) * 1000) / 10 : 0 }
}
