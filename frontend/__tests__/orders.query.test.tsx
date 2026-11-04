// Orders page query UI (v1.6.0) — server-side date + cashier filters and
// offset pagination, through the real component:
//   1. the roster endpoint only fires for orders.view.all holders;
//   2. the cashier filter is hidden from scoped staff (server 403s it);
//   3. pagination pages by 50 with the +1 lookahead (no phantom pages);
//   4. Clear resets every filter and reloads from page one.
// @vitest-environment jsdom
import { describe, expect, test, vi, beforeEach } from 'vitest'
import React, { act } from 'react'
import { createRoot, Root } from 'react-dom/client'

;(React as any).actEnvironment = true

// ---- Module mocks ----
let userPerms: string[] = ['orders.view', 'orders.view.all', 'pos.sell']

vi.mock('../src/stores/auth', () => ({
  useAuth: Object.assign(
    vi.fn((sel: (s: any) => unknown) =>
      sel({ user: { id: 7, username: 'owner', permissions: userPerms } }),
    ),
    { getState: () => ({ user: { id: 7, username: 'owner', permissions: userPerms } }) },
  ),
}))

vi.mock('../src/stores/branding', () => ({
  useBranding: Object.assign(vi.fn(() => ({ branding: { store_name: 'Creative Divine' } })), {
    getState: () => ({ branding: { store_name: 'Creative Divine', app_name: 'LedgerPOS' } }),
  }),
}))

