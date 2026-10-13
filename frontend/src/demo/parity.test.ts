// Demo parity tests (review 8.12): the web demo is a deliberately reduced
// sandbox, but its permission VOCABULARY must be the server's — pinned to
// the same generated JSON the Go test gates on.
import { describe, expect, it } from 'vitest'
import catalog from './permissions.catalog.json'
import { PERMISSION_CATALOG } from './seed'
import { demoRequest } from './backend'

describe('demo permission parity', () => {
  it('uses the generated catalog verbatim', () => {
    expect(PERMISSION_CATALOG).toEqual(catalog)
  })

  it('covers the server keys the review found missing', () => {
    const keys = PERMISSION_CATALOG.map((p) => p.key)
    for (const k of [
      'quotes.view', 'quotes.manage', 'orders.assign', 'orders.perform',
      'orders.notify', 'loyalty.redeem', 'credit.manage', 'pos.hold',
      'payments.apply_discount',
    ]) {
      expect(keys).toContain(k)
    }
  })

  it('demo roles only grant catalog permissions', async () => {
    const keys = new Set(PERMISSION_CATALOG.map((p) => p.key))
    const roles = await demoRequest<{ name: string; permissions: string[] }[]>('GET', '/roles')
    for (const role of roles) {
      for (const perm of role.permissions) {
        expect(keys.has(perm), `${role.name} grants unknown permission ${perm}`).toBe(true)
      }
    }
  })

  it('Admin holds the full catalog', async () => {
    const roles = await demoRequest<{ name: string; permissions: string[] }[]>('GET', '/roles')
    const admin = roles.find((r) => r.name === 'Admin')!
    expect([...admin.permissions].sort()).toEqual([...catalog.map((p) => p.key)].sort())
  })
})
