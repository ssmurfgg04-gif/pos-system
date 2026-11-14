// EtimsReceiptBlock — v1.10 the KRA control-unit block a signed tax invoice
// carries: CU details, invoice/receipt numbers, internal data, signature
// and the QR the buyer scans to verify the invoice on KRA's site.
import { useEffect, useState } from 'react'
import QRCode from 'qrcode'
import { EtimsStamp } from '../lib/api'

/** 20261114143005 → 14/11/2026 14:30:05 */
export function fmtSdcDate(s: string): string {
  if (!/^\d{14}$/.test(s)) return s
  return `${s.slice(6, 8)}/${s.slice(4, 6)}/${s.slice(0, 4)} ${s.slice(8, 10)}:${s.slice(10, 12)}:${s.slice(12, 14)}`
}

export function EtimsReceiptBlock({ stamp }: { stamp: EtimsStamp }) {
  const [qr, setQr] = useState('')
  useEffect(() => {
    let live = true
    if (stamp.status === 'signed' && stamp.qr) {
      QRCode.toDataURL(stamp.qr, { margin: 1, width: 160, errorCorrectionLevel: 'M' })
        .then((u) => { if (live) setQr(u) })
        .catch(() => {})
    }
    return () => { live = false }
  }, [stamp.qr, stamp.status])

  if (stamp.status !== 'signed') {
    return (
      <p className="text-center text-[10px] text-ink-muted">
        {stamp.status === 'failed' ? 'KRA eTIMS: not signed yet, the shop will resend' : 'KRA eTIMS: sending to KRA…'}
      </p>
    )
  }
  const row = (k: string, v: string | number) => (
    <div className="flex justify-between gap-2">
      <span className="text-ink-muted">{k}</span>
      <span className="text-right break-all">{v}</span>
    </div>
  )
  return (
    <div className="text-[10px] leading-snug" aria-label="KRA eTIMS details">
      <p className="text-center font-bold tracking-wide">SCU INFORMATION</p>
      {row('Date', fmtSdcDate(stamp.sdcDateTime))}
      {row('SCU ID', stamp.sdcId)}
      {row('CU invoice no.', `${stamp.sdcId}/${stamp.curRcptNo}`)}
      {row('Receipt no.', `${stamp.curRcptNo}/${stamp.totRcptNo} NS`)}
      {row('Internal data', stamp.intrlData)}
      {row('Receipt signature', stamp.rcptSign)}
      {stamp.mrcNo && row('MRC no.', stamp.mrcNo)}
      {qr && <img src={qr} alt="KRA eTIMS verification QR code" className="mx-auto mt-1.5 w-28 h-28" />}
    </div>
  )
}
