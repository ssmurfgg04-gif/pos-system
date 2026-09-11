// Connectivity heartbeat + queue flush. Pings /health every 10s (cheap,
// public); on reconnect, replays the queued checkouts through /sync.
// In demo mode the in-browser backend is always "online" — the loop is a
// no-op so the offline banner never shows on a static deploy.

import { api, Order, demoForced } from '../lib/api'
import { create } from 'zustand'
import { drainQueue, queueSize, removeFromQueue } from './queue'
import { toast } from '../stores/toasts'

interface NetState {
  online: boolean
  pending: number
  setOnline: (v: boolean) => void
  setPending: (n: number) => void
  refreshPending: () => Promise<void>
}

export const useNet = create<NetState>((set) => ({
  online: navigator.onLine,
  pending: 0,
  setOnline: (v) => set({ online: v }),
  setPending: (n) => set({ pending: n }),
  refreshPending: async () => {
    try {
      set({ pending: await queueSize() })
    } catch {
      /* IndexedDB unavailable (private mode) — queue is inert */
    }
  },
}))

let flushing = false

export async function flushQueue(): Promise<Order[]> {
  if (flushing) return []
  flushing = true
  const created: Order[] = []
  try {
    const queued = await drainQueue()
    if (queued.length > 0) {
      const results = await api.post<{ clientUuid: string; orderId: number; error?: string }[]>(
        '/api/v1/sync',
        { transactions: queued.map((q) => q.request) },
      )
      for (const r of results) {
        if (!r.error) {
          await removeFromQueue(r.clientUuid)
        }
      }
      const ok = results.filter((r) => !r.error).length
      if (ok > 0) {
        toast.success(`Synced ${ok} offline sale${ok > 1 ? 's' : ''}`)
      }
      const failed = results.filter((r) => r.error)
      for (const f of failed) {
        toast.error('Offline sale rejected', f.error)
        await removeFromQueue(f.clientUuid) // server rejected it for good reason
      }
    }
    await useNet.getState().refreshPending()
  } catch {
    // still offline — keep the queue
  } finally {
    flushing = false
  }
  return created
}

export function startHeartbeat() {
  if (demoForced()) {
    useNet.getState().setOnline(true)
    return
  }
  useNet.getState().refreshPending()

  const check = async () => {
    try {
      await fetch('/api/v1/health', { cache: 'no-store' })
      const was = useNet.getState().online
      if (!was) {
        useNet.getState().setOnline(true)
        await flushQueue()
      }
    } catch {
      useNet.getState().setOnline(false)
    }
  }
  setInterval(check, 10000)
  window.addEventListener('online', check)
  window.addEventListener('offline', () => useNet.getState().setOnline(false))
  check()
}
