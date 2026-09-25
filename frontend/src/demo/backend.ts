const DEFAULT_PASSWORDS = new Set([
  'admin123', 'cashier123', 'designer123', 'password', 'letmein', 'qwerty',
  '1234', '0000', '2222', '3333', '1111', '123456',
])
function isDefaultPassword(pw: string) {
  return DEFAULT_PASSWORDS.has(String(pw || '').trim().toLowerCase())
}

// Demo backend — an in-browser API that mirrors the Go server's routes,
// envelopes, permissions, and money math so the static Netlify build is a
// fully working demo (nothing is a dead stub). State persists in
// localStorage; "Reset demo data" in Settings rebuilds the seed.

import { ApiError, token } from '../lib/api'
import { cartTotals } from '../lib/money'
import { buildSeed, receiptCode, DemoDB, DemoOrder, DemoUser, DemoCustomer, DemoLedgerEntry, DemoSupplier, DemoPurchaseOrder, DemoStockTake, PERMISSION_CATALOG } from './seed'

const KEY = 'pos-demo-db-v1'
const MASK = '__SET__'
const LATENCY = [90, 260] as const

let db: DemoDB | null = null

// ---- Retail expansion (v8/v9 server parity) ----

interface DemoHeldSale {
  id: number
  refName: string
  items: { productId: number; qty: number; unitPriceCents?: number }[]
  customerId: number
  customerName: string
  note: string
  deviceId: string
  createdBy: number
  createdByName: string
  createdAt: string
}

interface DemoVoidReason {
  id: number
  label: string
  active: boolean
  sortOrder: number
}

/** The demo DB gains retail collections; older stored seeds migrate lazily. */
type DemoDBX = DemoDB & {
  voidReasons?: DemoVoidReason[]
  heldSales?: DemoHeldSale[]
  productImages?: Record<string, string>
}

const DEFAULT_VOID_REASONS = [
  'Wrong item', 'Customer changed mind', 'Duplicate order',
  'Price dispute', 'Out of stock', 'Training / test',
]

// v9 added permission keys; the seeded catalog (and demo.test's 20-perm
// assertion on buildSeed) stays untouched — they union in at runtime.
const RETAIL_PERMS = [
  { key: 'pos.hold', group: 'Selling', label: 'Park and resume sales' },
  { key: 'payments.apply_discount', group: 'Payments', label: 'Apply order discounts' },
  { key: 'loyalty.redeem', group: 'Payments', label: 'Redeem loyalty points as payment' },
  { key: 'credit.manage', group: 'Payments', label: 'Top up and take store credit payments' },
]
const FULL_PERMS = [...PERMISSION_CATALOG, ...RETAIL_PERMS.filter((r) => !PERMISSION_CATALOG.some((p) => p.key === r.key))]

function load(): DemoDB {
  if (db) return db
  try {
    const raw = localStorage.getItem(KEY)
    if (raw) {
      const parsed = JSON.parse(raw) as DemoDB
      if (parsed && (parsed.v === 1 || parsed.v === 2 || parsed.v === 3)) {
        db = parsed
        migrateDemo(db)
        return db
      }
    }
  } catch {
    /* corrupted — reseed */
  }
  db = buildSeed()
  migrateDemo(db)
  persist()
  return db
}

/** Upgrade stored demo DBs (v1 → v2: tabs & credit). Idempotent. */
function migrateDemo(d: DemoDB) {
  let dirty = false
  if (!Array.isArray((d as any).customers) || !Array.isArray((d as any).ledger)) {
    const fresh = buildSeed()
    d.customers = fresh.customers
    d.ledger = []
    d.seq.customer = fresh.seq.customer
    d.seq.ledger = 1
    dirty = true
  }
  if (d.seq.customer === undefined) {
    d.seq.customer = d.customers.length + 1
    d.seq.ledger = d.ledger.length + 1
    dirty = true
  }
  for (const o of d.orders) {
    if ((o as any).customerId === undefined) {
      (o as any).customerId = 0
      dirty = true
    }
  }
  for (const u of d.users) {
    if ((u as any).mustRotate === undefined) {
      (u as any).mustRotate = true
      dirty = true
    }
    if ((u as any).passwordChangedAt === undefined) {
      (u as any).passwordChangedAt = 0
      dirty = true
    }
  }
  // Cashiers created before tabs existed need customers.view to use them.
  const cashier = d.roles.find((r) => r.name === 'Cashier')
  if (cashier && !cashier.permissions.includes('customers.view')) {
    cashier.permissions.push('customers.view')
    dirty = true
  }
  if (!Array.isArray((d as any).suppliers)) {
    const fresh = buildSeed()
    d.suppliers = fresh.suppliers
    d.purchaseOrders = []
    d.stockTakes = []
    d.seq.supplier = fresh.seq.supplier
    d.seq.po = 1
    d.seq.poItem = 1
    d.seq.take = 1
    d.seq.takeItem = 1
    dirty = true
  }
  if (d.seq.supplier === undefined) {
    d.seq.supplier = d.suppliers.length + 1
    d.seq.po = d.purchaseOrders.length + 1
    d.seq.poItem = 1
    d.seq.take = d.stockTakes.length + 1
    d.seq.takeItem = 1
    dirty = true
  }
  // ---- Retail expansion: reason catalog, parked sales, product photos,
  // store-credit wallets, program settings defaults (idempotent).
  const x = d as DemoDBX
  if (!Array.isArray(x.voidReasons) || x.voidReasons.length === 0) {
    x.voidReasons = DEFAULT_VOID_REASONS.map((label, i) => ({ id: i + 1, label, active: true, sortOrder: i + 1 }))
    dirty = true
  }
  if ((d.seq as any).voidReason === undefined) {
    ;(d.seq as any).voidReason = x.voidReasons!.length + 1
    dirty = true
  }
  if (!Array.isArray(x.heldSales)) {
    x.heldSales = []
    dirty = true
  }
  if ((d.seq as any).held === undefined) {
    ;(d.seq as any).held = 1
    dirty = true
  }
  if (!x.productImages || typeof x.productImages !== 'object') {
    x.productImages = {}
    dirty = true
  }
  for (const c of d.customers) {
    if (typeof (c as any).storeCreditCents !== 'number') {
      ;(c as any).storeCreditCents = 0
      dirty = true
    }
  }
  const settingsDefaults: Record<string, string> = {
    credit_enabled: 'true',
    loyalty_enabled: 'true',
    loyalty_point_cents: '100',
    loyalty_max_percent: '50',
    loyalty_earn_per_cents: '10000',
  }
  for (const [k, v] of Object.entries(settingsDefaults)) {
    if (d.settings[k] === undefined) {
      d.settings[k] = v
      dirty = true
    }
  }
  // v9 server parity: union the new retail permissions into existing roles
  // (Admin gains everything new; Cashier gains the till-side additions).
  for (const role of d.roles) {
    const extra = role.name === 'Admin'
      ? RETAIL_PERMS.map((r) => r.key)
      : role.name === 'Cashier'
        ? ['payments.apply_discount', 'loyalty.redeem', 'credit.manage']
        : []
    for (const k of extra) {
      if (!role.permissions.includes(k)) {
        role.permissions.push(k)
        dirty = true
      }
    }
  }
  if (d.v < 3) {
    d.v = 3
    dirty = true
  }
  if (dirty) persist()
}

function persist() {
  try {
    if (db) localStorage.setItem(KEY, JSON.stringify(db))
  } catch {
    /* storage full/private mode — demo keeps working in-memory */
  }
}

export function resetDemo() {
  db = buildSeed()
  persist()
}

export function isDemoSeeded() {
  return !!localStorage.getItem(KEY)
}

// ---- auth helpers ----

const tokenIssued = new Map<string, number>()

function userFromToken(): { user: DemoUser; perms: string[] } {
  const t = token()
  if (!t || !t.startsWith('demo.')) throw new ApiError(401, 'invalid or expired token')
  const id = Number(t.split('.')[1])
  const u = load().users.find((x) => x.id === id)
  if (!u || !u.active) throw new ApiError(403, 'account unavailable')
  // Sessions die with the credentials they were issued for (server parity).
  const issued = tokenIssued.get(t) ?? 0
  if (u.passwordChangedAt && issued < u.passwordChangedAt) {
    throw new ApiError(401, 'session expired — sign in again')
  }
  const role = db!.roles.find((r) => r.id === u.roleId)
  return { user: u, perms: role ? role.permissions : [] }
}

function requirePerm(perms: string[], key: string) {
  if (!perms.includes(key)) throw new ApiError(403, 'missing permission: ' + key)
}

function issueToken(u: DemoUser) {
  const t = `demo.${u.id}.${Math.random().toString(36).slice(2)}`
  tokenIssued.set(t, Date.now())
  return t
}

function userDTO(u: DemoUser) {
  const role = db!.roles.find((r) => r.id === u.roleId)
  return {
    id: u.id,
    username: u.username,
    fullName: u.fullName,
    roleId: u.roleId,
    roleName: role ? role.name : '',
    permissions: role ? role.permissions : [],
    active: u.active,
    pinSet: !!u.pin,
    mustRotate: !!u.mustRotate,
    createdAt: u.createdAt,
  }
}

function roleDTO(r: DemoDB['roles'][number]) {
  return {
    id: r.id,
    name: r.name,
    description: r.description,
    permissions: [...r.permissions],
    system: r.system,
    userCount: db!.users.filter((u) => u.roleId === r.id).length,
  }
}

function productDTO(p: DemoDB['products'][number]) {
  const c = db!.categories.find((x) => x.id === p.categoryId)
  return {
    id: p.id, sku: p.sku, barcode: p.barcode, name: p.name,
    categoryId: p.categoryId, categoryName: c ? c.name : '',
    priceCents: p.priceCents, costCents: p.costCents, stockQty: p.stockQty,
    trackStock: p.trackStock, active: p.active, updatedAt: p.updatedAt,
  }
}

function orderDTO(o: DemoOrder) {
  const u = db!.users.find((x) => x.id === o.cashierId)
  return {
    id: o.id, number: o.number, status: o.status,
    subtotalCents: o.subtotalCents, taxCents: o.taxCents, totalCents: o.totalCents,
    discountCents: (o as any).discountCents || 0,
    discountLabel: (o as any).discountLabel || '',
    pointsRedeemed: (o as any).pointsRedeemed || 0,
    cashierId: o.cashierId, cashierName: u ? u.fullName || u.username : '',
    customerName: o.customerName, customerId: o.customerId || 0, note: o.note, clientUuid: o.clientUuid,
    discrepancy: o.discrepancy, createdAt: o.createdAt, paidAt: o.paidAt,
    voidedAt: o.voidedAt, voidReason: o.voidReason,
    items: o.items.map((i) => ({ ...i })),
    payments: o.payments.map((p) => ({ ...p })),
  }
}

function customerDTO(c: DemoCustomer) {
  return { ...c }
}

/** Append one ledger row and apply it to the balance/points (server parity). */
function postLedger(customerId: number, orderId: number, kind: DemoLedgerEntry['kind'], amountCents: number, pointsDelta: number, note: string, by: number) {
  const d = load()
  const e: DemoLedgerEntry = {
    id: d.seq.ledger++, customerId, orderId, kind, amountCents,
    pointsDelta, note, createdBy: by, createdAt: nowIso(),
  }
  d.ledger.push(e)
  const c = d.customers.find((x) => x.id === customerId)
  if (c) {
    c.balanceCents += amountCents
    c.loyaltyPoints += pointsDelta
  }
  return e
}

function settingsSnapshot() {
  const out: Record<string, any> = {}
  const allowed = ALLOWED_KEYS
  for (const [k, v] of Object.entries(load().settings)) {
    if (k === 'jwt_secret') continue // never exposed (parity with the server fix)
    if (!allowed.has(k)) continue
    out[k] = isSecretKey(k) ? (v === '' ? '' : MASK) : v
  }
  return out
}

