// @vitest-environment jsdom
// Demo backend contract tests — the in-browser dataset must behave exactly
// like the Go server: same envelopes, same permissions, same money math,
// same order lifecycle (STK simulate, manual receipt, void+restore).

import { beforeEach, describe, expect, it } from 'vitest'
import { demoRequest, resetDemo } from '../src/demo/backend'
import { buildSeed, receiptCode } from '../src/demo/seed'

type Any = Record<string, any>

async function login(username: string, password: string): Promise<string> {
  const res = await demoRequest<{ token: string }>('POST', '/api/v1/auth/login', { username, password })
  localStorage.setItem('pos_token', res.token)
  // Seeded logins start flagged: rotate through the real self-service path.
  // Same password keeps every test's credentials valid for re-login.
  const me = await demoRequest<{ id: number }>('GET', '/api/v1/me')
  await demoRequest('PUT', `/api/v1/users/${me.id}/password`, { password })
  return res.token
}

beforeEach(() => {
  localStorage.clear()
  resetDemo()
})

describe('seed', () => {
  it('builds the three demo roles with server-parity permissions', () => {
    const db = buildSeed()
    const admin = db.roles.find((r) => r.name === 'Admin')!
    const cashier = db.roles.find((r) => r.name === 'Cashier')!
    const designer = db.roles.find((r) => r.name === 'Designer')!
    expect(admin.permissions).toHaveLength(20)
    expect(cashier.permissions).toContain('pos.sell')
    expect(cashier.permissions).not.toContain('settings.manage')
    expect(designer.permissions).toContain('design.manage')
    expect(designer.permissions).not.toContain('pos.sell')
  })

  it('seeds six weeks of order history with valid receipt codes', () => {
    const db = buildSeed()
    expect(db.orders.length).toBeGreaterThan(60)
    for (const o of db.orders) {
      expect(o.number).toMatch(/^ORD\d{12}$/)
      for (const p of o.payments) {
        if (p.mpesaReceipt) expect(p.mpesaReceipt).toMatch(/^[A-Z0-9]{10}$/)
      }
    }
    // Today has sales so dashboards look alive.
    const today = new Date().toISOString().slice(0, 10)
    expect(db.orders.some((o) => o.createdAt.startsWith(today))).toBe(true)
  })

  it('receiptCode generates 10-char Daraja-style codes', () => {
    expect(receiptCode()).toMatch(/^[A-Z0-9]{10}$/)
    expect(receiptCode()).not.toBe(receiptCode())
  })
})

describe('demo backend auth', () => {
  it('logs in with username/password', async () => {
    const res = await demoRequest<Any>('POST', '/api/v1/auth/login', { username: 'admin', password: 'admin123' })
    expect(res.token).toMatch(/^demo\.1\./)
    expect(res.user.roleName).toBe('Admin')
    expect(res.user.permissions.length).toBeGreaterThan(10)
  })

  it('rejects bad credentials', async () => {
    await expect(demoRequest<Any>('POST', '/api/v1/auth/login', { username: 'admin', password: 'nope' }))
      .rejects.toMatchObject({ status: 401 })
  })

  it('rejects requests without a token', async () => {
    await expect(demoRequest<Any>('GET', '/api/v1/products')).rejects.toMatchObject({ status: 401 })
  })

  it('pin quick-switch works and rejects wrong PINs', async () => {
    const users = await demoRequest<Any[]>('GET', '/api/v1/auth/pin-users')
    expect(users.length).toBeGreaterThanOrEqual(3)
    const res = await demoRequest<Any>('POST', '/api/v1/auth/pin', { userId: 2, pin: '2222' })
    expect(res.user.username).toBe('cashier')
    await expect(demoRequest<Any>('POST', '/api/v1/auth/pin', { userId: 2, pin: '9999' }))
      .rejects.toMatchObject({ status: 401 })
  })
})

