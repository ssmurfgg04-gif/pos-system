// WebSocket client: token auth on the first message, reconnect with
// backoff, typed event callbacks (ORDER_PAID etc.). In demo mode there is
// no server to talk to — connect is a no-op.

import { token, backendMode } from '../lib/api'

export type WsEvent =
  | 'ORDER_CREATED'
  | 'ORDER_PAID'
  | 'ORDER_VOIDED'
  | 'DESIGN_JOB_UPDATED'
  | 'SHIFT_UPDATED'
  | 'SETTINGS_UPDATED'
  | 'HELD_SALES_UPDATED'
  | 'TEAM_SYNC_UPDATED'
  | 'JOB_UPDATED'
  | 'ORDER_NOTIFIED'

type Handler = (data: any) => void

const handlers = new Map<WsEvent, Set<Handler>>()
const reconnectHandlers = new Set<() => void>()
let ws: WebSocket | null = null
let retry = 0
let closed = false
let reconnectTimer: ReturnType<typeof setTimeout> | null = null

export function onWsEvent(event: WsEvent, handler: Handler): () => void {
  if (!handlers.has(event)) handlers.set(event, new Set())
  handlers.get(event)!.add(handler)
  return () => handlers.get(event)?.delete(handler)
}

/** Fires once after every reconnection: pages reload their data to catch
 *  anything broadcast while the socket was down (no event replay in the
 *  hub, so a refresh-on-reconnect closes the gap). */
export function onWsReconnect(handler: () => void): () => void {
  reconnectHandlers.add(handler)
  return () => reconnectHandlers.delete(handler)
}

function dispatch(event: WsEvent, data: any) {
  handlers.get(event)?.forEach((h) => {
    try {
      h(data)
    } catch (e) {
      console.error('ws handler error', e)
    }
  })
}

export function connectWs() {
  closed = false
  if (!token()) return
  // Static demo build (forced or probed): no server, no socket.
  backendMode()
    .then((mode) => {
      if (mode === 'demo' || closed) return
      openSocket()
    })
    .catch(() => undefined)
}

function openSocket() {
  // One live socket at a time: a retry timer, a login and a shop switch
  // can all ask for one, and two sockets would deliver every event twice
  // (double receipts, double prints).
  if (ws && (ws.readyState === WebSocket.CONNECTING || ws.readyState === WebSocket.OPEN)) return
  // Same-origin by default; the Android client overrides via the stored
  // till address (serverUrl()).
  let target = `${location.host}`
  try {
    const stored = localStorage.getItem('ledgerpos_server_url')
    if (stored) target = stored.replace(/^https?:\/\//, '').replace(/\/+$/, '')
  } catch { /* private mode — stay same-origin */ }
  const proto = location.protocol === 'https:' || target.startsWith('https') ? 'wss' : 'ws'
  let sock: WebSocket
  try {
    sock = new WebSocket(`${proto}://${target}/api/v1/ws`)
  } catch {
    scheduleReconnect()
    return
  }
  ws = sock

  sock.onopen = () => {
    if (sock !== ws) { sock.close(); return } // replaced while connecting
    const wasReconnect = retry > 0
    retry = 0
    const tk = token()
    if (!tk) { sock.close(); return } // signed out meanwhile
    sock.send(JSON.stringify({ token: tk }))
    if (wasReconnect) {
      reconnectHandlers.forEach((h) => {
        try {
          h()
        } catch (e) {
          console.error('ws reconnect handler error', e)
        }
      })
    }
  }

  sock.onmessage = (ev) => {
    if (sock !== ws) return
    try {
      const msg = JSON.parse(ev.data)
      if (msg && typeof msg.type === 'string') dispatch(msg.type as WsEvent, msg.data)
    } catch {
      /* ignore malformed */
    }
  }

  sock.onclose = () => {
    // Only the current socket may schedule a retry: the old socket of a
    // reconnectWs() closes late, after connectWs() has cleared `closed`.
    if (sock !== ws) return
    ws = null
    if (!closed) scheduleReconnect()
  }
  sock.onerror = () => {
    sock.close()
  }
}

function scheduleReconnect() {
  retry++
  const delay = Math.min(15000, 1000 * Math.pow(2, Math.min(retry, 4)))
  if (reconnectTimer) clearTimeout(reconnectTimer)
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null
    if (!closed) openSocket()
  }, delay)
}

export function disconnectWs() {
  closed = true
  if (reconnectTimer) {
    clearTimeout(reconnectTimer)
    reconnectTimer = null
  }
  const old = ws
  ws = null
  old?.close()
}

/** Re-auth after login/switch (server validates the first message). */
export function reconnectWs() {
  disconnectWs()
  connectWs()
}
