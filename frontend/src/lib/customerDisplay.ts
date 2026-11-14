// Customer-facing display (second screen).
//
// The till window PUBLISHES what the customer should see; a separate
// window at /display RENDERS it. They talk over a same-origin
// BroadcastChannel, so the display needs no login, no API access and no
// settings of its own — it shows exactly what the till sends and nothing
// else. Works on a second monitor or a second window of the same browser
// on the same computer (not across devices: that would need the server).
//
// Why it matters for Kenyan shops: the most common M-Pesa error is paying
// the wrong amount or to the wrong number. Showing "Pay KES 1,250 to Till
// 123456" on a screen facing the customer stops it at the source.

export const DISPLAY_CHANNEL = 'ledgerpos-customer-display'

/** How long the display keeps the "Asante" screen before the welcome. */
export const PAID_HOLD_MS = 8000

export interface DisplayBrand {
  storeName: string
  logoUrl: string
  color: string
}

export interface DisplayLine {
  name: string
  qty: number
  /** preformatted line total, e.g. "KES 1,100" */
  total: string
}

export type DisplayState =
  | { kind: 'idle'; brand: DisplayBrand }
  | {
      kind: 'cart'
      brand: DisplayBrand
      lines: DisplayLine[]
      itemCount: number
      /** preformatted amounts */
      total: string
      discount?: string
      /** cash tendered so far + change, when the cashier is taking cash */
      received?: string
      change?: string
    }
  | {
      kind: 'pay'
      brand: DisplayBrand
      amount: string
      /** "Till 123456" or "Paybill 400200" */
      payTo: string
      /** paybill account number (the order number), when paying by paybill */
      account?: string
      /** STK push sent to the customer's phone */
      stkSent: boolean
    }
  | { kind: 'paid'; brand: DisplayBrand; total: string; change?: string }

type Msg = { type: 'state'; state: DisplayState } | { type: 'hello' }

let channel: BroadcastChannel | null = null
let last: DisplayState | null = null

function getChannel(): BroadcastChannel | null {
  if (channel) return channel
  if (typeof BroadcastChannel === 'undefined') return null
  try {
    channel = new BroadcastChannel(DISPLAY_CHANNEL)
    // a display that opens late asks for the current picture
    channel.onmessage = (e: MessageEvent<Msg>) => {
      if (e.data?.type === 'hello' && last) channel?.postMessage({ type: 'state', state: last } satisfies Msg)
    }
  } catch {
    channel = null
  }
  return channel
}

/** Till side: publish what the customer should see now. Cheap and
 *  idempotent; identical consecutive states are not re-sent. */
export function publishDisplay(state: DisplayState): void {
  if (last && JSON.stringify(last) === JSON.stringify(state)) return
  last = state
  getChannel()?.postMessage({ type: 'state', state } satisfies Msg)
}

/** Display side: receive states. Returns an unsubscribe function. */
export function subscribeDisplay(onState: (s: DisplayState) => void): () => void {
  if (typeof BroadcastChannel === 'undefined') return () => {}
  const ch = new BroadcastChannel(DISPLAY_CHANNEL)
  ch.onmessage = (e: MessageEvent<Msg>) => {
    if (e.data?.type === 'state') onState(e.data.state)
  }
  ch.postMessage({ type: 'hello' } satisfies Msg)
  return () => ch.close()
}

/** "Till 123456" / "Paybill 400200" from the shop's payment settings. */
export function payTarget(b: { till_number?: string; paybill_number?: string }): { payTo: string; isPaybill: boolean } {
  if (b.paybill_number) return { payTo: `Paybill ${b.paybill_number}`, isPaybill: true }
  if (b.till_number) return { payTo: `Till ${b.till_number}`, isPaybill: false }
  return { payTo: '', isPaybill: false }
}

/** Open (or focus) the customer screen in its own window. */
export function openCustomerDisplay(withBase: (p: string) => string): void {
  const w = window.open(withBase('/display'), 'ledgerpos-customer-display', 'popup,width=1024,height=640')
  w?.focus()
}

/** test hook */
export function _resetDisplayForTests(): void {
  channel?.close()
  channel = null
  last = null
}

// ---------------------------------------------------------------------------
// Till-side state builder (pure, unit-tested): what the customer should see
// for the rail's current situation.

export interface RailSnapshot {
  brand: DisplayBrand
  lines: { name: string; qty: number; unitPriceCents: number }[]
  /** the amount the customer pays right now (after discount/redemption) */
  dueCents: number
  discountCents: number
  receivedCents: number | null
  /** an order is awaiting M-Pesa (the rail is on its payment panel) */
  pending: null | {
    number: string
    totalCents: number
    payments?: { method: string; status: string; amountCents: number }[]
    stkStage: string
    manualOnly: boolean
  }
  till: { till_number?: string; paybill_number?: string }
  /** a just-finished sale's thank-you screen is still showing */
  holdingPaid: boolean
  fmt: (cents: number) => string
}

/** What is still owed on an order: total minus COMPLETED payments (a split
 *  sale's cash leg is already in). Never negative. */
export function outstandingCents(o: { totalCents: number; payments?: { status: string; amountCents: number }[] }): number {
  const paid = (o.payments ?? []).filter((p) => p.status === 'COMPLETED').reduce((a, p) => a + p.amountCents, 0)
  return Math.max(0, o.totalCents - paid)
}

export function railDisplayState(s: RailSnapshot): DisplayState | null {
  const { brand, fmt } = s
  if (s.pending) {
    if (s.pending.stkStage === 'paid') {
      return { kind: 'paid', brand, total: fmt(s.pending.totalCents) }
    }
    const t = payTarget(s.till)
    const stkSent = !s.pending.manualOnly && ['sent', 'waiting', 'slow'].includes(s.pending.stkStage)
    return {
      kind: 'pay',
      brand,
      amount: fmt(outstandingCents(s.pending)),
      payTo: t.payTo,
      account: t.isPaybill ? s.pending.number : undefined,
      stkSent,
    }
  }
  if (s.lines.length === 0) {
    // keep "Asante" up after a sale; the display returns to idle itself
    return s.holdingPaid ? null : { kind: 'idle', brand }
  }
  const change = s.receivedCents !== null && s.receivedCents >= s.dueCents ? s.receivedCents - s.dueCents : null
  return {
    kind: 'cart',
    brand,
    lines: s.lines.map((l) => ({ name: l.name, qty: l.qty, total: fmt(l.unitPriceCents * l.qty) })),
    itemCount: s.lines.reduce((a, l) => a + l.qty, 0),
    total: fmt(s.dueCents),
    discount: s.discountCents > 0 ? fmt(s.discountCents) : undefined,
    received: s.receivedCents !== null && s.receivedCents > 0 ? fmt(s.receivedCents) : undefined,
    change: change !== null && s.receivedCents! > 0 ? fmt(change) : undefined,
  }
}