describe('demo backend RBAC parity', () => {
  it('cashier cannot read users or settings', async () => {
    await login('cashier', 'cashier123')
    await expect(demoRequest<Any>('GET', '/api/v1/users')).rejects.toMatchObject({ status: 403 })
    await expect(demoRequest<Any>('GET', '/api/v1/settings')).rejects.toMatchObject({ status: 403 })
    // products/branding are read-accessible to any authenticated user (POS needs them)
    const products = await demoRequest<Any[]>('GET', '/api/v1/products')
    expect(products.length).toBeGreaterThan(10)
  })

  it('seeded logins must rotate before gated endpoints', async () => {
    const res = await demoRequest<{ token: string; user: Any }>('POST', '/api/v1/auth/login', { username: 'cashier', password: 'cashier123' })
    expect(res.user.mustRotate).toBe(true)
    localStorage.setItem('pos_token', res.token)
    await expect(demoRequest('GET', '/api/v1/products')).rejects.toMatchObject({ status: 403 })
    const me = await demoRequest<{ id: number }>('GET', '/api/v1/me')
    await demoRequest('PUT', `/api/v1/users/${me.id}/password`, { password: 'cashier123' })
    const products = await demoRequest<Any[]>('GET', '/api/v1/products')
    expect(products.length).toBeGreaterThan(10)
  })

  it('designer cannot checkout (no pos.sell)', async () => {
    await login('designer', 'designer123')
    await expect(
      demoRequest<Any>('POST', '/api/v1/orders/checkout', {
        items: [{ productId: 1, qty: 1 }],
        paymentMethod: 'cash',
      }),
    ).rejects.toMatchObject({ status: 403 })
  })
})

