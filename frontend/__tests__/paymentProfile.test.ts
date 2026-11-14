import { describe, expect, it } from 'vitest'
import { describeProfile, PaymentProfile } from '../src/components/PaymentProfilePanel'

const base: PaymentProfile = {
  stkMode: 'manual', stkAvailable: false, tillNumber: '', paybillNumber: '', displayName: 'Mama Pima',
  orgShortcode: '', ownRequested: false, ownVerified: false, hasPasskey: false, linked: true,
}

describe('where customers pay (v1.9 per-shop payments)', () => {
  it('a new shop pays on its own till number, never the platform till', () => {
    const d = describeProfile({ ...base, tillNumber: '5123456' })
    expect(d.label).toBe('Customers pay your till number')
    expect(d.detail).toContain('till 5123456')
    expect(d.detail).not.toMatch(/LedgerPOS KCB till/)
  })
  it('asks for a number when none is set', () => {
    expect(describeProfile(base).detail).toMatch(/Add your till or paybill/)
  })
  it('own short code is pending until verified', () => {
    const d = describeProfile({ ...base, stkMode: 'own', orgShortcode: '600111' })
    expect(d.tone).toBe('pending')
    expect(d.detail).toMatch(/Until then customers pay your till number/)
  })
  it('verified own short code names the shop and the code', () => {
    const d = describeProfile({ ...base, stkMode: 'own', orgShortcode: '600111', ownVerified: true, stkAvailable: true })
    expect(d.tone).toBe('paid')
    expect(d.detail).toContain('Mama Pima')
    expect(d.detail).toContain('600111')
  })
  it('only the founding store says the money goes to the LedgerPOS till', () => {
    expect(describeProfile({ ...base, stkMode: 'shared', stkAvailable: true }).detail).toMatch(/LedgerPOS KCB till/)
  })
  it('an unconnected shop is told how to connect', () => {
    expect(describeProfile({ ...base, linked: false }).label).toMatch(/Not connected/)
  })
})
