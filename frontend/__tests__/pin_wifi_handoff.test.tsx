// v1.3.5 regression tests — the PIN screen listens for the owner till's
// WiFi hand-off: when the owner taps Approve, the code lands on this till
// and the worker only types their new PIN. The read-aloud path stays.
// @vitest-environment jsdom
import { describe, expect, test, vi, beforeEach } from 'vitest'
import React, { act } from 'react'
import { createRoot, Root } from 'react-dom/client'

const apiGet = vi.fn()
vi.mock('../src/lib/api', () => ({
  api: {
    get: (...a: unknown[]) => apiGet(...(a as [])),
    post: vi.fn(),
  },
  PinUser: {},
}))

import { ForgotPinPanel } from '../src/pages/Pin'

;(React as any).actEnvironment = true

let root: Root | null = null

function mount(step: 'request' | 'code', requestId: number) {
  document.body.innerHTML = '<div id="root"></div>'
  root = createRoot(document.getElementById('root')!)
  const state = { step, requestId, username: 'Salma PC' }
  act(() => {
    root!.render(
      <ForgotPinPanel
        state={state}
        setState={() => undefined}
        onDone={() => undefined}
        onCancel={() => undefined}
      />,
    )
  })
  return document.getElementById('root')!
}

async function settle() {
  for (let i = 0; i < 40; i++) {
    await new Promise((r) => setTimeout(r, 10))
    await act(async () => { await Promise.resolve() })
  }
}

beforeEach(() => {
  apiGet.mockReset()
})

describe('ForgotPinPanel WiFi hand-off', () => {
  test('pre-fills the code and swaps the banner when the owner pushes it over WiFi', async () => {
    apiGet.mockResolvedValue({ active: true, request: { requestId: 5, username: 'Salma PC', pushedCode: '42424242' } })
    const el = mount('code', 5)
    await settle()
    const codeInput = el.querySelector('input[inputmode="numeric"]') as HTMLInputElement
    expect(codeInput).toBeTruthy()
    expect(codeInput.value).toBe('42424242')
    expect(el.textContent).toContain('the code arrived on this till over the WiFi')
    expect(el.textContent).toContain('Type your new PIN below')
    // The read-aloud instructions must be gone once the code is here.
    expect(el.textContent).not.toContain('call or message the owner')
  })

  test('keeps the read-aloud banner when no code has been pushed yet', async () => {
    apiGet.mockResolvedValue({ active: true, request: { requestId: 5, username: 'Salma PC' } })
    const el = mount('code', 5)
    await settle()
    expect(el.textContent).toContain('Request sent')
    const codeInput = el.querySelector('input[inputmode="numeric"]') as HTMLInputElement
    expect(codeInput.value).toBe('')
  })

  test('survives status failures without crashing the pad', async () => {
    apiGet.mockRejectedValue(new Error('offline'))
    const el = mount('code', 5)
    await settle()
    expect(el.textContent).toContain('Request sent')
  })
})
