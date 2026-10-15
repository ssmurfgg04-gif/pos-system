// @vitest-environment jsdom
// sessioncontract.test.ts — pins the session-expiry contract of the API
// client and the join-link format:
//
//  1. A 401 outside the auth paths wipes pos_token (dead session → login).
//  2. A 401 ON an auth path (wrong password on the login form) never wipes
//     a still-valid token.
//  3. A 422 from the owner-approval dialog NEVER wipes the token. The
//     v1.3.3 kick-out was the backend answering 401 for bad portal
//     credentials; that is pinned SERVER-side (Go TestOwnerApproval...)
//     so a regression to 401 fails the Go suite. This test pins the
//     client side: whatever status the dialog endpoint answers, only a
//     real dead-session 401 may clear the token.
//  4. Join links carry team/role/token only — never a username.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'

const fetchMock = vi.fn()
vi.stubGlobal('fetch', fetchMock)

import { api, token } from '../src/lib/api'
import { joinLinkFor } from '../src/pages/Settings'

function res(status: number, body: unknown, isJson = true) {
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: new Headers(isJson ? { 'content-type': 'application/json' } : { 'content-type': 'text/html' }),
    text: async () => (isJson ? JSON.stringify(body) : '<html>spa</html>'),
  }
}

beforeEach(() => {
  localStorage.clear()
  fetchMock.mockReset()
  // The backend probe must resolve to 'real' so requests hit the interceptor.
  fetchMock.mockImplementation(async (url: string) => {
    if (String(url).includes('/api/v1/health')) return res(200, { status: 'ok' })
    return res(500, { error: 'unexpected fetch in test' })
  })
  history.replaceState(null, '', '/pos')
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('session expiry contract', () => {
  it('wipes pos_token on a 401 outside the auth paths', async () => {
    localStorage.setItem('pos_token', 'tok-1')
    fetchMock.mockImplementation(async (url: string) => {
      if (String(url).includes('/api/v1/health')) return res(200, { status: 'ok' })
      return res(401, { error: 'session expired' })
    })
    // Stay on /login so the redirect branch is inert; the wipe is the contract.
    history.replaceState(null, '', '/login')
    await expect(api.get('/api/v1/settings')).rejects.toMatchObject({ status: 401 })
    expect(token()).toBeNull()
  })

  it('keeps pos_token when an auth endpoint itself answers 401', async () => {
    localStorage.setItem('pos_token', 'tok-2')
    fetchMock.mockImplementation(async (url: string) => {
      if (String(url).includes('/api/v1/health')) return res(200, { status: 'ok' })
      return res(401, { error: 'invalid credentials' })
    })
    await expect(api.post('/api/v1/auth/login', { username: 'a', password: 'b' }))
      .rejects.toMatchObject({ status: 401 })
    expect(token()).toBe('tok-2')
  })

  it('keeps pos_token on 422 (owner-approval retry must not log the owner out)', async () => {
    localStorage.setItem('pos_token', 'tok-3')
    fetchMock.mockImplementation(async (url: string) => {
      if (String(url).includes('/api/v1/health')) return res(200, { status: 'ok' })
      return res(422, { error: 'those owner credentials were not recognized for this store' })
    })
    let caught: any = null
    try {
      await api.post('/api/v1/team-sync/owner-approval', { username: 'owner', password: 'wrong' })
    } catch (e) {
      caught = e
    }
    expect(caught?.status).toBe(422)
    expect(token()).toBe('tok-3')
  })
})

describe('join link format', () => {
  it('carries team, role and token only — never a username', () => {
    const link = joinLinkFor({
      teamCode: 'KIM-MAIN',
      roleName: 'Cashier',
      token: 'abcd1234efgh5678ijkl',
      expiresAt: '2026-12-31T00:00:00Z',
    })
    expect(link).toContain('#t=KIM-MAIN')
    expect(link).toContain('r=Cashier')
    expect(link).toContain('k=abcd1234efgh5678ijkl')
    expect(link.toLowerCase()).not.toContain('user')
    expect(link).toMatch(/#t=[^&]+&r=[^&]+&k=[A-Za-z0-9-]{12,64}$/)
  })
})
