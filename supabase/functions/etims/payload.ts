// payload.ts — builds the KRA eTIMS OSCU /saveTrnsSalesOsdc request from a
// LedgerPOS sale. Pure (no Deno / network): unit-tested from the frontend
// suite (frontend/__tests__/etimsPayload.test.ts).
//
// Money is handled in integer CENTS end to end and only turned into
// 2-decimal numbers at the very end, so every header total is the exact
// sum of the rounded lines (KRA rejects cent-level mismatches).
//
// Amounts are VAT-INCLUSIVE, as eTIMS expects: per line
//   splyAmt  = qty x prc
//   taxblAmt = splyAmt - dcAmt            (still includes the VAT)
//   taxAmt   = taxblAmt x rate / (100 + rate), half-up to the cent
//   totAmt   = taxblAmt

export type TaxType = 'A' | 'B' | 'C' | 'D' | 'E'
export const TAX_RATES: Record<TaxType, number> = { A: 0, B: 16, C: 0, D: 0, E: 8 }

export interface SaleLine {
  itemCd: string
  itemClsCd: string
  name: string
  barcode?: string
  qtyUnitCd: string
  pkgUnitCd?: string
  /** quantity in thousandths of a unit (LedgerPOS milli-units) */
  qtyMilli: number
  /** what the customer pays for this line, VAT included, before any
   *  order-level discount */
  lineTotalCents: number
  taxTy: TaxType
  /** v1.11: this line's share of the discount (when the till already
   *  spread it); when ANY line sets it, no order-level spreading happens */
  discountCents?: number
  /** v1.11: the line's VAT as the till computed and printed it; used when
   *  it is within a cent of the exact formula, so KRA and the receipt agree */
  taxCents?: number
}

export interface SaleInput {
  kind: 'S' | 'R'
  invcNo: number
  orgInvcNo?: number
  tin: string
  bhfId: string
  traderInvoiceNo: string
  /** yyyyMMddHHmmss in Kenya time */
  confirmedAt: string
  customerPin?: string
  customerName?: string
  customerPhone?: string
  /** LedgerPOS payment method: cash | mpesa | account | credit | ... */
  payment: string
  /** order-level discount (VAT included), spread over the lines */
  discountCents?: number
  cashierId: string
  cashierName: string
  tradeName?: string
  /** credit-note reason code (01-06), R only */
  refundReason?: string
  lines: SaleLine[]
}

const PAYMENT: Record<string, string> = {
  cash: '01', account: '02', tab: '02', credit: '07', mpesa: '06', card: '05', paystack: '05',
}

/** cents → number with 2 decimals (JSON) */
const m = (c: number) => Math.round(c) / 100

/** half-up integer division for non-negative a, positive b */
function divHalfUp(a: number, b: number): number {
  return Math.floor((2 * a + b) / (2 * b))
}

/** VAT inside an inclusive amount, half-up to the cent. */
export function inclusiveTax(amountCents: number, rate: number): number {
  if (rate <= 0 || amountCents <= 0) return 0
  return divHalfUp(amountCents * rate, 100 + rate)
}

/** Spread an order discount over lines by value: integer largest
 *  remainder, ties to the earlier line, so the pieces add up to exactly
 *  the discount. Same rule as the till (internal/money.Spread). */
export function spreadDiscount(lineTotals: number[], discount: number): number[] {
  const out = lineTotals.map(() => 0)
  const total = lineTotals.reduce((a, w) => a + (w > 0 ? w : 0), 0)
  if (discount <= 0 || total <= 0) return out
  const amt = BigInt(Math.min(discount, total))
  const bt = BigInt(total)
  const rems: { r: bigint; i: number }[] = []
  let given = 0n
  lineTotals.forEach((w, i) => {
    if (w <= 0) { rems.push({ r: -1n, i }); return }
    const p = BigInt(w) * amt
    out[i] = Number(p / bt)
    given += p / bt
    rems.push({ r: p % bt, i })
  })
  rems.sort((a, b) => (a.r === b.r ? a.i - b.i : a.r > b.r ? -1 : 1))
  for (let k = 0; given < amt && k < rems.length; k++) {
    if (rems[k].r < 0n) continue
    out[rems[k].i]++
    given++
  }
  return out
}

