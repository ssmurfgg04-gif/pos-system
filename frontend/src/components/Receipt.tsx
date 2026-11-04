// Receipt — a printable thermal-style receipt rendered from the Order
// object directly (identical in server and demo mode; no separate HTML
// endpoint round-trip). window.print() with print CSS produces a clean
// 80mm-friendly printout; browser "Save as PDF" covers the no-printer case.
//
// v1.6.0: the Share-via-WhatsApp button is back — the whapi gateway is
// still under review, so the cashier needs a manual path. It shares the
// BRANDED RECEIPT IMAGE (server-rendered PNG, invisible watermark
// included) through the device's native share sheet where the browser
// supports file sharing; otherwise it downloads the image and opens the
// customer's WhatsApp chat with the thank-you typed — one drag and it's
// sent.

import { Branding, Order, api, blob } from '../lib/api'
import { centsToAmount } from '../lib/money'
import { toast } from '../stores/toasts'
import { Button, Modal } from './ui'
import { Check, Printer, Share2 } from 'lucide-react'
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
             phone — the DEVICE paths below carry the very same image. */
        }
      }
      const b = await blob(`/api/v1/orders/${order.id}/receipt-image`)
      const file = new File([b], `receipt-${order.number}.png`, { type: 'image/png' })
      // Native share (Android WebView, Edge/Chrome on Windows): the
      // cashier picks WhatsApp and the IMAGE + thank-you go in one tap.
      const nav = navigator as Navigator & {
        canShare?: (d: ShareData) => boolean
        share?: (d: ShareData) => Promise<void>
      }
      if (nav.share && nav.canShare?.({ files: [file] })) {
        await nav.share({ files: [file], text: caption() })
        setShareState('idle')
        return
      }
      // Fallback: save the image + open the chat with the thank-you
      // typed; the cashier drags the receipt in and presses send.
      const url = URL.createObjectURL(b)
      const a = document.createElement('a')
      a.href = url
      a.download = `receipt-${order.number}.png`
      document.body.appendChild(a)
      a.click()
      a.remove()
      window.setTimeout(() => URL.revokeObjectURL(url), 8000)
      const digits = customerDigits()
      const wa = digits
        ? `https://wa.me/${digits}?text=${encodeURIComponent(caption())}`
        : `https://wa.me/?text=${encodeURIComponent(caption())}`
      const opened = window.open(wa, '_blank', 'noopener')
      if (!opened) setShareError('The chat did not open — allow pop-ups for this page, then tap Share again.')
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
              : shareState === 'saved' ? (shareTarget ? `Image saved — attach it in the chat for ${shareTarget}` : 'Image saved — attach it')
              : 'Share via WhatsApp'}
          </Button>
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
          {/* eTIMS (P6): a buyer KRA PIN on the order makes this a tax
              invoice, print the header plus both PINs so the document is
              ready for the KRA electronic tax invoice rules. */}
          {order.buyerPin && (
            <p className="font-bold text-[12px] tracking-widest uppercase mt-1">TAX INVOICE</p>
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
                  <span className="text-[10px] text-ink-muted">{i.qty} × {centsToAmount(i.unitPriceCents)}</span>
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
