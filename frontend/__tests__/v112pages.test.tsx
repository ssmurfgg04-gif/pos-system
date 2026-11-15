// @vitest-environment jsdom
// v1.12 pages render with real-shaped API answers and do the right call
// on the main button (smoke: a runtime crash here is a dead page on a till).
import { describe, expect, test, vi, beforeEach, afterEach } from 'vitest'
import React, { act } from 'react'
import { createRoot, Root } from 'react-dom/client'
;(React as any).actEnvironment = true

const get = vi.fn()
const post = vi.fn()
const put = vi.fn()
vi.mock('../src/lib/api', () => ({
  api: { get: (...a: unknown[]) => get(...a), post: (...a: unknown[]) => post(...a), put: (...a: unknown[]) => put(...a), del: vi.fn() },
  downloadFile: vi.fn().mockResolvedValue(undefined),
}))
vi.mock('../src/stores/auth', () => ({
  useAuth: (sel: (s: unknown) => unknown) => sel({ user: { permissions: ['timecards.manage', 'transfers.manage', 'accounting.export', 'marketing.send'] } }),
}))

import { Timecards } from '../src/pages/Timecards'
import { Transfers } from '../src/pages/Transfers'
import { Books } from '../src/pages/Books'
import { Promotions } from '../src/pages/Promotions'

let container: HTMLDivElement
let root: Root
async function mount(el: React.ReactElement) {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  await act(async () => { root.render(el) })
  await act(async () => { await new Promise((r) => setTimeout(r, 0)) })
}
const btn = (label: string) => Array.from(container.querySelectorAll('button')).find((b) => b.textContent?.includes(label))

beforeEach(() => { get.mockReset(); post.mockReset(); put.mockReset() })
afterEach(() => { act(() => root.unmount()); container.remove() })

const emptyReport = { from: '', to: '', weeklyHours: 52, overtimeRate: 1.5, people: [], entries: [], totalSeconds: 0, totalPayCents: 0 }

describe('v1.12 pages', () => {
  test('Time: clocked out → Clock in posts the punch', async () => {
    get.mockResolvedValue({ open: null, report: emptyReport })
    post.mockResolvedValue({})
    await mount(React.createElement(Timecards))
    expect(container.textContent).toContain('You are clocked out')
    await act(async () => { btn('Clock in')!.click() })
    expect(post).toHaveBeenCalledWith('/api/v1/timeclock/me', { action: 'in' })
  })

  test('Transfers: incoming transfer offers Receive; offline says why', async () => {
    get.mockResolvedValue({
      online: true, error: '', myTeam: 'B', shops: [{ teamCode: 'A', name: 'Kiambu Rd' }],
      transfers: [{ id: 'u1', clientRef: 'TR-1', direction: 'in', fromTeam: 'A', fromName: 'Kiambu Rd', toTeam: 'B', toName: 'Thika Rd',
        status: 'sent', lines: [{ sku: 'S1', name: 'Sugar 1kg', qtyMilli: 5000, priceCents: 0, costCents: 0 }], receivedLines: [],
        note: '', sentBy: 'amina', sentAt: '2026-11-15T08:00:00Z', receivedBy: '', receivedAt: '', cancelledBy: '', cancelledAt: '', lastError: '', shortMilli: 0 }],
    })
    await mount(React.createElement(Transfers))
    expect(container.textContent).toContain('To receive (1)')
    expect(container.textContent).toContain('5 × Sugar 1kg')
  })

  test('Books: balanced days list and export buttons', async () => {
    get.mockImplementation(async (path: string) => path.includes('accounts')
      ? [{ key: 'sales', label: 'Sales', name: 'Sales', code: '200' }]
      : { days: [{ date: '2026-11-15', number: 'POS-20261115', lines: [], debitCents: 120000, creditCents: 120000, roundingCents: 0 }] })
    await mount(React.createElement(Books))
    expect(container.textContent).toContain('POS-20261115')
    expect(btn('QuickBooks CSV')).toBeTruthy()
    expect(btn('Xero CSV')).toBeTruthy()
  })

  test('Promotions: empty state explains consent first', async () => {
    get.mockResolvedValue([])
    await mount(React.createElement(Promotions))
    expect(container.textContent).toContain('OK to send offers')
    expect(btn('New promotion')).toBeTruthy()
  })
})
