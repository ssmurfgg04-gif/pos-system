// ProductPhoto fallback-chain + cart-stepper regression tests (v1.1.7+):
//   1. Product images must walk the chain uploaded → bundled catalog photo →
//      monogram. A fresh till seeds the starter catalog with NO uploads, so
//      the bundled photo (shipped inside the binary) is what makes cards
//      show real images instead of placeholders.
//   2. An <img> that fails (404 on the /image route) must advance to the
//      next candidate — never render the browser's broken-image glyph.
//   3. NO candidate may ever be '' — <img src=""> resolves against the page
//      root, never fires onError, and shows the broken glyph forever.
//   4. The cart row's decrease control shows − at EVERY quantity (the old
//      ✕-at-qty-1 swap made the stepper look like a delete button).
// @vitest-environment jsdom
import { describe, expect, test, vi, beforeEach, afterEach } from 'vitest'
import React, { act } from 'react'
import { createRoot, Root } from 'react-dom/client'

;(React as any).actEnvironment = true

// ---- Module mocks — controllable mode + URL sources ----
let demoMode = false
let uploadedUrl = '/api/v1/products/7/image?v=2026-01-01'
let demoUrl = ''
const bundledUrls = new Map<string, string>([['TS-001', '/demo-products/TS-001.jpg'], ['HD-001', '/demo-products/HD-001.jpg']])

vi.mock('../src/lib/api', async (importOriginal) => {
  const orig = await importOriginal<Record<string, unknown>>()
  return {
    ...orig,
    isDemoSync: () => demoMode,
    productImageUrl: (p: { id: number; updatedAt: string }) =>
      uploadedUrl ? `/api/v1/products/${p.id}/image?v=${encodeURIComponent(p.updatedAt)}` : '',
  }
})

vi.mock('../src/demo/backend', () => ({
  demoProductImageUrl: () => demoUrl,
  bundledDemoProductImageUrl: (sku: string) => bundledUrls.get(sku) ?? '',
}))

import { ProductPhoto, ProductMonogram } from '../src/components/ProductPhoto'
import { productPhotoCandidates } from '../src/lib/photos'
import { CheckoutRail } from '../src/components/CheckoutRail'
import { useCart, CartLine } from '../src/stores/cart'

const product = (over: Partial<{ id: number; sku: string; name: string; updatedAt: string }> = {}) => ({
  id: 7,
  sku: 'TS-001',
  name: 'Classic Cotton Tee',
  updatedAt: '2026-01-01T00:00:00Z',
  ...over,
})

let root: Root | null = null
let container: HTMLDivElement

function mount(el: React.ReactElement) {
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
  act(() => {
    root!.render(el)
  })
}

function unmount() {
  if (root) {
    act(() => { root!.unmount() })
    root = null
  }
  container?.remove()
}

const img = () => container.querySelector('img') as HTMLImageElement | null

/** Fire a native image error (jsdom never loads resources). */
function imgError() {
  const el = img()
  expect(el).toBeTruthy()
  act(() => {
    el!.dispatchEvent(new Event('error'))
  })
}

beforeEach(() => {
  demoMode = false
  uploadedUrl = '/api/v1/products/7/image?v=2026-01-01'
  demoUrl = ''
  bundledUrls.set('TS-001', '/demo-products/TS-001.jpg')
  bundledUrls.set('HD-001', '/demo-products/HD-001.jpg')
  useCart.setState({ lines: [], customerName: '', note: '' })
})

afterEach(() => { unmount() })

describe('productPhotoCandidates — the fallback chain', () => {
  test('real mode: uploaded photo first, bundled catalog photo second', () => {
    const c = productPhotoCandidates(product() as never)
    expect(c).toEqual(['/api/v1/products/7/image?v=2026-01-01T00%3A00%3A00Z', '/demo-products/TS-001.jpg'])
  })

  test('real mode: no bundled photo for unknown SKUs — chain is just the upload', () => {
    const c = productPhotoCandidates(product({ sku: 'XX-999' }) as never)
    expect(c).toEqual(['/api/v1/products/7/image?v=2026-01-01T00%3A00%3A00Z'])
  })

  test('demo mode: demo URL first, bundled deduped behind it', () => {
    demoMode = true
    demoUrl = 'data:image/png;base64,upload'
    const c = productPhotoCandidates(product() as never)
    expect(c).toEqual(['data:image/png;base64,upload', '/demo-products/TS-001.jpg'])
  })

  test('a candidate is NEVER an empty string (the <img src=""> broken-glyph bug)', () => {
    demoMode = true
    demoUrl = ''
    uploadedUrl = ''
    const c = productPhotoCandidates(product({ sku: 'ZZ-000' }) as never)
    expect(c).toEqual([])
    expect(c.every((s) => s !== '')).toBe(true)
  })
})

describe('ProductPhoto — rendering through the chain', () => {
  test('renders the uploaded photo, advances to the bundled photo on error, then the monogram', () => {
    mount(<ProductPhoto p={product() as never} />)
    expect(img()!.getAttribute('src')).toBe('/api/v1/products/7/image?v=2026-01-01T00%3A00%3A00Z')
    imgError() // uploaded photo 404s (fresh till, no upload)
    expect(img()!.getAttribute('src')).toBe('/demo-products/TS-001.jpg')
    imgError() // bundled photo missing too
    expect(img()).toBeNull()
    expect(container.textContent).toContain('CC') // monogram initials
  })

  test('no candidates at all → monogram, never an <img>', () => {
    demoMode = true
    demoUrl = ''
    mount(<ProductPhoto p={product({ sku: 'ZZ-000' }) as never} />)
    expect(img()).toBeNull()
    expect(container.textContent).toContain('CC')
  })

  test('deleted cart product (p undefined) → monogram from the line name', () => {
    mount(<ProductPhoto p={undefined} name="Hoodie XL" />)
    expect(img()).toBeNull()
    expect(container.textContent).toContain('HX')
  })
})

describe('CheckoutRail cart row — the decrease control', () => {
  const line = (over: Partial<CartLine> = {}): CartLine => ({
    productId: 7,
    name: 'Classic Cotton Tee — Black',
    sku: 'TS-001',
    unitPriceCents: 55000,
    qty: 1,
    trackStock: false,
    stockQty: 0,
    ...over,
  })

  test('shows a − (minus) glyph at quantity 1 — never a ✕', () => {
    useCart.setState({ lines: [line()] })
    mount(<CheckoutRail products={null} payConfig={null} onOrderPaid={() => {}} onOrderVoided={() => {}} />)
    const dec = container.querySelector('button[aria-label="Remove Classic Cotton Tee — Black"]') as HTMLButtonElement
    expect(dec).toBeTruthy()
    const svg = dec.querySelector('svg')!
    expect(svg.getAttribute('class')).toContain('lucide-minus')
    expect(svg.getAttribute('class')).not.toContain('lucide-x')
  })

  test('shows a − glyph at quantity 3 too — stable control shape', () => {
    useCart.setState({ lines: [line({ qty: 3 })] })
    mount(<CheckoutRail products={null} payConfig={null} onOrderPaid={() => {}} onOrderVoided={() => {}} />)
    const dec = container.querySelector('button[aria-label="Decrease quantity"]') as HTMLButtonElement
    expect(dec).toBeTruthy()
    expect(dec.querySelector('svg')!.getAttribute('class')).toContain('lucide-minus')
  })
})
