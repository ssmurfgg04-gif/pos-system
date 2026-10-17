// v1.3.6 regression tests — the locked-till side of the direct PIN grant:
// the Forgot-PIN panel collects the grant the moment the owner approves
// (no code entry at all), and the PIN pad itself picks it up while a user
// is selected — the worker just types the PIN they were told.
// @vitest-environment jsdom
import { describe, expect, test, vi, beforeEach } from 'vitest'
import React, { act } from 'react'
import { createRoot, Root } from 'react-dom/client'

const routes = new Map<string, unknown>()
vi.mock('../src/lib/api', () => ({
  api: {
    get: vi.fn((url: string) => {
      if (routes.has(url)) return Promise.resolve(routes.get(url))
      return Promise.resolve([])
    }),
    post: vi.fn((url: string, body: any) => {
      if (routes.has('post:' + url)) return Promise.resolve(routes.get('post:' + url))
      return Promise.resolve({})
    }),
  },
}))

import { Pin, ForgotPinPanel } from '../src/pages/Pin'
import { api } from '../src/lib/api'

;(React as any).actEnvironment = true
;(globalThis as any).IS_REACT_ACT_ENVIRONMENT = true

let root: Root | null = null

async function mount(ui: React.ReactNode) {
  document.body.innerHTML = '<div id="root"></div>'
  root = createRoot(document.getElementById('root')!)
  await act(async () => {
    root!.render(ui)
    await Promise.resolve()
  })
  await act(async () => { await new Promise((r) => setTimeout(r, 20)) })
  return document.getElementById('root')!
}

beforeEach(() => {
  routes.clear()
  vi.mocked(api.get).mockClear()
  vi.mocked(api.post).mockClear()
})

describe('ForgotPinPanel direct grant', () => {
  test('a landed grant replaces the code box with go-type-your-PIN', async () => {
    routes.set('/api/v1/auth/forgot-pin/status', Promise.resolve({ active: false, grantApplied: true, username: 'Salma PC' }))
    const el = await mount(
      <ForgotPinPanel
        preselect={{ id: 7, username: 'Salma PC', fullName: 'Salma PC', roleName: 'Cashier', shopId: 1 }}
        state={{ step: 'code', requestId: 9, username: 'Salma PC' }}
        setState={() => undefined}
        onDone={() => undefined}
        onCancel={() => undefined}
      />,
    )
    expect(el.textContent).toContain('The owner just set your PIN')
    expect(el.textContent).toContain('Back to my PIN pad')
    // No 8-digit code input visible in the granted state.
    expect(el.textContent).not.toContain('The 8-digit code')
  })

  test('the request step sends the LOGIN name (not the display name) and no longer asks the worker to relay codes', async () => {
    routes.set('post:/api/v1/auth/forgot-pin', { requestId: 11, username: 'salma', reused: false })
    const el = await mount(
      <ForgotPinPanel
        preselect={{ id: 7, username: 'salma', fullName: 'Salma PC', roleName: 'Cashier', shopId: 1 }}
        state={{ step: 'request', requestId: 0, username: 'salma' }}
        setState={() => undefined}
        onDone={() => undefined}
        onCancel={() => undefined}
      />,
    )
    // The preselect's login name is shown (read-only path — no editable box).
    expect(el.textContent).toContain('salma')
    const btn = Array.from(el.querySelectorAll('button')).find((b) => b.textContent?.includes('Send reset request'))
    expect(btn).toBeTruthy()
    await act(async () => {
      btn!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
    })
    await act(async () => { await new Promise((r) => setTimeout(r, 20)) })
    expect(vi.mocked(api.post)).toHaveBeenCalledWith('/api/v1/auth/forgot-pin', { username: 'salma' })
  })
})

describe('Pin pad grant listening', () => {
  test('a grant landing while a user is picked shows the note on the pad', async () => {
    routes.set('/api/v1/auth/pin-users', [
      { id: 7, username: 'salma', fullName: 'Salma PC', roleName: 'Cashier', shopId: 1 },
    ])
    // The poll fires the moment a face is picked; the grant is already in.
    vi.mocked(api.get).mockImplementation((url: string) => {
      if (url === '/api/v1/auth/pin-users') return Promise.resolve(routes.get(url))
      if (url === '/api/v1/auth/forgot-pin/status') {
        return Promise.resolve({ active: false, grantApplied: true, username: 'Salma PC' })
      }
      return Promise.resolve([])
    })
    const el = await mount(<Pin />)
    const face = Array.from(el.querySelectorAll('button')).find((b) => b.textContent?.includes('Salma PC'))
    expect(face, 'user face visible').toBeTruthy()
    await act(async () => {
      face!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      await Promise.resolve()
    })
    await act(async () => { await new Promise((r) => setTimeout(r, 60)) })
    expect(el.textContent).toContain('Your PIN was set by the owner')
    // The pad stays a pad: no code box, worker just types their PIN.
    expect(el.textContent).toContain('Enter your 4-digit PIN')
  })
})
