// Product photo resolution — ONE chain used by every surface that shows a
// product image: catalog cards, list rows, cart thumbnails, inventory.
//
// The chain, in order:
//   1. the cashier-uploaded photo (server /image route with cache-buster, or
//      the demo's in-memory upload),
//   2. the bundled catalog photo that ships inside the app binary for that
//      SKU (frontend/public/demo-products/) — a fresh till seeds the starter
//      catalog with NO uploads, so without this step every card rendered a
//      bare monogram ("product images not visible"),
//   3. nothing — the caller renders the deliberately designed monogram
//      (ProductPhoto). No broken-image glyphs, no <img src=""> ever.
//
// demoProductImageUrl already resolves upload→bundled for demo mode; the
// bundled URL is still appended (deduped) so a demo product whose custom
// upload 404s in some exotic edge keeps its bundled fallback.

import { Product, isDemoSync, productImageUrl } from './api'
import { bundledDemoProductImageUrl, demoProductImageUrl } from '../demo/backend'

/** Ordered candidate URLs for a product's photo. Never contains '' — an
 *  empty entry would render as <img src=""> (resolves against the page
 *  root, never fires onError, shows the browser's broken-image glyph). */
export function productPhotoCandidates(p: Product): string[] {
  const out: string[] = []
  const push = (s: string) => {
    if (s && !out.includes(s)) out.push(s)
  }
  if (isDemoSync()) {
    push(demoProductImageUrl(p.id))
  } else {
    push(productImageUrl(p))
  }
  push(bundledDemoProductImageUrl(p.sku))
  return out
}
