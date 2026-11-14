// Money math — integer cents everywhere on the wire and in the UI state.
// Formatting uses Intl with KES defaults, overridable by branding.

let symbol = 'KES'

export function configureMoney(curSymbol: string) {
  symbol = curSymbol || 'KES'
}

/** Parse a human money string ("1,550.50", "550", ".5") into cents. */
export function parseToCents(input: string): number | null {
  const s = input.replace(/,/g, '').trim()
  if (s === '') return null
  if (!/^\d*(\.\d{0,2})?$/.test(s)) return null
  const [whole = '0', frac = ''] = s.split('.')
  const padded = (frac + '00').slice(0, 2)
  return Number(whole) * 100 + Number(padded || '0')
}

/** Cents → "1,550.50" (no symbol; for tables where symbol is a header). */
export function centsToAmount(cents: number): string {
  const neg = cents < 0
  const abs = Math.abs(cents)
  const s = new Intl.NumberFormat('en-KE', {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  }).format(abs / 100)
  return neg ? '-' + s : s
}

/** Cents → "KES 1,550.50" (full display form). */
export function formatMoney(cents: number): string {
  return `${symbol} ${centsToAmount(cents)}`
}

/**
 * Compact display for tight tiles/cart lines: drops ".00" when there are
 * no fractional cents ("KES 550"), keeps two decimals otherwise
 * ("KES 550.50" — NOT the wrong "KES 550.5").
 */
export function formatMoneyCompact(cents: number): string {
  const hasFraction = Math.abs(cents % 100) !== 0
  if (!hasFraction) {
    const whole = new Intl.NumberFormat('en-KE', { maximumFractionDigits: 0 }).format(Math.trunc(cents / 100))
    return `${symbol} ${whole}`
  }
  return formatMoney(cents)
}

/**
 * Split tender: how much of the total is not covered by any leg yet.
 * 0 = exact (go), negative = overpaid (red), positive = underpaid (amber).
 */
export function splitRemaining(totalCents: number, legs: { amountCents: number }[]): number {
  return totalCents - legs.reduce((sum, l) => sum + Math.max(0, Math.round(l.amountCents) || 0), 0)
}

/** VAT-inclusive breakdown (Kenya default): total known, extract tax. */
export function taxFromInclusive(subtotalCents: number, taxPercent: number): number {
  return Math.round((subtotalCents * taxPercent) / (100 + taxPercent))
}

/** VAT-exclusive math: tax added on top. */
export function taxFromExclusive(subtotalCents: number, taxPercent: number): number {
  return Math.round((subtotalCents * taxPercent) / 100)
}

export function cartTotals(
  lines: { qtyMilli: number; unitPriceCents: number; vatRate?: number }[],
  taxPercent: number,
  taxIncluded: boolean,
  discountCents = 0,
) {
  // v1.8.0 milli quantities: each line prices exactly like the server
  // (Math.round(price × milli / 1000), half-up — same rounding money.ts
  // uses everywhere else). v1.11: each line may carry its own VAT rate
  // (exempt / 8% / gift cards); a sale where every line has the shop rate
  // is exactly the old single-rate maths.
  const amounts = lines.map((l) => Math.round((l.unitPriceCents * l.qtyMilli) / 1000))
  const subtotal = amounts.reduce((a, b) => a + b, 0)
  const r = computeLines(
    lines.map((l, i) => ({ amountCents: amounts[i], rate: l.vatRate ?? taxPercent })),
    discountCents, 0, taxIncluded,
  )
  return { subtotal, tax: r.taxCents, total: r.totalCents, byRate: r.byRate }
}

/** Exact proportional split (integer largest remainder, ties to the earlier
 *  index) — the twin of money.Spread in Go. BigInt so huge baskets stay exact. */
export function spread(weights: number[], amount: number): number[] {
  const out = weights.map(() => 0)
  const total = weights.reduce((a, w) => a + (w > 0 ? w : 0), 0)
  if (amount <= 0 || total <= 0) return out
  const amt = BigInt(Math.min(amount, total))
  const bt = BigInt(total)
  const rems: { r: bigint; i: number }[] = []
  let given = 0n
  weights.forEach((w, i) => {
    if (w <= 0) { rems.push({ r: -1n, i }); return }
    const p = BigInt(w) * amt
    out[i] = Number(p / bt)
    given += p / bt
    rems.push({ r: p % bt, i })
  })
  rems.sort((a, b) => (a.r === b.r ? a.i - b.i : a.r > b.r ? -1 : 1))
  for (let k = 0; given < amt && k < rems.length; k++) {
    if (rems[k].r < 0n) continue
    out[rems[k].i]++
    given++
  }
  return out
}

export interface LineIn { amountCents: number; rate: number }
export interface LineOut { netCents: number; taxCents: number; paidCents: number }

/** Per-line VAT for a sale — the twin of money.ComputeLines in Go, pinned
 *  by testdata/money_lines.json on both sides. */
export function computeLines(lines: LineIn[], discountCents: number, pointsCents: number, inclusive: boolean) {
  const disc = spread(lines.map((l) => l.amountCents), discountCents)
  const out: LineOut[] = lines.map((l, i) => ({ netCents: l.amountCents - disc[i], taxCents: 0, paidCents: 0 }))
  const rates: number[] = []
  const members = new Map<number, number[]>()
  lines.forEach((l, i) => {
    if (!members.has(l.rate)) { rates.push(l.rate); members.set(l.rate, []) }
    members.get(l.rate)!.push(i)
  })
  let taxCents = 0
  let totalCents = 0
  const byRate: { rate: number; taxCents: number; grossCents: number }[] = []
  for (const r of rates) {
    const idx = members.get(r)!
    const net = idx.reduce((a, i) => a + out[i].netCents, 0)
    const tax = inclusive ? taxFromInclusive(net, r) : taxFromExclusive(net, r)
    const parts = spread(idx.map((i) => out[i].netCents), tax)
    idx.forEach((i, k) => {
      out[i].taxCents = parts[k]
      out[i].paidCents = out[i].netCents + (inclusive ? 0 : parts[k])
    })
    taxCents += tax
    totalCents += inclusive ? net : net + tax
    byRate.push({ rate: r, taxCents: tax, grossCents: inclusive ? net : net + tax })
  }
  if (pointsCents > 0) {
    const pts = spread(out.map((l) => l.paidCents), pointsCents)
    taxCents = 0
    byRate.length = 0
    for (const r of rates) {
      const idx = members.get(r)!
      idx.forEach((i) => { out[i].paidCents -= pts[i] })
      const paid = idx.reduce((a, i) => a + out[i].paidCents, 0)
      const tax = taxFromInclusive(paid, r)
      const parts = spread(idx.map((i) => out[i].paidCents), tax)
      idx.forEach((i, k) => { out[i].taxCents = parts[k] })
      taxCents += tax
      byRate.push({ rate: r, taxCents: tax, grossCents: paid })
    }
  }
  return { taxCents, totalCents, lines: out, byRate }
}

/** Normalizes Kenyan phone inputs to 254XXXXXXXXX (mirrors the server). */
export function normalizePhoneKe(input: string): string | null {
  let p = input.replace(/[\s-]/g, '')
  if (!p) return null
  if (p.startsWith('+254')) p = '254' + p.slice(4)
  else if (p.startsWith('254')) { /* ok */ }
  else if (p.startsWith('0')) p = '254' + p.slice(1)
  else if (p.length === 9 && (p.startsWith('7') || p.startsWith('1'))) p = '254' + p
  if (!/^254[17]\d{8}$/.test(p)) return null
  return p
}

export function getSymbol() { return symbol }
