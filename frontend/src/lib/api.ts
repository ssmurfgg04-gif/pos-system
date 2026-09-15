// API client — same-origin by default (single binary serves the SPA),
// token auth, consistent {data}/{error} envelope handling.
//
// DEMO MODE: static deployments (Netlify) have no Go server behind them.
// Three resolution rules, in order:
//   1. VITE_API_URL set            → always the real backend (fail loudly).
//   2. VITE_DEMO_MODE=true or ?demo=1 → always the in-browser demo backend.
//   3. otherwise                   → probe /api/v1/health once; a JSON
//      response means the real server, anything else (404 HTML SPA
//      fallback, timeout, connection refused) falls back to demo mode so
//      the deployed site is always demoable. The shell shows a "demo mode"
//      pill when this happens.

import { demoRequest, demoRaw } from '../demo/backend'

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

const BASE = (import.meta.env.VITE_API_URL as string | undefined) ?? '' // same origin by default; Vite dev proxies /api → :3000

// ---- backend selection ----

type Mode = 'real' | 'demo'

let resolvedMode: Mode | null = null
let modePromise: Promise<Mode> | null = null

export function demoForced(): boolean {
  return (
    import.meta.env.VITE_DEMO_MODE === 'true' ||
    new URLSearchParams(window.location.search).has('demo')
  )
}

async function resolveMode(): Promise<Mode> {
  if (BASE) return 'real' // explicit backend URL → never silently demo
  if (demoForced()) return 'demo'
  try {
    const ctl = new AbortController()
    const t = window.setTimeout(() => ctl.abort(), 1500)
    const res = await fetch('/api/v1/health', { cache: 'no-store', signal: ctl.signal })
    window.clearTimeout(t)
    // A static host answers /api/* with the SPA fallback (200 text/html) or
    // a 404 page — neither is the health JSON. Only JSON counts as "live".
    const ct = res.headers.get('content-type') || ''
    if (res.ok && ct.includes('json')) return 'real'
    return 'demo'
  } catch {
    return 'demo'
  }
}

export function backendMode(): Promise<Mode> {
  if (resolvedMode) return Promise.resolve(resolvedMode)
  if (!modePromise) {
    modePromise = resolveMode().then((m) => {
      resolvedMode = m
      return m
    })
  }
  return modePromise
}

/** True once the mode probe has settled on demo (for banners/UI hints). */
export function isDemoSync(): boolean {
  return resolvedMode === 'demo'
}

// ---- Desktop app detection ----
// The desktop build serves the SPA from 127.0.0.1 and exposes
// /api/v1/system/desktop; static deploys and plain server deploys answer
// with desktop:false (or nothing at all — that also means "not desktop").

export interface DesktopStatus {
  desktop: boolean
  version?: string
  firstRun?: boolean
  port?: string
}

let desktopStatusCache: DesktopStatus | null = null

export async function getDesktopStatus(): Promise<DesktopStatus> {
  if (desktopStatusCache) return desktopStatusCache
  let status: DesktopStatus = { desktop: false }
  try {
    if ((await backendMode()) === 'real') {
      const res = await fetch(BASE + '/api/v1/system/desktop', { cache: 'no-store' })
      if (res.ok) {
        const j = await res.json()
        if (j?.data && typeof j.data.desktop === 'boolean') status = j.data
      }
    }
  } catch {
    /* no such endpoint → not the desktop app */
  }
  desktopStatusCache = status
  return status
}

export function token(): string | null {
  return localStorage.getItem('pos_token')
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const mode = await backendMode()
  if (mode === 'demo') {
    return demoRequest<T>(method, path, body)
  }
  const headers: Record<string, string> = {}
  const t = token()
  if (t) headers['Authorization'] = 'Bearer ' + t
  if (body !== undefined && !(body instanceof FormData)) headers['Content-Type'] = 'application/json'
  const res = await fetch(BASE + path, {
    method,
    headers,
    body: body === undefined ? undefined : body instanceof FormData ? body : JSON.stringify(body),
  })
  const text = await res.text()
  let json: any = null
  try { json = text ? JSON.parse(text) : null } catch { /* non-JSON */ }
  if (!res.ok) {
    throw new ApiError(res.status, json?.error || `Request failed (${res.status})`)
  }
  return (json?.data !== undefined ? json.data : json) as T
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body?: unknown) => request<T>('PUT', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
  form: <T>(path: string, form: FormData) => request<T>('POST', path, form),
}

/**
 * Authenticated raw (text) download — CSV exports need the Bearer header,
 * so plain <a href> links 401 against the real server. Works in demo mode
 * too (the demo backend generates the same CSV bytes).
 */
export async function raw(path: string): Promise<string> {
  const mode = await backendMode()
  if (mode === 'demo') return demoRaw(path)
  const headers: Record<string, string> = {}
  const t = token()
  if (t) headers['Authorization'] = 'Bearer ' + t
  const res = await fetch(BASE + path, { headers })
  if (!res.ok) {
    let msg = `Download failed (${res.status})`
    try {
      const j = await res.json()
      if (j?.error) msg = j.error
    } catch { /* not JSON */ }
    throw new ApiError(res.status, msg)
  }
  return res.text()
}

/** Trigger a browser file download for an authenticated CSV endpoint. */
export async function downloadFile(path: string, filename: string): Promise<void> {
  const text = await raw(path)
  const blob = new Blob([text], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 4000)
}

// ---- Shared types (mirror backend DTOs) ----

