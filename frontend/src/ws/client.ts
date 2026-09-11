// WebSocket client: token auth on the first message, reconnect with
// backoff, typed event callbacks (ORDER_PAID etc.).

import { token } from '../lib/api'

export type WsEvent =
  | 'ORDER_CREATED'
  | 'ORDER_PAID'
  | 'ORDER_VOIDED'
  | 'DESIGN_JOB_UPDATED'
  | 'SHIFT_UPDATED'
  | 'SETTINGS_UPDATED'

type Handler = (data: any) => void

const handlers = new Map<WsEvent, Set<Handler>>()
let ws: WebSocket | null = null
let retry = 0
let closed = false

export function onWsEvent(event: WsEvent, handler: Handler): () => void {
  if (!handlers.has(event)) handlers.set(event, new Set())
  handlers.get(event)!.add(handler)
  return () => handlers.get(event)?.delete(handler)
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
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  try {
    ws = new WebSocket(`${proto}://${location.host}/api/v1/ws`)
  } catch {
    scheduleReconnect()
    return
  }

  ws.onopen = () => {
    retry = 0
    ws?.send(JSON.stringify({ token: token() }))
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
    if (!closed) connectWs()
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
