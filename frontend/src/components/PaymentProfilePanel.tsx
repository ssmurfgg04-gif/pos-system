// PaymentProfilePanel — "where does this shop's M-Pesa money go?" (v1.9).
//
// Each business on LedgerPOS Cloud is paid on its OWN numbers. Three states,
// decided in the cloud (store_payment_profiles), mirrored here in words:
//   shared  — the founding business's KCB till (LedgerPOS's own shop only)
//   own     — M-Pesa prompts pay this shop's short code, after LedgerPOS has
//             verified it live with KCB (a typo must never send money astray)
//   manual  — customers pay this shop's till/paybill and the cashier enters
//             the M-Pesa code (always works, even offline)

import { useEffect, useState } from 'react'
import { api } from '../lib/api'
import { Button, Field, Input, Spinner, StatusPill } from './ui'
import { toast } from '../stores/toasts'
import { Cloud, Smartphone } from 'lucide-react'

export interface PaymentProfile {
  stkMode: 'shared' | 'own' | 'manual'
  stkAvailable: boolean
  tillNumber: string
  paybillNumber: string
  displayName: string
  orgShortcode: string
  ownRequested: boolean
  ownVerified: boolean
  hasPasskey: boolean
  linked: boolean
}

/** Plain-words status for the owner. Exported for tests. */
export function describeProfile(p: PaymentProfile): { tone: 'paid' | 'pending' | 'info'; label: string; detail: string } {
  if (!p.linked) {
    return {
      tone: 'info',
      label: 'Not connected to LedgerPOS Cloud',
      detail: 'Customers pay your till or paybill number and the cashier types the M-Pesa code. Connect the shop to turn on M-Pesa prompts later.',
    }
  }
  if (p.stkMode === 'shared' && p.stkAvailable) {
    return {
      tone: 'paid',
      label: 'M-Pesa prompts on',
      detail: 'Customers get an M-Pesa prompt on their phone; the money goes to the LedgerPOS KCB till.',
    }
  }
  if (p.stkMode === 'own' && p.ownVerified) {
    return {
      tone: 'paid',
      label: `M-Pesa prompts pay ${p.orgShortcode}`,
      detail: `Customers get an M-Pesa prompt showing ${p.displayName || 'your shop'}, and the money goes straight to your short code ${p.orgShortcode}.`,
    }
  }
  if (p.stkMode === 'own') {
    return {
      tone: 'pending',
      label: 'Waiting for verification',
      detail: `You asked for prompts to pay ${p.orgShortcode}. LedgerPOS turns this on after a live test with KCB. Until then customers pay your till number.`,
    }
  }
  const nums = [p.tillNumber && `till ${p.tillNumber}`, p.paybillNumber && `paybill ${p.paybillNumber}`].filter(Boolean).join(' or ')
  return {
    tone: 'info',
    label: 'Customers pay your till number',
    detail: nums
      ? `Customers pay ${nums} (shown on the customer screen and receipt) and the cashier enters the M-Pesa code.`
      : 'Add your till or paybill number below so customers know where to pay.',
  }
}

