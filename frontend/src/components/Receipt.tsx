// Receipt — a printable thermal-style receipt rendered from the Order
// object directly (identical in server and demo mode; no separate HTML
// endpoint round-trip). window.print() with print CSS produces a clean
// 80mm-friendly printout; browser "Save as PDF" covers the no-printer case.
//
// v1.6.0: the Share-via-WhatsApp button — the whapi gateway is still under
// review, so the cashier needs a manual path sharing the BRANDED RECEIPT
// IMAGE (server-rendered PNG, invisible watermark included).
//
// v1.6.2: the customer complaints — the button "didn't open" anything on
// the desktop app (the WebView2 shell swallows window.open and custom
// protocols) and browser share sheets asked "which app?" with no contact
// loaded. Now: the image ALWAYS lands in Downloads first, then the HOST
// leg (POST /api/v1/whatsapp-open) launches the installed WhatsApp app
// with the customer's chat + caption preloaded; browser-only boxes fall
// back to the whatsapp:// deep link (wa.me/WhatsApp-Web last resort).
// A secondary "Send image…" keeps the one-tap Web Share where it works.

import { Branding, Order, api, blob } from '../lib/api'
import { centsToAmount } from '../lib/money'
import { fmtQty } from '../lib/qty'
import { openWhatsAppChat } from '../lib/whatsapp'
import { toast } from '../stores/toasts'
import { Button, Modal } from './ui'
import { Check, ImageDown, Printer, Share2 } from 'lucide-react'
import { useState } from 'react'

