// v1.1.9 regression — PIN screen keyboard input:
//   Cashiers had to tap the on-screen pad with the mouse only. Now the
//   physical keyboard works: 1–9 picks a face, 0–9 types the PIN,
//   Backspace erases, Escape clears, Enter submits — and the double-
//   submit guard keeps a 4th-digit auto-submit + Enter from logging in
//   twice (server lockout counts attempts).
// @vitest-environment jsdom
import { describe, expect, test, vi, beforeEach, afterEach } from 'vitest'
import React, { act } from 'react'
import { createRoot, Root } from 'react-dom/client'

;(React as any).actEnvironment = true

const pinUsers = [
  { id: 1, fullName: 'Asha Kimani', roleName: 'Cashier' },
  { id: 2, fullName: 'Brian Otieno', roleName: 'Manager' },
]

const pinLoginMock = vi.fn(async (_userId: number, _code: string) => {})

vi.mock('../src/lib/api', () => ({
  api: {
    get: vi.fn(async (path: string) =>
      path === '/api/v1/auth/pin-users' ? pinUsers : []),
    post: vi.fn(async () => ({})),
    put: vi.fn(async () => ({})),
    del: vi.fn(async () => ({})),
    form: vi.fn(async () => ({})),
  },
  isDemoSync: () => false,
  productImageUrl: () => '',
}))

vi.mock('../src/stores/auth', () => ({
  useAuth: Object.assign(
    vi.fn(() => ({ pinLogin: pinLoginMock })),
    { getState: () => ({ user: { id: 1, role: 'CASHIER' } }) },
  ),
}))

vi.mock('../src/stores/branding', () => ({
  useBranding: (sel: (s: unknown) => unknown) =>
    sel({ branding: { store_name: 'Creative Divine', app_name: 'LedgerPOS' } }),
}))

vi.mock('../src/lib/router', () => ({
  navigate: vi.fn(),
  homeFor: () => '/pos',
}))

vi.mock('../src/ws/client', () => ({ reconnectWs: vi.fn() }))
vi.mock('../src/stores/toasts', () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }))

import { Pin } from '../src/pages/Pin'

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

function pressKey(key: string) {
  act(() => {
    window.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
  })
}

describe('Pin screen keyboard input', () => {
  let host: HTMLDivElement
  let root: Root

  beforeEach(() => {
    host = document.createElement('div')
    document.body.appendChild(host)
    root = createRoot(host)
    pinLoginMock.mockClear()
  })
  afterEach(() => act(() => root.unmount()))

  function mount() {
    act(() => { root.render(<Pin />) })
  }

  test('1–9 on the picker selects the Nth face without the mouse', async () => {
    mount()
    await act(async () => { await sleep(20) })
    expect(host.textContent).toContain('Who is starting this shift?')
    pressKey('1')
    expect(host.textContent).toContain('Hello, Asha Kimani')
  })

  test('digits type the PIN and auto-submit fires one login', async () => {
    mount()
    await act(async () => { await sleep(20) })
    pressKey('1') // pick Asha
    for (const d of ['1', '2', '3', '4']) pressKey(d)
    await act(async () => { await sleep(250) }) // 120ms auto-submit + settle
    expect(pinLoginMock).toHaveBeenCalledTimes(1)
    expect(pinLoginMock).toHaveBeenCalledWith(1, '1234')
    expect(host.textContent).not.toContain('Who is starting this shift?')
  })

  test('Backspace erases; the retried code is what submits', async () => {
    mount()
    await act(async () => { await sleep(20) })
    pressKey('2') // pick Brian
    pressKey('1'); pressKey('2'); pressKey('3')
    pressKey('Backspace') // drop the 3
    pressKey('4'); pressKey('5')
    await act(async () => { await sleep(250) })
    expect(pinLoginMock).toHaveBeenCalledWith(2, '1245')
  })

  test('Enter right after the 4th digit does not double-login', async () => {
    mount()
    await act(async () => { await sleep(20) })
    pressKey('1')
    for (const d of ['9', '9', '9', '9']) pressKey(d)
    pressKey('Enter') // inside the 120ms auto-submit window
    await act(async () => { await sleep(250) })
    expect(pinLoginMock).toHaveBeenCalledTimes(1)
  })
})