/** eTIMS quantities carry 2 decimals; LedgerPOS carries 3. A quantity that
 *  doesn't fit (0.125 kg) is sent as ONE line at the line amount, with the
 *  real quantity in the item name, so the money is exact. */
function qtyAndPrice(l: SaleLine): { qty: number; prcCents: number; name: string } {
  if (l.qtyMilli % 10 === 0) {
    const qty = l.qtyMilli / 1000
    // unit price = line / qty; exact when the till priced per unit
    const prc = Math.round((l.lineTotalCents * 1000) / l.qtyMilli)
    if (Math.round((prc * l.qtyMilli) / 1000) === l.lineTotalCents) return { qty, prcCents: prc, name: l.name }
  }
  const shown = (l.qtyMilli / 1000).toString()
  return { qty: 1, prcCents: l.lineTotalCents, name: `${l.name} (${shown} ${l.qtyUnitCd})`.slice(0, 200) }
}

export function buildSale(input: SaleInput) {
  if (input.lines.length === 0) throw new Error('a sale needs at least one line')
  const perLine = input.lines.some((l) => l.discountCents !== undefined)
  const disc = perLine
    ? input.lines.map((l) => Math.max(0, Math.min(l.lineTotalCents, Math.round(l.discountCents ?? 0))))
    : spreadDiscount(input.lines.map((l) => l.lineTotalCents), input.discountCents ?? 0)
  const taxbl: Record<TaxType, number> = { A: 0, B: 0, C: 0, D: 0, E: 0 }
  const tax: Record<TaxType, number> = { A: 0, B: 0, C: 0, D: 0, E: 0 }
  const itemList = input.lines.map((l, i) => {
    const { qty, prcCents, name } = qtyAndPrice(l)
    const sply = l.lineTotalCents
    const dc = disc[i]
    const taxable = sply - dc
    const exact = inclusiveTax(taxable, TAX_RATES[l.taxTy])
    const given = l.taxCents
    const t = given !== undefined && Number.isFinite(given) && Math.abs(given - exact) <= 1 && (TAX_RATES[l.taxTy] > 0 || given === 0)
      ? Math.round(given)
      : exact
    taxbl[l.taxTy] += taxable
    tax[l.taxTy] += t
    return {
      itemSeq: i + 1,
      itemCd: l.itemCd,
      itemClsCd: l.itemClsCd,
      itemNm: name,
      bcd: l.barcode || null,
      pkgUnitCd: l.pkgUnitCd || 'NT',
      pkg: qty,
      qtyUnitCd: l.qtyUnitCd,
      qty,
      prc: m(prcCents),
      splyAmt: m(sply),
      dcRt: sply > 0 ? Math.round((dc / sply) * 10000) / 100 : 0,
      dcAmt: m(dc),
      isrccCd: null, isrccNm: null, isrcRt: null, isrcAmt: null,
      taxTyCd: l.taxTy,
      taxblAmt: m(taxable),
      taxAmt: m(t),
      totAmt: m(taxable),
    }
  })
  const totTaxbl = Object.values(taxbl).reduce((a, b) => a + b, 0)
  const totTax = Object.values(tax).reduce((a, b) => a + b, 0)
  const d8 = input.confirmedAt.slice(0, 8)
  const pin = (input.customerPin || '').trim().toUpperCase()
  return {
    tin: input.tin,
    bhfId: input.bhfId,
    trdInvcNo: input.traderInvoiceNo.slice(0, 50),
    invcNo: input.invcNo,
    orgInvcNo: input.kind === 'R' ? input.orgInvcNo ?? 0 : 0,
    custTin: pin || null,
    custNm: (input.customerName || '').slice(0, 60) || null,
    salesTyCd: 'N',
    rcptTyCd: input.kind,
    pmtTyCd: PAYMENT[input.payment] ?? '07',
    salesSttsCd: '02',
    cfmDt: input.confirmedAt,
    salesDt: d8,
    stockRlsDt: input.confirmedAt,
    cnclReqDt: null,
    cnclDt: null,
    rfdDt: input.kind === 'R' ? input.confirmedAt : null,
    rfdRsnCd: input.kind === 'R' ? input.refundReason || '06' : null,
    totItemCnt: itemList.length,
    taxblAmtA: m(taxbl.A), taxblAmtB: m(taxbl.B), taxblAmtC: m(taxbl.C), taxblAmtD: m(taxbl.D), taxblAmtE: m(taxbl.E),
    taxRtA: TAX_RATES.A, taxRtB: TAX_RATES.B, taxRtC: TAX_RATES.C, taxRtD: TAX_RATES.D, taxRtE: TAX_RATES.E,
    taxAmtA: m(tax.A), taxAmtB: m(tax.B), taxAmtC: m(tax.C), taxAmtD: m(tax.D), taxAmtE: m(tax.E),
    totTaxblAmt: m(totTaxbl),
    totTaxAmt: m(totTax),
    totAmt: m(totTaxbl),
    prchrAcptcYn: 'N',
    remark: null,
    regrId: input.cashierId.slice(0, 20) || 'pos',
    regrNm: input.cashierName.slice(0, 60) || 'POS',
    modrId: input.cashierId.slice(0, 20) || 'pos',
    modrNm: input.cashierName.slice(0, 60) || 'POS',
    receipt: {
      custTin: pin || null,
      custMblNo: input.customerPhone || null,
      rptNo: null,
      rcptPbctDt: input.confirmedAt,
      trdeNm: (input.tradeName || '').slice(0, 20) || null,
      adrs: null,
      topMsg: null,
      btmMsg: null,
      prchrAcptcYn: 'N',
    },
    itemList,
  }
}