export function ReceiptModal({ order, branding, open, onClose }: {
  order: Order
  branding: Branding
  open: boolean
  onClose: () => void
}) {
  const [shareState, setShareState] = useState<'idle' | 'working' | 'saved' | 'queued'>('idle')
  const [shareError, setShareError] = useState('')
  if (!open) return null
  // Older orders (or a partial payload) may omit items/payments — the
  // receipt must render, never crash the POS after money has moved.
  const items = order.items ?? []
  const store = branding.store_name || 'Store'

  const caption = () => {
    const first = (order.customerName || '').trim().split(/\s+/)[0]
    const base = `Thank you for shopping with ${store} — we're glad you chose us.`
    return first ? `Hi ${first}! ${base}` : base
  }
  // wa.me needs full international form (2547…). A stored 07… number
  // produced an invalid click-to-chat link — normalize like the server
  // does for automatic sends (services/whatsapp.go normalizeWhatsappPhone).
  const customerDigits = (): string => {
    const p = (order.payments ?? []).find((x) => (x.phone || '').replace(/\D/g, '').length >= 9)
    let d = (p?.phone || '').replace(/\D/g, '')
    if (d.startsWith('0')) d = '254' + d.slice(1)
    else if (d.startsWith('7') || d.startsWith('1')) d = '254' + d
    return d
  }
  // The cashier must SEE who is about to receive payment proof.
  const shareTarget = customerDigits()

  // v1.6.2: save the branded PNG to the device (WhatsApp cannot receive a
  // file over a link — the cashier attaches this in the chat), then open
  // the customer's chat with the caption typed.
  const saveReceiptImage = async (): Promise<Blob> => {
    const b = await blob(`/api/v1/orders/${order.id}/receipt-image`)
    const url = URL.createObjectURL(b)
    const a = document.createElement('a')
    a.href = url
    a.download = `receipt-${order.number}.png`
    document.body.appendChild(a)
    a.click()
    a.remove()
    window.setTimeout(() => URL.revokeObjectURL(url), 8000)
    return b
  }

  const shareWhatsapp = async () => {
    setShareState('working')
    setShareError('')
    try {
      // SERVER leg first (PAID orders, gateway connected): the branded
      // IMAGE is queued to the customer's WhatsApp with the thank-you
      // caption — the customer receives the picture without the cashier
      // touching their phone. The button is an honest RESEND: pressing it
      // again is exactly what "I didn't get it" needs.
      if (order.status === 'PAID') {
        try {
          const r = await api.post<{ queued: boolean; phone: string }>(`/api/v1/orders/${order.id}/whatsapp-send`, {})
          if (r.queued) {
            setShareState('queued')
            toast.success('Receipt queued', `The branded image goes to ${r.phone} on WhatsApp — check Settings → WhatsApp if it fails.`)
            return
          }
        } catch {
          /* gateway not connected (it is under review) or the sale has no
             phone — the HOST/DEVICE paths below carry the very same image. */
        }
      }
      await saveReceiptImage()
      const digits = customerDigits()
      // v1.6.2 HOST leg: the Go process on the till hands whatsapp:// to
      // the OS — WhatsApp Desktop / the mobile app opens with the chat to
      // the customer + thank-you preloaded. This is the leg that works
      // inside the WebView2 app window, where in-page window.open and
      // custom-protocol navigation go nowhere (the customer complaint).
      try {
        const r = await api.post<{ opened: boolean }>(`/api/v1/whatsapp-open`, { phone: digits, text: caption() })
        if (r.opened) {
          setShareState('saved')
          toast.success('WhatsApp opened', digits ? `Chat to ${digits} is loaded — attach the saved receipt image and send.` : 'Pick the chat, attach the saved receipt image and send.')
          return
        }
        setShareError('WhatsApp is not installed on this machine — the receipt image is saved; open a browser and press Share again, or install WhatsApp Desktop.')
        setShareState('saved')
        return
      } catch {
        /* browser-only mode or an older server — deep-link below */
      }
      // BROWSER leg: deep-link the installed app (contact preloaded); if
      // nothing took focus the helper falls back to wa.me (WhatsApp Web)
      // after ~2s — never a share sheet, never a dead click.
      openWhatsAppChat(digits, caption())
      setShareState('saved')
    } catch (e) {
      // The cashier cancelling the share sheet is normal, not an error.
      if (e instanceof DOMException && e.name === 'AbortError') {
        setShareState('idle')
        return
      }
      setShareState('idle')
      setShareError(e instanceof Error ? e.message : 'Could not prepare the receipt image')
    }
  }

  // Secondary, kept from v1.6.0: where the browser supports file sharing
  // (Android, some desktops) the image rides the Web Share sheet into the
  // chat in one tap — contact chosen there. Primary button stays the
  // app-opening, contact-first path.
  const shareImageSheet = async () => {
    setShareState('working')
    setShareError('')
    try {
      const b = await saveReceiptImage()
      const file = new File([b], `receipt-${order.number}.png`, { type: 'image/png' })
      const nav = navigator as Navigator & {
        canShare?: (d: ShareData) => boolean
        share?: (d: ShareData) => Promise<void>
      }
      if (nav.share && nav.canShare?.({ files: [file] })) {
        await nav.share({ files: [file], text: caption() })
        setShareState('idle')
        return
      }
      // No file-share support after all → the primary path.
      await shareWhatsapp()
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') {
        setShareState('idle')
        return
      }
      setShareState('idle')
      setShareError(e instanceof Error ? e.message : 'Could not prepare the receipt image')
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={`Receipt ${order.number}`}
      size="sm"
      footer={
        <>
          <Button variant="primary" onClick={() => {
            document.body.classList.add('printing-receipt')
            window.print()
            window.setTimeout(() => document.body.classList.remove('printing-receipt'), 400)
          }}>
            <Printer size={16} strokeWidth={2.25} aria-hidden />
            Print
          </Button>
          <Button variant="primary" disabled={shareState === 'working'} onClick={shareWhatsapp}>
            {shareState === 'queued' ? <Check size={16} strokeWidth={2.25} aria-hidden /> : <Share2 size={16} strokeWidth={2.25} aria-hidden />}
            {shareState === 'working' ? 'Preparing…'
              : shareState === 'queued' ? `Image queued to ${shareTarget || 'the customer'} on WhatsApp`
              : shareState === 'saved' ? (shareTarget ? `Image saved — WhatsApp opened for ${shareTarget}; attach and send` : 'Image saved — attach it in the chat')
              : 'Share via WhatsApp'}
          </Button>
          {typeof navigator.share === 'function' && (
            <Button variant="secondary" disabled={shareState === 'working'} onClick={shareImageSheet} title="The OS share sheet carries the image itself — pick WhatsApp there, the contact is chosen in the sheet">
              <ImageDown size={16} strokeWidth={2.25} aria-hidden />
              Send image…
            </Button>
          )}
        </>
      }
    >
      {shareError && (
        <p className="mb-2 rounded-input border-2 bg-danger-bg px-3 py-2 text-[12px] font-semibold text-danger-text border-danger-text/30">
          {shareError}
        </p>
      )}
      <div id="receipt-print" className="mx-auto max-w-[340px] bg-surface border-2 border-line-strong rounded-input p-5 font-mono text-[12px] leading-relaxed text-ink">
        <div className="text-center">
          <p className="font-bold text-[15px] uppercase tracking-wide">{store}</p>
          {(branding.store_address || branding.store_phone) && (
            <p className="text-[10px] text-ink-muted">
              {[branding.store_address, branding.store_phone].filter(Boolean).join(' · ')}
            </p>
          )}
          {branding.app_name && branding.app_name !== store && (
            <p className="text-[10px] text-ink-muted">{branding.app_name}</p>
          )}
          {/* A buyer KRA PIN makes this an INVOICE with both PINs on it, but
              NOT a tax invoice: only an invoice transmitted to KRA eTIMS
              (control-unit number + QR) lets the buyer claim input VAT, and
              LedgerPOS does not transmit yet. Saying "TAX INVOICE" here
              would mislead the buyer's accountant. */}
          {order.buyerPin && (
            <>
              <p className="font-bold text-[12px] tracking-widest uppercase mt-1">Invoice</p>
              <p className="text-[10px] text-ink-muted">Not an eTIMS tax invoice</p>
            </>
          )}
          {branding.kra_pin && (
            <p className="text-[10px] text-ink-muted">KRA PIN: {branding.kra_pin}</p>
          )}
          {order.buyerPin && <p className="text-[10px] text-ink-muted">Buyer PIN: {order.buyerPin}</p>}
          <p className="text-[10px] text-ink-muted mt-0.5">
            {new Date(order.paidAt || order.createdAt).toLocaleString('en-GB', { day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit' })}
          </p>
          <p className="text-[10px] text-ink-muted">Order {order.number}</p>
        </div>

        <div className="border-t-2 border-dashed border-line my-2.5" />

        <table className="w-full">
          <tbody>
            {items.map((i) => (
              <tr key={i.id} className="align-top">
                <td className="py-0.5 pr-2">
                  <span className="block">{i.name}</span>
                  <span className="text-[10px] text-ink-muted">{fmtQty(i.qtyMilli)} × {centsToAmount(i.unitPriceCents)}</span>
                </td>
                <td className="py-0.5 text-right whitespace-nowrap tabular font-semibold">{centsToAmount(i.lineTotalCents)}</td>
              </tr>
            ))}
          </tbody>
        </table>

        <div className="border-t-2 border-dashed border-line my-2.5" />

        <div className="space-y-0.5">
          <Row label="Subtotal" value={centsToAmount(order.subtotalCents)} />
          {!!order.discountCents && (
            <Row label={`Discount${order.discountLabel ? ` (${order.discountLabel})` : ''}`} value={`-${centsToAmount(order.discountCents)}`} />
          )}
          {!!order.pointsRedeemed && (
            <Row
              label={`Points redeemed (${order.pointsRedeemed} pts)`}
              value={`-${centsToAmount(order.pointsRedeemed * (branding.loyalty_point_cents || 100))}`}
            />
          )}
          {!!order.taxCents && (
            <Row label={`VAT (${order.taxPercent || branding.tax_percent}%)`} value={centsToAmount(order.taxCents)} />
          )}
          <div className="flex justify-between font-bold text-[14px] border-t-2 border-line mt-1 pt-1">
            <span>TOTAL</span>
            <span className="tabular">{branding.currency_code} {centsToAmount(order.totalCents)}</span>
          </div>
        </div>

        <div className="border-t-2 border-dashed border-line my-2.5" />

        <div className="space-y-0.5">
          {/* EVERY payment leg prints (split tender cash 300 + M-Pesa 700 must
              reconcile against the TOTAL) with the method named correctly
              the old last-leg-only render showed "Paid (M-Pesa) 300.00" and
              labelled a card charge as M-Pesa. */}
          {(order.payments ?? []).filter((p) => p.status !== 'FAILED').map((p, i) => (
            <div key={p.id ?? i}>
              <Row label={paymentLabel(p)} value={centsToAmount(p.amountCents)} />
              {p.method === 'account' && <p className="text-center text-[11px] font-semibold text-ink-muted">On tab, balance on Customers → Ledger</p>}
              {p.mpesaReceipt && <Row label="M-Pesa receipt" value={p.mpesaReceipt} />}
              {p.method === 'credit' && <p className="text-center text-[11px] font-semibold text-ink-muted">Paid from store credit</p>}
            </div>
          ))}
          {(order.giftCards ?? []).map((code) => (
            /* The code IS the value — same rows as paper, image and HTML. */
            <Row key={code} label="Gift card" value={code} />
          ))}
          {order.customerName && <Row label="Customer" value={order.customerName} />}
          <Row label="Served by" value={order.cashierName} />
          {order.status === 'VOIDED' && <p className="text-center font-bold mt-1">** VOIDED **</p>}
        </div>

        <div className="border-t-2 border-dashed border-line my-2.5" />
        <p className="text-center text-[10px] text-ink-muted leading-snug">
          {branding.store_name || store}, thank you!
          <br />
          {settingFooter(branding)}
        </p>
      </div>
    </Modal>
  )
}

function settingFooter(b: Branding): string {
  // The configured footer now rides the branding payload; fall back to
  // the generic retention line only when the shop never set one.
  return b.receipt_footer?.trim() || 'Goods remain the property of the store until fully paid.'
}

/** Human label per payment method — a Paystack CARD charge must never
 *  print as "M-Pesa". */
function paymentLabel(p: { method: string; status: string }): string {
  switch (p.method) {
    case 'cash': return 'Paid (cash)'
    case 'account': return 'Tab'
    case 'credit': return 'Store credit'
    case 'paystack': return p.status === 'COMPLETED' ? 'Paid (card/mobile)' : 'Card/mobile'
    case 'mpesa': return p.status === 'COMPLETED' ? 'Paid (M-Pesa)' : 'M-Pesa'
    default: return `Paid (${p.method})`
  }
}

// (v1.5.0) Receipts also go out AUTOMATICALLY to the customer's number
// when a whapi.cloud token is configured (Settings → WhatsApp): one job
// per paid sale, branded image, English thank-you — zero cashier steps.
// The button above is the manual path for the days the gateway is off
// (it is under review) — same image, same caption, human hands.

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-2">
      <span className="text-ink-muted">{label}</span>
      <span className="tabular">{value}</span>
    </div>
  )
}
