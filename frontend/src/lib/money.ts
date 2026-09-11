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

/** VAT-inclusive breakdown (Kenya default): total known, extract tax. */
export function taxFromInclusive(subtotalCents: number, taxPercent: number): number {
  return Math.round((subtotalCents * taxPercent) / (100 + taxPercent))
}

/** VAT-exclusive math: tax added on top. */
export function taxFromExclusive(subtotalCents: number, taxPercent: number): number {
  return Math.round((subtotalCents * taxPercent) / 100)
}

export function cartTotals(
  lines: { qty: number; unitPriceCents: number }[],
  taxPercent: number,
  taxIncluded: boolean,
) {
  const subtotal = lines.reduce((sum, l) => sum + l.qty * l.unitPriceCents, 0)
  const tax = taxIncluded ? taxFromInclusive(subtotal, taxPercent) : taxFromExclusive(subtotal, taxPercent)
  const total = taxIncluded ? subtotal : subtotal + tax
  return { subtotal, tax, total }
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
