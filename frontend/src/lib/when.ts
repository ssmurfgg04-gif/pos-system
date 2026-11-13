// Day-first date/time formatting for receipts, tables and logs.
// The browser default (toLocaleString() with no locale) renders US-style
// "11/14/2026, 8:56:28 AM" on many Android builds, which reads as the
// 11th of the 14th month to a Kenyan cashier. Everything goes through here.

type DateIn = string | number | Date | null | undefined

function toDate(v: DateIn): Date | null {
  if (v === null || v === undefined || v === '') return null
  const d = v instanceof Date ? v : new Date(v)
  return Number.isNaN(d.getTime()) ? null : d
}

/** "14 Nov 2026, 08:56" */
export function fmtDateTime(v: DateIn, fallback = '-'): string {
  const d = toDate(v)
  if (!d) return fallback
  return d.toLocaleString('en-GB', { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' })
}

/** "14 Nov 2026" */
export function fmtDate(v: DateIn, fallback = '-'): string {
  const d = toDate(v)
  if (!d) return fallback
  return d.toLocaleDateString('en-GB', { day: 'numeric', month: 'short', year: 'numeric' })
}

/** "08:56" */
export function fmtTime(v: DateIn, fallback = '-'): string {
  const d = toDate(v)
  if (!d) return fallback
  return d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
}
