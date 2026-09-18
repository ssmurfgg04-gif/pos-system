// Receipt — a printable thermal-style receipt rendered from the Order
// object directly (identical in server and demo mode; no separate HTML
// endpoint round-trip). window.print() with print CSS produces a clean
// 80mm-friendly printout; browser "Save as PDF" covers the no-printer case.

import { Branding, Order } from '../lib/api'
import { centsToAmount } from '../lib/money'
import { Button, Modal } from './ui'
import { Printer } from 'lucide-react'

export function ReceiptModal({ order, branding, open, onClose }: {
  order: Order
  branding: Branding
  open: boolean
  onClose: () => void
}) {
  if (!open) return null
  const pay = order.payments[order.payments.length - 1]
  const store = branding.store_name || 'Store'

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
        </>
      }
    >
      <div id="receipt-print" className="mx-auto max-w-[340px] bg-surface border-2 border-line-strong rounded-input p-5 font-mono text-[12px] leading-relaxed text-ink">
        <div className="text-center">
          <p className="font-bold text-[15px] uppercase tracking-wide">{store}</p>
          {branding.app_name && branding.app_name !== store && (
            <p className="text-[10px] text-ink-muted">{branding.app_name}</p>
          )}
          <p className="text-[10px] text-ink-muted mt-0.5">{new Date(order.createdAt).toLocaleString()}</p>
          <p className="text-[10px] text-ink-muted">Order {order.number}</p>
        </div>

        <div className="border-t-2 border-dashed border-line my-2.5" />

        <table className="w-full">
          <tbody>
            {order.items.map((i) => (
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
          <Row label={`VAT (${order.taxPercent || branding.tax_percent}%)`} value={centsToAmount(order.taxCents)} />
          <div className="flex justify-between font-bold text-[14px] border-t-2 border-line mt-1 pt-1">
            <span>TOTAL</span>
            <span className="tabular">{branding.currency_code} {centsToAmount(order.totalCents)}</span>
          </div>
        </div>

        <div className="border-t-2 border-dashed border-line my-2.5" />

        <div className="space-y-0.5">
          <Row label={pay?.method === 'cash' ? 'Paid (cash)' : pay?.method === 'account' ? 'Tab' : 'Paid (M-Pesa)'} value={pay ? centsToAmount(pay.amountCents) : '—'} />
          {pay?.method === 'account' && <p className="text-center text-[11px] font-semibold text-ink-muted">On tab — balance on Customers → Ledger</p>}
          {pay?.mpesaReceipt && <Row label="M-Pesa receipt" value={pay.mpesaReceipt} />}
          {pay?.phone && <Row label="Phone" value={pay.phone} />}
          {order.customerName && <Row label="Customer" value={order.customerName} />}
          <Row label="Served by" value={order.cashierName} />
          {order.status === 'VOIDED' && <p className="text-center font-bold mt-1">** VOIDED **</p>}
        </div>

        <div className="border-t-2 border-dashed border-line my-2.5" />
        <p className="text-center text-[10px] text-ink-muted leading-snug">
          {branding.store_name || store} — thank you!
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

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-2">
      <span className="text-ink-muted">{label}</span>
      <span className="tabular">{value}</span>
    </div>
  )
}
