// Barcode label printing — renders Code128 barcodes for the selected
// products with JsBarcode (canvas → PNG data URL), then opens a
// print-friendly window with a 50×30 mm label grid (product name, barcode,
// price, store name). Fail-soft: a label whose code cannot be encoded
// falls back to plain text, and a blocked popup just returns 0.

import JsBarcode from 'jsbarcode'
import { Product } from './api'
import { formatMoney } from './money'

const esc = (s: string) =>
  s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')

/** Renders one product's code to a PNG data URL ('' when unencodable). */
function barcodeDataUrl(text: string): string {
  try {
    const canvas = document.createElement('canvas')
    JsBarcode(canvas, text, {
      format: 'CODE128',
      width: 1.7,
      height: 40,
      displayValue: true,
      fontSize: 12,
      margin: 0,
    })
    return canvas.toDataURL('image/png')
  } catch {
    return ''
  }
}

/**
 * Opens the print window for the given products. Returns how many labels
 * were generated (0 when the popup was blocked).
 */
export function printBarcodeLabels(products: Product[], storeName: string): number {
  if (products.length === 0) return 0
  const labels = products.map((p) => {
    const code = p.barcode || p.sku || String(p.id)
    return {
      name: p.name,
      sku: p.sku,
      price: formatMoney(p.priceCents),
      code,
      img: barcodeDataUrl(code),
    }
  })

  const html = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>Barcode labels — ${esc(storeName)}</title>
<style>
  * { box-sizing: border-box; }
  body { margin: 0; font-family: system-ui, -apple-system, "Segoe UI", sans-serif; color: #111; }
  .toolbar { padding: 12px 16px; border-bottom: 2px solid #111; background: #f5f5f4; position: sticky; top: 0; }
  .toolbar button { font: inherit; font-weight: 700; padding: 8px 18px; border: 2px solid #111; border-radius: 6px; background: #10B981; cursor: pointer; }
  .sheet { display: flex; flex-wrap: wrap; gap: 4mm; padding: 8mm; }
  .label {
    width: 50mm; height: 30mm; padding: 2mm 2.5mm;
    border: 0.4mm solid #111; border-radius: 2mm;
    display: flex; flex-direction: column; justify-content: space-between;
    page-break-inside: avoid; break-inside: avoid; overflow: hidden; background: #fff;
  }
  .store { margin: 0; font-size: 7pt; font-weight: 700; text-transform: uppercase; letter-spacing: 0.04em; color: #444; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .name { margin: 0; font-size: 8.5pt; font-weight: 700; line-height: 1.15; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
  .label img { max-width: 100%; height: 11mm; object-fit: contain; }
  .code-fallback { margin: 0; font-family: ui-monospace, monospace; font-size: 10pt; font-weight: 700; letter-spacing: 0.12em; }
  .foot { display: flex; justify-content: space-between; align-items: baseline; gap: 2mm; }
  .price { margin: 0; font-size: 10pt; font-weight: 900; white-space: nowrap; }
  .sku { margin: 0; font-size: 6.5pt; color: #666; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  @page { margin: 8mm; }
  @media print {
    .toolbar { display: none; }
    .sheet { padding: 0; gap: 3mm; }
    body { -webkit-print-color-adjust: exact; print-color-adjust: exact; }
  }
</style>
</head>
<body>
  <div class="toolbar">
    <strong>${labels.length}</strong> label${labels.length === 1 ? '' : 's'} &mdash; 50&times;30&nbsp;mm
    <button onclick="window.print()">Print</button>
  </div>
  <div class="sheet">
    ${labels
      .map(
        (l) => `<div class="label">
      <div>
        <p class="store">${esc(storeName)}</p>
        <p class="name">${esc(l.name)}</p>
      </div>
      ${l.img ? `<img src="${l.img}" alt="${esc(l.code)}">` : `<p class="code-fallback">${esc(l.code)}</p>`}
      <div class="foot">
        <p class="price">${esc(l.price)}</p>
        <p class="sku">${esc(l.sku)}</p>
      </div>
    </div>`,
      )
      .join('\n')}
  </div>
  <script>window.addEventListener('load', function () { setTimeout(function () { window.print() }, 300) })</script>
</body>
</html>`

  const w = window.open('', '_blank', 'width=840,height=680')
  if (!w) return 0 // popup blocked — caller toasts a hint
  w.document.write(html)
  w.document.close()
  return labels.length
}
