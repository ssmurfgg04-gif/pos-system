// @vitest-environment jsdom
// v1.6.3 — expenses & returns: the demo backend's parity plus the pure
// net-sales card math the Reports page renders ("Money out / money back").

import { beforeEach, describe, expect, it } from 'vitest'
import { demoRequest, resetDemo } from '../src/demo/backend'
import { netSalesOf } from '../src/pages/Reports'

type Any = Record<string, any>

async function login(username: string, password: string): Promise<void> {
  const res = await demoRequest<{ token: string }>('POST', '/api/v1/auth/login', { username, password })
  localStorage.setItem('pos_token', res.token)
}

beforeEach(() => {
  localStorage.clear()
  resetDemo()
})

describe('net sales card math (Reports → Money out / money back)', () => {
  it('net = sales − returns when an older backend omits netSalesCents', () => {
    expect(netSalesOf({ salesCents: 100000, returnsCents: 25000 })).toBe(75000)
    expect(netSalesOf({ salesCents: 100000 })).toBe(100000)
    expect(netSalesOf({ salesCents: 50000, returnsCents: 0 })).toBe(50000)
  })

  it('prefers the server figure when present', () => {
    expect(netSalesOf({ salesCents: 100, returnsCents: 90, netSalesCents: 10 })).toBe(10)
  })
})

describe('demo expenses (v1.6.3 parity)', () => {
  it('seeds the 8-type catalog and a few plausible booked expenses', async () => {
    await login('admin', '0000')
    const types = await demoRequest<Any[]>('GET', '/api/v1/expenses/types')
    expect(types.map((t: Any) => t.name)).toEqual([
      'Rent', 'Electricity', 'Internet / Airtime', 'Transport', 'Salaries', 'Supplies', 'Maintenance', 'Other',
    ])
    const rows = await demoRequest<Any[]>('GET', '/api/v1/expenses')
    expect(rows.length).toBeGreaterThanOrEqual(3)
    expect(rows[0].number).toMatch(/^EXP/)
    expect(rows[0].createdByName).toBeTruthy()
    for (const r of rows) expect(r.amountCents).toBeGreaterThan(0)
  })

  it('records an expense (integer cents) and it lands in the daily report', async () => {
    await login('admin', '0000')
    const today = new Date().toISOString().slice(0, 10)
    const created = await demoRequest<Any>('POST', '/api/v1/expenses', {
      expenseTypeId: 2, amountCents: 12550, method: 'CASH', reference: 'R-1', note: 'drinking water', spentOn: today,
    })
    expect(created.amountCents).toBe(12550)
    expect(created.typeName).toBe('Electricity')
    const rows = await demoRequest<Any[]>('GET', `/api/v1/expenses?date=${today}`)
    expect(rows.some((r: Any) => r.id === created.id)).toBe(true)
    const daily = await demoRequest<Any>('GET', `/api/v1/reports/daily?date=${today}`)
    expect(daily.expensesCents).toBeGreaterThanOrEqual(12550)
    expect(daily.netSalesCents).toBe(daily.salesCents - daily.returnsCents)
    // Server parity: a zero/negative amount is rejected.
    await expect(demoRequest<Any>('POST', '/api/v1/expenses', {
      expenseTypeId: 2, amountCents: 0, method: 'CASH', spentOn: today,
    })).rejects.toMatchObject({ status: 400 })
  })

  it('cashier cannot record expenses (expenses.manage gate)', async () => {
    await login('cashier', '0000')
    await expect(demoRequest<Any>('POST', '/api/v1/expenses', {
      expenseTypeId: 1, amountCents: 100, method: 'CASH', spentOn: '2026-01-01',
    })).rejects.toMatchObject({ status: 403 })
  })
})

describe('demo returns (v1.6.3 parity)', () => {
  it('refunds the sold lines, appends the doc, guards over-returns', async () => {
    await login('admin', '0000')
    const paid = await demoRequest<Any[]>('GET', '/api/v1/orders?status=PAID&limit=5')
    expect(paid.length).toBeGreaterThan(0)
    const o = paid[0]
    const line = o.items[0]
    const r = await demoRequest<Any>('POST', '/api/v1/returns', {
      orderId: o.id,
      lines: [{ orderItemId: line.id, qty: 1 }],
      method: 'CASH',
      reason: 'wrong size',
    })
    expect(r.number).toMatch(/^RET/)
    expect(r.orderNumber).toBe(o.number)
    expect(r.totalCents).toBe(line.unitPriceCents)
    expect(r.items[0].qty).toBe(1)
    const list = await demoRequest<Any[]>('GET', '/api/v1/returns')
    expect(list.some((x: Any) => x.id === r.id)).toBe(true)
    // Over-return (server parity: a friendly 4xx, not a 500).
    await expect(demoRequest<Any>('POST', '/api/v1/returns', {
      orderId: o.id,
      lines: [{ orderItemId: line.id, qty: 999 }],
      method: 'CASH',
    })).rejects.toMatchObject({ status: 422 })
  })

  it('STORE_CREDIT refunds need an active customer and grow their wallet', async () => {
    await login('admin', '0000')
    const paid = await demoRequest<Any[]>('GET', '/api/v1/orders?status=PAID&limit=5')
    const o = paid[0]
    await expect(demoRequest<Any>('POST', '/api/v1/returns', {
      orderId: o.id, lines: [{ orderItemId: o.items[0].id, qty: 1 }], method: 'STORE_CREDIT',
    })).rejects.toMatchObject({ status: 400 })
    const customers = await demoRequest<Any[]>('GET', '/api/v1/customers')
    const c = customers.find((x: Any) => x.active)!
    const before = Number(c.storeCreditCents) || 0
    const r = await demoRequest<Any>('POST', '/api/v1/returns', {
      orderId: o.id,
      lines: [{ orderItemId: o.items[0].id, qty: 1 }],
      method: 'STORE_CREDIT',
      customerId: c.id,
      reason: 'changed mind',
    })
    const after = (await demoRequest<Any[]>('GET', '/api/v1/customers')).find((x: Any) => x.id === c.id)
    expect(Number(after.storeCreditCents)).toBe(before + r.totalCents)
  })
})

describe('demo stock ledger stub (v1.6.3)', () => {
  it('movement endpoints answer an empty list, not an error', async () => {
    await login('admin', '0000')
    await expect(demoRequest<Any[]>('GET', '/api/v1/stock-movements')).resolves.toEqual([])
    await expect(demoRequest<Any[]>('GET', '/api/v1/products/1/movements')).resolves.toEqual([])
  })
})
