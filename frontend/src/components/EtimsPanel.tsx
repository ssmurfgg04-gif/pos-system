// EtimsPanel — v1.10 Settings → eTIMS. Connect the shop's KRA eTIMS OSCU
// device (through LedgerPOS Cloud), pick the defaults every product falls
// back to, and watch the send queue. Sales are signed in the background;
// checkout never waits on KRA.
import { useEffect, useState } from 'react'
import { api, EtimsClass, EtimsStatus } from '../lib/api'
import { Button, Field, Input, Select, Spinner, StatusPill } from './ui'
import { toast } from '../stores/toasts'
import { ReceiptText, RotateCcw, Search } from 'lucide-react'

export const TAX_TYPES: { code: string; label: string }[] = [
  { code: 'B', label: 'B · Standard VAT 16%' },
  { code: 'E', label: 'E · Reduced VAT 8%' },
  { code: 'C', label: 'C · Zero rated 0%' },
  { code: 'A', label: 'A · Exempt' },
  { code: 'D', label: 'D · Non-VAT (not VAT registered)' },
]

export function EtimsPanel({ kraPin }: { kraPin?: string }) {
  const [st, setSt] = useState<EtimsStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ env: 'sandbox', tin: kraPin || '', bhfId: '00', dvcSrlNo: '', lastInvcNo: '' })
  const [err, setErr] = useState('')

  const load = async () => {
    try {
      setSt(await api.get<EtimsStatus>('/api/v1/etims/status'))
    } catch (e: any) {
      setErr(e?.message || 'Could not load eTIMS status')
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => { void load() }, [])

  const connect = async () => {
    setBusy(true)
    setErr('')
    try {
      await api.post('/api/v1/etims/connect', { ...form, lastInvcNo: Number(form.lastInvcNo) || 0 })
      toast.success('Connected to KRA eTIMS', 'New sales will be signed by KRA.')
      await load()
    } catch (e: any) {
      setErr(e?.message || 'KRA did not accept the device')
    } finally {
      setBusy(false)
    }
  }

  if (loading) return <div className="py-8 flex justify-center"><Spinner /></div>
  const d = st?.device
  const connected = !!d?.connected && st?.enabled

  return (
    <div className="space-y-5 max-w-2xl">
      <div className="flex gap-3 items-start">
        <span className="w-10 h-10 rounded-input bg-brand-soft text-brand flex items-center justify-center shrink-0" aria-hidden>
          <ReceiptText size={18} strokeWidth={2.25} />
        </span>
        <div>
          <h3 className="font-bold text-ink">KRA eTIMS</h3>
          <p className="text-[13px] text-ink-muted">
            Every paid sale is sent to KRA in the background and its receipt becomes a <b>TAX INVOICE</b> with the
            control-unit number, signature and QR code. Returns are sent as credit notes. Checkout never waits on KRA:
            if the internet is down, sales queue and go when it's back.
          </p>
        </div>
      </div>

      {!connected ? (
        <section className="rounded-card border-2 border-line p-4 space-y-3">
          <p className="text-[13px] text-ink">
            You need an <b>OSCU</b> device for this shop from the KRA eTIMS portal (etims.kra.go.ke). Enter its details below.
            Use <b>Test (sandbox)</b> first if KRA gave you test credentials.
          </p>
          <div className="grid sm:grid-cols-2 gap-3">
            <Field label="KRA server">
              <Select value={form.env} onChange={(e) => setForm({ ...form, env: e.target.value })}>
                <option value="sandbox">Test (sandbox)</option>
                <option value="production">Live (production)</option>
              </Select>
            </Field>
            <Field label="Business KRA PIN">
              <Input value={form.tin} onChange={(e) => setForm({ ...form, tin: e.target.value.toUpperCase() })} placeholder="P051234567X" className="font-mono" />
            </Field>
            <Field label="Branch ID" hint="00 for the head office">
              <Input value={form.bhfId} onChange={(e) => setForm({ ...form, bhfId: e.target.value.replace(/\D/g, '').slice(0, 2) })} inputMode="numeric" />
            </Field>
            <Field label="OSCU device serial number">
              <Input value={form.dvcSrlNo} onChange={(e) => setForm({ ...form, dvcSrlNo: e.target.value.trim() })} className="font-mono" />
            </Field>
            <Field label="Last invoice number used (optional)" hint="Only if this device already sent invoices from another system">
              <Input value={form.lastInvcNo} onChange={(e) => setForm({ ...form, lastInvcNo: e.target.value.replace(/\D/g, '') })} inputMode="numeric" />
            </Field>
          </div>
          {(err || st?.cloudError) && <p role="alert" className="text-[13px] font-semibold text-danger-text">{err || st?.cloudError}</p>}
          <Button onClick={connect} disabled={busy || !form.tin || !form.dvcSrlNo}>
            {busy ? <Spinner className="border-t-brand-ink" /> : null}Connect to KRA
          </Button>
        </section>
      ) : (
        <>
          <section className="rounded-card border-2 border-paid-text/30 bg-paid-bg p-4 space-y-1 text-[13px]">
            <div className="flex items-center gap-2">
              <StatusPill status="paid" label={d?.env === 'production' ? 'Connected · Live' : 'Connected · Test'} />
              <span className="font-bold text-ink">{d?.tradeName || d?.tin}</span>
            </div>
            <p className="text-ink-muted">
              PIN {d?.tin} · Branch {d?.bhfId}{d?.branchName ? ` (${d.branchName})` : ''} · SCU {d?.sdcId || '—'} · next invoice no. {d?.nextInvoiceNo}
            </p>
          </section>

          <Defaults st={st!} onSaved={setSt} />

          <section className="rounded-card border-2 border-line p-4 space-y-2">
            <h4 className="font-bold text-ink">Send queue</h4>
            <div className="grid grid-cols-3 gap-3 text-center">
              <Count label="Signed by KRA" n={st!.signed} tone="text-paid-text" />
              <Count label="Waiting" n={st!.pending} tone="text-pending-text" />
              <Count label="Need attention" n={st!.failed} tone={st!.failed ? 'text-danger-text' : 'text-ink'} />
            </div>
            {st!.failed > 0 && (
              <div role="alert" className="rounded-input border border-danger-text/30 bg-danger-bg p-2.5 text-[12.5px] text-ink">
                <b>KRA said:</b> {st!.lastError}
                <p className="mt-1 text-ink-muted">Fix the cause (often a missing item class or a wrong tax type), then send again.</p>
              </div>
            )}
            <div className="flex flex-wrap gap-2">
              <Button size="sm" variant="secondary" onClick={async () => {
                const r = await api.post<{ requeued: number }>('/api/v1/etims/retry', {})
                toast.info('Sending again', `${r.requeued} invoice(s) queued`)
                setTimeout(load, 2500)
              }}>
                <RotateCcw size={14} strokeWidth={2.5} aria-hidden /> Send again now
              </Button>
              <Button size="sm" variant="ghost" onClick={async () => {
                setSt(await api.post<EtimsStatus>('/api/v1/etims/disable', {}))
                toast.info('eTIMS paused', 'New sales will not be sent to KRA until you connect again.')
              }}>
                Pause eTIMS
              </Button>
            </div>
          </section>
        </>
      )}
    </div>
  )
}

