// Offline layer: IndexedDB checkout queue + connectivity heartbeat.
// Checkout requests get a clientUuid BEFORE the first attempt; failed
// network calls enqueue and replay through /sync on reconnect (server is
// idempotent on clientUuid, so double-replays are harmless).

import type { CheckoutRequest } from '../lib/api'

const DB_NAME = 'pos-offline'
const STORE = 'checkout-queue'

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
}

export async function enqueue(request: CheckoutRequest): Promise<void> {
  const db = await openDB()
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite')
    tx.objectStore(STORE).put({ clientUuid: request.clientUuid, request, queuedAt: Date.now() })
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error)
  })
  db.close()
}

export async function queueSize(): Promise<number> {
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
  const db = await openDB()
  await new Promise<void>((resolve, reject) => {
    const tx = db.transaction(STORE, 'readwrite')
    tx.objectStore(STORE).delete(clientUuid)
    tx.oncomplete = () => resolve()
    tx.onerror = () => reject(tx.error)
  })
  db.close()
}

/** Generate the idempotency key for a new checkout. */
export function newClientUuid(): string {
  return (
    'web-' + Date.now().toString(36) + '-' +
    Math.random().toString(36).slice(2, 10)
  )
}
