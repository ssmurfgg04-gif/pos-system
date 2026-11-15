// Promotions (v1.12) — offers to customers by SMS (the shop's own Africa's
// Talking account) or WhatsApp. Only customers who agreed to offers are
// ever sent to; every message carries the opt-out line; nothing goes out
// in quiet hours. The owner sees who and how many before pressing Send.

import { useEffect, useState } from 'react'
import { api } from '../lib/api'
import { Button, Card, EmptyState, Field, Input, Modal, MoneyInput, Select, Spinner, StatusPill, Table, Textarea } from '../components/ui'
import { toast } from '../stores/toasts'
import { Megaphone, Pause, Play, Plus, Send, Settings2, UserX, X } from 'lucide-react'
import { PromoAudience, PromoCampaign, PromoRecipient, PromoSegment } from '../lib/v112'

const emptySegment: PromoSegment = { minSpentCents: 0, visitedWithinDays: 0, awayForDays: 0, minPoints: 0, withTabBalance: false }

const pill = (c: PromoCampaign) => {
  switch (c.status) {
    case 'draft': return <StatusPill status="queued" label="Draft" />
    case 'sending': return <StatusPill status="progress" label={c.quietNow ? 'Waiting (quiet hours)' : 'Sending'} />
    case 'paused': return <StatusPill status="pending" label="Paused" />
    case 'done': return <StatusPill status="paid" label="Done" />
    case 'cancelled': return <StatusPill status="void" label="Cancelled" />
  }
}

export function Promotions() {
  const [list, setList] = useState<PromoCampaign[] | null>(null)
  const [creating, setCreating] = useState(false)
  const [detail, setDetail] = useState<number | null>(null)
  const [setup, setSetup] = useState(false)
  const [optOut, setOptOut] = useState(false)
  const load = async () => {
    try {
      setList(await api.get<PromoCampaign[]>('/api/v1/promotions'))
    } catch (e: any) {
      toast.error('Could not load promotions', e?.message)
    }
  }
  useEffect(() => { load() }, [])
  // live progress while something is sending
  useEffect(() => {
    if (!list?.some((c) => c.status === 'sending')) return
    const t = setInterval(load, 10_000)
    return () => clearInterval(t)
  }, [list])

  return (
    <div className="space-y-4">
      <Card
        title="Promotions"
        sub="Offers by SMS or WhatsApp to customers who agreed to receive them."
        actions={
          <div className="flex flex-wrap gap-2">
            <Button onClick={() => setOptOut(true)}><UserX size={15} aria-hidden />Opt someone out</Button>
            <Button onClick={() => setSetup(true)}><Settings2 size={15} aria-hidden />Sending setup</Button>
            <Button variant="primary" onClick={() => setCreating(true)}><Plus size={15} aria-hidden />New promotion</Button>
          </div>
        }
        pad={false}
      >
        {!list ? <div className="py-10 flex justify-center"><Spinner /></div> : list.length === 0 ? (
          <EmptyState icon={<Megaphone size={24} />} title="No promotions yet"
            body="Mark customers who agree to offers (Customers → edit → 'OK to send offers'), then create a promotion." />
        ) : (
          <Table head={['Name', 'Channel', 'Status', 'Sent', 'Failed', 'Left', 'Created']}>
            {list.map((c) => (
              <tr key={c.id} className="cursor-pointer hover:bg-surface-muted" onClick={() => setDetail(c.id)}>
                <td className="px-3 py-2.5 font-semibold">{c.name}{c.lastError && <span className="block text-[12px] text-danger-text">{c.lastError}</span>}</td>
                <td className="px-3 py-2.5 uppercase text-[12px] font-bold text-ink-muted">{c.channel}</td>
                <td className="px-3 py-2.5">{pill(c)}</td>
                <td className="px-3 py-2.5 tabular">{c.sent}/{c.total}</td>
                <td className="px-3 py-2.5 tabular">{c.failed || '—'}</td>
                <td className="px-3 py-2.5 tabular">{c.pending || '—'}</td>
                <td className="px-3 py-2.5 text-[12px] text-ink-subtle">{c.createdAt.slice(0, 10)} by {c.createdBy}</td>
              </tr>
            ))}
          </Table>
        )}
      </Card>
      {creating && <CreateModal onClose={() => setCreating(false)} onDone={(id) => { setCreating(false); load(); setDetail(id) }} />}
      {detail !== null && <DetailModal id={detail} onClose={() => { setDetail(null); load() }} />}
      {setup && <SetupModal onClose={() => setSetup(false)} />}
      {optOut && <OptOutModal onClose={() => setOptOut(false)} />}
    </div>
  )
}

