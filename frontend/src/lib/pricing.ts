// v1.6.3 price tiers — ONE rule for every caller (cart adds, tier
// switches, tests): a wholesale customer pays the product's wholesale
// price when the product actually has one (>0); everyone else pays retail.

/**
 * The unit price for `p` under price tier `tier`.
 * `tier === 'wholesale'` uses `p.wholesalePriceCents` only when it is set
 * and >0 — 0/absent means "same as retail", so the retail price wins.
 */
export function tierPriceCents(
  p: { priceCents: number; wholesalePriceCents?: number },
  tier?: string,
): number {
  const w = p.wholesalePriceCents ?? 0
  return tier === 'wholesale' && w > 0 ? w : p.priceCents
}
