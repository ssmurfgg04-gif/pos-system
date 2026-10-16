// v1.3.5 regression tests — the owner-side PIN reset approve: tapping
// Approve must show the one-time code immediately (no "confirm it's you"
// password challenge — the admin is already signed in on their own till)
// and say so when the code also flew to the locked till over the WiFi.
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

let approveReply: any = { code: '42424242', username: 'Salma PC', deviceName: 'DESKTOP-O8A5QMR', expiresMins: 15, delivered: true }
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
  approveReply = { code: '42424242', username: 'Salma PC', deviceName: 'DESKTOP-O8A5QMR', expiresMins: 15, delivered: true }
  vi.mocked(api.get).mockClear()
  vi.mocked(api.post).mockClear()
  useAuth.setState({ user: { id: 1, username: 'owner', role: 'owner', shopId: 1 } as any, token: 't' } as any)
})

describe('Settings PIN reset approve', () => {
  test('shows the code right away and the WiFi delivery note, no password dialog', async () => {
    const pinResets = [{ id: 3, deviceId: 'dev-o8a5qmr', deviceName: 'DESKTOP-O8A5QMR', username: 'Salma PC', status: 'pending', createdAt: new Date().toISOString() }]
    routes.set('/api/v1/team-sync/pin-resets', pinResets)
    const el = await mountSettings()
    await clickApprove(el)
    expect(el.textContent).toContain('42424242')
    expect(el.textContent).toContain('Sent straight to that till over the WiFi')
    // The confirm-it's-you dialog must never appear on the approve path.
    expect(el.textContent).not.toContain('Confirm it')
    expect(el.textContent).not.toContain('owner password')
    expect(vi.mocked(api.post)).toHaveBeenCalledWith('/api/v1/team-sync/pin-resets/approve', { requestId: 3 })
  })

  test('falls back to the read-aloud note when WiFi delivery missed', async () => {
    approveReply = { code: '42424242', username: 'Salma PC', deviceName: 'DESKTOP-O8A5QMR', expiresMins: 15, delivered: false }
    const pinResets = [{ id: 3, deviceId: 'dev-o8a5qmr', deviceName: 'DESKTOP-O8A5QMR', username: 'Salma PC', status: 'pending', createdAt: new Date().toISOString() }]
    routes.set('/api/v1/team-sync/pin-resets', pinResets)
    const el = await mountSettings()
    await clickApprove(el)
    expect(el.textContent).toContain('42424242')
    expect(el.textContent).toContain('Read it to them')
    expect(el.textContent).not.toContain('Sent straight to that till')
  })
})