export interface User {
  id: number
  username: string
  fullName: string
  roleId: number
  roleName: string
  permissions: string[]
  active: boolean
  pinSet: boolean
  mustRotate: boolean
  createdAt: string
}

export interface PinUser { id: number; fullName: string; roleName: string }

export interface Category { id: number; name: string; slug: string; productCount?: number }

export interface Product {
  id: number
  sku: string
  barcode: string
  name: string
  categoryId: number
  categoryName: string
  priceCents: number
  costCents: number
  stockQty: number
  trackStock: boolean
  active: boolean
  updatedAt: string
}

export interface OrderItem {
  id: number
  productId: number
  name: string
  sku: string
  qty: number
  unitPriceCents: number
  lineTotalCents: number
}

export interface Payment {
  id: number
  orderId: number
  method: 'cash' | 'mpesa' | 'account'
  mode: string
  amountCents: number
  status: string
  phone: string
  mpesaReceipt: string
  checkoutRequestId?: string
  resultDesc: string
  discrepancy: boolean
  createdAt: string
  completedAt: string
}

export interface Order {
  id: number
  number: string
  status: 'PENDING' | 'PAID' | 'VOIDED'
  subtotalCents: number
  taxCents: number
  totalCents: number
  cashierId: number
  cashierName: string
  customerName: string
  customerId: number
  note: string
  clientUuid: string
  discrepancy: boolean
  createdAt: string
  paidAt: string
  voidedAt: string
  voidReason: string
  items: OrderItem[]
  payments: Payment[]
}

export interface Branding {
  app_name: string
  store_name: string
  brand_logo_url: string
  brand_color: string
  currency_symbol: string
  currency_code: string
  tax_percent: number
  tax_included: boolean
  payment_mode: 'auto' | 'stk' | 'manual'
  till_number: string
  paybill_number: string
  mpesa_env: string
}

export interface CheckoutRequest {
  items: { productId: number; qty: number; unitPriceCents?: number }[]
  paymentMethod: 'cash' | 'mpesa' | 'account'
  paymentMode?: 'auto' | 'stk' | 'manual'
  customerPhone?: string
  customerName?: string
  customerId?: number
  note?: string
  clientUuid?: string
}

export interface OffsiteStatus {
  enabled: boolean
  pending: number
  lastOk: string
  lastError: string
  lastAt: string
}

export interface Shift {
  id: number
  userId: number
  userName: string
  openingFloatCents: number
  expectedCents: number
  countedCents: number
  varianceCents: number
  openedAt: string
  closedAt: string
}

export interface DesignJob {
  id: number
  title: string
  productName: string
  customerName: string
  notes: string
  status: 'queue' | 'in_progress' | 'ready' | 'delivered'
  assigneeId: number
  assigneeName: string
  createdBy: string
  createdAt: string
  updatedAt: string
}

export interface DailySummary {
  date: string
  salesCents: number
  ordersPaid: number
  ordersOpen: number
  ordersVoided: number
  avgOrderCents: number
  cashCents: number
  mpesaCents: number
  discrepancies: number
  topProducts: { productId: number; name: string; qty: number; salesCents: number }[]
  series: { date: string; salesCents: number; orders: number }[]
}

export interface MonthlySummary {
  month: string
  grossCents: number
  nettCents: number
  vatCents: number
  ordersPaid: number
  ordersVoided: number
  avgOrderCents: number
  cashCents: number
  mpesaCents: number
  discrepancies: number
  taxPercent: number
  taxIncluded: boolean
  series: { date: string; salesCents: number; orders: number }[]
  topProducts: { productId: number; name: string; qty: number; salesCents: number }[]
}

export interface BackupResult {
  file: string
  bytes: number
  at: string
}

export interface AuditEntry {
  id: number
  userId: number
  username: string
  action: string
  entity: string
  entityId: string
  details: string
  createdAt: string
}

export interface Role {
  id: number
  name: string
  description: string
  permissions: string[]
  system: boolean
  userCount?: number
}

export interface PermissionDef { key: string; group: string; label: string }

export interface Customer {
  id: number
  name: string
  phone: string
  creditLimitCents: number
  loyaltyPoints: number
  balanceCents: number
  active: boolean
  createdAt: string
  updatedAt: string
}

export interface Supplier {
  id: number
  name: string
  phone: string
  email: string
  address: string
  notes: string
  active: boolean
  createdAt: string
  updatedAt: string
}

export interface POItem {
  id: number
  poId: number
  productId: number
  name: string
  sku: string
  qty: number
  costCents: number
  lineTotalCents: number
}

export interface PurchaseOrder {
  id: number
  number: string
  supplierId: number
  supplierName: string
  status: 'PENDING' | 'RECEIVED' | 'CANCELLED'
  subtotalCents: number
  note: string
  items: POItem[]
  createdAt: string
  receivedAt: string
}

export interface StockTakeItem {
  id: number
  takeId: number
  productId: number
  name: string
  sku: string
  expectedQty: number
  countedQty: number
}

export interface StockTake {
  id: number
  number: string
  status: 'OPEN' | 'APPLIED' | 'CANCELLED'
  note: string
  items: StockTakeItem[]
  itemCount: number
  createdAt: string
  appliedAt: string
}

export interface LedgerEntry {
  id: number
  customerId: number
  orderId: number
  kind: 'charge' | 'payment' | 'adjustment' | 'loyalty'
  amountCents: number
  pointsDelta: number
  note: string
  createdBy: number
  createdAt: string
}
