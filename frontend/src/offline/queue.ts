// Offline layer: IndexedDB checkout queue + connectivity heartbeat.
// Checkout requests get a clientUuid BEFORE the first attempt; failed
// network calls enqueue and replay through /sync on reconnect (server is
// idempotent on clientUuid, so double-replays are harmless).
//
// Hardening: every queued sale carries attempts/lastError so a flaky
// server cannot silently eat it after one try, and when IndexedDB is
// unavailable (private browsing, storage pressure) the queue falls back
// to localStorage — a queued sale must survive, whatever the storage.

import type { CheckoutRequest } from '../lib/api'

const DB_NAME = 'pos-offline'
const STORE = 'checkout-queue'
const LS_KEY = 'pos-offline-queue'

function openDB(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, 1)
    req.onupgradeneeded = () => {
      if (!req.result.objectStoreNames.contains(STORE)) {
        req.result.createObjectStore(STORE, { keyPath: 'clientUuid' })
      }
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

export interface QueuedCheckout {
  clientUuid: string
  request: CheckoutRequest
  queuedAt: number
  attempts?: number
  lastError?: string
}

// ---- localStorage mirror (fallback when IndexedDB is unavailable) ----

function lsAll(): QueuedCheckout[] {
  try {
    return JSON.parse(localStorage.getItem(LS_KEY) || '[]') as QueuedCheckout[]
  } catch {
    return []
  }
}

function lsSave(items: QueuedCheckout[]): void {
  try {
    localStorage.setItem(LS_KEY, JSON.stringify(items))
  } catch {
    /* storage full — nothing more we can do */
  }
}

let idbBroken = false

export async function enqueue(request: CheckoutRequest): Promise<void> {
  const record: QueuedCheckout = {
    clientUuid: request.clientUuid || newClientUuid(),
    request,
    queuedAt: Date.now(),
  }
  if (!idbBroken) {
    try {
      const db = await openDB()
      await new Promise<void>((resolve, reject) => {
        const tx = db.transaction(STORE, 'readwrite')
        tx.objectStore(STORE).put(record)
        tx.oncomplete = () => resolve()
        tx.onerror = () => reject(tx.error)
      })
      db.close()
      return
    } catch {
      idbBroken = true // private mode / quota — fall through to localStorage
    }
  }
  const all = lsAll().filter((q) => q.clientUuid !== record.clientUuid)
  all.push(record)
  lsSave(all)
}

export async function queueSize(): Promise<number> {
  if (!idbBroken) {
    try {
      return await idbSize()
    } catch {
      idbBroken = true
    }
  }
  return lsAll().length
}

async function idbSize(): Promise<number> {
  const db = await openDB()
  const n = await new Promise<number>((resolve, reject) => {
    const tx = db.transaction(STORE, 'readonly')
    const req = tx.objectStore(STORE).count()
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
  db.close()
  return n
}

export async function drainQueue(): Promise<QueuedCheckout[]> {
  if (!idbBroken) {
    try {
      return await idbDrain()
    } catch {
      idbBroken = true
    }
  }
  return lsAll().sort((a, b) => a.queuedAt - b.queuedAt)
}

async function idbDrain(): Promise<QueuedCheckout[]> {
  const db = await openDB()
  const all = await new Promise<QueuedCheckout[]>((resolve, reject) => {
    const tx = db.transaction(STORE, 'readonly')
    const req = tx.objectStore(STORE).getAll()
    req.onsuccess = () => resolve(req.result as QueuedCheckout[])
    req.onerror = () => reject(req.error)
  })
  db.close()
  return all.sort((a, b) => a.queuedAt - b.queuedAt)
}

export async function removeFromQueue(clientUuid: string): Promise<void> {
  if (!idbBroken) {
    try {
      const db = await openDB()
      await new Promise<void>((resolve, reject) => {
        const tx = db.transaction(STORE, 'readwrite')
        tx.objectStore(STORE).delete(clientUuid)
        tx.oncomplete = () => resolve()
        tx.onerror = () => reject(tx.error)
      })
      db.close()
      return
    } catch {
      idbBroken = true
    }
  }
  lsSave(lsAll().filter((q) => q.clientUuid !== clientUuid))
}

/** Record a failed replay attempt against a queued sale. */
export async function markQueueAttempt(clientUuid: string, error: string): Promise<void> {
  const patch = (q: QueuedCheckout) => {
    if (q.clientUuid === clientUuid) {
      q.attempts = (q.attempts || 0) + 1
      q.lastError = error
    }
  }
  if (!idbBroken) {
    try {
      const all = await idbDrain()
      all.forEach(patch)
      const db = await openDB()
      await new Promise<void>((resolve, reject) => {
        const tx = db.transaction(STORE, 'readwrite')
        const store = tx.objectStore(STORE)
        for (const q of all) store.put(q)
        tx.oncomplete = () => resolve()
        tx.onerror = () => reject(tx.error)
      })
      db.close()
      return
    } catch {
      idbBroken = true
    }
  }
  const all = lsAll()
  all.forEach(patch)
  lsSave(all)
}

/** Number of queued sales that have failed to replay at least once. */
export async function failedQueueSize(): Promise<number> {
  const all = await drainQueue()
  return all.filter((q) => (q.attempts || 0) > 0).length
}

/** Generate the idempotency key for a new checkout. */
export function newClientUuid(): string {
  return (
    'web-' + Date.now().toString(36) + '-' +
    Math.random().toString(36).slice(2, 10)
  )
}