function Count({ label, n, tone }: { label: string; n: number; tone: string }) {
  return (
    <div className="rounded-input border border-line bg-surface-muted p-2">
      <p className={`text-xl font-black tabular ${tone}`}>{n}</p>
      <p className="text-[11px] font-semibold text-ink-muted">{label}</p>
    </div>
  )
}

function Defaults({ st, onSaved }: { st: EtimsStatus; onSaved: (s: EtimsStatus) => void }) {
  const [tax, setTax] = useState(st.defaultTax || 'B')
  const [cls, setCls] = useState(st.device?.defaultClsCd || '')
  const [busy, setBusy] = useState(false)
  const save = async (next: { defaultTax?: string; defaultClsCd?: string }) => {
    setBusy(true)
    try {
      onSaved(await api.put<EtimsStatus>('/api/v1/etims/defaults', next))
      toast.success('Saved')
    } catch (e: any) {
      toast.error('Not saved', e?.message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="rounded-card border-2 border-line p-4 space-y-3">
      <h4 className="font-bold text-ink">Defaults for your products</h4>
      <p className="text-[12.5px] text-ink-muted">Each product can override these in Inventory. KRA needs both before it accepts an item.</p>
      <Field label="Tax type">
        <Select value={tax} disabled={busy} onChange={(e) => { setTax(e.target.value); void save({ defaultTax: e.target.value }) }}>
          {TAX_TYPES.map((t) => <option key={t.code} value={t.code}>{t.label}</option>)}
        </Select>
      </Field>
      <div>
        <span className="block text-[13px] font-semibold text-ink-muted mb-1">KRA item class</span>
        {cls && <p className="text-[13px] mb-1.5">Current: <b className="font-mono">{cls}</b></p>}
        <ClassPicker onPick={(c) => { setCls(c.code); void save({ defaultClsCd: c.code }) }} />
        {!cls && <p className="text-[12px] font-semibold text-pending-text mt-1">Pick one: KRA refuses items without a class.</p>}
      </div>
    </section>
  )
}

/** Search KRA's item classification list (cached in the cloud). */
export function ClassPicker({ onPick }: { onPick: (c: EtimsClass) => void }) {
  const [q, setQ] = useState('')
  const [list, setList] = useState<EtimsClass[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const search = async () => {
    setBusy(true)
    setErr('')
    try {
      setList(await api.get<EtimsClass[]>(`/api/v1/etims/classes?q=${encodeURIComponent(q)}`))
    } catch (e: any) {
      setErr(e?.message || 'KRA list unavailable')
    } finally {
      setBusy(false)
    }
  }
  return (
    <div>
      <div className="flex gap-2">
        <Input value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') { e.preventDefault(); void search() } }} placeholder="e.g. clothing, beverages, 5020…" aria-label="Search KRA item classes" />
        <Button size="sm" variant="secondary" onClick={search} disabled={busy}>
          {busy ? <Spinner /> : <Search size={14} strokeWidth={2.5} aria-hidden />} Search
        </Button>
      </div>
      {err && <p className="text-[12px] text-danger-text mt-1">{err}</p>}
      {list && (
        <ul className="mt-2 max-h-56 overflow-y-auto rounded-input border border-line divide-y divide-line">
          {list.length === 0 && <li className="p-2 text-[12.5px] text-ink-muted">No match, try another word.</li>}
          {list.map((c) => (
            <li key={c.code}>
              <button type="button" onClick={() => { onPick(c); setList(null) }} className="w-full text-left p-2 hover:bg-surface-muted text-[12.5px]">
                <span className="font-mono font-bold">{c.code}</span> · {c.name}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
