// API client — same-origin by default (single binary serves the SPA),
// token auth, consistent {data}/{error} envelope handling.

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

const BASE = '' // same origin; Vite dev proxies /api → :3000

export function token(): string | null {
  return localStorage.getItem('pos_token')
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
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
  method: 'cash' | 'mpesa'
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
  paymentMethod: 'cash' | 'mpesa'
  paymentMode?: 'auto' | 'stk' | 'manual'
  customerPhone?: string
  customerName?: string
  note?: string
  clientUuid?: string
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
