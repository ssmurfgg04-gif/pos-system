// v1.3.6 regression tests — the owner-side PIN reset approve: type the new
// 4-digit PIN once, tap Approve, done — the locked till collects the grant
// by itself. NO "confirm it's you" password dialog anywhere (the admin is
// already signed in on their own till), and the legacy code fallback (empty
// PIN box) still shows the one-time code.
// @vitest-environment jsdom
import { describe, expect, test, vi, beforeEach } from 'vitest'
import React, { act } from 'react'
import { createRoot, Root } from 'react-dom/client'

const routes = new Map<string, unknown>()
vi.mock('../src/lib/api', () => ({
  backendMode: vi.fn(() => Promise.resolve('desktop')),
  api: {
    get: vi.fn((url: string) => {
      if (routes.has(url)) return Promise.resolve(routes.get(url))
      if (url === '/api/v1/team-sync/pin-resets') return Promise.resolve([])
      if (url === '/api/v1/roles') return Promise.resolve([{ id: 1, name: 'Cashier' }])
      if (url.startsWith('/api/v1/team-sync')) return Promise.resolve({
        enabled: true, source: 'cloud', registered: true, approved: true,
        pending: 0, teamCode: 'KIAMBU-MAIN', deviceCount: 2,
        devices: [], storePending: false, autoApprove: false,
      })
      return Promise.resolve([])
    }),
    post: vi.fn((url: string, body: any) => {
      if (url === '/api/v1/team-sync/pin-resets/approve') {
        return Promise.resolve(approveReply)
      }
      return Promise.resolve({})
    }),
    put: vi.fn(() => Promise.resolve({})),
  },
  PinResetRow: {},
}))

import { Settings } from '../src/pages/Settings'
import { api } from '../src/lib/api'
import { useAuth } from '../src/stores/auth'

;(React as any).actEnvironment = true
;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true

let approveReply: any = { granted: true, code: '', username: 'Salma PC', deviceName: 'DESKTOP-O8A5QMR', expiresMins: 1440 }
let root: Root | null = null

async function mountSettings() {
  document.body.innerHTML = '<div id="root"></div>'
  root = createRoot(document.getElementById('root')!)
  await act(async () => {
    root!.render(<Settings />)
    await Promise.resolve()
  })
  await act(async () => { await new Promise((r) => setTimeout(r, 30)) })
  const el = document.getElementById('root')!
  // Switch to the Team tab where the PIN reset panel lives.
  const teamTab = Array.from(el.querySelectorAll('button')).find((b) => b.textContent?.trim().toLowerCase() === 'team')
  if (teamTab) {
    await act(async () => {
      teamTab.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
    })
    await act(async () => { await new Promise((r) => setTimeout(r, 30)) })
  }
  return document.getElementById('root')!
}

async function typeNewPin(el: HTMLElement, pin: string) {
  const input = el.querySelector<HTMLInputElement>('input[aria-label^="New PIN for"]')
  expect(input, 'new-PIN input visible').toBeTruthy()
  // React tracks input value: set through the native setter so onChange fires.
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  await act(async () => {
    set.call(input!, pin)
    input!.dispatchEvent(new Event('input', { bubbles: true }))
    await Promise.resolve()
  })
}

async function clickApprove(el: HTMLElement) {
  const btn = Array.from(el.querySelectorAll('button')).find((b) => b.textContent?.includes('Approve'))
  expect(btn, 'Approve button visible').toBeTruthy()
  await act(async () => {
    btn!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await Promise.resolve()
  })
  await act(async () => { await new Promise((r) => setTimeout(r, 30)) })
}

beforeEach(() => {
  routes.clear()
  approveReply = { granted: true, code: '', username: 'Salma PC', deviceName: 'DESKTOP-O8A5QMR', expiresMins: 1440 }
  vi.mocked(api.get).mockClear()
  vi.mocked(api.post).mockClear()
  useAuth.setState({ user: { id: 1, username: 'owner', role: 'owner', shopId: 1 } as any, token: 't' } as any)
})

describe('Settings PIN reset approve', () => {
  test('approve with a typed PIN sends the grant and says they just sign in', async () => {
    const pinResets = [{ id: 3, deviceId: 'dev-o8a5qmr', deviceName: 'DESKTOP-O8A5QMR', username: 'Salma PC', status: 'pending', createdAt: new Date().toISOString() }]
    routes.set('/api/v1/team-sync/pin-resets', pinResets)
    const el = await mountSettings()
    await typeNewPin(el, '8188')
    await clickApprove(el)
    expect(vi.mocked(api.post)).toHaveBeenCalledWith('/api/v1/team-sync/pin-resets/approve', { requestId: 3, newPin: '8188' })
    expect(el.textContent).toContain('now signs in with the PIN you typed')
    // The confirm-it's-you dialog must never appear on the approve path.
    expect(el.textContent).not.toContain('Confirm it')
    expect(el.textContent).not.toContain('owner password')
  })

  test('empty PIN box falls back to the one-time code, shown once', async () => {
    approveReply = { granted: false, code: '42424242', username: 'Salma PC', deviceName: 'DESKTOP-O8A5QMR', expiresMins: 15, delivered: true }
    const pinResets = [{ id: 3, deviceId: 'dev-o8a5qmr', deviceName: 'DESKTOP-O8A5QMR', username: 'Salma PC', status: 'pending', createdAt: new Date().toISOString() }]
    routes.set('/api/v1/team-sync/pin-resets', pinResets)
    const el = await mountSettings()
    await clickApprove(el)
    expect(vi.mocked(api.post)).toHaveBeenCalledWith('/api/v1/team-sync/pin-resets/approve', { requestId: 3, newPin: '' })
    expect(el.textContent).toContain('42424242')
    expect(el.textContent).toContain('Sent straight to that till over the WiFi')
    expect(el.textContent).not.toContain('Confirm it')
  })

  test('a non-4-digit PIN is refused locally, nothing is sent', async () => {
    const pinResets = [{ id: 3, deviceId: 'dev-o8a5qmr', deviceName: 'DESKTOP-O8A5QMR', username: 'Salma PC', status: 'pending', createdAt: new Date().toISOString() }]
    routes.set('/api/v1/team-sync/pin-resets', pinResets)
    const el = await mountSettings()
    await typeNewPin(el, '81')
    await clickApprove(el)
    expect(vi.mocked(api.post)).not.toHaveBeenCalled()
  })
})