const ALLOWED_KEYS = new Set([
  'app_name', 'store_name', 'store_address', 'store_phone', 'receipt_footer',
  'currency_code', 'currency_symbol', 'tax_percent', 'tax_included', 'brand_color',
  'payment_mode', 'till_number', 'paybill_number', 'mpesa_env', 'mpesa_shortcode',
  'mpesa_passkey', 'mpesa_consumer_key', 'mpesa_consumer_secret', 'mpesa_callback_url',
  'mpesa_mock_delay_ms', 'mpesa_mock_result_code', 'printer_target', 'printer_width',
  'auto_print_receipts', 'receipt_logo', 'low_stock_threshold', 'backup_auto', 'backup_keep',
  'offsite_enabled', 'offsite_endpoint', 'offsite_bucket', 'offsite_region',
  'offsite_access_key', 'offsite_secret_key', 'offsite_prefix', 'offsite_keep',
  'offsite_passphrase', 'onboarding_done', 'update_channel', 'update_api_base',
  'credit_enabled', 'loyalty_enabled', 'loyalty_point_cents', 'loyalty_max_percent', 'loyalty_earn_per_cents',
])

function isSecretKey(k: string) {
  return k.includes('secret') || k.includes('passkey') || k.includes('passphrase')
}

function brandingDTO() {
  const s = load().settings
  return {
    app_name: s.app_name || 'Point of Sale',
    store_name: s.store_name || '',
    brand_logo_url: '',
    brand_color: s.brand_color || '#10B981',
    currency_symbol: s.currency_symbol || 'KES',
    currency_code: s.currency_code || 'KES',
    tax_percent: Number(s.tax_percent) || 16,
    tax_included: (s.tax_included ?? 'true') === 'true',
    payment_mode: (s.payment_mode || 'auto') as 'auto' | 'stk' | 'manual',
    till_number: s.till_number || '',
    paybill_number: s.paybill_number || '',
    mpesa_env: s.mpesa_env || 'mock',
  }
}

function audit(userId: number, username: string, action: string, entity: string, entityId: string, details: string) {
  const d = load()
  d.audit.unshift({ id: d.seq.audit++, userId, username, action, entity, entityId, details, createdAt: nowIso() })
  if (d.audit.length > 300) d.audit.length = 300
}

function nowIso() {
  return new Date().toISOString().slice(0, 19)
}

function nextOrderNumber(d: DemoDB) {
  const day = new Date().toISOString().slice(0, 10)
  d.dailyOrderSeq[day] = (d.dailyOrderSeq[day] || 0) + 1
  return `ORD${day.replace(/-/g, '')}${String(d.dailyOrderSeq[day]).padStart(4, '0')}`
}

function nextDocNumber(d: DemoDB, prefix: string) {
  const day = new Date().toISOString().slice(0, 10)
  const key = `${prefix}:${day}`
  d.dailyOrderSeq[key] = (d.dailyOrderSeq[key] || 0) + 1
  return `${prefix}${day.replace(/-/g, '')}${String(d.dailyOrderSeq[key]).padStart(4, '0')}`
}

/** Weighted-average unit cost in cents, half-up (server parity). */
function avgCost(oldStock: number, oldCost: number, recvQty: number, unitCost: number) {
  const newStock = oldStock + recvQty
  if (newStock <= 0 || recvQty <= 0) return unitCost
  return Math.floor((oldStock * oldCost + recvQty * unitCost + newStock / 2) / newStock)
}

function supplierDTO(s: DemoSupplier) {
  return { ...s }
}

function takeDTO(t: DemoStockTake) {
  return { ...t, itemCount: t.items.length }
}

function adjustStock(productId: number, delta: number) {
  const p = load().products.find((x) => x.id === productId)
  if (p && p.trackStock) p.stockQty = Math.max(0, p.stockQty + delta)
}

/** Store-credit ledger rows (kinds the seed type doesn't know about). */
function postCreditLedger(d: DemoDB, customerId: number, orderId: number, kind: 'credit_topup' | 'credit_redeem', amountCents: number, note: string, by: number) {
  d.ledger.push({
    id: d.seq.ledger++, customerId, orderId,
    kind: kind as unknown as DemoLedgerEntry['kind'],
    amountCents, pointsDelta: 0, note, createdBy: by, createdAt: nowIso(),
  })
}

function completeOrder(orderId: number, opts: { receipt?: string; method: 'cash' | 'mpesa' | 'credit'; mode: string; amountCents?: number }) {
  const d = load()
  const o = d.orders.find((x) => x.id === orderId)
  if (!o || o.status !== 'PENDING') return
  const pay = o.payments[o.payments.length - 1]
  o.status = 'PAID'
  o.paidAt = nowIso()
  pay.status = 'COMPLETED'
  pay.completedAt = nowIso()
  if (opts.receipt) pay.mpesaReceipt = opts.receipt
  if (opts.amountCents !== undefined && opts.amountCents !== o.totalCents) {
    pay.discrepancy = true
    o.discrepancy = true
    pay.amountCents = opts.amountCents
  }
  // Guarded stock decrement (never double-deduct: only PENDING→PAID passes).
  for (const it of o.items) adjustStock(it.productId, -it.qty)
  // Loyalty earns on EVERY paid order tied to a customer (server parity).
  if (o.customerId && (d.settings.loyalty_enabled ?? 'true') !== 'false') {
    const per = Math.round(Number(d.settings.loyalty_earn_per_cents))
    const earn = Math.floor(o.totalCents / (per > 0 ? per : 10000))
    if (earn > 0) {
      postLedger(o.customerId, o.id, 'loyalty', 0, earn, `loyalty earned ${o.number}`, o.cashierId)
    }
  }
}

// ---- product photo helpers (demo stores data URLs in-memory) ----

function sniffedImage(b: Uint8Array): boolean {
  if (b.length >= 8 && b[0] === 0x89 && b[1] === 0x50 && b[2] === 0x4e && b[3] === 0x47) return true // png
  if (b.length >= 3 && b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return true // jpeg
  const head6 = String.fromCharCode(...b.slice(0, 6))
  if (b.length >= 6 && (head6 === 'GIF87a' || head6 === 'GIF89a')) return true // gif
  if (b.length >= 12 && String.fromCharCode(...b.slice(8, 12)) === 'WEBP') return true // webp
  return false
}

function bytesToBase64(bytes: Uint8Array): string {
  let bin = ''
  const chunk = 0x8000
  for (let i = 0; i < bytes.length; i += chunk) {
    bin += String.fromCharCode(...bytes.subarray(i, i + chunk))
  }
  return btoa(bin)
}

/** In-memory photo for a product (data URL or ''). Demo <img> srcs can't
 * hit the real /image route on a static host, so the UI reads this. */
export function demoProductImageUrl(productId: number): string {
  const x = load() as DemoDBX
  return x.productImages?.[String(productId)] || ''
}

/** Simulate the M-Pesa STK lifecycle (mock provider semantics). */
function simulateStk(orderId: number) {
  const d = load()
  const delay = Math.max(1200, Number(d.settings.mpesa_mock_delay_ms) || 4000)
  const resultCode = Number(d.settings.mpesa_mock_result_code) || 0
  window.setTimeout(() => {
    const o = load().orders.find((x) => x.id === orderId)
    if (!o || o.status !== 'PENDING') return // already settled (manual entry, void…)
    const pay = o.payments[o.payments.length - 1]
    if (pay.status !== 'PENDING' || !pay.checkoutRequestId) return
    if (resultCode === 0) {
      completeOrder(orderId, { receipt: receiptCode(), method: 'mpesa', mode: pay.mode })
    } else {
      pay.status = 'FAILED'
      pay.resultDesc = resultCode === 1032 ? 'Request cancelled by user' : `M-Pesa error ${resultCode}`
    }
    persist()
  }, delay)
}

// ---- reports ----

function dayBounds(date: string) {
  return [date + 'T00:00:00', date + 'T23:59:59'] as const
}

function dailySummary(date: string) {
  const d = load()
  const day = date || new Date().toISOString().slice(0, 10)
  const [from, to] = dayBounds(day)
  const inDay = d.orders.filter((o) => o.createdAt >= from && o.createdAt <= to)
  const paid = inDay.filter((o) => o.status === 'PAID')
  const sales = paid.reduce((s, o) => s + o.totalCents, 0)
  const completedPays = d.orders.flatMap((o) => o.payments).filter((p) => p.status === 'COMPLETED' && p.completedAt >= from && p.completedAt <= to)
  const cash = completedPays.filter((p) => p.method === 'cash').reduce((s, p) => s + p.amountCents, 0)
  const mpesa = completedPays.filter((p) => p.method === 'mpesa').reduce((s, p) => s + p.amountCents, 0)
  const topMap = new Map<number, { productId: number; name: string; qty: number; salesCents: number }>()
  for (const o of paid) {
    for (const it of o.items) {
      const cur = topMap.get(it.productId) || { productId: it.productId, name: it.name, qty: 0, salesCents: 0 }
      cur.qty += it.qty
      cur.salesCents += it.lineTotalCents
      topMap.set(it.productId, cur)
    }
  }
  const topProducts = [...topMap.values()].sort((a, b) => b.salesCents - a.salesCents).slice(0, 10)
  const series: { date: string; salesCents: number; orders: number }[] = []
  const start = new Date(day + 'T00:00:00')
  for (let i = 6; i >= 0; i--) {
    const dt = new Date(start.getTime() - i * 864e5).toISOString().slice(0, 10)
    const [f, t] = dayBounds(dt)
    const dayPaid = d.orders.filter((o) => o.status === 'PAID' && o.createdAt >= f && o.createdAt <= t)
    series.push({ date: dt, salesCents: dayPaid.reduce((s, o) => s + o.totalCents, 0), orders: dayPaid.length })
  }
  return {
    date: day, salesCents: sales, ordersPaid: paid.length,
    ordersOpen: inDay.filter((o) => o.status === 'PENDING').length,
    ordersVoided: inDay.filter((o) => o.status === 'VOIDED').length,
    avgOrderCents: paid.length ? Math.round(sales / paid.length) : 0,
    cashCents: cash, mpesaCents: mpesa,
    discrepancies: paid.filter((o) => o.discrepancy).length,
    topProducts, series,
  }
}

function monthlySummary(month: string) {
  const d = load()
  const m = month || new Date().toISOString().slice(0, 7)
  if (!/^\d{4}-\d{2}$/.test(m)) throw new ApiError(400, 'month must look like YYYY-MM')
  const [y, mo] = m.split('-').map(Number)
  const from = `${m}-01T00:00:00`
  const lastDay = new Date(y, mo, 0).getDate()
  const to = `${m}-${String(lastDay).padStart(2, '0')}T23:59:59`
  const paid = d.orders.filter((o) => o.status === 'PAID' && o.createdAt >= from && o.createdAt <= to)
  const gross = paid.reduce((s, o) => s + o.totalCents, 0)
  const vat = paid.reduce((s, o) => s + o.taxCents, 0)
  const completedPays = d.orders.flatMap((o) => o.payments).filter((p) => p.status === 'COMPLETED' && p.completedAt >= from && p.completedAt <= to)
  const series: { date: string; salesCents: number; orders: number }[] = []
  for (let i = 1; i <= lastDay; i++) {
    const dt = `${m}-${String(i).padStart(2, '0')}`
    const [f, t] = dayBounds(dt)
    const dayPaid = d.orders.filter((o) => o.status === 'PAID' && o.createdAt >= f && o.createdAt <= t)
    series.push({ date: dt, salesCents: dayPaid.reduce((s, o) => s + o.totalCents, 0), orders: dayPaid.length })
  }
  const topMap = new Map<number, { productId: number; name: string; qty: number; salesCents: number }>()
  for (const o of paid) {
    for (const it of o.items) {
      const cur = topMap.get(it.productId) || { productId: it.productId, name: it.name, qty: 0, salesCents: 0 }
      cur.qty += it.qty
      cur.salesCents += it.lineTotalCents
      topMap.set(it.productId, cur)
    }
  }
  return {
    month: m, grossCents: gross, nettCents: gross - vat, vatCents: vat,
    ordersPaid: paid.length,
    ordersVoided: d.orders.filter((o) => o.status === 'VOIDED' && o.createdAt >= from && o.createdAt <= to).length,
    avgOrderCents: paid.length ? Math.round(gross / paid.length) : 0,
    cashCents: completedPays.filter((p) => p.method === 'cash').reduce((s, p) => s + p.amountCents, 0),
    mpesaCents: completedPays.filter((p) => p.method === 'mpesa').reduce((s, p) => s + p.amountCents, 0),
    discrepancies: paid.filter((o) => o.discrepancy).length,
    taxPercent: Number(d.settings.tax_percent) || 16,
    taxIncluded: (d.settings.tax_included ?? 'true') === 'true',
    series,
    topProducts: [...topMap.values()].sort((a, b) => b.salesCents - a.salesCents).slice(0, 10),
  }
}

// ---- CSV ----

function csvField(s: string) {
  return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}

function productsCSV() {
  const d = load()
  const rows = ['sku,barcode,name,category,price,cost,stock,track_stock,active']
  for (const p of d.products) {
    const c = d.categories.find((x) => x.id === p.categoryId)
    rows.push(`${csvField(p.sku)},${csvField(p.barcode)},${csvField(p.name)},${csvField(c ? c.name : '')},${(p.priceCents / 100).toFixed(2)},${(p.costCents / 100).toFixed(2)},${p.stockQty},${p.trackStock},${p.active}`)
  }
  return rows.join('\n') + '\n'
}

function parseCSV(text: string): string[][] {
  const records: string[][] = []
  let rec: string[] = []
  let field = ''
  let inQ = false
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    if (inQ) {
      if (ch === '"') {
        if (text[i + 1] === '"') { field += '"'; i++ } else inQ = false
      } else field += ch
    } else if (ch === '"') inQ = true
    else if (ch === ',') { rec.push(field); field = '' }
    else if (ch === '\n') { rec.push(field); field = ''; if (rec.some((f) => f.trim() !== '')) records.push(rec); rec = [] }
    else if (ch !== '\r') field += ch
  }
  if (field || rec.length) { rec.push(field); if (rec.some((f) => f.trim() !== '')) records.push(rec) }
  return records
}