function CreateModal({ onClose, onDone }: { onClose: () => void; onDone: (id: number) => void }) {
  const [name, setName] = useState('')
  const [channel, setChannel] = useState<'sms' | 'whatsapp'>('sms')
  const [body, setBody] = useState('Hi {name}! ')
  const [seg, setSeg] = useState<PromoSegment>(emptySegment)
  const [aud, setAud] = useState<PromoAudience | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => {
    const t = setTimeout(() => {
      api.post<PromoAudience>('/api/v1/promotions/audience', { segment: seg, body }).then(setAud).catch(() => setAud(null))
    }, 350)
    return () => clearTimeout(t)
  }, [seg, body])
  const num = (v: string) => Math.max(0, Math.floor(Number(v) || 0))
  const create = async () => {
    setBusy(true)
    setError('')
    try {
      const c = await api.post<PromoCampaign>('/api/v1/promotions', { name, channel, body, segment: seg })
      toast.success('Promotion ready', `${c.total} customers. Check it, then press Send.`)
      onDone(c.id)
    } catch (e: any) {
      setError(e?.message || 'Could not create')
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal open onClose={onClose} title="New promotion" size="lg"
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" disabled={busy || !name.trim() || !body.trim() || !aud?.count} onClick={create}>Create (not sent yet)</Button></>}>
      <div className="space-y-3">
        <div className="grid grid-cols-2 gap-3">
          <Field label="Name (only you see it)"><Input value={name} onChange={(e) => setName(e.target.value)} maxLength={80} placeholder="December offer" /></Field>
          <Field label="Send by">
            <Select value={channel} onChange={(e) => setChannel(e.target.value as 'sms' | 'whatsapp')}>
              <option value="sms">SMS</option>
              <option value="whatsapp">WhatsApp</option>
            </Select>
          </Field>
        </div>
        <Field label="Message" hint="{name} = first name, {points} = loyalty points, {shop} = shop name, {phone} = shop phone">
          <Textarea value={body} onChange={(e) => setBody(e.target.value)} maxLength={900} rows={4} />
        </Field>
        <details className="border border-line rounded-input px-3 py-2">
          <summary className="font-semibold text-sm cursor-pointer">Who gets it (all who agreed, unless you narrow it)</summary>
          <div className="grid grid-cols-2 gap-3 mt-3">
            <Field label="Spent at least (KES, ever)"><MoneyInput value={seg.minSpentCents} onCents={(v) => setSeg({ ...seg, minSpentCents: v })} /></Field>
            <Field label="Bought in the last … days"><Input type="number" min={0} value={seg.visitedWithinDays || ''} onChange={(e) => setSeg({ ...seg, visitedWithinDays: num(e.target.value) })} /></Field>
            <Field label="Not back for … days (win-back)"><Input type="number" min={0} value={seg.awayForDays || ''} onChange={(e) => setSeg({ ...seg, awayForDays: num(e.target.value) })} /></Field>
            <Field label="At least … loyalty points"><Input type="number" min={0} value={seg.minPoints || ''} onChange={(e) => setSeg({ ...seg, minPoints: num(e.target.value) })} /></Field>
          </div>
          <label className="flex items-center gap-2 mt-3 text-sm"><input type="checkbox" checked={seg.withTabBalance} onChange={(e) => setSeg({ ...seg, withTabBalance: e.target.checked })} />Only customers who owe on a tab</label>
        </details>
        {aud && (
          <div className="bg-surface-muted rounded-input px-3 py-2.5 text-sm">
            <p className="font-bold text-ink">{aud.count} customer(s) will get it{channel === 'sms' ? ` · ${aud.segments} SMS each (${aud.count * aud.segments} SMS in total)` : ''}</p>
            {aud.noConsent > 0 && <p className="text-ink-muted">{aud.noConsent} matching customer(s) have not agreed to offers and are left out.</p>}
            {aud.badPhone > 0 && <p className="text-ink-muted">{aud.badPhone} have no valid Kenyan mobile number.</p>}
            <p className="text-[12px] text-ink-subtle mt-2">Preview:</p>
            <pre className="whitespace-pre-wrap text-[13px] text-ink font-sans">{aud.previewBody}</pre>
          </div>
        )}
        {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
      </div>
    </Modal>
  )
}

function DetailModal({ id, onClose }: { id: number; onClose: () => void }) {
  const [c, setC] = useState<PromoCampaign | null>(null)
  const [rcp, setRcp] = useState<PromoRecipient[]>([])
  const [busy, setBusy] = useState(false)
  const load = async () => {
    const r = await api.get<{ campaign: PromoCampaign; recipients: PromoRecipient[] }>(`/api/v1/promotions/${id}`)
    setC(r.campaign)
    setRcp(r.recipients)
  }
  useEffect(() => { load().catch((e) => toast.error('Could not load', e?.message)) }, [id])
  useEffect(() => {
    if (c?.status !== 'sending') return
    const t = setInterval(() => load().catch(() => undefined), 8000)
    return () => clearInterval(t)
  }, [c?.status])
  const act = async (action: 'start' | 'pause' | 'resume' | 'cancel') => {
    if (action === 'start' && c && !window.confirm(`Send "${c.name}" to ${c.total} customer(s) now?`)) return
    if (action === 'cancel' && !window.confirm('Cancel? Messages not sent yet will not go.')) return
    setBusy(true)
    try {
      setC(await api.post<PromoCampaign>(`/api/v1/promotions/${id}/${action}`))
      load()
    } catch (e: any) {
      toast.error('Could not ' + action, e?.message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal open onClose={onClose} title={c?.name ?? 'Promotion'} size="lg"
      footer={c && (
        <>
          {(c.status === 'draft' || c.status === 'paused' || c.status === 'sending') && <Button variant="ghost" disabled={busy} onClick={() => act('cancel')}><X size={15} aria-hidden />Cancel</Button>}
          {c.status === 'sending' && <Button disabled={busy} onClick={() => act('pause')}><Pause size={15} aria-hidden />Pause</Button>}
          {c.status === 'paused' && <Button variant="primary" disabled={busy} onClick={() => act('resume')}><Play size={15} aria-hidden />Resume</Button>}
          {c.status === 'draft' && <Button variant="primary" disabled={busy || !c.providerSet} onClick={() => act('start')}><Send size={15} aria-hidden />Send to {c.total}</Button>}
        </>
      )}>
      {!c ? <div className="py-10 flex justify-center"><Spinner /></div> : (
        <div className="space-y-3">
          <div className="flex flex-wrap gap-2 items-center">{pill(c)}<span className="text-sm text-ink-muted">{c.sent} sent · {c.failed} failed · {c.pending} left of {c.total}</span></div>
          {!c.providerSet && <p className="text-sm text-pending-text font-semibold">Set up {c.channel === 'sms' ? 'SMS' : 'WhatsApp'} sending first (Sending setup).</p>}
          {c.quietNow && c.status === 'sending' && <p className="text-sm text-ink-muted">Quiet hours now: sending resumes in the morning.</p>}
          {c.lastError && <p className="text-sm text-danger-text font-semibold">{c.lastError}</p>}
          <pre className="whitespace-pre-wrap text-[13px] bg-surface-muted rounded-input px-3 py-2 font-sans">{c.body}</pre>
          <Table head={['Customer', 'Phone', 'Status', '']}>
            {rcp.slice(0, 300).map((r) => (
              <tr key={r.phone}>
                <td className="px-3 py-2 text-[13px]">{r.name}</td>
                <td className="px-3 py-2 text-[13px] tabular">{r.phone}</td>
                <td className="px-3 py-2 text-[12px] font-bold uppercase text-ink-muted">{r.status}</td>
                <td className="px-3 py-2 text-[12px] text-danger-text">{r.error}</td>
              </tr>
            ))}
          </Table>
          {rcp.length > 300 && <p className="text-xs text-ink-subtle">Showing 300 of {rcp.length}.</p>}
        </div>
      )}
    </Modal>
  )
}

const SETUP_KEYS = ['sms_username', 'sms_api_key', 'sms_sender_id', 'sms_env', 'promo_quiet_start', 'promo_quiet_end', 'promo_footer'] as const

function SetupModal({ onClose }: { onClose: () => void }) {
  const [v, setV] = useState<Record<string, string> | null>(null)
  const [testPhone, setTestPhone] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    api.get<Record<string, string>>('/api/v1/settings').then((s) => {
      const o: Record<string, string> = {}
      SETUP_KEYS.forEach((k) => { o[k] = String(s[k] ?? '') })
      setV(o)
    }).catch((e) => toast.error('Could not load settings', e?.message))
  }, [])
  const save = async () => {
    if (!v) return
    setBusy(true)
    try {
      const values: Record<string, string> = { ...v }
      if (values.sms_api_key === '__SET__') delete values.sms_api_key // the saved key, shown masked: unchanged
      await api.put('/api/v1/settings', { values })
      toast.success('Saved')
    } catch (e: any) {
      toast.error('Could not save', e?.message)
    } finally {
      setBusy(false)
    }
  }
  const test = async (channel: 'sms' | 'whatsapp') => {
    setBusy(true)
    try {
      await save()
      await api.post('/api/v1/promotions/test', { channel, phone: testPhone })
      toast.success('Test sent', `Check ${testPhone}.`)
    } catch (e: any) {
      toast.error('Test failed', e?.message)
    } finally {
      setBusy(false)
    }
  }
  const set = (k: string) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setV({ ...v!, [k]: e.target.value })
  return (
    <Modal open onClose={onClose} title="Sending setup" size="lg"
      footer={<><Button onClick={onClose}>Close</Button><Button variant="primary" disabled={busy || !v} onClick={save}>Save</Button></>}>
      {!v ? <div className="py-10 flex justify-center"><Spinner /></div> : (
        <div className="space-y-3">
          <p className="text-sm text-ink-muted">
            SMS goes through your own Africa's Talking account (africastalking.com → your app → Settings → API key).
            A sender ID (your shop name on the message) needs approval from Africa's Talking; until then messages come from a shared number.
            WhatsApp uses the connection in Settings → WhatsApp.
          </p>
          <div className="grid grid-cols-2 gap-3">
            <Field label="Africa's Talking username"><Input value={v.sms_username} onChange={set('sms_username')} placeholder="sandbox to test" /></Field>
            <Field label="API key"><Input type="password" value={v.sms_api_key} onChange={set('sms_api_key')} autoComplete="off" /></Field>
            <Field label="Sender ID (optional)"><Input value={v.sms_sender_id} onChange={set('sms_sender_id')} maxLength={11} /></Field>
            <Field label="Account">
              <Select value={v.sms_env || 'production'} onChange={set('sms_env')}>
                <option value="production">Live</option>
                <option value="sandbox">Sandbox (test, nothing really sent)</option>
              </Select>
            </Field>
            <Field label="Quiet from"><Input type="time" value={v.promo_quiet_start} onChange={set('promo_quiet_start')} /></Field>
            <Field label="Quiet until"><Input type="time" value={v.promo_quiet_end} onChange={set('promo_quiet_end')} /></Field>
          </div>
          <Field label="Opt-out line (added to every message)" hint="The law needs an easy way to stop offers. {phone} = shop phone."><Input value={v.promo_footer} onChange={set('promo_footer')} maxLength={120} /></Field>
          <div className="flex flex-wrap items-end gap-2 border-t border-line pt-3">
            <div className="w-48"><Field label="Send a test to"><Input value={testPhone} onChange={(e) => setTestPhone(e.target.value)} placeholder="07…" /></Field></div>
            <Button disabled={busy || !testPhone} onClick={() => test('sms')}>Test SMS</Button>
            <Button disabled={busy || !testPhone} onClick={() => test('whatsapp')}>Test WhatsApp</Button>
          </div>
        </div>
      )}
    </Modal>
  )
}

function OptOutModal({ onClose }: { onClose: () => void }) {
  const [phone, setPhone] = useState('')
  const [busy, setBusy] = useState(false)
  const go = async () => {
    setBusy(true)
    try {
      const r = await api.post<{ customers: number }>('/api/v1/promotions/opt-out', { phone })
      toast.success('Opted out', r.customers ? `${r.customers} customer record(s) will get no more offers.` : 'No customer has that number; nothing will be sent to it.')
      onClose()
    } catch (e: any) {
      toast.error('Could not opt out', e?.message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal open onClose={onClose} title="Stop offers to a number" size="sm"
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" disabled={busy || !phone.trim()} onClick={go}>Stop offers</Button></>}>
      <Field label="Phone number" hint="Every till stops sending offers to it, including promotions already running.">
        <Input value={phone} onChange={(e) => setPhone(e.target.value)} placeholder="0722 000 000" autoFocus />
      </Field>
    </Modal>
  )
}
