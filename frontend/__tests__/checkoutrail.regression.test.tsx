// v1.1.7 regression suite — the client-visible payment batch:
//   1. WHITE-SCREEN CRASH: entering the in-rail STK payment panel unmounted
//      the whole app ("Rendered fewer hooks than expected") because
//      CheckoutRail ran a useMemo BELOW an early return. This test drives
//      the exact cashier flow that crashed: cart → phone → Charge (STK).
//   2. ErrorBoundary: a render crash must show a recovery card, never a
//      white screen.
//   3. Cart: quantity 1 → minus removes the line (card stepper semantics).
// @vitest-environment jsdom
import { describe, expect, test, vi, beforeEach, afterEach } from 'vitest'
import React, { act } from 'react'
import { createRoot, Root } from 'react-dom/client'

;(React as any).actEnvironment = true

// ---- Module mocks (CheckoutRail's network layer) ----
const paidOrder = (id: number) => ({
  id,
  number: `ORD-${1000 + id}`,
  status: 'PENDING',
  subtotalCents: 55000,
  taxCents: 8483,
  totalCents: 55000,
  cashierId: 1,
  cashierName: 'Test',
  discrepancy: false,
  createdAt: new Date().toISOString(),
  items: [{ id: 1, productId: 7, name: 'Classic Cotton Tee — Black', sku: 'TS-001', qty: 1, unitPriceCents: 55000, lineTotalCents: 55000 }],
  payments: [{ id: 1, method: 'mpesa', mode: 'stk', amountCents: 55000, status: 'PENDING', phone: '254722123456' }],
})

const postMock = vi.fn(async (_path: string, _body?: unknown) => paidOrder(42))

vi.mock('../src/lib/api', () => ({
  api: {
    get: vi.fn(async () => []),
    post: (...a: unknown[]) => postMock(a[0] as string, a[1]),
    put: vi.fn(async () => ({})),
    del: vi.fn(async () => ({})),
    form: vi.fn(async () => ({})),
  },
  isDemoSync: () => false,
  productImageUrl: () => '',
  openPaystackPopup: vi.fn(),
}))

vi.mock('../src/offline/queue', () => ({
  enqueue: vi.fn(async () => {}),
  newClientUuid: () => 'test-uuid-0001',
}))

vi.mock('../src/offline/heartbeat', () => ({
  useNet: (sel: (s: { online: boolean }) => boolean) =>
    sel({ online: true }),
}))

import { CheckoutRail } from '../src/components/CheckoutRail'
import { ErrorBoundary } from '../src/components/ui'
import { useCart, CartLine } from '../src/stores/cart'

let root: Root | null = null
let container: HTMLDivElement

function mount(el: React.ReactElement) {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(el)
  })
}

function unmount() {
  if (root) {
    act(() => { root!.unmount() })
    root = null
  }
  container?.remove()
}

