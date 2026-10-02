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
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  try {
    ws = new WebSocket(`${proto}://${location.host}/api/v1/ws`)
  } catch {
    scheduleReconnect()
    return
  }

  ws.onopen = () => {
    const wasReconnect = retry > 0
    retry = 0
    ws?.send(JSON.stringify({ token: token() }))
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

  ws.onmessage = (ev) => {
    try {
      const msg = JSON.parse(ev.data)
      if (msg && typeof msg.type === 'string') dispatch(msg.type as WsEvent, msg.data)
    } catch {
      /* ignore malformed */
    }
  }

  ws.onclose = () => {
    if (!closed) scheduleReconnect()
  }
  ws.onerror = () => {
    ws?.close()
  }
}

function scheduleReconnect() {
  retry++
  const delay = Math.min(15000, 1000 * Math.pow(2, Math.min(retry, 4)))
  setTimeout(() => {
    if (!closed) openSocket()
  }, delay)
}

export function disconnectWs() {
  closed = true
  ws?.close()
  ws = null
}

/** Re-auth after login/switch (server validates the first message). */
export function reconnectWs() {
  disconnectWs()
  connectWs()
}
