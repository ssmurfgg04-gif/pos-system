// @vitest-environment jsdom
// The socket client keeps exactly ONE live socket. Before v1.12.1 a login or
// shop switch (reconnectWs) left a second socket behind (the old socket's
// late close re-armed a retry), so every live event fired twice.
import { describe, expect, test, vi, beforeEach, afterEach } from 'vitest'

vi.mock('../src/lib/api', () => ({
  token: () => 'tok',
  backendMode: () => Promise.resolve('server'),
}))

class FakeWS {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  static all: FakeWS[] = []
  readyState = 0
  sent: string[] = []
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onmessage: ((e: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  constructor(public url: string) { FakeWS.all.push(this) }
  send(d: string) { this.sent.push(d) }
  open() { this.readyState = 1; this.onopen?.() }
  close() { // real sockets close asynchronously
    if (this.readyState === 3) return
    this.readyState = 2
    setTimeout(() => { this.readyState = 3; this.onclose?.() }, 0)
  }
  message(o: unknown) { this.onmessage?.({ data: JSON.stringify(o) }) }
}
const live = () => FakeWS.all.filter((s) => s.readyState === 0 || s.readyState === 1)
const flush = () => new Promise((r) => setTimeout(r, 5))

let mod: typeof import('../src/ws/client')
beforeEach(async () => {
  vi.useRealTimers()
  FakeWS.all = []
  ;(globalThis as any).WebSocket = FakeWS
  vi.resetModules()
  mod = await import('../src/ws/client')
})
afterEach(() => { mod.disconnectWs() })

describe('ws client', () => {
  test('login + shop switch leave exactly one live socket, and events fire once', async () => {
    vi.useFakeTimers()
    const tick = (ms = 10) => vi.advanceTimersByTimeAsync(ms)
    mod.connectWs(); await tick()
    live()[0].open()
    mod.reconnectWs(); await tick() // login
    live().forEach((s) => s.open())
    mod.reconnectWs(); await tick() // shop switch
    live().forEach((s) => s.open())
    await tick(30000) // let any stale retry timer (the old bug's extra socket) fire
    live().forEach((s) => { if (s.readyState === 0) s.open() })
    expect(live().length).toBe(1)
    const got: unknown[] = []
    mod.onWsEvent('ORDER_PAID', (d) => got.push(d))
    live()[0].message({ type: 'ORDER_PAID', data: { id: 7 } })
    expect(got).toEqual([{ id: 7 }])
    // the first frame the server sees is the token handshake
    expect(live()[0].sent[0]).toBe(JSON.stringify({ token: 'tok' }))
  })

  test('a dropped socket reconnects once; disconnect cancels the retry', async () => {
    vi.useFakeTimers()
    mod.connectWs(); await vi.advanceTimersByTimeAsync(0)
    const first = live()[0]; first.open()
    first.readyState = 3; first.onclose?.() // server went away
    await vi.advanceTimersByTimeAsync(2100)
    expect(live().length).toBe(1)
    expect(live()[0]).not.toBe(first)
    mod.disconnectWs()
    live().forEach((s) => { s.readyState = 3 })
    const n = FakeWS.all.length
    await vi.advanceTimersByTimeAsync(20000)
    expect(FakeWS.all.length).toBe(n) // no retry after sign-out
  })

  test('a socket replaced while connecting never sends the token', async () => {
    mod.connectWs(); await flush()
    const a = live()[0]
    mod.reconnectWs(); await flush()
    a.open() // late open of the replaced socket
    expect(a.sent).toEqual([])
  })
})