/** Types through the native setter so React 18 controlled inputs update. */
function typeInto(el: HTMLInputElement, text: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  act(() => {
    setter.call(el, text)
    el.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

async function click(el: HTMLElement) {
  await act(async () => {
    el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    // let the async checkout promise settle and the state machine mount
    await new Promise((r) => setTimeout(r, 30))
  })
}

const line = (over: Partial<CartLine> = {}): CartLine => ({
  productId: 7,
  name: 'Classic Cotton Tee — Black',
  sku: 'TS-001',
  unitPriceCents: 55000,
  qty: 1,
  trackStock: false,
  stockQty: 0,
  ...over,
})

beforeEach(() => {
  useCart.setState({ lines: [], customerName: '', note: '' })
  postMock.mockClear()
})

afterEach(() => { unmount() })

describe('CheckoutRail — in-rail STK payment (white-screen regression)', () => {
  test('entering the STK payment panel keeps every hook mounted and renders in-rail feedback', async () => {
    useCart.setState({ lines: [line()] })
    mount(<CheckoutRail products={null} payConfig={null} onOrderPaid={() => {}} onOrderVoided={() => {}} />)

    // Phone is required for M-Pesa — type a Kenyan number.
    const phone = container.querySelector('input[placeholder="07XX XXX XXX"]') as HTMLInputElement
    expect(phone).toBeTruthy()
    typeInto(phone, '0722123456')

    // The charge button must be enabled and labeled with the STK action.
    const charge = Array.from(container.querySelectorAll('button')).find((b) => /Send STK/.test(b.textContent || ''))
    expect(charge).toBeTruthy()
    expect((charge as HTMLButtonElement).disabled).toBe(false)

    await click(charge!)

    // Pre-fix, this re-render crashed the whole React tree
    // ("Rendered fewer hooks than expected") — the app white-screened.
    expect(container.textContent).toContain('Payment —')
    expect(container.textContent).toContain('Initiating')
    expect(container.textContent).toContain('Prompt sent')
    expect(container.textContent).toContain('Enter PIN')
    expect(postMock).toHaveBeenCalledTimes(2) // checkout, then the STK push
    expect(postMock.mock.calls[0][0]).toContain('/orders/checkout')
    expect(postMock.mock.calls[1][0]).toContain('/stkpush')
  })

  test('failed STK shows an in-rail error with retry — cart preserved, app alive', async () => {
    postMock.mockImplementation(async (path: string) => {
      if (String(path).includes('/stkpush')) {
        const o = paidOrder(43)
        return { ...o, payments: [{ ...o.payments[0], status: 'FAILED', resultDesc: 'Request cancelled by user' }] }
      }
      return paidOrder(43)
    })
    useCart.setState({ lines: [line()] })
    mount(<CheckoutRail products={null} payConfig={null} onOrderPaid={() => {}} onOrderVoided={() => {}} />)

    const phone = container.querySelector('input[placeholder="07XX XXX XXX"]') as HTMLInputElement
    typeInto(phone, '0722123456')
    const charge = Array.from(container.querySelectorAll('button')).find((b) => /Send STK/.test(b.textContent || ''))
    await click(charge!)

    expect(container.textContent).toContain("STK push didn't complete")
    expect(container.textContent).toContain('Request cancelled by user')
    expect(container.textContent).toContain('Retry')
    // The cart is NOT cleared by a failed payment (the order stays pending
    // server-side; the rail only clears on verified success or explicit void).
    expect(useCart.getState().lines).toHaveLength(1)
    // The app itself is still alive — no boundary triggered, no empty root.
    expect(container.children.length).toBeGreaterThan(0)
  })
})

describe('ErrorBoundary', () => {
  test('a render crash renders the recovery card instead of an empty screen', () => {
    function Boom(): never {
      throw new Error('boom — simulated payment panel crash')
    }
    mount(
      <ErrorBoundary label="test" compact>
        <Boom />
      </ErrorBoundary>,
    )
    expect(container.textContent).toContain('This panel hit a snag')
    expect(container.textContent).toContain('boom')
    expect(container.textContent).toContain('Try again')
    expect(container.children.length).toBeGreaterThan(0)
  })
})

describe('Cart quantity semantics (card stepper)', () => {
  test('setQty to 0 removes the line — minus at qty 1 means remove', () => {
    act(() => { useCart.getState().add({ id: 7, sku: 'TS-001', barcode: '', name: 'Tee', categoryId: 1, categoryName: '', priceCents: 55000, costCents: 0, stockQty: 10, trackStock: true, active: true, updatedAt: '' }, 1) })
    expect(useCart.getState().lines).toHaveLength(1)
    act(() => { useCart.getState().setQty(7, 2) })
    expect(useCart.getState().lines[0].qty).toBe(2)
    act(() => { useCart.getState().setQty(7, 1) })
    expect(useCart.getState().lines[0].qty).toBe(1)
    act(() => { useCart.getState().setQty(7, 0) })
    expect(useCart.getState().lines).toHaveLength(0)
  })
})
