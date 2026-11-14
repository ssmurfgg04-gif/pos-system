import { afterEach, describe, expect, it } from 'vitest'
import {
  DisplayState,
  _resetDisplayForTests,
  outstandingCents,
  payTarget,
  publishDisplay,
  railDisplayState,
  RailSnapshot,
  subscribeDisplay,
} from '../src/lib/customerDisplay'

const brand = { storeName: 'Amani Print', logoUrl: '', color: '#0047AB' }
const fmt = (c: number) => `KES ${(c / 100).toLocaleString('en-KE')}`
const base: RailSnapshot = {
  brand,
  lines: [],
  dueCents: 0,
  discountCents: 0,
  receivedCents: null,
  pending: null,
  till: { till_number: '123456' },
  holdingPaid: false,
  fmt,
}

describe('customer display state', () => {
  it('shows the welcome when the cart is empty', () => {
    expect(railDisplayState(base)).toEqual({ kind: 'idle', brand })
  })

  it('keeps the thank-you screen while the cart resets after a sale', () => {
    expect(railDisplayState({ ...base, holdingPaid: true })).toBeNull()
  })

  it('lists lines and the amount actually due (after discount)', () => {
    const s = railDisplayState({
      ...base,
      lines: [
        { name: 'Tee', qty: 2, unitPriceCents: 55000 },
        { name: 'Mug', qty: 1, unitPriceCents: 40000 },
      ],
      dueCents: 140000,
      discountCents: 10000,
    })
    expect(s).toMatchObject({
      kind: 'cart',
      itemCount: 3,
      total: 'KES 1,400',
      discount: 'KES 100',
      lines: [{ name: 'Tee', qty: 2, total: 'KES 1,100' }, { name: 'Mug', qty: 1, total: 'KES 400' }],
    })
  })

  it('shows cash received and change only when enough was tendered', () => {
    const lines = [{ name: 'Tee', qty: 1, unitPriceCents: 55000 }]
    const short = railDisplayState({ ...base, lines, dueCents: 55000, receivedCents: 50000 }) as Extract<DisplayState, { kind: 'cart' }>
    expect(short.received).toBe('KES 500')
    expect(short.change).toBeUndefined()
    const ok = railDisplayState({ ...base, lines, dueCents: 55000, receivedCents: 100000 }) as Extract<DisplayState, { kind: 'cart' }>
    expect(ok.change).toBe('KES 450')
  })

  it('asks for the OUTSTANDING amount on M-Pesa (a split sale\'s cash leg is already in)', () => {
    const s = railDisplayState({
      ...base,
      lines: [{ name: 'Hoodie', qty: 1, unitPriceCents: 200000 }],
      pending: {
        number: 'ORD1',
        totalCents: 200000,
        payments: [
          { method: 'cash', status: 'COMPLETED', amountCents: 50000 },
          { method: 'mpesa', status: 'PENDING', amountCents: 150000 },
        ],
        stkStage: 'waiting',
        manualOnly: false,
      },
    })
    expect(s).toMatchObject({ kind: 'pay', amount: 'KES 1,500', payTo: 'Till 123456', stkSent: true })
    expect((s as { account?: string }).account).toBeUndefined()
  })

  it('shows the paybill account (the order number) for manual paybill payments', () => {
    const s = railDisplayState({
      ...base,
      till: { paybill_number: '400200', till_number: '123456' },
      pending: { number: 'ORD7', totalCents: 90000, stkStage: 'waiting', manualOnly: true },
    })
    expect(s).toMatchObject({ kind: 'pay', payTo: 'Paybill 400200', account: 'ORD7', stkSent: false, amount: 'KES 900' })
  })

  it('switches to thank-you once the payment lands', () => {
    const s = railDisplayState({ ...base, pending: { number: 'ORD1', totalCents: 90000, stkStage: 'paid', manualOnly: false } })
    expect(s).toEqual({ kind: 'paid', brand, total: 'KES 900' })
  })

  it('outstanding never goes negative and ignores failed legs', () => {
    expect(outstandingCents({ totalCents: 100, payments: [{ status: 'COMPLETED', amountCents: 150 }] })).toBe(0)
    expect(outstandingCents({ totalCents: 100, payments: [{ status: 'FAILED', amountCents: 100 }] })).toBe(100)
  })

  it('prefers the paybill over the till when both exist', () => {
    expect(payTarget({ till_number: '1', paybill_number: '2' })).toEqual({ payTo: 'Paybill 2', isPaybill: true })
    expect(payTarget({})).toEqual({ payTo: '', isPaybill: false })
  })
})

describe('customer display channel', () => {
  afterEach(() => _resetDisplayForTests())

  it('a display that opens late receives the current picture', async () => {
    publishDisplay({ kind: 'idle', brand })
    const got: DisplayState[] = []
    const off = subscribeDisplay((s) => got.push(s))
    await new Promise((r) => setTimeout(r, 50))
    off()
    expect(got[0]).toEqual({ kind: 'idle', brand })
  })

  it('live updates reach an open display, without repeats', async () => {
    const got: DisplayState[] = []
    const off = subscribeDisplay((s) => got.push(s))
    const paid: DisplayState = { kind: 'paid', brand, total: 'KES 900' }
    publishDisplay(paid)
    publishDisplay(paid) // identical: not re-sent
    await new Promise((r) => setTimeout(r, 50))
    off()
    expect(got).toEqual([paid])
  })
})
