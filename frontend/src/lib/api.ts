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
  /** Raw error body — the multi-shop login picker reads needsShop/shops from it. */
  body?: any
  constructor(status: number, message: string, body?: any) {
    super(message)
    this.status = status
    this.body = body
  }
}

const BASE = (import.meta.env.VITE_API_URL as string | undefined) ?? '' // same origin by default; Vite dev proxies /api → :3000

// Android client: the APK bundles this SPA and talks to a till server on
// the shop LAN. The till address is set once on the connect screen and
// stored in localStorage. When set it OVERRIDES same-origin AND disables
// the demo fallback — a POS client must never silently swap to fake data
// just because the till is unreachable (it shows a connect error instead).
export const SERVER_URL_KEY = 'ledgerpos_server_url'
export function serverUrl(): string {
  try {
    return localStorage.getItem(SERVER_URL_KEY) || BASE
  } catch {
    return BASE
  }
}
export function setServerUrl(url: string): void {
  try {
    localStorage.setItem(SERVER_URL_KEY, url.replace(/\/+$/, ''))
  } catch { /* storage unavailable — keep the in-memory value only */ }
}
export function clearServerUrl(): void {
  try { localStorage.removeItem(SERVER_URL_KEY) } catch { /* noop */ }
}
const RUNTIME_BASE = serverUrl()

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
  if (RUNTIME_BASE) return 'real' // explicit backend URL → never silently demo
  if (demoForced()) return 'demo'
  try {
    const ctl = new AbortController()
    const t = window.setTimeout(() => ctl.abort(), 1500)
    const res = await fetch(`${RUNTIME_BASE}/api/v1/health`, { cache: 'no-store', signal: ctl.signal })
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
  signupAllowed?: boolean
}

let desktopStatusCache: DesktopStatus | null = null

