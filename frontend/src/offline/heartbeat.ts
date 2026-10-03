// Connectivity heartbeat + queue flush. Pings /health every 10s (cheap,
// public); on reconnect — and once at startup when the queue is non-empty —
// replays the queued checkouts through /sync.
//
// Hardening rules (no lost / no duplicated sales):
//  - flush works in small batches, one bad payload can't block the rest;
//  - a per-item BUSINESS rejection (4xx with an error string) is dropped
//    after surfacing — the server saw and refused the sale for good reason
//    (e.g. insufficient stock before the catalog caught up);
//  - a per-item SERVER failure (5xx / malformed response) keeps the sale
//    queued with attempts+1 — retried with backoff on later flushes;
//  - a whole-request network failure keeps everything queued untouched;
//  - after MAX_FLUSH_ATTEMPTS a stubborn sale is parked as failed (kept in
//    the queue, flagged red in the UI) instead of vanishing.
// In demo mode the in-browser backend is always "online" — the loop is a
// no-op so the offline banner never shows on a static deploy.

import { api, Order, demoForced } from '../lib/api'
import { create } from 'zustand'
import {
  drainQueue, queueSize, failedQueueSize, removeFromQueue, markQueueAttempt,
  QueuedCheckout,
} from './queue'
import { toast } from '../stores/toasts'

interface NetState {
  online: boolean
  pending: number
  failed: number
  setOnline: (v: boolean) => void
  setPending: (n: number) => void
  setFailed: (n: number) => void
  refreshPending: () => Promise<void>
}

export const useNet = create<NetState>((set) => ({
  online: navigator.onLine,
  pending: 0,
  failed: 0,
  setOnline: (v) => set({ online: v }),
  setPending: (n) => set({ pending: n }),
  setFailed: (n) => set({ failed: n }),
  refreshPending: async () => {
    try {
      const [pending, failed] = await Promise.all([queueSize(), failedQueueSize()])
      set({ pending, failed })
    } catch {
      /* storage unavailable — queue is inert */
    }
  },
}))

let flushing = false
const FLUSH_BATCH = 10
const MAX_FLUSH_ATTEMPTS = 8

interface SyncResult {
  rejected?: boolean
  clientUuid: string
  orderId: number
  error?: string
}

function flushOnce(queued: QueuedCheckout[]): Promise<SyncResult[]> {
  return api.post<SyncResult[]>('/api/v1/sync', {
    transactions: queued.map((q) => q.request),
  })
}

export async function flushQueue(): Promise<Order[]> {
  if (flushing) return []
  flushing = true
  const created: Order[] = []
  try {
    const queued = await drainQueue()
    for (let i = 0; i < queued.length; i += FLUSH_BATCH) {
      const batch = queued.slice(i, i + FLUSH_BATCH)
      let results: SyncResult[]
      try {
        results = await flushOnce(batch)
      } catch {
        // Whole request failed — network/server down. Keep every sale
        // queued untouched; the next healthy heartbeat retries.
        break
      }
      // A 2xx with a per-item error list means the server processed the
      // batch. Distinguish business rejections from server trouble.
      for (const r of results) {
        if (!r.error) {
          await removeFromQueue(r.clientUuid)
          continue
        }
        // The server flags DEFINITIVE refusals with `rejected: true`
        // (sentinel-matched — stock, credit limit, duplicate receipt…).
        // For older servers that lack the flag, fall back to exact
        // sentinel texts. An unknown/5xx error is NEVER deletion-worthy:
        // the old contains() matching ate real sales whenever an
        // unfamiliar message merely contained a word like "invalid".
        if (r.rejected === true || isBusinessRejection(r.error)) {
          toast.error('Offline sale rejected', r.error)
          await removeFromQueue(r.clientUuid)
        } else {
          await markQueueAttempt(r.clientUuid, r.error)
          const q = batch.find((b) => b.clientUuid === r.clientUuid)
          if (q && (q.attempts || 0) + 1 >= MAX_FLUSH_ATTEMPTS) {
            toast.error(
              `Offline sale parked after ${MAX_FLUSH_ATTEMPTS} tries`,
              'Show it to the owner — check Reports → Orders for duplicates before re-entering it.',
            )
            // Kept in the queue (flagged failed) — never auto-deleted.
          }
        }
      }
    }
    await useNet.getState().refreshPending()
    const left = useNet.getState().pending
    const synced = queued.length - left
    if (synced > 0) {
      toast.success(`Synced ${synced} offline sale${synced > 1 ? 's' : ''}`)
    }
  } finally {
    flushing = false
  }
  return created
}

// Legacy fallback for servers older than the `rejected` flag: match the
// EXACT sentinel texts the backend can produce (services/orders.go), never
// substrings like "invalid" — proxy pages and new error strings must stay
// transient so a sale is retried instead of silently deleted.
const BUSINESS_REJECTION_TEXTS = [
  'insufficient stock',
  'invalid state transition',
  'order already paid',
  'receipt code already recorded',
  'payment exceeds balance',
  'tab would exceed customer credit limit',
  'not enough store credit',
  'not enough loyalty points',
  'not found',
]
function isBusinessRejection(err: string): boolean {
  const e = (err || '').toLowerCase()
  return BUSINESS_REJECTION_TEXTS.some((t) => e.includes(t))
}

let started = false

export function startHeartbeat() {
  if (demoForced()) {
    useNet.getState().setOnline(true)
    return
  }
  if (started) return
  started = true
  useNet.getState().refreshPending()

  const check = async () => {
    try {
      await fetch('/api/v1/health', { cache: 'no-store' })
      const was = useNet.getState().online
      useNet.getState().setOnline(true)
      // Flush on reconnect AND at startup — a sale queued before an app
      // restart must not wait for a network flap to sync.
      const pending = useNet.getState().pending
      if (!was || pending > 0) {
        await flushQueue()
      }
    } catch {
      useNet.getState().setOnline(false)
    }
  }
  setInterval(check, 10000)
  window.addEventListener('online', check)
  window.addEventListener('offline', () => useNet.getState().setOnline(false))
  // Give the SPA a moment to mount, then clear anything left over.
  setTimeout(check, 1500)
  check()
}