/** KRA item code: country + product type + packaging + quantity unit +
 *  7-digit sequence (OSCU spec 4.19), e.g. KE2NTU0000001. */
export function itemCode(seq: number, opts: { service?: boolean; qtyUnitCd: string; pkgUnitCd?: string }): string {
  const ty = opts.service ? '3' : '2'
  return `KE${ty}${opts.pkgUnitCd || 'NT'}${opts.qtyUnitCd}${String(seq).padStart(7, '0')}`.slice(0, 20)
}

/** LedgerPOS unit → KRA quantity unit code (spec 4.7). */
export function qtyUnitFor(unit: string | undefined): string {
  switch ((unit || '').toLowerCase()) {
    case 'kg': return 'KG'
    case 'g': return 'GRM'
    case 'l': return 'L'
    case 'ml': return 'ML'
    case 'm': return 'M'
    case 'box': return 'BX'
    case 'pack': return 'PA'
    case 'dozen': return 'DZ'
    default: return 'U'
  }
}

/** Receipt QR text (what KRA's verifier page takes). */
export function receiptQrUrl(env: 'sandbox' | 'production', tin: string, bhfId: string, rcptSign: string): string {
  const host = env === 'production' ? 'etims.kra.go.ke' : 'etims-sbx.kra.go.ke'
  return `https://${host}/common/link/etims/receipt/indexEtimsReceiptData?Data=${encodeURIComponent(tin + bhfId + rcptSign)}`
}

/** Kenya time (UTC+3, no DST) as yyyyMMddHHmmss. */
export function kenyaStamp(d: Date): string {
  const k = new Date(d.getTime() + 3 * 3600_000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${k.getUTCFullYear()}${p(k.getUTCMonth() + 1)}${p(k.getUTCDate())}${p(k.getUTCHours())}${p(k.getUTCMinutes())}${p(k.getUTCSeconds())}`
}