// ---- route dispatch ----

type Body = any

const sleep = () => new Promise((r) => setTimeout(r, LATENCY[0] + Math.random() * (LATENCY[1] - LATENCY[0])))

/** handle routes a request to the demo dataset; returns the `data` payload. */
export async function demoRequest<T>(method: string, path: string, body?: Body): Promise<T> {
  await sleep()
  const d = load()
  const url = new URL(path, 'http://demo.local')
  const p = url.pathname.replace(/^\/api\/v1/, '') || '/'
  const q = url.searchParams
  const m = method.toUpperCase()

  // ---------- public ----------
  if (m === 'POST' && p === '/auth/login') {
    const u = d.users.find((x) => x.username === String(body?.username || '').trim())
    if (!u || u.password !== body?.password) throw new ApiError(401, 'invalid username or password')
    if (!u.active) throw new ApiError(403, 'account deactivated')
    audit(u.id, u.username, 'LOGIN', 'user', String(u.id), 'password')
    persist()
    return { token: issueToken(u), user: userDTO(u) } as T
  }
  if (m === 'GET' && p === '/auth/pin-users') {
    return d.users.filter((u) => u.active && u.pin).map((u) => {
      const role = d.roles.find((r) => r.id === u.roleId)
      return { id: u.id, fullName: u.fullName || u.username, roleName: role ? role.name : '' }
    }) as T
  }
  if (m === 'POST' && p === '/auth/pin') {
    const u = d.users.find((x) => x.id === Number(body?.userId))
    if (!u || !u.active || u.pin !== String(body?.pin || '')) throw new ApiError(401, 'invalid PIN')
    audit(u.id, u.username, 'LOGIN', 'user', String(u.id), 'pin quick-switch')
    persist()
    return { token: issueToken(u), user: userDTO(u) } as T
  }
  if (m === 'POST' && p === '/auth/signup') {
    const username = String(body?.username || '').trim()
    if (!/^[A-Za-z0-9._-]{3,32}$/.test(username)) throw new ApiError(400, 'username must be 3-32 letters, digits, dot, underscore, or hyphen')
    if (isDefaultPassword(String(body?.password))) throw new ApiError(400, 'choose a stronger password — that one is public')
    if (!body?.shopName || String(body.shopName).trim().length > 80) throw new ApiError(400, 'shop name required')
    // Single-shop demo: no tenant provisioning here.
    throw new ApiError(501, 'signup is not available in the demo')
  }
  if (m === 'GET' && p === '/branding') return brandingDTO() as T
  if (m === 'POST' && p === '/payments/mpesa/callback') return { ResultCode: 0 } as T

  // Public product photo (like the real route — <img> tags can't auth).
  const pubImg = p.match(/^\/products\/(\d+)\/image$/)
  if (m === 'GET' && pubImg) {
    const dataUrl = (d as DemoDBX).productImages?.[pubImg[1]] || ''
    if (!dataUrl) throw new ApiError(404, 'no image')
    return dataUrl as T
  }

  // ---------- everything else needs auth ----------
  const { user, perms } = userFromToken()

  // Forced rotation parity: seeded defaults stop here until changed.
  if (user.mustRotate) {
    const rotAllow = p === '/me' || p === '/branding' ||
      p.startsWith('/auth/') || /\/users\/\d+\/(password|pin)$/.test(p)
    if (!rotAllow) throw new ApiError(403, 'password rotation required')
  }

  if (m === 'GET' && p === '/me') return userDTO(user) as T

  // Tender capabilities snapshot (public identifiers only — server parity).
  if (m === 'GET' && p === '/payments/config') {
    return {
      paystack: {
        enabled: false, publicKey: '',
        currency: String(d.settings.currency_code || 'KES').toUpperCase(),
        callbackUrl: '', configured: false,
      },
      mpesa: {
        env: d.settings.mpesa_env || 'mock',
        till: d.settings.till_number || '',
        paybill: d.settings.paybill_number || '',
      },
      creditEnabled: (d.settings.credit_enabled ?? 'true') !== 'false',
      loyaltyEnabled: (d.settings.loyalty_enabled ?? 'true') !== 'false',
    } as T
  }

  // products & categories
  if (m === 'GET' && p === '/products') return d.products.map(productDTO) as T
  if (m === 'GET' && p === '/products/low-stock') {
    requirePerm(perms, 'products.view')
    const th = Number(d.settings.low_stock_threshold) || 5
    return d.products.filter((x) => x.active && x.trackStock && x.stockQty <= th).map(productDTO) as T
  }
  if (m === 'GET' && p === '/categories') {
    return d.categories.map((c) => ({ id: c.id, name: c.name, slug: c.slug, productCount: d.products.filter((x) => x.categoryId === c.id).length })) as T
  }
  if (m === 'POST' && p === '/products') {
    requirePerm(perms, 'products.manage')
    const sku = String(body?.sku || '').trim() || `SKU-${d.seq.product}`
    if (d.products.some((x) => x.sku === sku)) throw new ApiError(409, 'sku already exists')
    const cat = d.categories.find((c) => c.id === Number(body?.categoryId)) || d.categories[0]
    const prod = {
      id: d.seq.product++, sku, barcode: String(body?.barcode || ''), name: String(body?.name || ''),
      categoryId: cat.id, priceCents: Math.max(0, Number(body?.priceCents) || 0),
      costCents: Math.max(0, Number(body?.costCents) || 0), stockQty: Math.max(0, Number(body?.stockQty) || 0),
      trackStock: body?.trackStock !== false, active: body?.active !== false, updatedAt: nowIso(),
    }
    d.products.push(prod)
    audit(user.id, user.username, 'PRODUCT_CREATED', 'product', String(prod.id), prod.name)
    persist()
    return productDTO(prod) as T
  }
  const prodMatch = p.match(/^\/products\/(\d+)$/)
  if (prodMatch) {
    const prod = d.products.find((x) => x.id === Number(prodMatch[1]))
    if (!prod) throw new ApiError(404, 'product not found')
    if (m === 'PUT') {
      requirePerm(perms, 'products.manage')
      if (body?.name !== undefined) prod.name = String(body.name)
      if (body?.sku !== undefined) prod.sku = String(body.sku)
      if (body?.barcode !== undefined) prod.barcode = String(body.barcode)
      if (body?.categoryId !== undefined) prod.categoryId = Number(body.categoryId)
      if (body?.priceCents !== undefined) prod.priceCents = Math.max(0, Number(body.priceCents))
      if (body?.costCents !== undefined) prod.costCents = Math.max(0, Number(body.costCents))
      if (body?.stockQty !== undefined) prod.stockQty = Math.max(0, Number(body.stockQty))
      if (body?.trackStock !== undefined) prod.trackStock = !!body.trackStock
      if (body?.active !== undefined) prod.active = !!body.active
      prod.updatedAt = nowIso()
      audit(user.id, user.username, 'PRODUCT_UPDATED', 'product', String(prod.id), prod.name)
      persist()
      return productDTO(prod) as T
    }
    if (m === 'DELETE') {
      requirePerm(perms, 'products.manage')
      prod.active = false
      audit(user.id, user.username, 'PRODUCT_DEACTIVATED', 'product', String(prod.id), prod.name)
      persist()
      return { deactivated: true } as T
    }
  }
  const adjMatch = p.match(/^\/products\/(\d+)\/adjust-stock$/)
  if (m === 'POST' && adjMatch) {
    requirePerm(perms, 'products.manage')
    const prod = d.products.find((x) => x.id === Number(adjMatch[1]))
    if (!prod) throw new ApiError(404, 'product not found')
    const delta = Number(body?.delta) || 0
    if (!delta) throw new ApiError(400, 'delta must be non-zero')
    prod.stockQty = Math.max(0, prod.stockQty + delta)
    prod.updatedAt = nowIso()
    audit(user.id, user.username, 'STOCK_ADJUSTED', 'product', String(prod.id), `${delta > 0 ? '+' : ''}${delta} ${body?.reason || ''}`.trim())
    persist()
    return productDTO(prod) as T
  }
  // Product photo upload (multipart 'file', ≤2 MB, raster images only —
  // stored as a data URL, served public from the GET image route).
  const imgMatch = p.match(/^\/products\/(\d+)\/image$/)
  if (m === 'POST' && imgMatch) {
    requirePerm(perms, 'products.manage')
    const prod = d.products.find((x) => x.id === Number(imgMatch[1]))
    if (!prod) throw new ApiError(404, 'product not found')
    const file = body instanceof FormData ? (body.get('file') as File | null) : null
    if (!file) throw new ApiError(400, "multipart file field required ('file')")
    if (file.size > 2 << 20) throw new ApiError(413, 'image too large (max 2 MB)')
    const mime = (file as File & { type?: string }).type || ''
    if (!mime.startsWith('image/') || mime.includes('svg')) {
      throw new ApiError(422, 'only png, jpeg, webp or gif images are allowed')
    }
    const buf = new Uint8Array(await file.arrayBuffer())
    if (!sniffedImage(buf)) throw new ApiError(422, 'file content is not a recognised image')
    const dataUrl = `data:${mime};base64,${bytesToBase64(buf)}`
    const x = d as DemoDBX
    x.productImages = { ...(x.productImages || {}), [String(prod.id)]: dataUrl }
    prod.updatedAt = nowIso()
    audit(user.id, user.username, 'PRODUCT_IMAGE_SET', 'product', String(prod.id), `${buf.length} bytes`)
    persist()
    return { imageUrl: dataUrl } as T
  }
  if (m === 'DELETE' && imgMatch) {
    requirePerm(perms, 'products.manage')
    const prod = d.products.find((x) => x.id === Number(imgMatch[1]))
    if (!prod) throw new ApiError(404, 'product not found')
    const x = d as DemoDBX
    if (x.productImages) {
      const next = { ...x.productImages }
      delete next[String(prod.id)]
      x.productImages = next
    }
    prod.updatedAt = nowIso()
    audit(user.id, user.username, 'PRODUCT_IMAGE_CLEARED', 'product', String(prod.id), '')
    persist()
    return { deleted: true } as T
  }
  if (m === 'POST' && p === '/products/import') {
    requirePerm(perms, 'products.manage')
    const text = await (body instanceof FormData ? (body.get('file') as File)?.text?.() : Promise.resolve(''))
    if (!text) throw new ApiError(400, 'CSV file required (multipart field \'file\')')
    const records = parseCSV(text)
    if (records.length < 2) throw new ApiError(400, 'empty CSV')
    const header = records[0].map((h) => h.trim().toLowerCase().replace(/^\ufeff/, ''))
    const col = (k: string) => header.indexOf(k)
    for (const want of ['sku', 'barcode', 'name', 'category', 'price', 'cost', 'stock', 'track_stock', 'active']) {
      if (col(want) < 0) throw new ApiError(400, 'missing column ' + want + ' (use the template endpoint)')
    }
    let created = 0
    let updated = 0
    for (const r of records.slice(1)) {
      const get = (k: string) => (r[col(k)] || '').trim()
      const name = get('name')
      if (!name) continue
      const cents = (s: string) => Math.round(parseFloat(s.replace(/,/g, '')) * 100) || 0
      let cat = d.categories.find((c) => c.name.toLowerCase() === get('category').toLowerCase())
      if (!cat && get('category')) {
        cat = { id: d.seq.cat++, name: get('category'), slug: get('category').toLowerCase().replace(/[^a-z0-9]+/g, '-'), sortOrder: d.categories.length }
        d.categories.push(cat)
      }
      cat = cat || d.categories[0]
      const existing = get('sku') ? d.products.find((x) => x.sku === get('sku')) : undefined
      const fields = {
        barcode: get('barcode'), name, categoryId: cat.id,
        priceCents: cents(get('price')), costCents: cents(get('cost')),
        stockQty: parseInt(get('stock')) || 0,
        trackStock: ['true', '1', 'yes'].includes(get('track_stock').toLowerCase()),
        active: ['true', '1', 'yes'].includes(get('active').toLowerCase()),
      }
      if (existing) {
        Object.assign(existing, fields)
        existing.updatedAt = nowIso()
        updated++
      } else {
        d.products.push({ id: d.seq.product++, sku: get('sku') || `SKU-${d.seq.product}`, ...fields, updatedAt: nowIso() })
        created++
      }
    }
    audit(user.id, user.username, 'PRODUCTS_IMPORTED', 'product', '', `created ${created}, updated ${updated}`)
    persist()
    return { created, updated } as T
  }
  if (m === 'POST' && p === '/categories') {
    requirePerm(perms, 'products.manage')
    const name = String(body?.name || '').trim()
    if (!name) throw new ApiError(400, 'name required')
    const cat = { id: d.seq.cat++, name, slug: name.toLowerCase().replace(/[^a-z0-9]+/g, '-'), sortOrder: d.categories.length }
    d.categories.push(cat)
    audit(user.id, user.username, 'CATEGORY_CREATED', 'category', String(cat.id), name)
    persist()
    return cat as T
  }
  const catMatch = p.match(/^\/categories\/(\d+)$/)
  if (catMatch) {
    const cat = d.categories.find((x) => x.id === Number(catMatch[1]))
    if (!cat) throw new ApiError(404, 'category not found')
    if (m === 'PUT') {
      requirePerm(perms, 'products.manage')
      cat.name = String(body?.name || cat.name)
      cat.slug = cat.name.toLowerCase().replace(/[^a-z0-9]+/g, '-')
      audit(user.id, user.username, 'CATEGORY_UPDATED', 'category', String(cat.id), cat.name)
      persist()
      return cat as T
    }
    if (m === 'DELETE') {
      requirePerm(perms, 'products.manage')
      if (d.products.some((x) => x.categoryId === cat.id)) throw new ApiError(409, 'category has products — move them first')
      d.categories = d.categories.filter((x) => x.id !== cat.id)
      audit(user.id, user.username, 'CATEGORY_DELETED', 'category', String(cat.id), cat.name)
      persist()
      return { deleted: true } as T
    }
  }

  // orders
  if (m === 'GET' && p === '/orders') {
    requirePerm(perms, 'orders.view')
    let list = [...d.orders].sort((a, b) => b.id - a.id)
    const status = q.get('status')
    if (status) list = list.filter((o) => o.status === status)
    const search = (q.get('search') || '').toLowerCase()
    if (search) {
      list = list.filter((o) =>
        o.number.toLowerCase().includes(search) ||
        o.customerName.toLowerCase().includes(search) ||
        o.payments.some((pay) => pay.mpesaReceipt.toLowerCase().includes(search) || pay.phone.includes(search)))
    }
    const from = q.get('from')
    if (from) list = list.filter((o) => o.createdAt >= from + 'T00:00:00')
    const to = q.get('to')
    if (to) list = list.filter((o) => o.createdAt <= to + 'T23:59:59')
    const limit = Math.min(200, Number(q.get('limit')) || 50)
    const offset = Number(q.get('offset')) || 0
    return list.slice(offset, offset + limit).map(orderDTO) as T
  }
  const orderMatch = p.match(/^\/orders\/(\d+)$/)
  if (m === 'GET' && orderMatch) {
    requirePerm(perms, 'orders.view')
    const o = d.orders.find((x) => x.id === Number(orderMatch[1]))
    if (!o) throw new ApiError(404, 'order not found')
    return orderDTO(o) as T
  }
  if (m === 'POST' && p === '/orders/checkout') {
    requirePerm(perms, 'pos.sell')
    const items = Array.isArray(body?.items) ? body.items : []
    if (items.length === 0) throw new ApiError(400, 'cart is empty')
    // resolve lines (override price only with permission)
    const lines: { product: DemoDB['products'][number]; qty: number; unitPriceCents: number }[] = []
    for (const it of items) {
      const prod = d.products.find((x) => x.id === Number(it.productId))
      if (!prod || !prod.active) throw new ApiError(400, 'product not found or inactive')
      const qty = Math.max(1, Number(it.qty) || 1)
      if (prod.trackStock && qty > prod.stockQty) throw new ApiError(400, `insufficient stock for ${prod.name} (${prod.stockQty} left)`)
      let unit = prod.priceCents
      if (it.unitPriceCents !== undefined && Number(it.unitPriceCents) !== prod.priceCents) {
        if (!perms.includes('payments.override_price')) throw new ApiError(403, 'price override needs permission')
        unit = Math.max(0, Number(it.unitPriceCents))
      }
      lines.push({ product: prod, qty, unitPriceCents: unit })
    }
    // idempotent replay (offline sync)
    if (body?.clientUuid) {
      const existing = d.orders.find((o) => o.clientUuid === body.clientUuid)
      if (existing) return orderDTO(existing) as T
    }
    const taxPercent = Number(d.settings.tax_percent) || 16
    const taxIncluded = (d.settings.tax_included ?? 'true') === 'true'
    const t = cartTotals(lines.map((l) => ({ qty: l.qty, unitPriceCents: l.unitPriceCents })), taxPercent, taxIncluded)
    const rawMethod = body?.paymentMethod
    const method =
      rawMethod === 'mpesa' ? 'mpesa'
      : rawMethod === 'account' ? 'account'
      : rawMethod === 'credit' ? 'credit'
      : rawMethod === 'paystack' ? 'paystack'
      : 'cash'
    const mode = method === 'mpesa'
      ? (body?.paymentMode || d.settings.payment_mode || 'auto')
      : method === 'cash' ? 'cash' : ''
    // Tab checkout, store-credit checkout and loyalty redemption all need a
    // live customer up front (server parity: active, and the charge fits
    // inside the limit/wallet — points inside the balance).
    const wantPoints = Math.round(Number(body?.redeemPoints) || 0)
    let tabCustomer: DemoCustomer | undefined
    if (method === 'account' || method === 'credit' || wantPoints > 0) {
      const cid = Number(body?.customerId) || 0
      if (!cid) throw new ApiError(400, `${wantPoints > 0 ? 'redeeming points' : method === 'credit' ? 'store credit' : 'tab'} checkout needs a customer`)
      tabCustomer = d.customers.find((x) => x.id === cid)
      if (!tabCustomer) throw new ApiError(404, 'customer not found')
      if (!tabCustomer.active) throw new ApiError(409, 'customer is inactive')
    }
    // Order-level discount (server parity: permission-gated, must stay
    // below the subtotal; tax is recomputed on the discounted subtotal).
    const discount = Math.round(Number(body?.discountCents) || 0)
    if (discount < 0 || discount >= t.subtotal) {
      throw new ApiError(400, `discount out of range (0 to ${t.subtotal - 1})`)
    }
    if (discount > 0 && !perms.includes('payments.apply_discount')) {
      throw new ApiError(403, 'applying a discount requires payments.apply_discount permission')
    }
    const discountedSub = t.subtotal - discount
    const taxCents = taxIncluded
      ? Math.round((discountedSub * taxPercent) / (100 + taxPercent))
      : Math.round((discountedSub * taxPercent) / 100)
    const totalCents = taxIncluded ? discountedSub : discountedSub + taxCents
    // Loyalty redemption: points × point value, capped at a configured
    // share of the order total (whole points only). Spent points are
    // deducted now and refunded automatically if the order is voided.
    let pointCents = Math.round(Number(d.settings.loyalty_point_cents))
    if (isNaN(pointCents) || pointCents < 0) pointCents = 100
    let maxPct = Math.round(Number(d.settings.loyalty_max_percent))
    if (isNaN(maxPct) || maxPct < 0 || maxPct > 100) maxPct = 50
    let redeemCents = 0
    let redeemSpent = 0
    if (wantPoints > 0) {
      if (!perms.includes('loyalty.redeem')) throw new ApiError(403, 'redeeming points requires loyalty.redeem permission')
      if ((d.settings.loyalty_enabled ?? 'true') === 'false') throw new ApiError(400, 'loyalty program is disabled')
      if (!tabCustomer) throw new ApiError(400, 'redeeming points needs a customer')
      const capCents = (totalCents * maxPct) / 100
      redeemCents = wantPoints * pointCents
      if (redeemCents > capCents) {
        redeemCents = pointCents > 0 ? Math.floor(capCents / pointCents) * pointCents : 0
      }
      if (redeemCents < 0) redeemCents = 0
      if (pointCents > 0) redeemSpent = Math.floor(redeemCents / pointCents)
      if (redeemSpent === 0) throw new ApiError(400, 'points value too small to apply on this order')
      if (tabCustomer.loyaltyPoints < redeemSpent) {
        throw new ApiError(409, `not enough loyalty points: has ${tabCustomer.loyaltyPoints}, wants ${redeemSpent}`)
      }
    }
    // Payable is what changes hands (discount + points already applied).
    const payable = Math.max(0, totalCents - redeemCents)
    const o: DemoOrder = {
      id: d.seq.order++, number: nextOrderNumber(d), status: 'PENDING',
      subtotalCents: t.subtotal, taxCents: taxCents, totalCents: payable,
      cashierId: user.id, customerName: tabCustomer ? tabCustomer.name : String(body?.customerName || ''),
      customerId: tabCustomer ? tabCustomer.id : 0, note: String(body?.note || ''),
      clientUuid: String(body?.clientUuid || ''), discrepancy: false,
      createdAt: nowIso(), paidAt: '', voidedAt: '', voidReason: '',
      items: lines.map((l) => ({
        id: d.seq.item++, productId: l.product.id, name: l.product.name, sku: l.product.sku,
        qty: l.qty, unitPriceCents: l.unitPriceCents, lineTotalCents: l.unitPriceCents * l.qty,
      })),
      payments: [],
    }
    if (discount > 0) {
      ;(o as any).discountCents = discount
      if (body?.discountLabel) (o as any).discountLabel = String(body.discountLabel).slice(0, 120)
    }
    ;(o as any).pointsRedeemed = redeemSpent
    const pay = {
      id: d.seq.pay++, orderId: o.id, method: method as 'cash' | 'mpesa' | 'account', mode: mode as string, amountCents: payable,
      status: 'PENDING' as const, phone: '', mpesaReceipt: '', checkoutRequestId: '',
      resultDesc: '', discrepancy: false, createdAt: nowIso(), completedAt: '',
    }
    if (method === 'account') {
      const c = tabCustomer!
      if (c.creditLimitCents <= 0) throw new ApiError(409, 'customer has no credit — cash only')
      if (c.balanceCents + payable > c.creditLimitCents) {
        throw new ApiError(409, `tab would exceed customer credit limit (${c.name})`)
      }
      // Stock was fail-fast checked per line above; the guarded deduction
      // happens once at settle time (server parity — never here).
      o.payments.push(pay)
      d.orders.push(o)
      postLedger(c.id, o.id, 'charge', payable, 0, `tab charge ${o.number}`, user.id)
      if (redeemSpent > 0) {
        postLedger(c.id, o.id, 'loyalty', 0, -redeemSpent, 'points redeemed at checkout', user.id)
      }
      audit(user.id, user.username, 'TAB_CHARGED', 'order', o.number, `total ${payable}`)
      persist()
      return orderDTO(o) as T
    } else if (method === 'credit') {
      const c = tabCustomer!
      const wallet = Math.round(Number((c as any).storeCreditCents) || 0)
      if (wallet < payable) {
        throw new ApiError(409, `not enough store credit: has ${wallet}, needs ${payable}`)
      }
      ;(c as any).storeCreditCents = wallet - payable
      o.payments.push(pay)
      d.orders.push(o)
      postCreditLedger(d, c.id, o.id, 'credit_redeem', -payable, `store credit payment ${o.number}`, user.id)
      if (redeemSpent > 0) {
        postLedger(c.id, o.id, 'loyalty', 0, -redeemSpent, 'points redeemed at checkout', user.id)
      }
      audit(user.id, user.username, 'ORDER_CREATED', 'order', String(o.id), `${o.number} credit`)
      completeOrder(o.id, { method: 'credit', mode: '' })
      persist()
      return orderDTO(o) as T
    } else if (method === 'cash') {
      o.payments.push(pay)
      d.orders.push(o)
      if (redeemSpent > 0 && tabCustomer) {
        postLedger(tabCustomer.id, o.id, 'loyalty', 0, -redeemSpent, 'points redeemed at checkout', user.id)
      }
      completeOrder(o.id, { method: 'cash', mode: 'cash' })
    } else if (method === 'paystack') {
      // Server parity: the order is created PENDING and checkout is opened
      // by a separate init call — which the demo answers with 501 below.
      o.payments.push(pay)
      d.orders.push(o)
      if (redeemSpent > 0 && tabCustomer) {
        postLedger(tabCustomer.id, o.id, 'loyalty', 0, -redeemSpent, 'points redeemed at checkout', user.id)
      }
    } else if (mode === 'manual') {
      o.payments.push(pay)
      d.orders.push(o) // stays PENDING until the cashier enters the receipt code
      if (redeemSpent > 0 && tabCustomer) {
        postLedger(tabCustomer.id, o.id, 'loyalty', 0, -redeemSpent, 'points redeemed at checkout', user.id)
      }
    } else {
      pay.phone = String(body?.customerPhone || '')
      pay.checkoutRequestId = 'ws_CO_' + Math.random().toString(36).slice(2, 12)
      o.payments.push(pay)
      d.orders.push(o)
      if (redeemSpent > 0 && tabCustomer) {
        postLedger(tabCustomer.id, o.id, 'loyalty', 0, -redeemSpent, 'points redeemed at checkout', user.id)
      }
      simulateStk(o.id)
    }
    audit(user.id, user.username, 'ORDER_CREATED', 'order', String(o.id), `${o.number} ${method}`)
    persist()
    return orderDTO(o) as T
  }
  const voidMatch = p.match(/^\/orders\/(\d+)\/void$/)
  if (m === 'POST' && voidMatch) {
    requirePerm(perms, 'pos.void')
    const o = d.orders.find((x) => x.id === Number(voidMatch[1]))
    if (!o) throw new ApiError(404, 'order not found')
    if (o.status === 'VOIDED') throw new ApiError(409, 'order already voided')
    if (o.status === 'PAID') {
      for (const it of o.items) adjustStock(it.productId, it.qty) // restore
    }
    // Tab void: reverse the ledger charge so a cancelled sale leaves no
    // debt on the balance (server parity — applies to PENDING and PAID).
    if (o.customerId && o.payments.some((pay) => pay.method === 'account')) {
      postLedger(o.customerId, o.id, 'adjustment', -o.totalCents, 0, 'void reversal', user.id)
    }
    // Refund loyalty points redeemed at checkout — a cancelled sale gives
    // the points back (server parity).
    const spent = Math.round(Number((o as any).pointsRedeemed) || 0)
    if (spent > 0 && o.customerId) {
      const c = d.customers.find((x) => x.id === o.customerId)
      if (c) {
        c.loyaltyPoints += spent
        d.ledger.push({
          id: d.seq.ledger++, customerId: c.id, orderId: o.id, kind: 'loyalty',
          amountCents: 0, pointsDelta: spent, note: 'void refund — points returned',
          createdBy: user.id, createdAt: nowIso(),
        })
      }
    }
    // A completed store-credit payment goes back to the wallet.
    const creditPay = o.payments.find((pay) => (pay.method as string) === 'credit' && pay.status === 'COMPLETED')
    if (creditPay && o.customerId) {
      const c = d.customers.find((x) => x.id === o.customerId)
      if (c) {
        ;(c as any).storeCreditCents = Math.round(Number((c as any).storeCreditCents) || 0) + creditPay.amountCents
        postCreditLedger(d, c.id, o.id, 'credit_redeem', creditPay.amountCents, 'void refund — store credit returned', user.id)
      }
    }
    o.status = 'VOIDED'
    o.voidedAt = nowIso()
    o.voidReason = String(body?.reason || '')
    for (const pay of o.payments) if (pay.status === 'PENDING') pay.status = 'VOIDED'
    audit(user.id, user.username, 'ORDER_VOIDED', 'order', String(o.id), o.voidReason)
    persist()
    return orderDTO(o) as T
  }
  const stkMatch = p.match(/^\/orders\/(\d+)\/stkpush$/)
  if (m === 'POST' && stkMatch) {
    requirePerm(perms, 'pos.sell')
    const o = d.orders.find((x) => x.id === Number(stkMatch[1]))
    if (!o) throw new ApiError(404, 'order not found')
    if (o.status !== 'PENDING') return orderDTO(o) as T
    const phone = String(body?.phone || '')
    if (!/^254[17]\d{8}$/.test(phone.replace(/^0/, '254').replace(/^\+?254/, '254'))) {
      // light validation — the UI normalizes; accept 07.. too
      const norm = phone.replace(/[\s-]/g, '').replace(/^0/, '254')
      if (!/^254[17]\d{8}$/.test(norm)) throw new ApiError(400, 'a valid Safaricom number is required')
    }
    const pay = o.payments[o.payments.length - 1]
    pay.phone = phone.replace(/[\s-]/g, '').replace(/^0/, '254')
    pay.mode = pay.mode === 'manual' ? 'stk' : pay.mode || 'stk'
    pay.checkoutRequestId = 'ws_CO_' + Math.random().toString(36).slice(2, 12)
    pay.status = 'PENDING'
    simulateStk(o.id)
    persist()
    return orderDTO(o) as T
  }
  const manualMatch = p.match(/^\/orders\/(\d+)\/manual$/)
  if (m === 'POST' && manualMatch) {
    requirePerm(perms, 'payments.manual')
    const o = d.orders.find((x) => x.id === Number(manualMatch[1]))
    if (!o) throw new ApiError(404, 'order not found')
    if (o.status !== 'PENDING') throw new ApiError(409, 'order is not pending')
    const code = String(body?.receiptCode || '').toUpperCase()
    if (!/^[A-Z0-9]{10}$/.test(code)) throw new ApiError(400, 'receipt code must be 10 letters/digits')
    if (d.orders.some((x) => x.payments.some((pay) => pay.mpesaReceipt === code))) {
      throw new ApiError(409, 'this receipt code was already used')
    }
    completeOrder(o.id, { receipt: code, method: 'mpesa', mode: 'manual' })
    audit(user.id, user.username, 'PAYMENT_CONFIRMED', 'order', String(o.id), `receipt ${code}`)
    persist()
    return orderDTO(o) as T
  }
  const settleMatch = p.match(/^\/orders\/(\d+)\/settle$/)
  if (m === 'POST' && settleMatch) {
    requirePerm(perms, 'pos.sell')
    const o = d.orders.find((x) => x.id === Number(settleMatch[1]))
    if (!o) throw new ApiError(404, 'order not found')
    if (o.status !== 'PENDING') throw new ApiError(409, 'order is not pending')
    const tabPay = o.payments.find((pay) => pay.method === 'account' && pay.status === 'PENDING')
    if (!tabPay || !o.customerId) throw new ApiError(409, 'order has no pending tab payment')
    const method = body?.method === 'mpesa' ? 'mpesa' : 'cash'
    if (method === 'mpesa') {
      const code = String(body?.receiptCode || '').toUpperCase()
      if (!/^[A-Z0-9]{10}$/.test(code)) throw new ApiError(400, 'receipt code must be 10 letters/digits')
      if (d.orders.some((x) => x.payments.some((pay) => pay.mpesaReceipt === code))) {
        throw new ApiError(409, 'this receipt code was already used')
      }
      // Manual-confirm path (server parity): receipt on the tab payment.
      tabPay.mpesaReceipt = code
      tabPay.mode = 'manual'
      completeOrder(o.id, { method: 'mpesa', mode: 'manual' })
    } else {
      tabPay.mode = 'cash'
      completeOrder(o.id, { method: 'cash', mode: 'cash' })
    }
    // Ledger payment posted after the guarded transition; loyalty now
    // earns inside completeOrder (every paid order, server parity).
    postLedger(o.customerId, o.id, 'payment', -o.totalCents, 0, `tab settled ${o.number}`, user.id)
    audit(user.id, user.username, 'TAB_SETTLED', 'order', o.number, method)
    persist()
    return orderDTO(o) as T
  }

  // ---- Paystack checkout (demo: init/verify always refuse — no gateway,
  // no secret key. The PENDING order can be reopened from Orders or voided.)
  const psInitMatch = p.match(/^\/orders\/(\d+)\/paystack\/init$/)
  if (m === 'POST' && psInitMatch) {
    requirePerm(perms, 'pos.sell')
    if (!d.orders.some((x) => x.id === Number(psInitMatch[1]))) throw new ApiError(404, 'order not found')
    throw new ApiError(501, 'payments unavailable in demo mode')
  }
  const psVerifyMatch = p.match(/^\/orders\/(\d+)\/paystack\/verify$/)
  if (m === 'POST' && psVerifyMatch) {
    requirePerm(perms, 'pos.sell')
    if (!d.orders.some((x) => x.id === Number(psVerifyMatch[1]))) throw new ApiError(404, 'order not found')
    throw new ApiError(501, 'payments unavailable in demo mode')
  }

  // ---- parked (held) sales ----
  if (m === 'GET' && p === '/held-sales') {
    requirePerm(perms, 'pos.hold')
    const x = d as DemoDBX
    return [...(x.heldSales || [])].sort((a, b) => b.id - a.id) as T
  }
  if (m === 'POST' && p === '/held-sales') {
    requirePerm(perms, 'pos.hold')
    const x = d as DemoDBX
    const rawItems = Array.isArray(body?.cart?.items) ? body.cart.items : []
    const items = rawItems
      .map((it: any) => ({
        productId: Math.round(Number(it?.productId) || 0),
        qty: Math.max(1, Math.round(Number(it?.qty) || 1)),
        unitPriceCents: it?.unitPriceCents === undefined ? undefined : Math.max(0, Math.round(Number(it.unitPriceCents)) || 0),
      }))
      .filter((it: any) => it.productId > 0)
    if (items.length === 0) throw new ApiError(400, 'nothing to hold — the cart is empty')
    const customerId = Math.round(Number(body?.cart?.customerId) || 0)
    let customerName = String(body?.cart?.customerName || '')
    if (customerId) {
      const c = d.customers.find((y) => y.id === customerId)
      if (c) customerName = c.name
    }
    const sale: DemoHeldSale = {
      id: (d.seq as any).held++,
      refName: String(body?.refName || '').trim().slice(0, 120) || 'Sale',
      items,
      customerId,
      customerName,
      note: String(body?.cart?.note || '').slice(0, 500),
      deviceId: 'demo-device',
      createdBy: user.id,
      createdByName: user.fullName || user.username,
      createdAt: nowIso(),
    }
    x.heldSales = [...(x.heldSales || []), sale]
    audit(user.id, user.username, 'SALE_HELD', 'held_sale', String(sale.id), sale.refName)
    persist()
    return sale as T
  }
  const heldMatch = p.match(/^\/held-sales\/(\d+)$/)
  if (m === 'DELETE' && heldMatch) {
    requirePerm(perms, 'pos.hold')
    const x = d as DemoDBX
    const before = (x.heldSales || []).length
    x.heldSales = (x.heldSales || []).filter((s) => s.id !== Number(heldMatch[1]))
    if (x.heldSales.length === before) throw new ApiError(404, 'held sale not found')
    persist()
    return { deleted: true } as T
  }

  // ---- void-reason catalog (GET is any-authed; edits need settings.manage)
  if (m === 'GET' && p === '/void-reasons') {
    const x = d as DemoDBX
    const all = q.get('all') === 'true'
    return (x.voidReasons || [])
      .filter((r) => all || r.active)
      .sort((a, b) => a.sortOrder - b.sortOrder) as T
  }
  if (m === 'POST' && p === '/void-reasons') {
    requirePerm(perms, 'settings.manage')
    const x = d as DemoDBX
    const label = String(body?.label || '').trim()
    if (!label) throw new ApiError(400, 'label required')
    if ((x.voidReasons || []).some((r) => r.label.toLowerCase() === label.toLowerCase())) throw new ApiError(409, 'reason exists')
    const r: DemoVoidReason = {
      id: (d.seq as any).voidReason++, label,
      active: body?.active !== false,
      sortOrder: (x.voidReasons || []).length + 1,
    }
    x.voidReasons = [...(x.voidReasons || []), r]
    audit(user.id, user.username, 'VOID_REASON_CREATED', 'void_reason', String(r.id), r.label)
    persist()
    return r as T
  }
  const vrMatch = p.match(/^\/void-reasons\/(\d+)$/)
  if (m === 'PUT' && vrMatch) {
    requirePerm(perms, 'settings.manage')
    const x = d as DemoDBX
    const r = (x.voidReasons || []).find((v) => v.id === Number(vrMatch[1]))
    if (!r) throw new ApiError(404, 'reason not found')
    if (body?.label !== undefined && String(body.label).trim()) r.label = String(body.label).trim()
    if (body?.active !== undefined) r.active = !!body.active
    audit(user.id, user.username, 'VOID_REASON_UPDATED', 'void_reason', String(r.id), r.label)
    persist()
    return { updated: true } as T
  }
  if (m === 'POST' && p === '/sync') {
    requirePerm(perms, 'pos.sell')
    const txs = Array.isArray(body?.transactions) ? body.transactions : []
    const results: { clientUuid: string; orderId: number; orderNumber?: string; status?: string; error?: string }[] = []
    for (const r of txs) {
      try {
        const o = await demoRequest<DemoOrder>('POST', '/api/v1/orders/checkout', r)
        results.push({ clientUuid: r.clientUuid, orderId: o.id, orderNumber: o.number, status: o.status })
      } catch (e: any) {
        results.push({ clientUuid: r.clientUuid, orderId: 0, error: e?.message || 'rejected' })
      }
    }
    return results as T
  }

  // ---- customers & tabs (mirrors the Go routes + permission gates) ----
  if (m === 'GET' && p === '/customers') {
    requirePerm(perms, 'customers.view')
    const needle = (q.get('search') || '').trim().toLowerCase()
    const list = d.customers
      .filter((c) => !needle || c.name.toLowerCase().includes(needle) || (c.phone || '').includes(needle))
      .sort((a, b) => Number(b.active) - Number(a.active) || b.balanceCents - a.balanceCents || a.name.localeCompare(b.name))
      .slice(0, 200)
    return list.map(customerDTO) as T
  }
  if (m === 'POST' && p === '/customers') {
    requirePerm(perms, 'customers.manage')
    const name = String(body?.name || '').trim()
    if (!name) throw new ApiError(400, 'customer name required')
    const limit = Math.round(Number(body?.creditLimitCents) || 0)
    if (limit < 0) throw new ApiError(400, 'credit limit cannot be negative')
    const c: DemoCustomer = {
      id: d.seq.customer++, name, phone: String(body?.phone || '').trim(),
      creditLimitCents: limit, loyaltyPoints: 0, balanceCents: 0, active: true,
      createdAt: nowIso(), updatedAt: nowIso(),
    }
    d.customers.push(c)
    audit(user.id, user.username, 'CUSTOMER_CREATED', 'customer', String(c.id), name)
    persist()
    return customerDTO(c) as T
  }
  const custMatch = p.match(/^\/customers\/(\d+)(\/(ledger|payments|adjustments))?$/)
  if (custMatch) {
    const c = d.customers.find((x) => x.id === Number(custMatch[1]))
    if (!c) throw new ApiError(404, 'customer not found')
    const sub = custMatch[3] || ''
    if (m === 'PUT' && !sub) {
      requirePerm(perms, 'customers.manage')
      const name = String(body?.name || '').trim()
      if (!name) throw new ApiError(400, 'customer name required')
      const limit = Math.round(Number(body?.creditLimitCents) || 0)
      if (limit < 0) throw new ApiError(400, 'credit limit cannot be negative')
      c.name = name
      c.phone = String(body?.phone || '').trim()
      c.creditLimitCents = limit
      if (body?.active !== undefined) c.active = !!body.active
      c.updatedAt = nowIso()
      audit(user.id, user.username, 'CUSTOMER_UPDATED', 'customer', String(c.id), name)
      persist()
      return customerDTO(c) as T
    }
    if (m === 'GET' && sub === 'ledger') {
      requirePerm(perms, 'customers.view')
      return d.ledger
        .filter((e) => e.customerId === c.id)
        .sort((a, b) => b.id - a.id)
        .slice(0, 200) as T
    }
    if (m === 'POST' && sub === 'payments') {
      // Walk-in till payments ride pos.sell: cashiers take them all day.
      requirePerm(perms, 'pos.sell')
      const cents = Math.round(Number(body?.amountCents) || 0)
      if (!(cents > 0)) throw new ApiError(400, 'payment amount must be positive')
      if (!c.active) throw new ApiError(409, 'customer is inactive')
      if (cents > c.balanceCents) throw new ApiError(409, `overpayment: ${cents} against balance ${c.balanceCents}`)
      postLedger(c.id, 0, 'payment', -cents, 0, String(body?.note || 'walk-in payment'), user.id)
      audit(user.id, user.username, 'CUSTOMER_PAYMENT', 'customer', String(c.id), String(cents))
      persist()
      return customerDTO(c) as T
    }
    if (m === 'POST' && sub === 'adjustments') {
      requirePerm(perms, 'customers.manage')
      const note = String(body?.note || '').trim()
      if (!note) throw new ApiError(400, 'adjustment needs a note')
      const cents = Math.round(Number(body?.amountCents) || 0)
      postLedger(c.id, 0, 'adjustment', cents, 0, note, user.id)
      audit(user.id, user.username, 'CUSTOMER_ADJUST', 'customer', String(c.id), note)
      persist()
      return customerDTO(c) as T
    }
  }
  // Store-credit top-up (credit.manage) — prepaid money in, wallet grows,
  // ledger records it without touching the tab balance (server parity).
  const topupMatch = p.match(/^\/customers\/(\d+)\/credit-topup$/)
  if (m === 'POST' && topupMatch) {
    requirePerm(perms, 'credit.manage')
    const c = d.customers.find((x) => x.id === Number(topupMatch[1]))
    if (!c) throw new ApiError(404, 'customer not found')
    if (!c.active) throw new ApiError(409, 'customer is inactive')
    const cents = Math.round(Number(body?.amountCents) || 0)
    if (!(cents > 0)) throw new ApiError(400, 'amountCents (positive) required')
    ;(c as any).storeCreditCents = Math.round(Number((c as any).storeCreditCents) || 0) + cents
    c.updatedAt = nowIso()
    postCreditLedger(d, c.id, 0, 'credit_topup', cents, String(body?.note || 'store credit top-up'), user.id)
    audit(user.id, user.username, 'CREDIT_TOPUP', 'customer', String(c.id), String(cents))
    persist()
    return customerDTO(c) as T
  }

  // ---- suppliers & stock-in (mirrors the Go routes + gates) ----
  if (m === 'GET' && p === '/suppliers') {
    requirePerm(perms, 'suppliers.view')
    const needle = (q.get('search') || '').trim().toLowerCase()
    const list = d.suppliers
      .filter((s) => !needle || s.name.toLowerCase().includes(needle) || (s.phone || '').includes(needle))
      .sort((a, b) => Number(b.active) - Number(a.active) || a.name.localeCompare(b.name))
      .slice(0, 200)
    return list.map(supplierDTO) as T
  }
  if (m === 'POST' && p === '/suppliers') {
    requirePerm(perms, 'suppliers.manage')
    const name = String(body?.name || '').trim()
    if (!name) throw new ApiError(400, 'supplier name required')
    const s: DemoSupplier = {
      id: d.seq.supplier++, name, phone: String(body?.phone || '').trim(),
      email: String(body?.email || '').trim(), address: String(body?.address || '').trim(),
      notes: String(body?.notes || '').trim(), active: true,
      createdAt: nowIso(), updatedAt: nowIso(),
    }
    d.suppliers.push(s)
    audit(user.id, user.username, 'SUPPLIER_CREATED', 'supplier', String(s.id), name)
    persist()
    return supplierDTO(s) as T
  }
  const supMatch = p.match(/^\/suppliers\/(\d+)$/)
  if (m === 'PUT' && supMatch) {
    requirePerm(perms, 'suppliers.manage')
    const s = d.suppliers.find((x) => x.id === Number(supMatch[1]))
    if (!s) throw new ApiError(404, 'supplier not found')
    const name = String(body?.name || '').trim()
    if (!name) throw new ApiError(400, 'supplier name required')
    s.name = name
    s.phone = String(body?.phone || '').trim()
    s.email = String(body?.email || '').trim()
    s.address = String(body?.address || '').trim()
    s.notes = String(body?.notes || '').trim()
    if (body?.active !== undefined) s.active = !!body.active
    s.updatedAt = nowIso()
    audit(user.id, user.username, 'SUPPLIER_UPDATED', 'supplier', String(s.id), name)
    persist()
    return supplierDTO(s) as T
  }
  if (m === 'GET' && p === '/purchase-orders') {
    requirePerm(perms, 'suppliers.view')
    return [...d.purchaseOrders].sort((a, b) => b.id - a.id).slice(0, 200) as T
  }
  if (m === 'POST' && p === '/purchase-orders') {
    requirePerm(perms, 'suppliers.manage')
    const sup = d.suppliers.find((x) => x.id === Number(body?.supplierId))
    if (!sup) throw new ApiError(404, 'supplier not found')
    if (!sup.active) throw new ApiError(409, 'supplier is inactive')
    const items = Array.isArray(body?.items) ? body.items : []
    if (items.length === 0) throw new ApiError(400, 'purchase order needs at least one line')
    const o: DemoPurchaseOrder = {
      id: d.seq.po++, number: nextDocNumber(d, 'PO'), supplierId: sup.id, supplierName: sup.name,
      status: 'PENDING', subtotalCents: 0, note: String(body?.note || ''),
      items: [], createdAt: nowIso(), receivedAt: '',
    }
    for (const it of items) {
      const prod = d.products.find((x) => x.id === Number(it.productId))
      if (!prod || !prod.active) throw new ApiError(400, `product ${it.productId} not found or inactive`)
      const qty = Math.max(1, Math.round(Number(it.qty)) || 0)
      if (!(qty > 0)) throw new ApiError(400, 'quantity must be positive')
      const cost = Math.max(0, Math.round(Number(it.costCents)) || 0)
      o.items.push({
        id: d.seq.poItem++, poId: o.id, productId: prod.id, name: prod.name, sku: prod.sku,
        qty, costCents: cost, lineTotalCents: qty * cost,
      })
    }
    o.subtotalCents = o.items.reduce((s, i) => s + i.lineTotalCents, 0)
    d.purchaseOrders.push(o)
    audit(user.id, user.username, 'PO_CREATED', 'purchase_order', o.number, `total ${o.subtotalCents}`)
    persist()
    return o as T
  }
  const poMatch = p.match(/^\/purchase-orders\/(\d+)(\/(receive|cancel))?$/)
  if (poMatch) {
    const o = d.purchaseOrders.find((x) => x.id === Number(poMatch[1]))
    if (!o) throw new ApiError(404, 'purchase order not found')
    const op = poMatch[3] || ''
    if (m === 'GET' && !op) {
      requirePerm(perms, 'suppliers.view')
      return o as T
    }
    if (m === 'POST' && op === 'receive') {
      requirePerm(perms, 'suppliers.manage')
      if (o.status !== 'PENDING') throw new ApiError(409, 'only pending orders receive')
      for (const it of o.items) {
        const prod = d.products.find((x) => x.id === it.productId)
        if (!prod) throw new ApiError(400, `product ${it.productId} not found`)
        if (prod.trackStock) {
          prod.stockQty += it.qty
          prod.costCents = avgCost(prod.stockQty - it.qty, prod.costCents, it.qty, it.costCents)
        }
      }
      o.status = 'RECEIVED'
      o.receivedAt = nowIso()
      audit(user.id, user.username, 'PO_RECEIVED', 'purchase_order', o.number, `total ${o.subtotalCents}`)
      persist()
      return o as T
    }
    if (m === 'POST' && op === 'cancel') {
      requirePerm(perms, 'suppliers.manage')
      if (o.status !== 'PENDING') throw new ApiError(409, 'only pending orders cancel')
      o.status = 'CANCELLED'
      audit(user.id, user.username, 'PO_CANCELLED', 'purchase_order', o.number, String(body?.reason || ''))
      persist()
      return o as T
    }
  }
  if (m === 'GET' && p === '/stock-takes') {
    requirePerm(perms, 'suppliers.view')
    return [...d.stockTakes].sort((a, b) => b.id - a.id).slice(0, 200).map(takeDTO) as T
  }
  if (m === 'POST' && p === '/stock-takes') {
    requirePerm(perms, 'suppliers.manage')
    const ids: number[] = Array.isArray(body?.productIds) ? body.productIds.map(Number) : []
    let prods = d.products.filter((x) => x.active && x.trackStock)
    if (ids.length > 0) {
      prods = ids.map((id) => {
        const prod = d.products.find((x) => x.id === id)
        if (!prod) throw new ApiError(400, `product ${id} not found`)
        return prod
      })
    }
    const t: DemoStockTake = {
      id: d.seq.take++, number: nextDocNumber(d, 'STK'), status: 'OPEN',
      note: String(body?.note || ''), items: [], itemCount: 0,
      createdAt: nowIso(), appliedAt: '',
    }
    for (const prod of prods) {
      t.items.push({
        id: d.seq.takeItem++, takeId: t.id, productId: prod.id, name: prod.name, sku: prod.sku,
        expectedQty: prod.stockQty, countedQty: prod.stockQty,
      })
    }
    t.itemCount = t.items.length
    d.stockTakes.push(t)
    audit(user.id, user.username, 'TAKE_CREATED', 'stock_take', t.number, t.note)
    persist()
    return t as T
  }
  const takeMatch = p.match(/^\/stock-takes\/(\d+)(\/(count|apply|cancel))?$/)
  if (takeMatch) {
    const t = d.stockTakes.find((x) => x.id === Number(takeMatch[1]))
    if (!t) throw new ApiError(404, 'stock take not found')
    const op = takeMatch[3] || ''
    if (m === 'GET' && !op) {
      requirePerm(perms, 'suppliers.view')
      return t as T
    }
    if (m === 'POST' && op === 'count') {
      requirePerm(perms, 'suppliers.manage')
      if (t.status !== 'OPEN') throw new ApiError(409, 'only open takes take counts')
      const counts = body?.counts || {}
      for (const [pid, qty] of Object.entries(counts)) {
        const line = t.items.find((i) => i.productId === Number(pid))
        if (!line) throw new ApiError(400, `product ${pid} is not on this take`)
        const q = Math.round(Number(qty))
        if (!(q >= 0)) throw new ApiError(400, 'count cannot be negative')
        line.countedQty = q
      }
      audit(user.id, user.username, 'TAKE_COUNTED', 'stock_take', String(t.id), `${Object.keys(counts).length} lines`)
      persist()
      return t as T
    }
    if (m === 'POST' && op === 'apply') {
      requirePerm(perms, 'suppliers.manage')
      if (t.status !== 'OPEN') throw new ApiError(409, 'only open takes apply')
      for (const line of t.items) {
        const prod = d.products.find((x) => x.id === line.productId)
        if (prod && prod.trackStock) prod.stockQty = line.countedQty
      }
      t.status = 'APPLIED'
      t.appliedAt = nowIso()
      audit(user.id, user.username, 'TAKE_APPLIED', 'stock_take', t.number, `${t.items.length} lines`)
      persist()
      return t as T
    }
    if (m === 'POST' && op === 'cancel') {
      requirePerm(perms, 'suppliers.manage')
      if (t.status !== 'OPEN') throw new ApiError(409, 'only open takes cancel')
      t.status = 'CANCELLED'
      audit(user.id, user.username, 'TAKE_CANCELLED', 'stock_take', String(t.id), String(body?.reason || ''))
      persist()
      return t as T
    }
  }

  if (m === 'POST' && p === '/printer/kick') {
    requirePerm(perms, 'printer.test')
    // No hardware in the demo: surface the same 422 the server returns
    // with no target configured.
    throw new ApiError(422, 'no printer target configured')
  }
  if (m === 'GET' && p === '/system/offsite') {
    requirePerm(perms, 'settings.manage')
    return { enabled: false, pending: 0, lastOk: '', lastError: '', lastAt: '' } as T
  }
  if (m === 'GET' && p === '/system/update') {
    requirePerm(perms, 'settings.manage')
    return { current: 'demo', latest: '', notes: '', url: '', updateAvailable: false, checkedAt: nowIso(), lastError: '', staged: false } as T
  }
  if (m === 'POST' && (p === '/system/update/refresh' || p === '/system/update/download' || p === '/system/update/install')) {
    requirePerm(perms, 'settings.manage')
    throw new ApiError(501, 'updates are not available in the demo')
  }

  // reports
  if (m === 'GET' && p === '/reports/daily') return dailySummary(q.get('date') || '') as T
  if (m === 'GET' && p === '/reports/monthly') return monthlySummary(q.get('month') || '') as T

  // shifts
  if (m === 'POST' && p === '/shifts/open') {
    requirePerm(perms, 'shifts.manage')
    if (d.shifts.some((s) => s.userId === user.id && !s.closedAt)) throw new ApiError(409, 'you already have an open shift')
    const shift = { id: d.seq.shift++, userId: user.id, openingFloatCents: Math.max(0, Number(body?.openingFloatCents) || 0), expectedCents: 0, countedCents: 0, varianceCents: 0, openedAt: nowIso(), closedAt: '' }
    d.shifts.push(shift)
    audit(user.id, user.username, 'SHIFT_OPENED', 'shift', String(shift.id), `float ${(shift.openingFloatCents / 100).toFixed(2)}`)
    persist()
    return shift as T
  }
  if (m === 'POST' && p === '/shifts/close') {
    requirePerm(perms, 'shifts.manage')
    const shift = d.shifts.find((s) => s.userId === user.id && !s.closedAt)
    if (!shift) throw new ApiError(404, 'no open shift')
    const cash = d.orders
      .filter((o) => o.status === 'PAID' && o.payments[0].method === 'cash' && o.paidAt >= shift.openedAt)
      .reduce((s, o) => s + o.totalCents, 0)
    shift.expectedCents = shift.openingFloatCents + cash
    shift.countedCents = Math.max(0, Number(body?.countedCents) || 0)
    shift.varianceCents = shift.countedCents - shift.expectedCents
    shift.closedAt = nowIso()
    audit(user.id, user.username, 'SHIFT_CLOSED', 'shift', String(shift.id), `variance ${(shift.varianceCents / 100).toFixed(2)}`)
    persist()
    return shift as T
  }
  if (m === 'GET' && p === '/shifts/current') {
    const shift = d.shifts.find((s) => s.userId === user.id && !s.closedAt)
    return (shift || null) as T
  }
  if (m === 'GET' && p === '/shifts') {
    return d.shifts
      .filter((s) => s.userId === user.id)
      .sort((a, b) => b.id - a.id)
      .map((s) => ({ ...s, userName: (d.users.find((u) => u.id === s.userId)?.fullName) || '' })) as T
  }

  // design board
  if (m === 'GET' && p === '/design') {
    requirePerm(perms, 'design.view')
    return d.designJobs.map((j) => ({
      ...j,
      assigneeName: (d.users.find((u) => u.id === j.assigneeId)?.fullName) || '',
    })) as T
  }
  if (m === 'POST' && p === '/design') {
    requirePerm(perms, 'design.manage')
    const job = {
      id: d.seq.job++, title: String(body?.title || 'Untitled'), productName: String(body?.productName || ''),
      customerName: String(body?.customerName || ''), notes: String(body?.notes || ''),
      status: 'queue' as const, assigneeId: Number(body?.assigneeId) || 3,
      createdBy: user.username, createdAt: nowIso(), updatedAt: nowIso(),
    }
    d.designJobs.push(job)
    audit(user.id, user.username, 'DESIGN_JOB_CREATED', 'design', String(job.id), job.title)
    persist()
    return job as T
  }
  const designMatch = p.match(/^\/design\/(\d+)$/)
  if (designMatch) {
    requirePerm(perms, 'design.manage')
    const job = d.designJobs.find((x) => x.id === Number(designMatch[1]))
    if (!job) throw new ApiError(404, 'job not found')
    if (m === 'PUT') {
      if (body?.title !== undefined) job.title = String(body.title)
      if (body?.productName !== undefined) job.productName = String(body.productName)
      if (body?.customerName !== undefined) job.customerName = String(body.customerName)
      if (body?.notes !== undefined) job.notes = String(body.notes)
      if (body?.assigneeId !== undefined) job.assigneeId = Number(body.assigneeId)
      job.updatedAt = nowIso()
      audit(user.id, user.username, 'DESIGN_JOB_UPDATED', 'design', String(job.id), job.title)
      persist()
      return job as T
    }
    if (m === 'DELETE') {
      d.designJobs = d.designJobs.filter((x) => x.id !== job.id)
      persist()
      return { deleted: true } as T
    }
  }
  const designMove = p.match(/^\/design\/(\d+)\/move$/)
  if (m === 'POST' && designMove) {
    requirePerm(perms, 'design.manage')
    const job = d.designJobs.find((x) => x.id === Number(designMove[1]))
    if (!job) throw new ApiError(404, 'job not found')
    const to = String(body?.status || '')
    if (!['queue', 'in_progress', 'ready', 'delivered'].includes(to)) throw new ApiError(400, 'bad status')
    job.status = to as typeof job.status
    job.updatedAt = nowIso()
    audit(user.id, user.username, 'DESIGN_JOB_MOVED', 'design', String(job.id), job.status)
    persist()
    return job as T
  }

  // users & roles
  if (m === 'GET' && p === '/users') {
    requirePerm(perms, 'users.manage')
    return d.users.map(userDTO) as T
  }
  if (m === 'POST' && p === '/users') {
    requirePerm(perms, 'users.manage')
    const username = String(body?.username || '').trim()
    if (!/^[A-Za-z0-9._-]{3,32}$/.test(username)) throw new ApiError(400, 'username must be 3-32 letters, digits, dot, underscore, or hyphen')
    if (d.users.some((u) => u.username === username)) throw new ApiError(409, 'username taken')
    if (!body?.password || String(body.password).length < 6 || String(body.password).length > 128) throw new ApiError(400, 'password must be 6-128 characters')
    if (isDefaultPassword(String(body.password))) throw new ApiError(400, 'choose a stronger password — that one is public')
    if (body?.pin && !/^\d{4}$/.test(String(body.pin))) throw new ApiError(400, 'PIN must be exactly 4 digits')
    const u: DemoUser = {
      id: d.seq.user++, username, fullName: String(body?.fullName || ''),
      password: String(body.password), pin: body?.pin ? String(body.pin) : '',
      roleId: Number(body?.roleId) || 2, active: true, mustRotate: true, passwordChangedAt: 0, createdAt: nowIso(),
    }
    d.users.push(u)
    audit(user.id, user.username, 'USER_CREATED', 'user', String(u.id), username)
    persist()
    return userDTO(u) as T
  }
  const userMatch = p.match(/^\/users\/(\d+)(\/(password|pin))?$/)
  if (userMatch) {
    const u = d.users.find((x) => x.id === Number(userMatch[1]))
    if (!u) throw new ApiError(404, 'user not found')
    const kind = userMatch[3]
    if (m === 'PUT' && (kind === 'password' || kind === 'pin')) {
      // Self-service rotation; managing others needs users.manage.
      // Resetting someone else re-arms rotation (server parity).
      const self = u.id === user.id
      if (!self) requirePerm(perms, 'users.manage')
      if (kind === 'password') {
        if (!body?.password || String(body.password).length < 6 || String(body.password).length > 128) throw new ApiError(400, 'password must be 6-128 characters')
        if (isDefaultPassword(String(body.password))) throw new ApiError(400, 'choose a stronger password — that one is public')
        u.password = String(body.password)
        u.mustRotate = !self
        u.passwordChangedAt = Date.now()
        audit(user.id, user.username, 'PASSWORD_RESET', 'user', String(u.id), u.username)
      } else {
        if (!/^\d{4}$/.test(String(body?.pin || ''))) throw new ApiError(400, 'PIN must be exactly 4 digits')
        if (isDefaultPassword(String(body.pin))) throw new ApiError(400, 'choose a stronger PIN — that one is well-known')
        u.pin = String(body.pin)
        u.mustRotate = !self
        u.passwordChangedAt = Date.now()
        audit(user.id, user.username, 'PIN_UPDATED', 'user', String(u.id), u.username)
      }
      persist()
      return { updated: true } as T
    }
    requirePerm(perms, 'users.manage')
    if (m === 'PUT' && !kind) {
      if (body?.fullName !== undefined) u.fullName = String(body.fullName)
      if (body?.roleId !== undefined) u.roleId = Number(body.roleId)
      if (body?.active !== undefined) u.active = !!body.active
      audit(user.id, user.username, 'USER_UPDATED', 'user', String(u.id), u.username)
      persist()
      return userDTO(u) as T
    }
    if (m === 'DELETE' && !kind) {
      u.active = false
      audit(user.id, user.username, 'USER_DEACTIVATED', 'user', String(u.id), u.username)
      persist()
      return { deactivated: true } as T
    }
  }
  if (m === 'GET' && p === '/roles') {
    requirePerm(perms, 'roles.manage')
    return d.roles.map(roleDTO) as T
  }
  if (m === 'GET' && p === '/permissions') {
    requirePerm(perms, 'roles.manage')
    return { catalog: FULL_PERMS } as T
  }
  if (m === 'POST' && p === '/roles') {
    requirePerm(perms, 'roles.manage')
    const name = String(body?.name || '').trim()
    if (!name) throw new ApiError(400, 'name required')
    if (d.roles.some((r) => r.name === name)) throw new ApiError(409, 'role exists')
    for (const k of body?.permissions || []) {
      if (!FULL_PERMS.some((pc) => pc.key === k)) throw new ApiError(400, 'unknown permission: ' + k)
    }
    const role = { id: d.seq.role++, name, description: String(body?.description || ''), permissions: [...(body?.permissions || [])], system: false }
    d.roles.push(role)
    audit(user.id, user.username, 'ROLE_CREATED', 'role', String(role.id), name)
    persist()
    return roleDTO(role) as T
  }
  const roleMatch = p.match(/^\/roles\/(\d+)$/)
  if (roleMatch) {
    requirePerm(perms, 'roles.manage')
    const role = d.roles.find((x) => x.id === Number(roleMatch[1]))
    if (!role) throw new ApiError(404, 'role not found')
    if (m === 'PUT') {
      for (const k of body?.permissions || []) {
        if (!FULL_PERMS.some((pc) => pc.key === k)) throw new ApiError(400, 'unknown permission: ' + k)
      }
      if (!role.system && body?.name) role.name = String(body.name)
      if (body?.description !== undefined) role.description = String(body.description)
      role.permissions = [...(body?.permissions || role.permissions)]
      audit(user.id, user.username, 'ROLE_UPDATED', 'role', String(role.id), `${role.permissions.length} permissions`)
      persist()
      return roleDTO(role) as T
    }
    if (m === 'DELETE') {
      if (role.system) throw new ApiError(409, 'system roles cannot be deleted')
      if (d.users.some((u) => u.roleId === role.id)) throw new ApiError(409, 'role still has users')
      d.roles = d.roles.filter((x) => x.id !== role.id)
      audit(user.id, user.username, 'ROLE_DELETED', 'role', String(role.id), role.name)
      persist()
      return { deleted: true } as T
    }
  }

  // settings, printer, audit, system
  if (m === 'GET' && p === '/settings') {
    requirePerm(perms, 'settings.manage')
    return settingsSnapshot() as T
  }
  if (m === 'PUT' && p === '/settings') {
    requirePerm(perms, 'settings.manage')
    const values = body?.values || {}
    for (const [k, v] of Object.entries(values)) {
      if (!ALLOWED_KEYS.has(k)) {
        if (v === MASK) continue // untouched read-only echo — skip (server parity)
        throw new ApiError(400, 'unknown setting key: ' + k)
      }
      if (String(v).length > 500) throw new ApiError(400, 'value too long for key: ' + k)
    }
    const changed: string[] = []
    for (const [k, v] of Object.entries(values)) {
      if (!ALLOWED_KEYS.has(k)) continue
      if (isSecretKey(k) && v === MASK) continue
      if (d.settings[k] !== String(v)) changed.push(k)
      d.settings[k] = String(v)
    }
    audit(user.id, user.username, 'SETTINGS_UPDATED', 'settings', '', changed.join(', '))
    persist()
    return settingsSnapshot() as T
  }
  if (m === 'POST' && p === '/settings/test-print') {
    requirePerm(perms, 'printer.test')
    audit(user.id, user.username, 'PRINTER_TESTED', 'printer', d.settings.printer_target || '', '')
    persist()
    return { printed: true } as T
  }
  if (m === 'GET' && p === '/print-jobs') {
    requirePerm(perms, 'printer.test')
    return [] as T
  }
  if (m === 'GET' && p === '/audit') {
    requirePerm(perms, 'audit.view')
    return d.audit.slice(0, 200) as T
  }
  if (m === 'POST' && p === '/system/backup') {
    requirePerm(perms, 'settings.manage')
    const file = `pos-backup-${new Date().toISOString().slice(0, 19).replace(/[-:T]/g, '').slice(0, 15)}.db`
    const bytes = 50000 + d.orders.length * 1200
    audit(user.id, user.username, 'BACKUP_CREATED', 'backup', file, `${bytes} bytes (simulated)`)
    persist()
    return { file, bytes, at: nowIso() } as T
  }
  if (m === 'GET' && p === '/system/backups') {
    requirePerm(perms, 'settings.manage')
    const keep = Number(d.settings.backup_keep) || 7
    const out: { file: string; bytes: number; at: string }[] = []
    for (let i = 0; i < Math.min(keep, 3); i++) {
      const dt = new Date(Date.now() - i * 864e5)
      out.push({ file: `pos-backup-${dt.toISOString().slice(0, 10).replace(/-/g, '')}-020000.db`, bytes: 50000 + Math.max(0, d.orders.length - i * 40) * 1200, at: dt.toISOString().slice(0, 19) })
    }
    return out as T
  }

  throw new ApiError(404, 'not found: ' + m + ' ' + p)
}

