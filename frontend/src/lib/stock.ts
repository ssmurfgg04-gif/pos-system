import type { Product } from './api'

// Low-stock threshold for one product: the per-product setting wins when set
// (>0), otherwise 5, matching the server's low_stock_threshold default.
// Shared so the Sell screen and Inventory agree on what "low" means.
export function lowStockAt(p: Pick<Product, 'lowStockUnits'>): number {
  return (p.lowStockUnits ?? 0) > 0 ? (p.lowStockUnits as number) : 5
}
