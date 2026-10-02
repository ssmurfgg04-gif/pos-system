// ProductPhoto — the single product-image component used by the catalog
// grid, list rows, cart thumbnails and inventory. It walks the photo
// fallback chain (uploaded → bundled → monogram, see lib/photos.ts) and
// guarantees the cashier NEVER sees a browser broken-image glyph:
//
//   - an empty candidate list renders the monogram (no <img> at all —
//     an empty src resolves against the page root, never fires onError
//     and would show the broken glyph forever — the original bug),
//   - a failed <img> onError advances to the next candidate (the bundled
//     catalog photo that ships inside the binary), then the monogram,
//   - a photo change (updatedAt cache-buster) resets the chain, so a
//     freshly uploaded photo appears without a remount.

import { useEffect, useMemo, useState } from 'react'
import { Product } from '../lib/api'
import { productPhotoCandidates } from '../lib/photos'

/** Deliberate fallback: two-letter monogram on a quiet surface. */
export function ProductMonogram({ name }: { name: string }) {
  const initials =
    name
      .split(/\s+—\s+|\s+/)
      .slice(0, 2)
      .map((w) => w[0] ?? '')
      .join('')
      .toUpperCase() || '·'
  return (
    <div className="w-full h-full flex items-center justify-center bg-surface-muted text-ink-muted" aria-hidden>
      <span className="font-bold text-[11px] tracking-wide text-ink-muted select-none" title={name}>
        {initials}
      </span>
    </div>
  )
}

/**
 * Renders inside a caller-provided sized box (the box controls size, border
 * and rounding for BOTH the image and the monogram). `p` is optional so cart
 * lines whose product was deleted from the catalog still get a clean
 * monogram built from `name`.
 */
export function ProductPhoto({ p, name, imgClassName }: { p?: Product | null; name?: string; imgClassName?: string }) {
  const key = p ? `${p.id}|${p.sku}|${p.updatedAt}` : ''
  const candidates = useMemo(() => (p ? productPhotoCandidates(p) : []), [key]) // eslint-disable-line react-hooks/exhaustive-deps
  const [idx, setIdx] = useState(0)
  useEffect(() => {
    setIdx(0)
  }, [key])
  const src = candidates[idx] ?? ''
  const label = name ?? p?.name ?? ''
  if (!src) return <ProductMonogram name={label} />
  return (
    <img
      src={src}
      alt=""
      loading="lazy"
      onError={() => setIdx((i) => i + 1)}
      className={`w-full h-full object-contain ${imgClassName ?? ''}`}
    />
  )
}
