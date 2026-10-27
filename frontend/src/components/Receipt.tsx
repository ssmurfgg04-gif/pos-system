// Receipt — a printable thermal-style receipt rendered from the Order
// object directly (identical in server and demo mode; no separate HTML
// endpoint round-trip). window.print() with print CSS produces a clean
// 80mm-friendly printout; browser "Save as PDF" covers the no-printer case.

import { useState } from 'react'
import { Branding, Order, api } from '../lib/api'
import { centsToAmount } from '../lib/money'
import { Button, Modal } from './ui'
import { Printer } from 'lucide-react'
import { toast } from '../stores/toasts'

export function ReceiptModal({ order, branding, open, onClose }: {
  order: Order
  branding: Branding
  open: boolean
  onClose: () => void
}) {
  if (!open) return null
  // Older orders (or a partial payload) may omit items/payments — the
  // receipt must render, never crash the POS after money has moved.
  const items = order.items ?? []
  const store = branding.store_name || 'Store'

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={`Receipt ${order.number}`}
      size="sm"
      footer={
        <>
          <WhatsAppButton order={order} />
          <Button variant="primary" onClick={() => {
            document.body.classList.add('printing-receipt')
            window.print()
            window.setTimeout(() => document.body.classList.remove('printing-receipt'), 400)
          }}>
            <Printer size={16} strokeWidth={2.25} aria-hidden />
            Print
          </Button>
        </>
      }
    >
      <div id="receipt-print" className="mx-auto max-w-[340px] bg-surface border-2 border-line-strong rounded-input p-5 font-mono text-[12px] leading-relaxed text-ink">
        <div className="text-center">
          <p className="font-bold text-[15px] uppercase tracking-wide">{store}</p>
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
          <p className="text-[10px] text-ink-muted mt-0.5">{new Date(order.createdAt).toLocaleString()}</p>
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
          {!!order.pointsRedeemed && <Row label="Points redeemed" value={`${order.pointsRedeemed} pts`} />}
          <Row label={`VAT (${order.taxPercent || branding.tax_percent}%)`} value={centsToAmount(order.taxCents)} />
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

function settingFooter(_b: Branding): string {
  // Footer text is configured server-side; the branding payload doesn't
  // carry it, so keep the generic line (receipts print the real footer
  // via the server renderer when a printer target is configured).
  return 'Goods remain the property of the store until fully paid.'
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

// WhatsAppButton — hands the receipt to the customer's WhatsApp. The
// server builds the receipt text (single source of truth) and a wa.me
// deep link into the SHOP's own WhatsApp (zero config, works today); when
// the shop configured a gateway, the automated send is queued on the
// server and the button becomes a re-send.
function WhatsAppButton({ order }: { order: Order }) {
  const [busy, setBusy] = useState(false)
  const go = async () => {
    if (!order.id || busy) return
    setBusy(true)
    try {
      const res = await api.post<{ phone: string; link: string; queued: boolean; reason: string }>(
        `/api/v1/orders/${order.id}/whatsapp`, {})
      if (res.link) {
        window.open(res.link, '_blank', 'noopener')
        toast.success(res.queued ? 'Receipt queued for WhatsApp' : 'Opening WhatsApp',
          res.queued ? `Sending to ${res.phone}` : res.reason || 'Press send in WhatsApp')
      } else {
        toast.info('No customer phone', 'Add the customer\u2019s number at checkout to send receipts on WhatsApp.')
      }
    } catch (e: any) {
      toast.error('WhatsApp failed', e?.message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Button variant="ghost" onClick={go} disabled={busy || !order.id} title="Send this receipt to the customer on WhatsApp">
      <svg viewBox="0 0 24 24" width="16" height="16" fill="currentColor" aria-hidden>
        <path d="M12.04 2a9.9 9.9 0 0 0-8.4 15.2L2.2 21.8l4.7-1.4A9.9 9.9 0 1 0 12.04 2Zm5.8 14.2c-.25.7-1.45 1.35-2 1.4-.55.05-1.05.25-3.5-.75s-3.95-3.5-4.1-3.7c-.15-.2-1-1.35-1-2.6s.65-1.85.9-2.1c.25-.25.5-.3.7-.3h.5c.15 0 .35-.05.55.4.2.5.7 1.75.75 1.9.05.1.1.25 0 .4-.1.15-.15.25-.3.4-.15.15-.3.35-.2.55.1.2.5.85 1.05 1.35.7.65 1.3.85 1.5.95.2.1.35.05.5-.1.15-.15.6-.7.75-.95.15-.25.35-.2.55-.15.2.1 1.3.65 1.5.75.2.15.4.2.45.3.05.15.05.65-.2 1.35Z"/>
      </svg>
      {busy ? '…' : 'WhatsApp'}
    </Button>
  )
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-2">
      <span className="text-ink-muted">{label}</span>
      <span className="tabular">{value}</span>
    </div>
  )
}
