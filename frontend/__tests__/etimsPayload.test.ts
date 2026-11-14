// The eTIMS invoice builder lives with the edge function (pure TS); these
// tests pin the arithmetic KRA validates to the cent.
import { describe, expect, test } from 'vitest'
import {
  buildSale, inclusiveTax, itemCode, kenyaStamp, qtyUnitFor, receiptQrUrl, spreadDiscount, SaleInput,
} from '../../supabase/functions/etims/payload'

const base = (over: Partial<SaleInput> = {}): SaleInput => ({
  kind: 'S', invcNo: 7, tin: 'P051234567X', bhfId: '00', traderInvoiceNo: 'ORD202611140001',
  confirmedAt: '20261114143005', payment: 'cash', cashierId: '3', cashierName: 'Amina',
  lines: [
    { itemCd: 'KE2NTU0000001', itemClsCd: '5020230100', name: 'Tee', qtyUnitCd: 'U', qtyMilli: 2000, lineTotalCents: 116000, taxTy: 'B' },
  ],
  ...over,
})

const sum = (xs: number[]) => Math.round(xs.reduce((a, b) => a + b, 0) * 100) / 100

describe('eTIMS sale payload', () => {
  test('VAT-inclusive 16% line: tax = amount x 16/116, totals tie', () => {
    const p = buildSale(base())
    expect(p.itemList[0]).toMatchObject({ qty: 2, prc: 580, splyAmt: 1160, dcAmt: 0, taxblAmt: 1160, taxAmt: 160, totAmt: 1160, taxTyCd: 'B' })
    expect(p).toMatchObject({ taxblAmtB: 1160, taxAmtB: 160, totTaxblAmt: 1160, totTaxAmt: 160, totAmt: 1160, taxRtB: 16 })
    expect(p).toMatchObject({ rcptTyCd: 'S', salesTyCd: 'N', salesSttsCd: '02', pmtTyCd: '01', salesDt: '20261114', invcNo: 7, orgInvcNo: 0 })
  })

  test('mixed tax types and many odd prices: every header total is the sum of the rounded lines', () => {
    const lines = [33333, 1999, 12345, 777, 50001, 9].map((c, i) => ({
      itemCd: `KE2NTU000000${i}`, itemClsCd: '5020230100', name: `I${i}`, qtyUnitCd: 'U',
      qtyMilli: 1000, lineTotalCents: c, taxTy: (['A', 'B', 'C', 'D', 'E', 'B'] as const)[i],
    }))
    const p = buildSale(base({ lines, discountCents: 1234 }))
    expect(sum(p.itemList.map((l) => l.dcAmt))).toBe(12.34)
    expect(p.totTaxAmt).toBe(sum(p.itemList.map((l) => l.taxAmt)))
    expect(p.totTaxblAmt).toBe(sum(p.itemList.map((l) => l.taxblAmt)))
    expect(p.totAmt).toBe(sum(p.itemList.map((l) => l.totAmt)))
    expect(p.taxAmtB).toBe(sum(p.itemList.filter((l) => l.taxTyCd === 'B').map((l) => l.taxAmt)))
    expect(p.taxAmtA + p.taxAmtC + p.taxAmtD).toBe(0)
    for (const l of p.itemList) expect(Math.round((l.splyAmt - l.dcAmt) * 100)).toBe(Math.round(l.taxblAmt * 100))
    // the invoice total is what the customer paid
    expect(p.totAmt).toBe((33333 + 1999 + 12345 + 777 + 50001 + 9 - 1234) / 100)
  })

  test('weighed quantity that does not fit 2 decimals goes as one priced line', () => {
    const p = buildSale(base({ lines: [{ itemCd: 'KE2NTKG0000001', itemClsCd: '5020230100', name: 'Sugar', qtyUnitCd: 'KG', qtyMilli: 125, lineTotalCents: 2256, taxTy: 'B' }] }))
    expect(p.itemList[0]).toMatchObject({ qty: 1, prc: 22.56, splyAmt: 22.56, itemNm: 'Sugar (0.125 KG)' })
  })

  test('half kilo keeps its real quantity', () => {
    const p = buildSale(base({ lines: [{ itemCd: 'KE2NTKG0000001', itemClsCd: '5020230100', name: 'Rice', qtyUnitCd: 'KG', qtyMilli: 500, lineTotalCents: 9000, taxTy: 'B' }] }))
    expect(p.itemList[0]).toMatchObject({ qty: 0.5, prc: 180, splyAmt: 90 })
  })

  test('credit note carries the original invoice number and a reason', () => {
    const p = buildSale(base({ kind: 'R', invcNo: 9, orgInvcNo: 7, refundReason: '06' }))
    expect(p).toMatchObject({ rcptTyCd: 'R', orgInvcNo: 7, rfdRsnCd: '06', rfdDt: '20261114143005' })
  })

  test('payment codes: M-Pesa is mobile money, tab is credit', () => {
    expect(buildSale(base({ payment: 'mpesa' })).pmtTyCd).toBe('06')
    expect(buildSale(base({ payment: 'account' })).pmtTyCd).toBe('02')
  })

  test('buyer PIN is upper-cased onto the invoice and the receipt', () => {
    const p = buildSale(base({ customerPin: 'p051111111z', customerName: 'Acme Ltd' }))
    expect(p.custTin).toBe('P051111111Z')
    expect(p.receipt.custTin).toBe('P051111111Z')
  })
})

