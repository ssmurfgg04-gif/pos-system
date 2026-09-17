// Demo seed — a realistic Nairobi print & branding shop so the static
// (Netlify) build has the same feel as a live box: 3 staff roles, a full
// catalog, ~6 weeks of order history feeding the dashboards, shifts,
// design jobs, and an audit trail. Mirrors the Go server's seeds so demo
// logins match the README (admin/admin123, cashier/cashier123,
// designer/designer123).

export interface DemoUser {
  id: number
  username: string
  fullName: string
  password: string
  pin: string
  roleId: number
  active: boolean
  mustRotate: boolean
  passwordChangedAt: number
  createdAt: string
}

export interface DemoRole {
  id: number
  name: string
  description: string
  permissions: string[]
  system: boolean
}

export interface DemoCategory {
  id: number
  name: string
  slug: string
  sortOrder: number
}

export interface DemoProduct {
  id: number
  sku: string
  barcode: string
  name: string
  categoryId: number
  priceCents: number
  costCents: number
  stockQty: number
  trackStock: boolean
  active: boolean
  updatedAt: string
}

export interface DemoOrderItem {
  id: number
  productId: number
  name: string
  sku: string
  qty: number
  unitPriceCents: number
  lineTotalCents: number
}

export interface DemoCustomer {
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

export interface DemoSupplier {
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

export interface DemoPOItem {
  id: number
  poId: number
  productId: number
  name: string
  sku: string
  qty: number
  costCents: number
  lineTotalCents: number
}

export interface DemoPurchaseOrder {
  id: number
  number: string
  supplierId: number
  supplierName: string
  status: 'PENDING' | 'RECEIVED' | 'CANCELLED'
  subtotalCents: number
  note: string
  items: DemoPOItem[]
  createdAt: string
  receivedAt: string
}

export interface DemoTakeItem {
  id: number
  takeId: number
  productId: number
  name: string
  sku: string
  expectedQty: number
  countedQty: number
}

export interface DemoStockTake {
  id: number
  number: string
  status: 'OPEN' | 'APPLIED' | 'CANCELLED'
  note: string
  items: DemoTakeItem[]
  itemCount: number
  createdAt: string
  appliedAt: string
}

export interface DemoLedgerEntry {
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

export interface DemoPayment {
  id: number
  orderId: number
  method: 'cash' | 'mpesa' | 'account'
  mode: string
  amountCents: number
  status: 'PENDING' | 'COMPLETED' | 'FAILED' | 'VOIDED'
  phone: string
  mpesaReceipt: string
  checkoutRequestId: string
  resultDesc: string
  discrepancy: boolean
  createdAt: string
  completedAt: string
}

export interface DemoOrder {
  id: number
  number: string
  status: 'PENDING' | 'PAID' | 'VOIDED'
  subtotalCents: number
  taxCents: number
  totalCents: number
  cashierId: number
  customerName: string
  customerId: number
  note: string
  clientUuid: string
  discrepancy: boolean
  createdAt: string
  paidAt: string
  voidedAt: string
  voidReason: string
  items: DemoOrderItem[]
  payments: DemoPayment[]
}

export interface DemoShift {
  id: number
  userId: number
  openingFloatCents: number
  expectedCents: number
  countedCents: number
  varianceCents: number
  openedAt: string
  closedAt: string
}

export interface DemoDesignJob {
  id: number
  title: string
  productName: string
  customerName: string
  notes: string
  status: 'queue' | 'in_progress' | 'ready' | 'delivered'
  assigneeId: number
  createdBy: string
  createdAt: string
  updatedAt: string
}

export interface DemoAudit {
  id: number
  userId: number
  username: string
  action: string
  entity: string
  entityId: string
  details: string
  createdAt: string
}

export interface DemoDB {
  v: number
  users: DemoUser[]
  roles: DemoRole[]
  categories: DemoCategory[]
  products: DemoProduct[]
  orders: DemoOrder[]
  customers: DemoCustomer[]
  ledger: DemoLedgerEntry[]
  suppliers: DemoSupplier[]
  purchaseOrders: DemoPurchaseOrder[]
  stockTakes: DemoStockTake[]
  shifts: DemoShift[]
  designJobs: DemoDesignJob[]
  audit: DemoAudit[]
  settings: Record<string, string>
  seq: { user: number; role: number; cat: number; product: number; order: number; item: number; pay: number; shift: number; job: number; audit: number; customer: number; ledger: number; supplier: number; po: number; poItem: number; take: number; takeItem: number }
  dailyOrderSeq: Record<string, number>
}

export const PERMISSION_CATALOG: { key: string; group: string; label: string }[] = [
  { key: 'pos.sell', group: 'Selling', label: 'Checkout and sell' },
  { key: 'pos.void', group: 'Selling', label: 'Void / cancel orders' },
  { key: 'orders.view', group: 'Selling', label: 'View order history' },
  { key: 'customers.view', group: 'Selling', label: 'View customers and tabs' },
  { key: 'customers.manage', group: 'Selling', label: 'Manage customers, credit and tabs' },
  { key: 'suppliers.view', group: 'Catalog', label: 'View suppliers and purchase orders' },
  { key: 'suppliers.manage', group: 'Catalog', label: 'Manage suppliers, receive stock, stock takes' },
  { key: 'payments.manual', group: 'Payments', label: 'Enter manual M-Pesa receipt codes' },
  { key: 'payments.override_price', group: 'Payments', label: 'Override line item prices' },
  { key: 'products.view', group: 'Catalog', label: 'View products and stock' },
  { key: 'products.manage', group: 'Catalog', label: 'Manage products, categories, CSV import' },
  { key: 'reports.view', group: 'Insights', label: 'View sales reports' },
  { key: 'shifts.manage', group: 'Shifts', label: 'Open and close shifts' },
  { key: 'design.view', group: 'Production', label: 'View design board' },
  { key: 'design.manage', group: 'Production', label: 'Manage design jobs' },
  { key: 'users.manage', group: 'Administration', label: 'Manage users' },
  { key: 'roles.manage', group: 'Administration', label: 'Manage roles' },
  { key: 'settings.manage', group: 'Administration', label: 'Manage settings' },
  { key: 'audit.view', group: 'Administration', label: 'View audit log' },
  { key: 'printer.test', group: 'Administration', label: 'Test receipt printer' },
]

const ALL_PERMS = PERMISSION_CATALOG.map((p) => p.key)

const DEMO_SETTINGS: Record<string, string> = {
  app_name: 'Point of Sale',
  store_name: 'Amani Print Studio',
  store_address: 'Moi Avenue, Nairobi CBD',
  store_phone: '+254 712 345 678',
  receipt_footer: 'Asante! Karibu tena. Exchange within 14 days with receipt.',
  currency_code: 'KES',
  currency_symbol: 'KES',
  tax_percent: '16',
  tax_included: 'true',
  brand_color: '#10B981',
  payment_mode: 'auto',
  till_number: '987654',
  paybill_number: '',
  mpesa_env: 'mock',
  mpesa_shortcode: '',
  mpesa_passkey: '',
  mpesa_consumer_key: '',
  mpesa_consumer_secret: '',
  mpesa_callback_url: '',
  mpesa_mock_delay_ms: '4000',
  mpesa_mock_result_code: '0',
  printer_target: '',
  printer_width: '80',
  auto_print_receipts: 'true',
  receipt_logo: 'true',
  offsite_enabled: 'false',
  offsite_endpoint: '',
  offsite_bucket: '',
  offsite_secret_key: '',
  offsite_prefix: '',
  offsite_keep: '14',
  offsite_passphrase: '',
  onboarding_done: 'false',
  update_channel: 'stable',
  update_api_base: 'https://api.github.com',
  low_stock_threshold: '5',
  backup_auto: 'true',
  backup_keep: '7',
}

// Deterministic PRNG so seeded history looks the same on every reload
// (until the user interacts — then state persists anyway).
function mulberry32(seed: number) {
  return function () {
    seed |= 0
    seed = (seed + 0x6d2b79f5) | 0
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

const iso = (d: Date) => d.toISOString().slice(0, 19) // "YYYY-MM-DDTHH:MM:SS"

const CATEGORIES: [string, string][] = [
  ['T-Shirts', 'tshirts'],
  ['Hoodies', 'hoodies'],
  ['Mugs & Bottles', 'mugs-bottles'],
  ['Accessories', 'accessories'],
  ['Services', 'services'],
]

const PRODUCTS: [string, string, string, string, number, number, number, boolean][] = [
  // sku, barcode, name, catSlug, priceCents, costCents, stock, track
  ['TS-001', '4001234500011', 'Classic Cotton Tee — Black', 'tshirts', 55000, 32000, 46, true],
  ['TS-002', '4001234500028', 'Classic Cotton Tee — White', 'tshirts', 55000, 32000, 38, true],
  ['TS-003', '4001234500035', 'Premium Heavyweight Tee', 'tshirts', 85000, 48000, 22, true],
  ['TS-004', '4001234500042', 'Oversized Streetwear Tee', 'tshirts', 90000, 52000, 17, true],
  ['TS-005', '4001234500127', 'Ladies Fitted Tee — Navy', 'tshirts', 65000, 36000, 25, true],
  ['TS-006', '4001234500134', 'Kids Cotton Tee — Assorted', 'tshirts', 40000, 22000, 30, true],
  ['HD-001', '4001234500141', 'Pullover Hoodie — Grey', 'hoodies', 180000, 110000, 12, true],
  ['HD-002', '4001234500158', 'Zip Hoodie — Black', 'hoodies', 200000, 125000, 8, true],
  ['HD-003', '4001234500165', 'Sleeveless Hoodie — Olive', 'hoodies', 150000, 92000, 4, true],
  ['MG-001', '4001234500059', 'Ceramic Mug — 11oz', 'mugs-bottles', 45000, 22000, 54, true],
  ['MG-002', '4001234500066', 'Enamel Camping Mug', 'mugs-bottles', 60000, 34000, 26, true],
  ['MG-003', '4001234500073', 'Insulated Travel Tumbler', 'mugs-bottles', 120000, 70000, 14, true],
  ['MG-004', '4001234500172', 'Magic Colour-Changing Mug', 'mugs-bottles', 80000, 45000, 3, true],
  ['AC-001', '4001234500080', 'Silicone Wristband', 'accessories', 15000, 5000, 210, true],
  ['AC-002', '4001234500097', 'Fabric Wristband', 'accessories', 20000, 8000, 160, true],
  ['AC-003', '4001234500103', 'Canvas Tote Bag', 'accessories', 55000, 28000, 33, true],
  ['AC-004', '4001234500110', 'Embroidered Cap', 'accessories', 70000, 40000, 19, true],
  ['AC-005', '4001234500189', 'Lanyard with PVC Pouch', 'accessories', 35000, 16000, 75, true],
  ['SV-001', '', 'Custom Print Run — per design', 'services', 25000, 0, 0, false],
  ['SV-002', '', 'Artwork & Branding Setup', 'services', 100000, 0, 0, false],
  ['SV-003', '', 'Same-Day Rush Fee', 'services', 50000, 0, 0, false],
]

const CUSTOMERS = [
  '', 'Grace W.', 'Brian O.', 'Faith M.', 'Kevin K.', 'Wanjiku', 'Otieno', 'Aisha N.',
  '', 'Dennis M.', '', 'Zawadi Ventures Ltd', 'Njeri', '', 'Mutua', 'Priya S.',
]

/** Builds the full demo database (deterministic). */
export function buildSeed(): DemoDB {
  const rnd = mulberry32(20260911)
  const now = new Date()

  const roles: DemoRole[] = [
    { id: 1, name: 'Admin', description: 'Full access: settings, users, stock, reports, KRA returns — and checkout when fixing problems', permissions: [...ALL_PERMS], system: true },
    { id: 2, name: 'Cashier', description: 'Point of sale: checkout, manual M-Pesa entry, shifts', permissions: ['pos.sell', 'pos.void', 'orders.view', 'customers.view', 'payments.manual', 'shifts.manage'], system: true },
    { id: 3, name: 'Designer', description: 'Design & production board, catalog visibility', permissions: ['design.view', 'design.manage', 'products.view', 'orders.view'], system: true },
  ]

  const users: DemoUser[] = [
    { id: 1, username: 'admin', fullName: 'Amina Hassan', password: 'admin123', pin: '1234', roleId: 1, active: true, mustRotate: true, passwordChangedAt: 0, createdAt: iso(new Date(now.getTime() - 90 * 864e5)) },
    { id: 2, username: 'cashier', fullName: 'Brian Otieno', password: 'cashier123', pin: '2222', roleId: 2, active: true, mustRotate: true, passwordChangedAt: 0, createdAt: iso(new Date(now.getTime() - 60 * 864e5)) },
    { id: 3, username: 'designer', fullName: 'Wanjiru Mwangi', password: 'designer123', pin: '3333', roleId: 3, active: true, mustRotate: true, passwordChangedAt: 0, createdAt: iso(new Date(now.getTime() - 45 * 864e5)) },
  ]

  const categories: DemoCategory[] = CATEGORIES.map(([name, slug], i) => ({ id: i + 1, name, slug, sortOrder: i }))

  const catBySlug: Record<string, number> = {}
  categories.forEach((c) => (catBySlug[c.slug] = c.id))

  const products: DemoProduct[] = PRODUCTS.map((p, i) => ({
    id: i + 1,
    sku: p[0],
    barcode: p[1],
    name: p[2],
    categoryId: catBySlug[p[3]],
    priceCents: p[4],
    costCents: p[5],
    stockQty: p[6],
    trackStock: p[7],
    active: true,
    updatedAt: iso(new Date(now.getTime() - Math.floor(rnd() * 20) * 864e5)),
  }))

  // ---- Tab customers (a few regulars with credit, one cash-only) ----
  const custSeed: [string, string, number, boolean][] = [
    ['Zawadi Ventures Ltd', '0722123456', 5000000, true],
    ['Faith M.', '0733987654', 200000, true],
    ['Kevin K.', '0711223344', 100000, true],
    ['Brian O.', '', 0, true],
    ['Aisha N.', '0755667788', 300000, false],
  ]
  const customers: DemoCustomer[] = custSeed.map((c, i) => ({
    id: i + 1, name: c[0], phone: c[1], creditLimitCents: c[2],
    loyaltyPoints: 0, balanceCents: 0, active: c[3],
    createdAt: iso(new Date(now.getTime() - (40 - i * 5) * 864e5)),
    updatedAt: iso(new Date(now.getTime() - (40 - i * 5) * 864e5)),
  }))

  // ---- Suppliers (two wholesalers) ----
  const suppliers: DemoSupplier[] = [
    { id: 1, name: 'Nairobi Wholesalers', phone: '0722000000', email: 'orders@nbwholesale.co.ke', address: 'River Road, Nairobi', notes: 'Delivery Tue/Thu', active: true, createdAt: iso(new Date(now.getTime() - 50 * 864e5)), updatedAt: iso(new Date(now.getTime() - 50 * 864e5)) },
    { id: 2, name: 'Kiambu Prints Supply', phone: '0733111222', email: '', address: 'Kiambu Town', notes: '', active: true, createdAt: iso(new Date(now.getTime() - 30 * 864e5)), updatedAt: iso(new Date(now.getTime() - 30 * 864e5)) },
  ]

  // ---- Order history: ~45 days, weekend-heavy for a retail feel ----
  const orders: DemoOrder[] = []
  const TAX = 16
  let orderId = 1
  let itemId = 1
  let payId = 1
  const dailySeq: Record<string, number> = {}
  const sellable = products.filter((p) => p.active)

  const mkOrder = (d: Date, cashierId: number, status: 'PAID' | 'VOIDED' | 'PENDING', method: 'cash' | 'mpesa', extraNote = '') => {
    const day = d.toISOString().slice(0, 10)
    dailySeq[day] = (dailySeq[day] || 0) + 1
    const number = `ORD${day.replace(/-/g, '')}${String(dailySeq[day]).padStart(4, '0')}`
    const nLines = 1 + Math.floor(rnd() * 3)
    const items: DemoOrderItem[] = []
    for (let i = 0; i < nLines; i++) {
      const p = sellable[Math.floor(rnd() * sellable.length)]
      const qty = 1 + Math.floor(rnd() * 3)
      items.push({
        id: itemId++, productId: p.id, name: p.name, sku: p.sku, qty,
        unitPriceCents: p.priceCents, lineTotalCents: p.priceCents * qty,
      })
    }
    const subtotal = items.reduce((s, l) => s + l.lineTotalCents, 0)
    const tax = Math.round((subtotal * TAX) / (100 + TAX))
    const total = subtotal
    const createdAt = iso(new Date(d.getTime() + (8 + Math.floor(rnd() * 9)) * 36e5)) // 08:00–17:00
    const pay: DemoPayment = {
      id: payId++, orderId, method, mode: method === 'cash' ? 'cash' : rnd() < 0.75 ? 'stk' : 'manual',
      amountCents: total, status: status === 'PAID' ? 'COMPLETED' : status === 'VOIDED' ? 'VOIDED' : 'PENDING',
      phone: method === 'mpesa' ? '2547' + String(10000000 + Math.floor(rnd() * 89999999)) : '',
      mpesaReceipt: method === 'mpesa' && status === 'PAID' ? receiptCode(rnd) : '',
      checkoutRequestId: method === 'mpesa' ? 'ws_CO_' + Math.floor(rnd() * 1e12).toString(36) : '',
      resultDesc: '', discrepancy: false,
      createdAt,
      completedAt: status === 'PAID' ? createdAt : '',
    }
    orders.push({
      id: orderId++, number, status, subtotalCents: subtotal, taxCents: tax, totalCents: total,
      cashierId, customerName: CUSTOMERS[Math.floor(rnd() * CUSTOMERS.length)],
      customerId: 0,
      note: extraNote, clientUuid: '', discrepancy: false,
      createdAt, paidAt: status === 'PAID' ? createdAt : '',
      voidedAt: status === 'VOIDED' ? iso(new Date(d.getTime() + 18 * 36e5)) : '',
      voidReason: status === 'VOIDED' ? 'Customer changed mind on design' : '',
      items, payments: [pay],
    })
  }

  for (let back = 45; back >= 0; back--) {
    const d = new Date(now.getTime() - back * 864e5)
    const dow = d.getDay()
    const base = dow === 0 ? 0 : dow === 6 ? 6 : 2 // quiet Sundays, busy Saturdays
    const count = base + Math.floor(rnd() * 4)
    for (let i = 0; i < count; i++) {
      const r = rnd()
      const status = r < 0.94 ? 'PAID' : r < 0.97 ? 'VOIDED' : 'PENDING'
      const method = rnd() < 0.42 ? 'cash' : 'mpesa'
      mkOrder(d, rnd() < 0.8 ? 2 : 1, status as 'PAID' | 'VOIDED' | 'PENDING', method)
    }
  }

  // ---- Shifts: last 5 closed + one open today ----
  const shifts: DemoShift[] = []
  for (let i = 5; i >= 1; i--) {
    const opened = new Date(now.getTime() - i * 864e5)
    const openedAt = new Date(opened.getFullYear(), opened.getMonth(), opened.getDate(), 8, 30)
    const closedAt = new Date(opened.getFullYear(), opened.getMonth(), opened.getDate(), 18, 5)
    const dayStr = openedAt.toISOString().slice(0, 10)
    const dayOrders = orders.filter((o) => o.createdAt.slice(0, 10) === dayStr)
    const cash = dayOrders.filter((o) => o.status === 'PAID' && o.payments[0].method === 'cash').reduce((s, o) => s + o.totalCents, 0)
    const opening = 500000
    const expected = opening + cash
    const variance = Math.round((rnd() - 0.5) * 40000)
    shifts.push({
      id: shifts.length + 1, userId: 2, openingFloatCents: opening,
      expectedCents: expected, countedCents: expected + variance, varianceCents: variance,
      openedAt: iso(openedAt), closedAt: iso(closedAt),
    })
  }
  shifts.push({ id: shifts.length + 1, userId: 2, openingFloatCents: 500000, expectedCents: 0, countedCents: 0, varianceCents: 0, openedAt: iso(new Date(now.getFullYear(), now.getMonth(), now.getDate(), 8, 30)), closedAt: '' })

  // ---- Design board ----
  const designJobs: DemoDesignJob[] = [
    { id: 1, title: 'Zawadi Ventures — staff tees', productName: 'Classic Cotton Tee — White', customerName: 'Zawadi Ventures Ltd', notes: 'Logo front pocket, names on back. 25 pcs.', status: 'in_progress', assigneeId: 3, createdBy: 'admin', createdAt: iso(new Date(now.getTime() - 3 * 864e5)), updatedAt: iso(new Date(now.getTime() - 1 * 864e5)) },
    { id: 2, title: 'Wedding — couple mugs', productName: 'Magic Colour-Changing Mug', customerName: 'Faith M.', notes: 'Photos both sides, pastel theme.', status: 'ready', assigneeId: 3, createdBy: 'cashier', createdAt: iso(new Date(now.getTime() - 2 * 864e5)), updatedAt: iso(new Date(now.getTime() - 864e5)) },
    { id: 3, title: 'Church youth camp wristbands', productName: 'Fabric Wristband', customerName: 'Brian O.', notes: '300 pcs, 3 colourways, collect Sunday.', status: 'queue', assigneeId: 3, createdBy: 'cashier', createdAt: iso(new Date(now.getTime() - 864e5)), updatedAt: iso(new Date(now.getTime() - 864e5)) },
    { id: 4, title: 'Company hoodie re-order', productName: 'Pullover Hoodie — Grey', customerName: 'Zawadi Ventures Ltd', notes: 'Same artwork as last batch.', status: 'delivered', assigneeId: 3, createdBy: 'admin', createdAt: iso(new Date(now.getTime() - 9 * 864e5)), updatedAt: iso(new Date(now.getTime() - 5 * 864e5)) },
    { id: 5, title: 'Sports day caps', productName: 'Embroidered Cap', customerName: 'Mutua', notes: 'School crest embroidery, 40 pcs.', status: 'queue', assigneeId: 3, createdBy: 'cashier', createdAt: iso(new Date(now.getTime() - 12 * 36e5)), updatedAt: iso(new Date(now.getTime() - 12 * 36e5)) },
    { id: 6, title: 'Cafe branding tote bags', productName: 'Canvas Tote Bag', customerName: 'Priya S.', notes: 'Single-colour print, kraft handle.', status: 'in_progress', assigneeId: 3, createdBy: 'admin', createdAt: iso(new Date(now.getTime() - 30 * 36e5)), updatedAt: iso(new Date(now.getTime() - 6 * 36e5)) },
  ]

  // ---- Audit trail ----
  const audit: DemoAudit[] = [
    { id: 1, userId: 1, username: 'admin', action: 'SETTINGS_UPDATED', entity: 'settings', entityId: '', details: 'store_name, receipt_footer', createdAt: iso(new Date(now.getTime() - 2 * 864e5)) },
    { id: 2, userId: 1, username: 'admin', action: 'USER_CREATED', entity: 'user', entityId: '3', details: 'designer', createdAt: iso(new Date(now.getTime() - 45 * 864e5)) },
    { id: 3, userId: 1, username: 'admin', action: 'STOCK_ADJUSTED', entity: 'product', entityId: '1', details: '+20 received from supplier', createdAt: iso(new Date(now.getTime() - 4 * 864e5)) },
    { id: 4, userId: 2, username: 'cashier', action: 'ORDER_VOIDED', entity: 'order', entityId: '12', details: 'Wrong size entered', createdAt: iso(new Date(now.getTime() - 6 * 864e5)) },
    { id: 5, userId: 2, username: 'cashier', action: 'SHIFT_OPENED', entity: 'shift', entityId: '6', details: 'float KES 5,000.00', createdAt: new Date(now.getFullYear(), now.getMonth(), now.getDate(), 8, 30).toISOString().slice(0, 19) },
    { id: 6, userId: 1, username: 'admin', action: 'PRODUCTS_IMPORTED', entity: 'product', entityId: '', details: 'created 0, updated 3', createdAt: iso(new Date(now.getTime() - 8 * 864e5)) },
  ]

  return {
    v: 3, users, roles, categories, products, orders, customers, ledger: [],
    suppliers, purchaseOrders: [], stockTakes: [],
    shifts, designJobs, audit,
    settings: { ...DEMO_SETTINGS },
    seq: { user: users.length + 1, role: roles.length + 1, cat: categories.length + 1, product: products.length + 1, order: orderId, item: itemId, pay: payId, shift: shifts.length + 1, job: designJobs.length + 1, audit: audit.length + 1, customer: customers.length + 1, ledger: 1, supplier: suppliers.length + 1, po: 1, poItem: 1, take: 1, takeItem: 1 },
    dailyOrderSeq: dailySeq,
  }
}

/** Random-looking 10-char M-Pesa receipt code (same charset as Daraja). */
export function receiptCode(rnd: () => number = Math.random): string {
  const chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789'
  let out = ''
  for (let i = 0; i < 10; i++) out += chars[Math.floor(rnd() * chars.length)]
  return out
}
