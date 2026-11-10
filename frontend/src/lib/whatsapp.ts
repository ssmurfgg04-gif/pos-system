// whatsapp.ts — v1.6.2: ONE way to open a WhatsApp chat from the POS.
//
// The customer complaints that drove this (v1.6.0 behaviour):
//   • on the desktop app the share button "didn't open" anything — the UI
//     runs in a WebView2 shell where window.open('wa.me') and custom-protocol
//     navigation go nowhere (v1.6.0 fell back to WhatsApp Web in a tab);
//   • in a browser, navigator.share({files}) popped the OS "which app?"
//     sheet and the contact never loaded.
// The fix is shared: the receipt screen and the order-detail contact modal
// both open the chat to a NUMBER. In the desktop app the Go host does it
// (POST /api/v1/whatsapp-open → WhatsApp Desktop opens with the chat
// preloaded); in a browser this helper deep-links whatsapp:// first — the
// installed app picks it up with the contact loaded — and only falls back
// to wa.me (WhatsApp Web / the mobile app) when nothing took focus.

/** "0711 222 333" / "711222333" / "+254711222333" → "254711222333". */
export function waDigits(raw: string): string {
  let d = (raw || '').replace(/\D/g, '')
  if (d.startsWith('0')) d = '254' + d.slice(1)
  else if (d.startsWith('7') || d.startsWith('1')) d = '254' + d
  return d
}

/** Click-to-chat URL: WhatsApp Web on a desktop browser, the app on mobile. */
export function waChatUrl(digits: string, text: string): string {
  const q = text ? `?text=${encodeURIComponent(text)}` : ''
  return digits ? `https://wa.me/${digits}${q}` : `https://wa.me/${q}`
}

/** The installed-app scheme — WhatsApp Desktop registers whatsapp://, the
 *  Android/iOS apps answer it too. Contact rides `phone`, caption `text`. */
export function waAppDeepLink(digits: string, text: string): string {
  const q: string[] = []
  if (digits) q.push(`phone=${digits}`)
  if (text) q.push(`text=${encodeURIComponent(text)}`)
  return `whatsapp://send${q.length ? `?${q.join('&')}` : ''}`
}

export interface WaOpenDeps {
  navigate?: (url: string) => void
  open?: (url: string) => void
  delayMs?: number
  /** true when something took focus away — the app opened, no fallback. */
  appOpened?: () => boolean
}

/**
 * Open the customer's WhatsApp chat with the caption typed in. Deep-links
 * the installed app first; falls back to wa.me only if the OS never handed
 * off to an app (no WhatsApp installed / user dismissed the OS prompt).
 */
export function openWhatsAppChat(digits: string, text: string, deps: WaOpenDeps = {}): void {
  const navigate = deps.navigate ?? ((url: string) => { window.location.href = url })
  const open = deps.open ?? ((url: string) => { window.open(url, '_blank', 'noopener') })
  const delay = deps.delayMs ?? 2200
  const appOpened = deps.appOpened ?? (() => !document.hasFocus() || document.visibilityState === 'hidden')
  // 1) the installed app first — contact preloaded, no share sheet.
  navigate(waAppDeepLink(digits, text))
  // 2) nothing picked it up → WhatsApp Web (desktop) or the app (mobile).
  // Bare global (not window.setTimeout): the host test env is plain Node.
  setTimeout(() => {
    if (appOpened()) return
    open(waChatUrl(digits, text))
  }, delay)
}
