// photo.ts — product-photo normalisation before upload.
//
// Why this exists (Windows):
//   1. Phone photos are routinely 3–8 MB; the server caps uploads at 2 MB,
//      so picking a real photo used to bounce with "Image too large" and
//      nothing else happened — the classic "upload does nothing" report.
//      Large images are now downscaled + re-encoded in the browser so any
//      pickable photo fits.
//   2. The Windows file picker filters by MIME↔extension registration, and
//      MIME-only accept lists (image/webp, .jfif) hide files on many
//      Windows 10 builds — the dialog looks empty. Callers therefore pass
//      an accept list that includes both image/* and explicit extensions.

const MAX_SIDE = 1200 // downscaled photos: plenty for till + receipt
const KEEP_IF_UNDER = 1.5 * 1024 * 1024 // small-enough files pass through

/**
 * Returns a File guaranteed to satisfy the server's size cap: files that
 * are already small pass through untouched; anything bigger is downscaled
 * (longest side MAX_SIDE) and re-encoded as JPEG. If the browser cannot
 * decode the file at all (exotic formats), the original is returned so the
 * server's clear error reaches the cashier instead of a silent no-op.
 */
export async function normalizeProductImage(file: File): Promise<File> {
  if (file.size <= KEEP_IF_UNDER && !file.name.toLowerCase().endsWith('.heic')) {
    return file
  }
  try {
    const bitmap = await loadImage(file)
    const scale = Math.min(1, MAX_SIDE / Math.max(bitmap.width, bitmap.height))
    const w = Math.max(1, Math.round(bitmap.width * scale))
    const h = Math.max(1, Math.round(bitmap.height * scale))
    const canvas = document.createElement('canvas')
    canvas.width = w
    canvas.height = h
    const ctx = canvas.getContext('2d')
    if (!ctx) return file
    ctx.drawImage(bitmap, 0, 0, w, h)
    const blob = await new Promise<Blob | null>((resolve) =>
      canvas.toBlob(resolve, 'image/jpeg', 0.85),
    )
    if (!blob || blob.size >= file.size) return file
    const name = file.name.replace(/\.[^.]+$/, '') + '.jpg'
    return new File([blob], name, { type: 'image/jpeg' })
  } catch {
    return file // undecodable — let the server's 422 explain it
  }
}

async function loadImage(file: File): Promise<ImageBitmap | HTMLImageElement> {
  if ('createImageBitmap' in window) {
    return createImageBitmap(file)
  }
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file)
    const img = new Image()
    img.onload = () => { URL.revokeObjectURL(url); resolve(img) }
    img.onerror = () => { URL.revokeObjectURL(url); reject(new Error('decode failed')) }
    img.src = url
  })
}

/** Accept list that survives the Windows file dialog (MIME + extensions). */
export const PHOTO_ACCEPT = 'image/png,image/jpeg,image/webp,image/gif,image/*,.png,.jpg,.jpeg,.webp,.gif,.jfif'
