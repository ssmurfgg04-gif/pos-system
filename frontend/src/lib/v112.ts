// v1.12 types: timecards, stock transfers, the books, promotions.
// Shapes mirror internal/services/{timecards,transfers,accounting,promotions}.go.

export interface TimeEntry {
  id: number
  userId: number
  username: string
  fullName: string
  clockIn: string
  clockOut: string
  breakSeconds: number
  breakStarted: string
  onBreak: boolean
  open: boolean
  note: string
  status: 'active' | 'void'
  editedBy: string
  editReason: string
  workedSeconds: number
  flag: string
}

export interface TimecardSummary {
  userId: number
  username: string
  fullName: string
  hourlyRateCents: number
  daysWorked: number
  workedSeconds: number
  normalSeconds: number
  overtimeSeconds: number
  breakSeconds: number
  payCents: number
  openEntries: number
  flags: number
  days: { date: string; workedSeconds: number; entries: number }[]
}

export interface TimecardReport {
  from: string
  to: string
  weeklyHours: number
  overtimeRate: number
  people: TimecardSummary[]
  entries: TimeEntry[]
  totalSeconds: number
  totalPayCents: number
}

export interface TransferLine {
  sku: string
  barcode?: string
  name: string
  unit?: string
  allowFraction?: boolean
  qtyMilli: number
  priceCents: number
  costCents: number
}

export interface Transfer {
  id: string
  clientRef: string
  direction: 'out' | 'in'
  fromTeam: string
  fromName: string
  toTeam: string
  toName: string
  status: 'queued' | 'sent' | 'received' | 'cancelled' | 'rejected'
  lines: TransferLine[]
  receivedLines: { sku: string; qtyMilli: number }[]
  note: string
  sentBy: string
  sentAt: string
  receivedBy: string
  receivedAt: string
  cancelledBy: string
  cancelledAt: string
  lastError: string
  shortMilli: number
}

export interface TransferList {
  online: boolean
  error: string
  myTeam: string
  shops: { teamCode: string; name: string }[]
  transfers: Transfer[]
}

export interface AccountingAccount {
  key: string
  label: string
  name: string
  code: string
}

export interface JournalLine {
  account: string
  name: string
  code: string
  debitCents: number
  creditCents: number
  memo: string
}

export interface JournalDay {
  date: string
  number: string
  lines: JournalLine[]
  debitCents: number
  creditCents: number
  roundingCents: number
}

export interface PromoSegment {
  minSpentCents: number
  visitedWithinDays: number
  awayForDays: number
  minPoints: number
  withTabBalance: boolean
}

export interface PromoAudience {
  count: number
  sample: string[]
  noConsent: number
  badPhone: number
  segments: number
  characters: number
  previewBody: string
}

export interface PromoCampaign {
  id: number
  name: string
  channel: 'sms' | 'whatsapp'
  body: string
  segment: PromoSegment
  status: 'draft' | 'sending' | 'paused' | 'done' | 'cancelled'
  total: number
  sent: number
  failed: number
  pending: number
  lastError: string
  createdBy: string
  createdAt: string
  startedAt: string
  finishedAt: string
  quietNow: boolean
  segments: number
  providerSet: boolean
}

export interface PromoRecipient {
  name: string
  phone: string
  status: 'pending' | 'sending' | 'sent' | 'failed' | 'skipped'
  error: string
  sentAt: string
}

/** 3600 → "1:00", 5400 → "1:30" (hours:minutes) */
export function hm(seconds: number): string {
  const s = Math.max(0, Math.round(seconds))
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  return `${h}:${String(m).padStart(2, '0')}`
}

/** milli-units → "1", "0.5", "2.25" */
export function qty(milli: number): string {
  return (milli / 1000).toFixed(3).replace(/\.?0+$/, '')
}

/** a stored UTC stamp → value for <input type="datetime-local"> (Nairobi) */
export function toLocalInput(stamp: string): string {
  if (!stamp) return ''
  const d = new Date(stamp)
  if (isNaN(d.getTime())) return ''
  const k = new Date(d.getTime() + 3 * 3600_000) // Nairobi, no DST
  return k.toISOString().slice(0, 16)
}

/** <input type="datetime-local"> (Nairobi) → UTC ISO stamp */
export function fromLocalInput(v: string): string {
  if (!v) return ''
  const d = new Date(v + ':00+03:00')
  return isNaN(d.getTime()) ? '' : d.toISOString()
}
