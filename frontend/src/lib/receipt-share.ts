// receipt-share — ONE decision tree that gets the branded receipt IMAGE
// to the customer on WhatsApp from any device (v1.6.1).
//
// The cashier presses Share and the legs are tried in order:
//
//  1. SERVER (PAID order + gateway token): the backend queues the branded
//     image to the customer's number — nothing to touch. The button is an
//     honest RESEND for "I didn't get it".
//  2. NATIVE SHARE: navigator.share with files — Android Chrome/WebView
//     and Windows 11 with the Store WhatsApp app open the OS share sheet
//     and the image + thank-you travel together.
//  3. DESKTOP APP: copy the PNG to the clipboard, download it as a
//     backup, then navigate whatsapp://send?phone=…&text=… — the
//     installed WhatsApp desktop app jumps into the customer's chat with
//     the caption typed; the cashier presses Ctrl+V then Enter and the
//     picture is on its way. (WebView2/Edge show an "Open WhatsApp?"
//     dialog the first time — tick "Always allow".)
//  4. BROWSER CHAT: wa.me click-to-chat opens WhatsApp Web with the
//     caption typed; the downloaded PNG is one drag away.
//
// The UI layer renders the result (button label, toast, fallback link) —
// this module stays headless so it is unit-testable.

import { Order, api, blob } from './api'

export interface ReceiptShareResult {
  kind: 'queued' | 'shared' | 'app-opened' | 'chat-opened' | 'cancelled'
  /** Normalized international digits of the customer ('' if none). */
  phone: string
  /** Desktop leg: the image is on the clipboard, ready for Ctrl+V. */
  clipboardOk: boolean
  /** wa.me click-to-chat URL shown in the UI as the fallback hop. */
  waWebUrl: string
}

export interface ShareDeps {
  apiPost: (path: string, body: unknown) => Promise<{ queued: boolean; phone: string }>
  fetchImage: () => Promise<Blob>
  share: (data: { files: File[]; text: string }) => Promise<void>
  canShareFiles: (file: File) => boolean
  copyPng: (b: Blob) => Promise<void>
  download: (b: Blob, name: string) => void
  openExternal: (href: string) => void
}

const IMG_NAME = (order: Order) => `receipt-${order.number}.png`

export function receiptCaption(order: Order, store: string): string {
  const first = (order.customerName || '').trim().split(/\s+/)[0]
  const base = `Thank you for shopping with ${store} — we're glad you chose us.`
  return first ? `Hi ${first}! ${base}` : base
}

/** wa.me needs the full international form (2547…). A stored 07… number
 *  produced an invalid click-to-chat link — normalize exactly like the
 *  server does for automatic sends (services/whatsapp.go). */
export function receiptPhoneDigits(order: Order): string {
  const p = (order.payments ?? []).find((x) => (x.phone || '').replace(/\D/g, '').length >= 9)
  let d = (p?.phone || '').replace(/\D/g, '')
  if (d.startsWith('0')) d = '254' + d.slice(1)
  else if (d.startsWith('7') || d.startsWith('1')) d = '254' + d
  return d
}

export function waMeLink(digits: string, caption: string): string {
  return digits
    ? `https://wa.me/${digits}?text=${encodeURIComponent(caption)}`
    : `https://wa.me/?text=${encodeURIComponent(caption)}`
}

export function whatsappDeepLink(digits: string, caption: string): string {
  return digits
    ? `whatsapp://send?phone=${digits}&text=${encodeURIComponent(caption)}`
    : `whatsapp://send?text=${encodeURIComponent(caption)}`
}

function defaultDeps(order: Order): ShareDeps {
  // Lazy lookup: the test env (node) has no navigator — tests inject
  // every dep, so the closure body below must never run there.
  const nav = () => navigator as Navigator & {
    canShare?: (d: ShareData) => boolean
    share?: (d: ShareData) => Promise<void>
  }
  return {
    apiPost: (path, body) => api.post<{ queued: boolean; phone: string }>(path, body),
    fetchImage: () => blob(`/api/v1/orders/${order.id}/receipt-image`),
    share: (d) => nav().share!(d),
    canShareFiles: (file) => {
      const n = nav()
      // Runtime guard (some browsers lack share) — typeof keeps TS honest.
      return typeof n.share === 'function' && !!n.canShare?.({ files: [file] })
    },
    copyPng: async (b) => {
      if (!navigator.clipboard || typeof ClipboardItem === 'undefined') {
        throw new Error('clipboard images unsupported')
      }
      await navigator.clipboard.write([new ClipboardItem({ 'image/png': b })])
    },
    download: (b, name) => {
      const url = URL.createObjectURL(b)
      const a = document.createElement('a')
      a.href = url
      a.download = name
      document.body.appendChild(a)
      a.click()
      a.remove()
      window.setTimeout(() => URL.revokeObjectURL(url), 8000)
    },
    // Anchor-click navigation: the reliable way to hand a custom
    // protocol (whatsapp://) to the OS from a page — Chromium fires the
    // registered handler without navigating the tab away.
    openExternal: (href) => {
      const a = document.createElement('a')
      a.href = href
      document.body.appendChild(a)
      a.click()
      a.remove()
    },
  }
}

export async function shareReceiptWhatsapp(
  order: Order,
  store: string,
  overrides?: Partial<ShareDeps>,
): Promise<ReceiptShareResult> {
  const d: ShareDeps = { ...defaultDeps(order), ...overrides }
  const caption = receiptCaption(order, store)
  const digits = receiptPhoneDigits(order)
  const waWeb = waMeLink(digits, caption)
  const base = { phone: digits, clipboardOk: false, waWebUrl: waWeb }

  // Leg 1 — server queue (gateway connected, PAID order).
  if (order.status === 'PAID') {
    try {
      const r = await d.apiPost(`/api/v1/orders/${order.id}/whatsapp-send`, {})
      if (r?.queued) return { ...base, kind: 'queued', phone: r.phone || digits }
    } catch {
      /* gateway off (under review) or no phone — device legs carry the
         very same image below. */
    }
  }

  const b = await d.fetchImage()

  // Leg 2 — native share sheet (Android, Windows 11 + Store WhatsApp).
  const file = new File([b], IMG_NAME(order), { type: 'image/png' })
  if (d.canShareFiles(file)) {
    try {
      await d.share({ files: [file], text: caption })
      return { ...base, kind: 'shared' }
    } catch (e) {
      // The cashier cancelling the share sheet is normal, not an error —
      // but stop there instead of surprising them with more windows.
      if (e instanceof DOMException && e.name === 'AbortError') {
        return { ...base, kind: 'cancelled' }
      }
      /* fall through to the desktop leg */
    }
  }

  // Leg 3 — DESKTOP APP: clipboard + download backup + whatsapp:// deep
  // link into the installed app's chat.
  let clipboardOk = false
  try {
    await d.copyPng(b)
    clipboardOk = true
  } catch {
    /* clipboard blocked (focus/permissions) — the downloaded file below
       is the attach path. */
  }
  try {
    d.download(b, IMG_NAME(order))
  } catch {
    /* a blocked download must not kill the chat hop */
  }
  d.openExternal(whatsappDeepLink(digits, caption))
  return { ...base, kind: 'app-opened', clipboardOk }
}