describe('demo checkout lifecycle', () => {
  it('cash sale: PAID immediately, stock decremented, VAT math matches cartTotals', async () => {
    await login('cashier', 'cashier123')
    const before = (await demoRequest<Any[]>('GET', '/api/v1/products')).find((p: Any) => p.id === 1)!.stockQty
    const o = await demoRequest<Any>('POST', '/api/v1/orders/checkout', {
      items: [{ productId: 1, qty: 2 }],
      paymentMethod: 'cash',
      customerName: 'Test Buyer',
    })
    expect(o.status).toBe('PAID')
    expect(o.number).toMatch(/^ORD\d{12}$/)
    expect(o.customerName).toBe('Test Buyer')
    // 2 × 550.00 = 1100.00 subtotal, VAT 16% incl → tax = round(110000*16/116)
    expect(o.subtotalCents).toBe(110000)
    expect(o.taxCents).toBe(Math.round((110000 * 16) / 116))
    expect(o.totalCents).toBe(110000)
    const after = (await demoRequest<Any[]>('GET', '/api/v1/products')).find((p: Any) => p.id === 1)!.stockQty
    expect(after).toBe(before - 2)
    expect(o.payments[0].method).toBe('cash')
    expect(o.payments[0].status).toBe('COMPLETED')
  })

  it('rejects insufficient stock', async () => {
    await login('cashier', 'cashier123')
    const products = await demoRequest<Any[]>('GET', '/api/v1/products')
    const capped = products.find((p: Any) => p.trackStock && p.stockQty < 1000 && p.stockQty > 0)!
    await expect(
      demoRequest<Any>('POST', '/api/v1/orders/checkout', {
        items: [{ productId: capped.id, qty: capped.stockQty + 1 }],
        paymentMethod: 'cash',
      }),
    ).rejects.toMatchObject({ status: 400 })
  })

  it('rejects price overrides without permission (cashier) but honors them for admin', async () => {
    await login('cashier', 'cashier123')
    await expect(
      demoRequest<Any>('POST', '/api/v1/orders/checkout', {
        items: [{ productId: 1, qty: 1, unitPriceCents: 100 }],
        paymentMethod: 'cash',
      }),
    ).rejects.toMatchObject({ status: 403 })
    await login('admin', 'admin123')
    const o = await demoRequest<Any>('POST', '/api/v1/orders/checkout', {
      items: [{ productId: 1, qty: 1, unitPriceCents: 40000 }],
      paymentMethod: 'cash',
    })
    expect(o.items[0].unitPriceCents).toBe(40000)
    expect(o.totalCents).toBe(40000)
  })

  it('M-Pesa STK: PENDING → simulated customer pays → PAID with receipt code', async () => {
    // Admin shrinks the simulated customer-PIN delay so the test runs fast.
    await login('admin', 'admin123')
    await demoRequest<Any>('PUT', '/api/v1/settings', { values: { mpesa_mock_delay_ms: '700' } })
    await login('cashier', 'cashier123')
    const o = await demoRequest<Any>('POST', '/api/v1/orders/checkout', {
      items: [{ productId: 2, qty: 1 }],
      paymentMethod: 'mpesa',
      paymentMode: 'auto',
      customerPhone: '254712345678',
    })
    expect(o.status).toBe('PENDING')
    expect(o.payments[0].checkoutRequestId).toMatch(/^ws_CO_/)
    expect(o.payments[0].status).toBe('PENDING')
    // The mock provider "pays" after the configured delay — wait for it,
    // then the poller sees PAID.
    await new Promise((r) => setTimeout(r, 1300))
    const done = await demoRequest<Any>('GET', `/api/v1/orders/${o.id}`)
    expect(done.status).toBe('PAID')
    expect(done.payments[0].mpesaReceipt).toMatch(/^[A-Z0-9]{10}$/)
  }, 8000)

  it('manual receipt: format, dedupe, and completion', async () => {
    await login('cashier', 'cashier123')
    // Manual-mode order waits for the code.
    const o = await demoRequest<Any>('POST', '/api/v1/orders/checkout', {
      items: [{ productId: 3, qty: 1 }],
      paymentMethod: 'mpesa',
      paymentMode: 'manual',
    })
    expect(o.status).toBe('PENDING')
    await expect(
      demoRequest<Any>('POST', `/api/v1/orders/${o.id}/manual`, { receiptCode: 'SHORT' }),
    ).rejects.toMatchObject({ status: 400 })
    const done = await demoRequest<Any>('POST', `/api/v1/orders/${o.id}/manual`, { receiptCode: 'NLJ7RT61SV' })
    expect(done.status).toBe('PAID')
    expect(done.payments[0].mpesaReceipt).toBe('NLJ7RT61SV')
    // Dedupe: the same code cannot confirm another order.
    const o2 = await demoRequest<Any>('POST', '/api/v1/orders/checkout', {
      items: [{ productId: 3, qty: 1 }],
      paymentMethod: 'mpesa',
      paymentMode: 'manual',
    })
    await expect(
      demoRequest<Any>('POST', `/api/v1/orders/${o2.id}/manual`, { receiptCode: 'NLJ7RT61SV' }),
    ).rejects.toMatchObject({ status: 409 })
  })

  it('void restores stock and records the reason', async () => {
    await login('cashier', 'cashier123')
    const before = (await demoRequest<Any[]>('GET', '/api/v1/products')).find((p: Any) => p.id === 4)!.stockQty
    const o = await demoRequest<Any>('POST', '/api/v1/orders/checkout', {
      items: [{ productId: 4, qty: 3 }],
      paymentMethod: 'cash',
    })
    expect((await demoRequest<Any[]>('GET', '/api/v1/products')).find((p: Any) => p.id === 4)!.stockQty).toBe(before - 3)
    const v = await demoRequest<Any>('POST', `/api/v1/orders/${o.id}/void`, { reason: 'wrong size' })
    expect(v.status).toBe('VOIDED')
    expect(v.voidReason).toBe('wrong size')
    expect((await demoRequest<Any[]>('GET', '/api/v1/products')).find((p: Any) => p.id === 4)!.stockQty).toBe(before)
  })

  it('sync replays queued checkouts idempotently by clientUuid', async () => {
    await login('cashier', 'cashier123')
    const tx = {
      items: [{ productId: 5, qty: 1 }],
      paymentMethod: 'cash',
      clientUuid: 'web-test-uuid-1',
    }
    const first = await demoRequest<Any[]>('POST', '/api/v1/sync', { transactions: [tx, tx] })
    expect(first).toHaveLength(2)
    expect(first[0].orderId).toBe(first[1].orderId) // same order — no double-charge
    expect(first[0].status).toBe('PAID')
  })
})

