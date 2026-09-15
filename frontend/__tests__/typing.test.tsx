// Regression: typing into any input must never crash the app.
// Renders the real Login screen, the logged-in POS screen, and a full demo
// login flow, then types into each. Uses only react + react-dom.
// @vitest-environment jsdom
import { describe, expect, test, vi, beforeEach } from 'vitest'
import React, { act } from 'react'
import { createRoot } from 'react-dom/client'

vi.mock('../src/ws/client', () => ({ reconnectWs: vi.fn(), connectWs: () => () => undefined, disconnectWs: vi.fn(), onWsEvent: () => () => undefined }))

import { Login } from '../src/pages/Login'
import { App } from '../src/App'
import { Pos } from '../src/pages/Pos'
import { useAuth } from '../src/stores/auth'
import { useBranding } from '../src/stores/branding'

(React as any).actEnvironment = true

beforeEach(() => {
  localStorage.clear()
  document.body.innerHTML = '<div id="root"></div>'
  vi.spyOn(globalThis, 'fetch').mockRejectedValue(new Error('no server'))
})

function typeText(el: HTMLInputElement, text: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
  for (const ch of text) {
    act(() => {
      setter.call(el, el.value + ch)
      el.dispatchEvent(new Event('input', { bubbles: true }))
    })
  }
}

async function waitForText(t: string) {
  for (let i = 0; i < 60; i++) {
    await act(async () => { await new Promise((r) => setTimeout(r, 50)) })
    if (document.body.innerHTML.includes(t)) return
  }
  throw new Error(`timed out waiting for ${t}`)
}

async function clickButton(label: string) {
  for (let i = 0; i < 60; i++) {
    await act(async () => { await new Promise((r) => setTimeout(r, 50)) })
    const btn = [...document.querySelectorAll('button')].find((b) => b.textContent?.includes(label))
    if (btn && !(btn as HTMLButtonElement).disabled) {
      await act(async () => { btn.click() })
      return
    }
  }
  throw new Error(`timed out waiting for button ${label}`)
}

function fillPlaceholder(placeholder: string, text: string) {
  const el = document.querySelector(`input[placeholder="${placeholder}"]`) as HTMLInputElement
  expect(el).toBeTruthy()
  typeText(el, text)
}

// Walks the five onboarding wizard steps with minimal valid input.
async function walkOnboarding() {
  await waitForText('Set up your shop')
  fillPlaceholder('e.g. Zawadi Prints', 'Test Shop')
  await clickButton('Save & continue') // store
  await waitForText('Skip for now')
  await clickButton('Skip for now') // logo skipped → receipt step
  await waitForText('Receipt footer')
  await clickButton('Save & continue') // receipt (defaults kept)
  await clickButton('Save & continue') // printer (blank target kept)
  await waitForText('Your admin login')
  fillPlaceholder('6+ characters', 'admin123')
  fillPlaceholder('4 digits', '1234')
  await clickButton('Open shop') // staff → done
}