/** raw (text) downloads for CSV endpoints — mirrors api.raw in demo mode. */
export async function demoRaw(path: string): Promise<string> {
  await sleep()
  const url = new URL(path, 'http://demo.local')
  const p = url.pathname.replace(/^\/api\/v1/, '')
  const q = url.searchParams
  const { perms } = userFromToken()
  if (p === '/products/export') {
    requirePerm(perms, 'products.manage')
    return productsCSV()
  }
  if (p === '/products/template') {
    requirePerm(perms, 'products.manage')
    return 'sku,barcode,name,category,price,cost,stock,track_stock,active\nTS-101,4001234500103,Sample T-Shirt,T-Shirts,550.00,320.00,40,true,true\n'
  }
  if (p === '/reports/monthly.csv') {
    requirePerm(perms, 'reports.view')
    const m = monthlySummary(q.get('month') || '')
    const rows = [
      'KRA MONTHLY VAT RETURN SUMMARY',
      `Business,${csvField(load().settings.store_name || 'Store')}`,
      `Period,${m.month}`,
      'Figure,Amount',
      `Gross sales (KES),${(m.grossCents / 100).toFixed(2)}`,
      `Taxable value / nett (KES),${(m.nettCents / 100).toFixed(2)}`,
      `VAT at ${m.taxPercent}% (KES),${(m.vatCents / 100).toFixed(2)}`,
      `Transactions,${m.ordersPaid}`,
      `Average transaction (KES),${(m.avgOrderCents / 100).toFixed(2)}`,
      `Cash takings (KES),${(m.cashCents / 100).toFixed(2)}`,
      `M-Pesa takings (KES),${(m.mpesaCents / 100).toFixed(2)}`,
      `Voided orders,${m.ordersVoided}`,
      `Amount discrepancies,${m.discrepancies}`,
      '',
      'Top products',
      'Product,Qty,Sales (KES)',
      ...m.topProducts.map((tp) => `${csvField(tp.name)},${tp.qty},${(tp.salesCents / 100).toFixed(2)}`),
    ]
    return rows.join('\n') + '\n'
  }
  throw new ApiError(404, 'not found')
}