describe('demo tabs & credit parity', () => {
  it('tab lifecycle: charge, limit reject, pay, settle with loyalty', async () => {
    await login('cashier', 'cashier123')
    const customers = await demoRequest<Any[]>('GET', '/api/v1/customers?search=')
    expect(customers.length).toBeGreaterThanOrEqual(5)
    const kevin = customers.find((c: Any) => c.name === 'Kevin K.')!
    expect(kevin.creditLimitCents).toBe(100000)

    const o = await demoRequest<Any>('POST', '/api/v1/orders/checkout', {
      items: [{ productId: 1, qty: 1 }],
      paymentMethod: 'account',
      customerId: kevin.id,
    })
    expect(o.status).toBe('PENDING')
    expect(o.customerId).toBe(kevin.id)
    expect(o.payments[0].method).toBe('account')
    expect(o.payments[0].status).toBe('PENDING')

    let list = await demoRequest<Any[]>('GET', '/api/v1/customers?search=Kevin')
    expect(list[0].balanceCents).toBe(o.totalCents)

    // Over-limit rejected: 55000 owed + 2 x 55000 > 100000 limit.
    await expect(
      demoRequest<Any>('POST', '/api/v1/orders/checkout', {
        items: [{ productId: 1, qty: 2 }],
        paymentMethod: 'account',
        customerId: kevin.id,
      }),
    ).rejects.toMatchObject({ status: 409 })

    // Cash-only customer rejected.
    const brian = customers.find((c: Any) => c.name === 'Brian O.')!
    await expect(
      demoRequest<Any>('POST', '/api/v1/orders/checkout', {
        items: [{ productId: 1, qty: 1 }],
        paymentMethod: 'account',
        customerId: brian.id,
      }),
    ).rejects.toMatchObject({ status: 409 })

    // Walk-in overpayment rejected.
    await expect(
      demoRequest<Any>('POST', `/api/v1/customers/${kevin.id}/payments`, { amountCents: o.totalCents + 1 }),
    ).rejects.toMatchObject({ status: 409 })

    // Settle in cash: PAID, balance zero, loyalty earned (1 pt / 100 KES).
    const settled = await demoRequest<Any>('POST', `/api/v1/orders/${o.id}/settle`, { method: 'cash' })
    expect(settled.status).toBe('PAID')
    list = await demoRequest<Any[]>('GET', '/api/v1/customers?search=Kevin')
    expect(list[0].balanceCents).toBe(0)
    expect(list[0].loyaltyPoints).toBe(Math.floor(o.totalCents / 10000))

    const ledger = await demoRequest<Any[]>('GET', `/api/v1/customers/${kevin.id}/ledger`)
    expect(ledger.map((e: Any) => e.kind)).toEqual(expect.arrayContaining(['charge', 'payment', 'loyalty']))
  })

  it('voiding a pending tab reverses the charge', async () => {
    await login('cashier', 'cashier123')
    const faith = (await demoRequest<Any[]>('GET', '/api/v1/customers?search=Faith'))[0]
    const o = await demoRequest<Any>('POST', '/api/v1/orders/checkout', {
      items: [{ productId: 1, qty: 1 }],
      paymentMethod: 'account',
      customerId: faith.id,
    })
    expect(o.status).toBe('PENDING')
    await demoRequest<Any>('POST', `/api/v1/orders/${o.id}/void`, { reason: 'test' })
    const list = await demoRequest<Any[]>('GET', '/api/v1/customers?search=Faith')
    expect(list[0].balanceCents).toBe(0)
  })
})

describe('demo printer parity', () => {
  it('kick needs the printer permission and reports no target', async () => {
    await login('cashier', 'cashier123')
    await expect(demoRequest('POST', '/api/v1/printer/kick')).rejects.toMatchObject({ status: 403 })
    await login('admin', 'admin123')
    await expect(demoRequest('POST', '/api/v1/printer/kick')).rejects.toMatchObject({ status: 422 })
  })
})

describe('demo off-site settings parity', () => {
  it('off-site keys save and secrets mask', async () => {
    await login('admin', 'admin123')
    await demoRequest('PUT', '/api/v1/settings', {
      values: { offsite_enabled: 'true', offsite_bucket: 'shop-backups', offsite_secret_key: 's3cr3t', offsite_passphrase: 'correct horse' },
    })
    const snap = await demoRequest<Any>('GET', '/api/v1/settings')
    expect(snap.offsite_enabled).toBe('true')
    expect(snap.offsite_secret_key).toBe('__SET__')
    expect(snap.offsite_passphrase).toBe('__SET__')
    const st = await demoRequest<Any>('GET', '/api/v1/system/offsite')
    expect(st.enabled).toBe(false)
    expect(st.pending).toBe(0)
  })
})