export function PaymentProfilePanel() {
  const [p, setP] = useState<PaymentProfile | null>(null)
  const [busy, setBusy] = useState(false)
  const [till, setTill] = useState('')
  const [paybill, setPaybill] = useState('')
  const [name, setName] = useState('')
  const [wantOwn, setWantOwn] = useState(false)
  const [short, setShort] = useState('')
  const [passkey, setPasskey] = useState('')

  const load = async () => {
    try {
      const r = await api.get<PaymentProfile>('/api/v1/payments/profile')
      setP(r)
      setTill(r.tillNumber || '')
      setPaybill(r.paybillNumber || '')
      setName(r.displayName || '')
      setWantOwn(r.stkMode === 'own')
      setShort(r.orgShortcode || '')
    } catch (e: any) {
      toast.error('Could not load payment settings', e?.message)
    }
  }
  useEffect(() => { load() }, [])

  const save = async () => {
    if (!p) return
    setBusy(true)
    try {
      // The founding store keeps its shared till; saving numbers never
      // switches its prompts off.
      const stkMode = p.stkMode === 'shared' ? 'shared' : wantOwn ? 'own' : 'manual'
      const body: Record<string, unknown> = {
        stkMode, tillNumber: till, paybillNumber: paybill, displayName: name, orgShortcode: wantOwn ? short : '',
      }
      if (wantOwn && passkey.trim()) body.orgPasskey = passkey.trim()
      const r = await api.put<PaymentProfile>('/api/v1/payments/profile', body)
      setP(r)
      setPasskey('')
      toast.success('Payment settings saved', describeProfile(r).label)
    } catch (e: any) {
      toast.error('Not saved', e?.message)
    } finally {
      setBusy(false)
    }
  }

  const connect = async () => {
    setBusy(true)
    try {
      const r = await api.post<{ teamCode: string; storeName: string }>('/api/v1/team-sync/register-business', {})
      toast.success('Connected', `${r.storeName} now has its own space in LedgerPOS Cloud.`)
      await load()
    } catch (e: any) {
      toast.error('Could not connect', e?.message)
    } finally {
      setBusy(false)
    }
  }

  if (!p) {
    return <div className="sm:col-span-2 py-6 flex justify-center"><Spinner /></div>
  }
  const d = describeProfile(p)
  const digits = (v: string) => v.replace(/\D/g, '').slice(0, 10)
  return (
    <>
      <div className="sm:col-span-2 border-2 border-line rounded-input p-3 bg-surface-muted">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="text-[12px] uppercase font-bold text-ink-muted inline-flex items-center gap-1.5">
            <Smartphone size={14} strokeWidth={2.25} aria-hidden />Where customers pay
          </p>
          <StatusPill status={d.tone} label={d.label} />
        </div>
        <p className="text-[13px] text-ink-muted mt-1.5" data-testid="payment-detail">{d.detail}</p>
        {!p.linked && (
          <Button size="sm" variant="ghost" className="mt-2" onClick={connect} disabled={busy}>
            <Cloud size={13} strokeWidth={2.5} aria-hidden />Connect this shop to LedgerPOS Cloud
          </Button>
        )}
      </div>
      <Field label="Till number" hint="Buy Goods till customers pay">
        <Input value={till} onChange={(e) => setTill(digits(e.target.value))} inputMode="numeric" placeholder="e.g. 5123456" />
      </Field>
      <Field label="Paybill number" hint="If you use a paybill instead">
        <Input value={paybill} onChange={(e) => setPaybill(digits(e.target.value))} inputMode="numeric" />
      </Field>
      <Field label="Name customers see" hint="On the M-Pesa prompt (up to 13 letters show) and the customer screen">
        <Input value={name} onChange={(e) => setName(e.target.value.slice(0, 40))} />
      </Field>
      {p.linked && p.stkMode !== 'shared' && (
        <div className="sm:col-span-2 border-t-2 border-line pt-3">
          <label className="flex items-start gap-2 text-[13px] cursor-pointer">
            <input type="checkbox" className="mt-0.5" checked={wantOwn} onChange={(e) => setWantOwn(e.target.checked)} />
            <span>
              <strong className="text-ink">Send M-Pesa prompts to my own till.</strong>{' '}
              <span className="text-ink-muted">The customer gets a prompt on their phone and the money goes straight to you. LedgerPOS verifies it with a live test before it goes on.</span>
            </span>
          </label>
          {wantOwn && (
            <div className="grid sm:grid-cols-2 gap-3 mt-3">
              <Field label="Short code prompts should pay" hint="Your till or paybill short code">
                <Input value={short} onChange={(e) => setShort(digits(e.target.value))} inputMode="numeric" />
              </Field>
              <Field label="Passkey (from KCB / Safaricom)" hint={p.hasPasskey ? 'Saved. Leave blank to keep it.' : 'Only if your provider gave you one.'}>
                <Input type="password" autoComplete="off" value={passkey} onChange={(e) => setPasskey(e.target.value)} />
              </Field>
            </div>
          )}
        </div>
      )}
      <div className="sm:col-span-2 flex justify-end">
        <Button onClick={save} disabled={busy}>{busy ? <Spinner className="border-t-brand-ink" /> : null}Save payment settings</Button>
      </div>
    </>
  )
}
