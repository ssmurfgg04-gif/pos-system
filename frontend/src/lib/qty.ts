// qty.ts — v1.8.0 milli-unit quantity helpers (mirror of the Go helpers in
// internal/services/batches.go — the two sides are pinned by golden tests).
//
// Every quantity on the wire and in state is an integer MILLI-UNIT:
// 1000 = one piece / one kg, exactly like integer cents for money. SQLite
// has no decimal type and float quantities drift, so the till counts
// thousandths of a unit end to end.

export const MILLI = 1000
export const MAX_QTY_MILLI = 100_000_000 // 100k units (models.MaxOrderQtyMilli)

export const UNITS = ['pc', 'kg', 'g', 'l', 'ml', 'pack', 'box', 'dozen', 'm'] as const
export type Unit = (typeof UNITS)[number]

/** Units the till can weigh out: everything except pieces/packs. */
const FRACTION_UNITS = new Set<string>(['kg', 'g', 'l', 'ml', 'm'])

export function isFractionUnit(u?: string): boolean {
  return !!u && FRACTION_UNITS.has(u)
}

/** '' or 'pc' display as 'pc'; any other configured unit shows as itself. */
export function unitLabel(u?: string): string {
  return !u ? 'pc' : u
}

/**
 * Human input ("0.5", "250", "2") → milli, clamped [1, MAX_QTY_MILLI],
 * NaN → null. Accepts up to 3 decimals — anything finer cannot be
 * represented in milli and is rejected rather than silently rounded.
 */
export function parseQtyInput(s: string): number | null {
  const t = s.trim().replace(/,/g, '')
  if (t === '') return null
  if (!/^\d+(\.\d{1,3})?$/.test(t)) return null
  const [whole = '0', frac = ''] = t.split('.')
  const padded = ((frac + '000').slice(0, 3))
  const milli = Number(whole) * MILLI + Number(padded || '0')
  if (!Number.isFinite(milli) || milli <= 0) return null
  return Math.min(milli, MAX_QTY_MILLI)
}

/** milli → display string: whole units plain ("3"), else trimmed decimals ("0.5", "0.25"). */
export function fmtQty(milli: number): string {
  const m = Math.round(milli)
  if (m % MILLI === 0) return String(m / MILLI)
  // 3 decimals fixed, then trim trailing zeros AND the trailing dot —
  // mirrors models.OrderItem.QtyDisplay() on the server.
  let s = (m / MILLI).toFixed(3)
  if (s.includes('.')) s = s.replace(/0+$/, '').replace(/\.$/, '')
  return s
}

/** Line total in cents — the EXACT server formula (milliLineTotal, half-up). */
export function lineTotalCents(unitPriceCents: number, qtyMilli: number): number {
  return Math.round(unitPriceCents * qtyMilli / MILLI)
}