export async function getDesktopStatus(): Promise<DesktopStatus> {
  if (desktopStatusCache) return desktopStatusCache
  let status: DesktopStatus = { desktop: false }
  try {
    if ((await backendMode()) === 'real') {
      const res = await fetch(RUNTIME_BASE + '/api/v1/system/desktop', { cache: 'no-store' })
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

// ---- session token storage (single choke point) ----
// Every sign-in / sign-out site used to touch localStorage directly (6
// files). They all route through here now, so a future httpOnly-cookie
// session (server-set, CSRF-protected) is a change to THIS adapter only.
//
// localStorage persistence is deliberate, not an oversight: a cashier must
// survive a tab/webview refresh without re-signing in. The classic
// "XSS steals the localStorage token" path needs an injected SCRIPT
// origin; the CSP (router.go secureHeaders + netlify.toml) refuses every
// script origin except the Paystack popup toolkit, which cannot read this
// origin's storage.
const TOKEN_KEY = 'pos_token'

export function token(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

export function setToken(t: string): void {
  localStorage.setItem(TOKEN_KEY, t)
}

export function clearToken(): void {
  localStorage.removeItem(TOKEN_KEY)
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
  const res = await fetch(RUNTIME_BASE + path, {
    method,
    headers,
    body: body === undefined ? undefined : body instanceof FormData ? body : JSON.stringify(body),
  })
  const text = await res.text()
  let json: any = null
  try { json = text ? JSON.parse(text) : null } catch { /* non-JSON */ }
  if (!res.ok) {
    // Expired/invalidated session (12h JWT, password change, deactivation):
    // clear the dead token once and return to login instead of leaving the
    // user staring at scattered failures. Auth endpoints are exempt — a
    // wrong password on the login FORM must not wipe a still-valid session
    // token (the guard previously checked '/auth/', which never matches the
    // '/api/v1/auth/…' paths and so was dead code).
    if (res.status === 401 && token() && !path.startsWith('/api/v1/auth/')) {
      clearToken()
      if (!location.pathname.startsWith('/login') && !location.pathname.startsWith('/pin')) {
        location.href = '/login'
      }
    }
    throw new ApiError(res.status, json?.error || `Request failed (${res.status})`, json)
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
  const res = await fetch(RUNTIME_BASE + path, { headers })
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

/** Authenticated binary GET — branded receipt PNGs (Share via WhatsApp).
 *  Demo mode serves data URLs through the text channel; decode them. */
export async function blob(path: string): Promise<Blob> {
  const mode = await backendMode()
  if (mode === 'demo') {
    const dataUrl = await demoRaw(path)
    return (await fetch(dataUrl)).blob()
  }
  const headers: Record<string, string> = {}
  const t = token()
  if (t) headers['Authorization'] = 'Bearer ' + t
  const res = await fetch(RUNTIME_BASE + path, { headers })
  if (!res.ok) {
    let msg = `Download failed (${res.status})`
    try {
      const j = await res.json()
      if (j?.error) msg = j.error
    } catch { /* not JSON */ }
    throw new ApiError(res.status, msg)
  }
  return res.blob()
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
  shopId?: string
  createdAt: string
}

export interface PinUser {
  id: number
  /** Login name — the PIN-reset flow sends THIS (display names don't match accounts). */
  username: string
  fullName: string
  roleName: string
  /** v1.3.2: PIN sign-in is BOUND to the shop this list came from. */
  shopId?: string
}

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
  stockQtyMilli: number
  trackStock: boolean
  /** v1.8.0: sellable unit ('pc','kg','l',…) + fractional till input +
   *  batch/expiry tracking. Optional: backends predate v1.8. */
  unit?: string
  allowFraction?: boolean
  trackBatches?: boolean
  /** Mints a redeemable gift-card code per unit on every paid sale (the
   *  backend forces trackStock off for these). Optional: older backends
   *  predate the flag. */
  isGiftCard?: boolean
  /** v1.6.3 wholesale tier price — 0/absent = same as retail. */
  wholesalePriceCents?: number
  /** v1.6.3 per-product low-stock threshold — 0/absent = global setting. */
  lowStockUnits?: number
  /** v1.10 KRA eTIMS: tax type A-E and item class ("" = shop default) */
  etimsTaxType?: string
  etimsClsCd?: string
  active: boolean
  updatedAt: string
}

/** v1.8.0 — one received lot of a batch-tracked product. */
export interface ProductBatch {
  id: number
  productId: number
  productName: string
  sku: string
  batchNo: string
  expiryDate: string
  qtyReceivedMilli: number
  qtyRemainingMilli: number
  costCents: number
  supplierId: number
  supplierName: string
  reference: string
  status: 'ACTIVE' | 'EXHAUSTED' | 'RECALLED' | string
  createdAt: string
  updatedAt: string
}

/** v1.8.0 — one lot in the expiry report. */
export interface ExpiryBatchRow {
  batchId: number
  productId: number
  name: string
  sku: string
  batchNo: string
  expiryDate: string
  qtyRemainingMilli: number
  costCents: number
  valueCents: number
  status: string
}

export interface ExpiryReport {
  days: number
  expired: ExpiryBatchRow[]
  soon: ExpiryBatchRow[]
  valueCents: number
}

/** One append-only stock-ledger row (v1.6.3): every audited stock change. */
export interface StockMovement {
  id: number
  productId: number
  productName: string
  sku: string
  deltaMilli: number
  balanceAfterMilli: number
  kind: 'OPENING' | 'SALE' | 'VOID_RESTORE' | 'ADJUSTMENT' | 'COUNT' | 'PO_RECEIVE' | 'RETURN_RESTOCK' | string
  reference: string
  note: string
  userId: number
  username: string
  deviceId: string
  createdAt: string
}

/** A completed customer return (v1.6.3) with its line snapshots. */
export interface ReturnItem {
  id: number
  returnId: number
  orderItemId: number
  productId: number
  name: string
  sku: string
  qtyMilli: number
  unitPriceCents: number
  lineTotalCents: number
}

export interface ReturnDoc {
  id: number
  number: string
  orderId: number
  orderNumber: string
  totalCents: number
  method: 'CASH' | 'STORE_CREDIT' | string
  reason: string
  status: string
  createdBy: number
  createdByName: string
  createdAt: string
  items?: ReturnItem[]
}

export interface ExpenseType {
  id: number
  name: string
  isActive: boolean
}

export interface Expense {
  id: number
  number: string
  expenseTypeId: number
  typeName: string
  amountCents: number
  method: 'CASH' | 'MPESA' | 'BANK' | string
  reference: string
  note: string
  spentOn: string
  createdBy: number
  createdByName: string
  createdAt: string
}

/** Photo URL for a product ('' when none) — public, cache-busted route. */
export function productImageUrl(p: Pick<Product, 'id' | 'updatedAt'>): string {
  const q = p.updatedAt ? `?v=${encodeURIComponent(p.updatedAt)}` : ''
  // RUNTIME_BASE is '' on same-origin deploys → URLs stay relative there.
  return `${RUNTIME_BASE}/api/v1/products/${p.id}/image${q}`
}

export interface OrderItem {
  id: number
  productId: number
  name: string
  sku: string
  qtyMilli: number
  unitPriceCents: number
  lineTotalCents: number
  /** v1.7.0: unit cost frozen at sale time (0 = pre-v26 line). */
  costCents?: number
}

export interface Payment {
  id: number
  orderId: number
  method: 'cash' | 'mpesa' | 'account' | 'credit' | 'paystack'
  mode: string
  amountCents: number
  status: string
  phone: string
  email?: string
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
  /** Discount applied at checkout (cents off the subtotal, before VAT)
   *  and its label — receipt + order detail render them so the math adds
   *  up for the customer. */
  discountCents?: number
  discountLabel?: string
  /** Loyalty points spent on this sale (refunded on void). */
  pointsRedeemed?: number
  giftCards?: string[]
  taxPercent: number
  taxIncluded: boolean
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
  /** Connected lifecycle: fulfilment jobs for this sale (detail view). */
  jobs?: DesignJob[]
  assignedToId?: number
  notifiedAt?: string
  /** ERP depth (P6): eTIMS-ready invoice identity — buyerPin present
   *  means this is a tax invoice; invoiceNumber defaults to the order
   *  number and can be reassigned by an eTIMS bridge. */
  buyerPin?: string
  invoiceNumber?: string
  /** v1.10 KRA eTIMS stamp: present once the sale was queued for KRA. */
  etims?: EtimsStamp
}

export interface EtimsStamp {
  status: 'pending' | 'signed' | 'failed'
  invcNo: number
  curRcptNo: number
  totRcptNo: number
  intrlData: string
  rcptSign: string
  /** yyyyMMddHHmmss, Kenya time */
  sdcDateTime: string
  sdcId: string
  mrcNo: string
  qr: string
  lastError?: string
  attempts: number
}

export interface EtimsStatus {
  enabled: boolean
  enabledAt: string
  defaultTax: string
  device: {
    connected?: boolean; env?: 'sandbox' | 'production'; tin?: string; bhfId?: string; dvcSrlNo?: string
    tradeName?: string; branchName?: string; sdcId?: string; mrcNo?: string; defaultClsCd?: string; nextInvoiceNo?: number
  } | null
  signed: number
  pending: number
  failed: number
  lastError: string
  cloudError?: string
}

export interface EtimsClass { code: string; name: string; level: number; taxType: string }

export interface Branding {
  app_name: string
  store_name: string
  store_address?: string
  store_phone?: string
  receipt_footer?: string
  brand_logo_url: string
  loyalty_point_cents?: number
  brand_color: string
  currency_symbol: string
  currency_code: string
  tax_percent: number
  tax_included: boolean
  payment_mode: 'auto' | 'stk' | 'manual'
  till_number: string
  paybill_number: string
  mpesa_env: string
  /** eTIMS (P6): the shop's KRA PIN — when set, checkout offers a buyer
   *  PIN field and receipts can print the tax-invoice header. */
  kra_pin?: string
}

/** One tender in a split/mixed payment. Account tabs cannot be split and
 *  at most one asynchronous leg (mpesa) is allowed per checkout. (The
 *  retired Paystack rail stays valid on HISTORICAL order/payment display
 *  types below — new checkouts can no longer mint card legs.) */
export interface SplitLeg {
  method: 'cash' | 'mpesa' | 'credit'
  amountCents: number
  phone?: string  // mpesa leg (STK push target)
}

export interface CheckoutRequest {
  items: { productId: number; qtyMilli: number; unitPriceCents?: number }[]
  paymentMethod: 'cash' | 'mpesa' | 'account' | 'credit'
  paymentMode?: 'auto' | 'stk' | 'manual'
  customerPhone?: string
  customerEmail?: string
  customerName?: string
  customerId?: number
  /** eTIMS: customer's KRA PIN — present turns the receipt into a tax invoice. */
  buyerPin?: string
  note?: string
  clientUuid?: string
  discountCents?: number
  discountLabel?: string
  redeemPoints?: number
  /** Mixed tender: legs must sum exactly to the amount due (after
   *  discount/redemption). paymentMethod stays the display method. */
  splitPayments?: SplitLeg[]
}

export interface OffsiteStatus {
  enabled: boolean
  pending: number
  lastOk: string
  lastError: string
  lastAt: string
}

export interface UpdateStatus {
  current: string
  latest: string
  notes: string
  url: string
  updateAvailable: boolean
  checkedAt: string
  lastError: string
  staged: boolean
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

/** X/Z cash-session report (P6): X = live for an open shift, Z = frozen at
 *  close with the counted drawer and signed-off variance. */
export interface ShiftReport {
  shift: Shift
  open: boolean
  ordersCount: number
  voidsCount: number
  grossCents: number
  cashCents: number
  mpesaCents: number
  paystackCents: number
  creditCents: number
  otherCents: number
  expectedCashCents: number
  countedCents: number
  varianceCents: number
}

/** One shop row in a consolidated (multi-store) daily report. */
export interface ConsolidatedDaily {
  date: string
  consolidated?: boolean
  salesCents: number
  ordersPaid: number
  ordersOpen?: number
  ordersVoided?: number
  avgOrderCents: number
  cashCents: number
  mpesaCents: number
  paystackCents: number
  creditCents: number
  discrepancies?: number
  shops?: { id: string; name: string; salesCents: number; ordersPaid: number; cashCents: number; mpesaCents: number; paystackCents: number; creditCents: number; ordersVoided?: number; discrepancies?: number }[]
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
  /** Connected lifecycle: the sale this job fulfils + stage timestamps. */
  orderId?: number
  deadline?: string
  priority?: 'low' | 'normal' | 'high'
  assignedAt?: string
  readyAt?: string
  collectedAt?: string
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
  paystackCents: number
  creditCents: number
  discrepancies: number
  topProducts: { productId: number; name: string; qtyMilli: number; salesCents: number; profitCents?: number }[]
  series: { date: string; salesCents: number; orders: number }[]
  /** P5: who sold what today + how many new customers were captured. */
  perStaff?: { userId: number; name: string; orders: number; salesCents: number }[]
  /** v19: sales grouped by originating till (deviceId '' = pre-v19 rows). */
  byTill?: { deviceId: string; orders: number; salesCents: number }[]
  newCustomers?: number
  /** v1.6.3 — money out (expenses), money back (returns), shelf value. */
  expensesCents?: number
  returnsCents?: number
  returnsCount?: number
  netSalesCents?: number
  stockUnits?: number
  stockValueCents?: number
  lowStockCount?: number
  /** v1.7.0 — the day over cost + write-offs. */
  cogsCents?: number
  grossProfitCents?: number
  marginPct?: number
  disposalCount?: number
  disposalValueCents?: number
  /** v1.10 — sell minus buying price, after VAT; net after expenses. */
  salesVatCents?: number
  profitCents?: number
  netProfitCents?: number
  uncostedLines?: number
  uncostedSalesCents?: number
  returnsProfitCents?: number
  /** P6 multi-store: present (with per-shop rows) on the ?shop=all consolidated view. */
  consolidated?: boolean
  shops?: { id: string; name: string; salesCents: number; ordersPaid: number; cashCents: number; mpesaCents: number; paystackCents: number; creditCents: number; ordersVoided?: number; discrepancies?: number }[]
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
  paystackCents: number
  creditCents: number
  discrepancies: number
  taxPercent: number
  taxIncluded: boolean
  series: { date: string; salesCents: number; orders: number }[]
  topProducts: { productId: number; name: string; qtyMilli: number; salesCents: number; profitCents?: number }[]
  /** v1.6.3 — expenses booked in the month + refunds given out. */
  expensesCents?: number
  returnsCents?: number
  /** v1.7.0 — the month over cost + write-offs. */
  cogsCents?: number
  grossProfitCents?: number
  marginPct?: number
  disposalCount?: number
  disposalValueCents?: number
  /** v1.10 — sell minus buying price, after VAT; net after expenses. */
  salesVatCents?: number
  profitCents?: number
  netProfitCents?: number
  uncostedLines?: number
  uncostedSalesCents?: number
  returnsProfitCents?: number
}

export interface BackupResult {
  file: string
  bytes: number
  at: string
}

/** v1.7.0 — one written-off stock event (disposal report). */
export interface DisposalRow {
  at: string
  productName: string
  sku: string
  qtyMilli: number
  reason: string
  note: string
  unitCostCents: number
  lossCents: number
  byUsername: string
}

/** v1.7.0 — the /system/health self-audit payload. */
export interface SystemHealth {
  schemaVersion: number
  migrationsApplied: number
  indexesOk: boolean
  indexesFound: string[]
  indexesMissing: string[]
  dbSizeBytes: number
  journalMode: string
  outboxPending: number
  outboxFailed: number
  deadLetters: number
  customersChecked: number
  customerDriftCount: number
  customerDrift?: string[]
  stockChecked: number
  stockDriftCount: number
  stockDrift?: string[]
  stockNoLedger: number
  lastBackupFile: string
  lastBackupAt: string
  backupCount: number
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
  email?: string
  notes?: string
  creditLimitCents: number
  loyaltyPoints: number
  balanceCents: number
  /** v1.6.3: 'retail' (default) | 'wholesale' — the till auto-prices the
   *  cart with the product's wholesale price when a wholesale customer is
   *  on it. Optional: older backends predate the field. */
  priceTier?: 'retail' | 'wholesale' | string
  active: boolean
  createdAt: string
  updatedAt: string
}

/** One WhatsApp receipt delivery job (admin queue view, Settings → WhatsApp). */
export interface WhatsappJob {
  id: number
  orderId: number
  orderNumber: string
  phone: string
  status: 'PENDING' | 'SENT' | 'FAILED' | string
  attempts: number
  lastError: string
  createdAt: string
  sentAt: string
}

/** Verdict for POST /whatsapp/verify-receipt — the invisible-watermark
 *  authenticity check on a receipt image a customer sent as "proof". */
export interface VerifyVerdict {
  valid: boolean
  layer: 'block' | 'lsb' | string
  orderId: number
  number?: string
  orderStatus?: string
  totalCents?: number
  totalMatchesOrder?: boolean
  usable?: boolean
  issuedAt: string
  note?: string
}

export interface OrderEvent {
  id: number
  orderId: number
  kind: 'job' | 'whatsapp' | 'note' | 'status' | string
  message: string
  userId: number
  username: string
  createdAt: string
}

/** One row of a customer's shared purchase history (P5). */
export interface OrderSummary {
  id: number
  number: string
  status: string
  totalCents: number
  createdAt: string
  paidAt: string
}

/** Prepared "contact customer on WhatsApp" action (P4). */
export interface WhatsAppContact {
  orderId: number
  phone: string
  message: string
  url: string
  orderRef: string
  notifiedAt: string
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
  qtyMilli: number
  costCents: number
  lineTotalCents: number
  /** v1.8.0: goods-in lot metadata for batch-tracked products. */
  expiryDate?: string
  batchNo?: string
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
  expectedQtyMilli: number
  countedQtyMilli: number
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

// ---- Stocktake (count sessions) & gift cards ----

export interface StockCountLine {
  id: number
  countId: number
  productId: number
  sku: string
  name: string
  expectedQtyMilli: number
  /** null = not counted yet. */
  countedQtyMilli: number | null
  systemQtyMilli: number
  unitCostCents: number
  applied: boolean
}

export interface StockCount {
  id: number
  number: string
  status: 'OPEN' | 'DONE' | 'CANCELLED'
  note: string
  countedBy: number
  countedByName: string
  openedAt: string
  closedAt: string
  linesTotal: number
  linesCounted: number
  /** counted − system, summed over counted lines (negative = shrinkage). */
  varianceUnits: number
  varianceValueCents: number
}

export interface GiftCard {
  id: number
  code: string
  orderId: number
  initialCents: number
  remainingCents: number
  status: 'ACTIVE' | 'EMPTY'
  issuedAt: string
  redeemedAt: string
  redeemedByCustomerId: number
}

export interface LedgerEntry {
  id: number
  customerId: number
  orderId: number
  kind: 'charge' | 'payment' | 'adjustment' | 'loyalty' | 'credit_topup' | 'credit_redeem' | 'opening'
  amountCents: number
  pointsDelta: number
  note: string
  createdBy: number
  createdAt: string
  /** v1.7.0: what the customer owed right after this row (absent pre-v26). */
  balanceAfter?: number
  /** v1.7.0: non-zero = this row cancels that entry. */
  reversalOf?: number
}

// ---- Retail expansion ----

export interface HeldSale {
  id: number
  refName: string
  items: { productId: number; qtyMilli: number; unitPriceCents?: number }[]
  customerId: number
  customerName: string
  note: string
  deviceId: string
  createdBy: number
  createdByName: string
  createdAt: string
}

export interface VoidReason {
  id: number
  label: string
  active: boolean
  sortOrder: number
}

export interface DesignFile {
  id: number
  jobId: number
  filename: string
  mime: string
  size: number
  uploadedBy: number
  uploadedByName: string
  createdAt: string
}

export interface TeamMember extends User {
  salesToday: number
  salesTodayCents: number
  lastOrderAt: string
}

export interface DashboardConfig {
  /** Route prefixes hidden from this role's nav (Go wire format: hiddenNav). */
  hiddenNav?: string[]
  widgets?: { key: string; visible: boolean }[]
}

export interface Role {
  id: number
  name: string
  description: string
  permissions: string[]
  system: boolean
  userCount?: number
  home_page?: string
  dashboard_config?: DashboardConfig
}

export interface TeamDevice {
  deviceId: string
  deviceName: string
  appVersion: string
  lastSeen: string
  thisDevice: boolean
  /** Cloud device roster accepts this till's events. */
  approved: boolean
  /** True when the owner revoked this till (roster keeps the row so it can be un-revoked). */
  revoked?: boolean
}

export interface TeamStore {
  slug: string
  name: string
  /** Sync partition minted by the database — events never cross stores. */
  teamCode: string
  devices: number
}

export interface TeamInvite {
  id: number
  roleName: string
  note: string
  createdAt: string
  expiresAt: string
  usedBy: string
  usedAt: string
  revoked: boolean
}

/** Minted once — the token is shown a single time and stored hashed in the cloud. */
export interface TeamInviteCreated {
  teamCode: string
  token: string
  roleName: string
  expiresAt: string
}

export interface TeamJoinResult {
  token: string
  user: User
  teamCode: string
  storeName: string
  roleName: string
}

/** Open forgot-PIN request from a locked till (owner view). */
export interface PinResetRow {
  id: number
  deviceId: string
  deviceName: string
  username: string
  /** 'pending' = awaiting approval, 'approved' = a code was issued (can re-mint). */
  status: 'pending' | 'approved'
  createdAt: string
}

/** One priced line on a quotation. */
export interface QuoteItem {
  productId: number
  sku: string
  name: string
  qtyMilli: number
  unitPriceCents: number
  lineTotalCents: number
}

export type QuoteStatus = 'draft' | 'sent' | 'accepted' | 'declined' | 'converted'

/** A quotation: a priced offer sent to a customer before any sale exists. */
export interface Quote {
  id: number
  quoteNo: string
  clientUuid: string
  customerId: number
  customerName: string
  customerPhone: string
  status: QuoteStatus
  items: QuoteItem[]
  subtotalCents: number
  discountCents: number
  discountLabel: string
  taxCents: number
  totalCents: number
  notes: string
  validUntil: string
  createdBy: number
  createdByName: string
  convertedHeldSaleId: number
  createdAt: string
  updatedAt: string
}

export interface QuoteInput {
  customerId: number
  customerName: string
  customerPhone: string
  items: QuoteItem[]
  discountCents: number
  discountLabel: string
  notes: string
  validUntil: string
}

export interface TeamSyncStatus {
  enabled: boolean
  /** Legacy join code — empty/absent for zero-config cloud-identity tills. */
  teamCode?: string
  deviceId: string
  deviceName: string
  lastPush: string
  lastPull: string
  pending: number
  lastError: string
  devices: TeamDevice[]
  /** 'cloud' = automatic cloud identity, 'manual' = hand-entered keys. */
  source: 'cloud' | 'manual' | ''
  registered: boolean
  approved: boolean
  autoApprove: boolean
  /** Multi-store: this till is registered but the owner has not assigned it to a store yet. */
  storePending?: boolean
  /** More than one store lives under this owner's cloud project. */
  multiStore?: boolean
  stores?: TeamStore[]
  /** Registered tills awaiting store assignment (owner view). */
  pendingDevices?: TeamDevice[]
  /** Recent join links minted by this team's owner. */
  invites?: TeamInvite[]
  /** Hardened sync: events quarantined after repeated apply/push failures. */
  failedEvents?: number
  failed?: { direction: string; entity: string; op: string; error: string; attempts: number; updatedAt: string }[]
  /** v1.3.2 outbox reconciliation: committed rows whose sync event failed to be written locally. */
  outboxFailures?: number
  outboxLastError?: string
}

export interface PaymentConfig {
  mpesa: {
    env: string
    /** How STK currently runs: buni | mock | manual (v1.4.4 rail set). */
    route: 'buni' | 'mock' | 'manual' | string
    till: string
    paybill: string
  }
  creditEnabled: boolean
  loyaltyEnabled: boolean
}