describe('demo adversarial inputs', () => {
  const payloads = [
    `' OR '1'='1`,
    `'; DROP TABLE users; --`,
    `" OR ""="`,
    `<script>alert(1)</script>`,
    `=HYPERLINK("https://evil.example","click")`,
  ]

  it('search boxes swallow injection strings without errors or leaks', async () => {
    await login('admin', 'admin123')
    for (const p of payloads) {
      const q = encodeURIComponent(p)
      for (const path of [`/api/v1/products?search=${q}`, `/api/v1/customers?search=${q}`, `/api/v1/suppliers?search=${q}`, `/api/v1/orders?search=${q}`]) {
        const res = await demoRequest<Any[]>('GET', path)
        expect(Array.isArray(res)).toBe(true)
      }
    }
  })

  it('username rules reject scripts, shorts, and oversize passwords', async () => {
    await login('admin', 'admin123')
    await expect(
      demoRequest('POST', '/api/v1/users', { username: '<script>alert(1)</script>', password: 'longenough123', roleId: 2 }),
    ).rejects.toMatchObject({ status: 400 })
    await expect(
      demoRequest('POST', '/api/v1/users', { username: 'ab', password: 'longenough123', roleId: 2 }),
    ).rejects.toMatchObject({ status: 400 })
    await expect(
      demoRequest('POST', '/api/v1/users', { username: 'fine_name-1.2', password: 'x'.repeat(200), roleId: 2 }),
    ).rejects.toMatchObject({ status: 400 })
  })

  it('script payloads store verbatim and never execute', async () => {
    await login('admin', 'admin123')
    const payload = `<script>alert(1)</script><img src=x onerror=alert(2)>`
    const c = await demoRequest<Any>('POST', '/api/v1/customers', { name: payload, creditLimitCents: 1000 })
    expect(c.name).toBe(payload)
    const list = await demoRequest<Any[]>('GET', '/api/v1/customers?search=script')
    expect(list.some((x: Any) => x.name === payload)).toBe(true)
  })
})

describe('demo suppliers & stock-in parity', () => {
  it('PO receive posts stock with weighted-average cost', async () => {
    await login('admin', 'admin123')
    const sup = await demoRequest<Any>('POST', '/api/v1/suppliers', { name: 'Test Wholesaler' })
    expect(sup.id).toBeGreaterThan(0)
    const before = (await demoRequest<Any[]>('GET', '/api/v1/products')).find((p: Any) => p.id === 1)!
    const po = await demoRequest<Any>('POST', '/api/v1/purchase-orders', {
      supplierId: sup.id,
      items: [{ productId: 1, qty: 10, costCents: 40000 }],
    })
    expect(po.status).toBe('PENDING')
    expect(po.number).toMatch(/^PO\d{12}$/)
    const received = await demoRequest<Any>('POST', `/api/v1/purchase-orders/${po.id}/receive`, {})
    expect(received.status).toBe('RECEIVED')
    const after = (await demoRequest<Any[]>('GET', '/api/v1/products')).find((p: Any) => p.id === 1)!
    expect(after.stockQty).toBe(before.stockQty + 10)
    const newStock = before.stockQty + 10
    expect(after.costCents).toBe(Math.floor((before.stockQty * before.costCents + 10 * 40000 + newStock / 2) / newStock))
    await expect(
      demoRequest('POST', `/api/v1/purchase-orders/${po.id}/receive`, {}),
    ).rejects.toMatchObject({ status: 409 })
  })

  it('stock take counts variance and applies', async () => {
    await login('admin', 'admin123')
    const before = (await demoRequest<Any[]>('GET', '/api/v1/products')).find((p: Any) => p.id === 1)!
    const take = await demoRequest<Any>('POST', '/api/v1/stock-takes', { productIds: [1] })
    expect(take.status).toBe('OPEN')
    expect(take.items[0].expectedQty).toBe(before.stockQty)
    const counted = await demoRequest<Any>('POST', `/api/v1/stock-takes/${take.id}/count`, { counts: { '1': before.stockQty + 5 } })
    expect(counted.items[0].countedQty).toBe(before.stockQty + 5)
    const applied = await demoRequest<Any>('POST', `/api/v1/stock-takes/${take.id}/apply`, {})
    expect(applied.status).toBe('APPLIED')
    const after = (await demoRequest<Any[]>('GET', '/api/v1/products')).find((p: Any) => p.id === 1)!
    expect(after.stockQty).toBe(before.stockQty + 5)
  })
})