describe('typing repro', () => {
  test('typing a username on Login does not crash', async () => {
    const root = createRoot(document.getElementById('root')!)
    await act(async () => { root.render(<Login />) })
    const box = document.querySelector('input[placeholder="e.g. admin"]') as HTMLInputElement
    expect(box).toBeTruthy()
    typeText(box, 'admin')
    expect(box.value).toBe('admin')
    expect(document.body.innerHTML).toContain('Sign in')
    root.unmount()
  })

  test('logged-in POS search accepts typing without crashing', async () => {
    useAuth.setState({
      user: { id: 1, username: 'admin', fullName: 'Admin', roleId: 1, roleName: 'Admin', permissions: ['pos.sell'], active: true, pinSet: true, mustRotate: false, createdAt: '' },
      ready: true,
    })
    useBranding.setState({ loaded: true } as any)
    const viFetch = vi.spyOn(globalThis, 'fetch') as any
    viFetch.mockImplementation(async (url: string) => {
      const body = String(url).includes('/api/v1/products')
        ? [{ id: 1, sku: 'SKU1', barcode: '1234', name: 'Test Product', categoryId: 1, categoryName: 'General', priceCents: 100, costCents: 50, stockQty: 10, trackStock: true, active: true, updatedAt: '' }]
        : []
      return { ok: true, headers: { get: () => 'application/json' }, text: async () => JSON.stringify({ data: body }) } as any
    })
    const root = createRoot(document.getElementById('root')!)
    await act(async () => { root.render(<Pos />) })
    const box = document.querySelector('input[placeholder*="earch"]') as HTMLInputElement
    expect(box).toBeTruthy()
    typeText(box, 'test')
    expect(box.value).toBe('test')
    expect(document.body.innerHTML).not.toBe('')
    root.unmount()
  })

  test('demo login lands on POS and search typing works end to end', async () => {
    useAuth.setState({ user: null, ready: true })
    const root = createRoot(document.getElementById('root')!)
    await act(async () => { root.render(<App />) })
    // Demo chips appear once the backend probe settles on demo (fetch rejects).
    let adminBtn: HTMLElement | null = null
    for (let i = 0; i < 50 && !adminBtn; i++) {
      await act(async () => { await new Promise((r) => setTimeout(r, 50)) })
      const buttons = [...document.querySelectorAll('button')]
      adminBtn = buttons.find((b) => b.textContent?.includes('Admin')) || null
    }
    expect(adminBtn).toBeTruthy()
    await act(async () => { adminBtn!.click() })
    for (let i = 0; i < 50; i++) {
      await act(async () => { await new Promise((r) => setTimeout(r, 50)) })
      if (useAuth.getState().user) break
    }
    expect(useAuth.getState().user?.username).toBe('admin')
    // Seeded logins must rotate first: complete the Rotate screen.
    await waitForText('Set your login details')
    const pwBox = document.querySelector('input[placeholder="••••••"]') as HTMLInputElement
    const pinBox = document.querySelector('input[placeholder="••••"]') as HTMLInputElement
    expect(pwBox).toBeTruthy()
    typeText(pwBox, 'admin123')
    typeText(pinBox, '1234')
    await clickButton('Save & continue')
    for (let i = 0; i < 50; i++) {
      await act(async () => { await new Promise((r) => setTimeout(r, 50)) })
      if (useAuth.getState().user && !useAuth.getState().user!.mustRotate) break
    }
    expect(useAuth.getState().user?.mustRotate).toBe(false)
    // Fresh demo boxes onboard next: walk the five wizard steps.
    await walkOnboarding()
    // Admins land on /reports by design; go to the sell screen explicitly.
    const { navigate } = await import('../src/lib/router')
    await act(async () => { navigate('/') })
    for (let i = 0; i < 50; i++) {
      await act(async () => { await new Promise((r) => setTimeout(r, 50)) })
      if (document.querySelector('input[placeholder*="earch"]')) break
    }
    const box = document.querySelector('input[placeholder*="earch"]') as HTMLInputElement
    expect(box).toBeTruthy()
    typeText(box, 'coffee')
    expect(box.value).toBe('coffee')
    expect(document.body.innerHTML).not.toBe('')
    // Drain the demo backend's 90–260ms latency window before unmounting so
    // no in-flight request settles (and rejects) after teardown.
    await act(async () => { await new Promise((r) => setTimeout(r, 400)) })
    root.unmount()
  })

  test('full App boots and accepts typing without unmounting', async () => {
    const root = createRoot(document.getElementById('root')!)
    await act(async () => { root.render(<App />) })
    const box = document.querySelector('input[placeholder="e.g. admin"]') as HTMLInputElement
    expect(box).toBeTruthy()
    typeText(box, 'cashier')
    expect(box.value).toBe('cashier')
    expect(document.body.innerHTML).toContain('Sign in')
    root.unmount()
  })
})