describe('eTIMS helpers', () => {
  test('inclusive tax rounds half-up', () => {
    expect(inclusiveTax(116000, 16)).toBe(16000)
    expect(inclusiveTax(100, 16)).toBe(14) // 13.79 -> 14
    expect(inclusiveTax(10800, 8)).toBe(800)
    expect(inclusiveTax(5000, 0)).toBe(0)
  })
  test('discount spread adds up exactly', () => {
    const parts = spreadDiscount([100, 200, 300], 101)
    expect(parts.reduce((a, b) => a + b, 0)).toBe(101)
  })
  test('item code follows the KRA rule', () => {
    expect(itemCode(12, { qtyUnitCd: 'U' })).toBe('KE2NTU0000012')
    expect(itemCode(1, { qtyUnitCd: 'U', service: true })).toBe('KE3NTU0000001')
  })
  test('units map to KRA quantity codes', () => {
    expect(qtyUnitFor('kg')).toBe('KG')
    expect(qtyUnitFor('')).toBe('U')
    expect(qtyUnitFor('dozen')).toBe('DZ')
  })
  test('Kenya time stamp and QR url', () => {
    expect(kenyaStamp(new Date('2026-11-14T11:30:05Z'))).toBe('20261114143005')
    expect(receiptQrUrl('sandbox', 'P051234567X', '00', 'ABC')).toContain('etims-sbx.kra.go.ke')
    expect(receiptQrUrl('production', 'P051234567X', '00', 'A B')).toContain('Data=P051234567X00A%20B')
  })
})

describe('eTIMS payload: v1.11 per-line figures from the till', () => {
  test('per-line discount and VAT are used as given (receipt = KRA)', () => {
    const p = buildSale(base({
      lines: [
        { itemCd: 'A1', itemClsCd: '1', name: 'Tee', qtyUnitCd: 'U', qtyMilli: 1000, lineTotalCents: 116000, taxTy: 'B', discountCents: 1000, taxCents: 15862 },
        { itemCd: 'A2', itemClsCd: '1', name: 'Maize flour', qtyUnitCd: 'U', qtyMilli: 1000, lineTotalCents: 20000, taxTy: 'A', discountCents: 172, taxCents: 0 },
      ],
    }))
    expect(p.itemList[0]).toMatchObject({ dcAmt: 10, taxblAmt: 1150, taxAmt: 158.62 })
    expect(p.itemList[1]).toMatchObject({ dcAmt: 1.72, taxblAmt: 198.28, taxAmt: 0, taxTyCd: 'A' })
    expect(p).toMatchObject({ taxAmtB: 158.62, taxAmtA: 0, totTaxAmt: 158.62, totAmt: 1348.28 })
  })
  test('a VAT figure more than a cent off the formula is not trusted', () => {
    const p = buildSale(base({ lines: [{ itemCd: 'A1', itemClsCd: '1', name: 'Tee', qtyUnitCd: 'U', qtyMilli: 1000, lineTotalCents: 116000, taxTy: 'B', discountCents: 0, taxCents: 15000 }] }))
    expect(p.itemList[0].taxAmt).toBe(160)
  })
  test('exempt line never carries VAT', () => {
    const p = buildSale(base({ lines: [{ itemCd: 'A2', itemClsCd: '1', name: 'Milk', qtyUnitCd: 'U', qtyMilli: 1000, lineTotalCents: 6000, taxTy: 'A', discountCents: 0, taxCents: 1 }] }))
    expect(p.itemList[0].taxAmt).toBe(0)
  })
})