describe('demo settings + reports parity', () => {
  it('settings save tolerates the jwt_secret masked echo (regression parity)', async () => {
    await login('admin', 'admin123')
    // Old frontend bug: snapshot echoed read-only secrets back on save.
    const snap = await demoRequest<Any>('PUT', '/api/v1/settings', {
      values: { jwt_secret: '__SET__', store_name: 'Renamed Shop' },
    })
    expect(snap.store_name).toBe('Renamed Shop')
    expect(snap.jwt_secret).toBeUndefined()
    // A real attempt to write jwt_secret is still rejected.
    await expect(
      demoRequest<Any>('PUT', '/api/v1/settings', { values: { jwt_secret: 'hacked' } }),
    ).rejects.toMatchObject({ status: 400 })
  })

  it('settings snapshot masks Daraja secrets', async () => {
    await login('admin', 'admin123')
    await demoRequest<Any>('PUT', '/api/v1/settings', { values: { mpesa_consumer_secret: 'topsecret' } })
    const snap = await demoRequest<Any>('GET', '/api/v1/settings')
    expect(snap.mpesa_consumer_secret).toBe('__SET__')
    // Masked echo keeps it.
    const snap2 = await demoRequest<Any>('PUT', '/api/v1/settings', { values: { mpesa_consumer_secret: '__SET__' } })
    expect(snap2.mpesa_consumer_secret).toBe('__SET__')
  })

  it('monthly KRA summary: nett = gross - vat and full month series', async () => {
    await login('admin', 'admin123')
    const month = new Date().toISOString().slice(0, 7)
    const m = await demoRequest<Any>('GET', `/api/v1/reports/monthly?month=${month}`)
    expect(m.month).toBe(month)
    expect(m.grossCents).toBeGreaterThan(0)
    expect(m.vatCents).toBeGreaterThan(0)
    expect(m.nettCents).toBe(m.grossCents - m.vatCents)
    const [y, mo] = month.split('-').map(Number)
    expect(m.series).toHaveLength(new Date(y, mo, 0).getDate())
    await expect(demoRequest<Any>('GET', '/api/v1/reports/monthly?month=nope')).rejects.toMatchObject({ status: 400 })
  })

  it('daily summary reflects a fresh cash sale', async () => {
    await login('cashier', 'cashier123')
    await demoRequest<Any>('POST', '/api/v1/orders/checkout', {
      items: [{ productId: 1, qty: 1 }],
      paymentMethod: 'cash',
    })
    const today = new Date().toISOString().slice(0, 10)
    const d = await demoRequest<Any>('GET', `/api/v1/reports/daily?date=${today}`)
    expect(d.ordersPaid).toBeGreaterThanOrEqual(1)
    expect(d.cashCents).toBeGreaterThanOrEqual(55000)
    expect(d.series).toHaveLength(7)
  })

  it('users CRUD: admin resets passwords and PINs (the admin workflow)', async () => {
    await login('admin', 'admin123')
    const created = await demoRequest<Any>('POST', '/api/v1/users', {
      username: 'grace', fullName: 'Grace W.', password: 'grace123', roleId: 2, pin: '4321',
    })
    expect(created.username).toBe('grace')
    await expect(
      demoRequest<Any>('PUT', '/api/v1/users/3/password', { password: 'newpass' }),
    ).resolves.toMatchObject({ updated: true })
    // Admin-created users rotate on first login; resetting someone else
    // re-arms rotation too.
    const users = await demoRequest<Any[]>('GET', '/api/v1/users')
    expect(users.find((u: Any) => u.username === 'grace')!.mustRotate).toBe(true)
    const graceLogin = await demoRequest<{ user: Any }>('POST', '/api/v1/auth/login', { username: 'grace', password: 'grace123' })
    expect(graceLogin.user.mustRotate).toBe(true)
    // Duplicate username rejected.
    await expect(
      demoRequest<Any>('POST', '/api/v1/users', { username: 'grace', password: '123456', roleId: 2 }),
    ).rejects.toMatchObject({ status: 409 })
  })

  it('stock adjustment lands in the audit log', async () => {
    await login('admin', 'admin123')
    const p = await demoRequest<Any>('POST', '/api/v1/products/1/adjust-stock', { delta: 20, reason: 'received from supplier' })
    expect(p.stockQty).toBeGreaterThanOrEqual(20)
    const audit = await demoRequest<Any[]>('GET', '/api/v1/audit')
    expect(audit[0].action).toBe('STOCK_ADJUSTED')
    expect(audit[0].details).toContain('+20')
  })

  it('CSV export and template download', async () => {
    await login('admin', 'admin123')
    const csvText = await import('../src/demo/backend').then((m) => m.demoRaw('/api/v1/products/export'))
    expect(csvText).toContain('sku,barcode,name,category,price,cost,stock,track_stock,active')
    expect(csvText.split('\n').length).toBeGreaterThan(10)
  })
})
