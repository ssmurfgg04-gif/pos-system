// Kenya-first date helpers. The browser's toISOString() is UTC, which
// silently shifted every "today" on a Nairobi till between 00:00 and
// 03:00 EAT onto YESTERDAY (Reports defaulted to the wrong day, the date
// pickers forbade the real today, Quotes' 14-day validity rolled a day
// early near midnight). These helpers format in the BROWSER's local
// timezone — the till runs on Africa/Nairobi — instead of UTC.

const pad = (n: number) => String(n).padStart(2, '0')

/** YYYY-MM-DD for the given instant, in local (EAT) time. */
export function localDate(d: Date = new Date()): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** Today, local. */
export function todayLocal(): string {
  return localDate(new Date())
}

/** YYYY-MM for the given instant, local. */
export function localMonth(d: Date = new Date()): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}`
}

/** This month, local. */
export function thisMonthLocal(): string {
  return localMonth(new Date())
}

/** N days ago, local (Reports' 7-day windows, Quotes' validity). */
export function daysAgoLocal(n: number): string {
  const d = new Date()
  d.setDate(d.getDate() - n)
  return localDate(d)
}