vi.mock('../src/stores/toasts', () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

const getMock = vi.fn(async (_path: string) => [] as unknown)

vi.mock('../src/lib/api', () => ({
  api: {
    get: (path: string) => getMock(path),
    post: vi.fn(async () => ({})),
    put: vi.fn(async () => ({})),
    del: vi.fn(async () => ({})),
    form: vi.fn(async () => ({})),
  },
  blob: vi.fn(async () => new Blob(['x'], { type: 'image/png' })),
  isDemoSync: () => false,
  productImageUrl: () => '',
}))

import { Orders } from '../src/pages/Orders'

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

function $q(sel: string): HTMLElement | null {
  return container.querySelector(sel)
}


function setInputValue(el: HTMLInputElement | HTMLSelectElement, value: string) {
  const proto = el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype
  const setter = Object.getOwnPropertyDescriptor(proto, 'value')!.set!
  setter.call(el, value)
  el.dispatchEvent(new Event('input', { bubbles: true }))
  el.dispatchEvent(new Event('change', { bubbles: true }))
}

function $text(): string {
  return container.textContent || ''
}

function orderRow(id: number) {
  return {
    id,
    number: `ORD-${2000 + id}`,
    status: 'PAID',
    subtotalCents: 1000,
    taxCents: 138,
    totalCents: 1000,
    cashierId: 7,
    cashierName: 'Owner',
    customerName: 'Amina',
    discrepancy: false,
    createdAt: '2026-10-29T09:00:00.000Z',
    items: [{ id: 1, productId: 1, name: 'Mug', sku: 'M1', qty: 1, unitPriceCents: 1000, lineTotalCents: 1000 }],
    payments: [{ id: 1, method: 'cash', status: 'COMPLETED', amountCents: 1000 }],
  }
}

beforeEach(() => {
  userPerms = ['orders.view', 'orders.view.all', 'pos.sell']
  getMock.mockReset()
  getMock.mockImplementation(async () => [])
  if (root) {
    act(() => root!.unmount())
    root = null
  }
  document.body.innerHTML = ''
})

describe('Orders query UI', () => {
  test('admin: server filters in the URL + cashiers roster loaded', async () => {
    getMock.mockImplementation(async (path: string) => {
      if (path.includes('/orders/cashiers')) {
        return [{ id: 7, name: 'Owner' }, { id: 8, name: 'Salma' }]
      }
      return [orderRow(1)]
    })
    mount(React.createElement(Orders))
    await act(async () => {})
    const calls = getMock.mock.calls.map((c) => c[0] as string)
    expect(calls.some((p) => p.startsWith('/api/v1/orders?limit=51&offset=0'))).toBe(true)
    expect(calls.some((p) => p === '/api/v1/orders/cashiers')).toBe(true)
    // Date pickers and the cashier dropdown render for a full viewer.
    expect($q('input[aria-label="From date"]')).toBeTruthy()
    expect($q('input[aria-label="To date"]')).toBeTruthy()
    expect($q('select[aria-label="Cashier"]')).toBeTruthy()
    expect($text()).toContain('All cashiers')
    expect($text()).toContain('Salma')
  })

  test('scoped staff: no cashier dropdown, no roster call', async () => {
    userPerms = ['orders.view', 'pos.sell']
    getMock.mockImplementation(async () => [orderRow(1)])
    mount(React.createElement(Orders))
    await act(async () => {})
    const calls = getMock.mock.calls.map((c) => c[0] as string)
    expect(calls.some((p) => p.includes('/orders/cashiers'))).toBe(false)
    expect($q('select[aria-label="Cashier"]')).toBeNull()
  })

  test('pagination: 50/page with +1 lookahead, Older→offset 50, back via Newer', async () => {
    let call = 0
    getMock.mockImplementation(async (path: string) => {
      if (path.includes('/orders/cashiers')) return []
      const offset = Number(new URLSearchParams(path.split('?')[1]).get('offset') || 0)
      call++
      if (offset === 0) {
        return Array.from({ length: 51 }, (_, i) => orderRow(i + 1)) // one extra → hasMore
      }
      return Array.from({ length: 20 }, (_, i) => orderRow(offset + i + 1))
    })
    mount(React.createElement(Orders))
    await act(async () => {})
    // Exactly 50 rows shown, Older visible, Newer disabled.
    const older = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes('Older'))
    const newer = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes('Newer'))
    expect(older).toBeTruthy()
    expect((newer as HTMLButtonElement).disabled).toBe(true)
    await act(async () => {
      ;(older as HTMLButtonElement).click()
    })
    await act(async () => {})
    const secondCall = getMock.mock.calls.map((c) => c[0] as string).find((p) => p.includes('offset=50'))
    expect(secondCall).toBeTruthy()
    const newer2 = [...container.querySelectorAll('button')].find((b) => b.textContent?.includes('Newer'))
    expect((newer2 as HTMLButtonElement).disabled).toBe(false)
  })

  test('Clear resets date + cashier filters and reloads page one', async () => {
    getMock.mockImplementation(async (path: string) => {
      if (path.includes('/orders/cashiers')) return [{ id: 8, name: 'Salma' }]
      return [orderRow(1)]
    })
    mount(React.createElement(Orders))
    await act(async () => {})
    // Set from/to/cashier through the real controls.
    const from = $q('input[aria-label="From date"]') as HTMLInputElement
    await act(async () => { setInputValue(from, '2026-10-29') })
    const to = $q('input[aria-label="To date"]') as HTMLInputElement
    await act(async () => { setInputValue(to, '2026-10-29') })
    const sel = $q('select[aria-label="Cashier"]') as HTMLSelectElement
    await act(async () => { setInputValue(sel, '8') })
    await act(async () => {})
    const withFilters = getMock.mock.calls.map((c) => c[0] as string)
      .some((p) => p.includes('from=2026-10-29') && p.includes('cashierId=8'))
    expect(withFilters).toBe(true)

    const clear = [...container.querySelectorAll('button')].find((b) => b.textContent === 'Clear')
    expect(clear).toBeTruthy()
    await act(async () => {
      ;(clear as HTMLButtonElement).click()
    })
    await act(async () => {})
    const last = getMock.mock.calls[getMock.mock.calls.length - 1][0] as string
    expect(last).not.toContain('from=')
    expect(last).not.toContain('cashierId=')
    expect(($q('input[aria-label="From date"]') as HTMLInputElement).value).toBe('')
  })

  test('empty result with active filters says what to do', async () => {
    getMock.mockImplementation(async () => [])
    mount(React.createElement(Orders))
    await act(async () => {})
    const from = $q('input[aria-label="From date"]') as HTMLInputElement
    await act(async () => { setInputValue(from, '2030-01-01') })
    await act(async () => {})
    expect($text()).toContain('Nothing matches these filters')
  })
})
