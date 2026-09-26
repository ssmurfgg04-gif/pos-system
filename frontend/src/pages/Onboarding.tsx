// Onboarding — guided first run for admins: store → logo → receipt →
// printer → staff. Every step saves through the same endpoints Settings
// uses. Finishing flips onboarding_done; the rotation gate stays as backup.

import { useEffect, useRef, useState } from 'react'
import { api, Role } from '../lib/api'
import { navigate } from '../lib/router'
import { useAuth } from '../stores/auth'
import { useBranding } from '../stores/branding'
import { Button, Card, Field, Input, Select, Spinner } from '../components/ui'
import { toast } from '../stores/toasts'
import { Check, ChevronLeft, ChevronRight, Store } from 'lucide-react'

const STEPS = ['Store', 'Logo', 'Receipt', 'Printer', 'Staff'] as const

export function Onboarding({ onDone }: { onDone: () => void }) {
  const user = useAuth((s) => s.user)
  const [step, setStep] = useState(0)
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState<boolean[]>([false, false, false, false, false])
  const mark = (i: number) => setDone((d) => d.map((v, j) => (j === i ? true : v)))

  const saveSettings = async (values: Record<string, string>) => {
    setBusy(true)
    try {
      await api.put('/api/v1/settings', { values })
      return true
    } catch (e: any) {
      toast.error('Save failed', e?.message)
      return false
    } finally {
      setBusy(false)
    }
  }

  const finish = async () => {
    setBusy(true)
    try {
      await api.put('/api/v1/settings', { values: { onboarding_done: 'true' } })
      await useBranding.getState().load()
      toast.success('Shop ready', 'Karibu — add your products in Inventory, or go sell.')
      // Continue means CONTINUE: land on the main page immediately instead of
      // waiting for a second click on the success banner.
      onDone()
    } catch (e: any) {
      toast.error('Finish failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-h-dvh bg-surface-muted flex items-center justify-center p-4">
      <Card
        title="Set up your shop"
        sub={`Step ${step + 1} of ${STEPS.length} — ${STEPS[step]}`}
        pad
      >
        <div className="min-w-80 max-w-md">
          <ol className="flex gap-1.5 mb-5" aria-label="Setup progress">
            {STEPS.map((s, i) => (
              <li
                key={s}
                title={s}
                className={`h-2 flex-1 rounded-full ${i < step || done[i] ? 'bg-paid-text' : i === step ? 'bg-brand' : 'bg-line'}`}
              />
            ))}
          </ol>

          {step === 0 && <StoreStep onSave={saveSettings} onNext={() => { mark(0); setStep(1) }} busy={busy} />}
          <p className="text-[12px] text-ink-subtle mt-3 text-center">
            Setting up a second till for a shop that already runs LedgerPOS?{' '}
            <button type="button" onClick={() => navigate('/join')} className="font-bold underline decoration-line hover:decoration-ink">
              Use a team join link instead
            </button>{' '}— no setup needed, the shop syncs in.
          </p>
          {step === 1 && <LogoStep onNext={() => { mark(1); setStep(2) }} onBack={() => setStep(0)} />}
          {step === 2 && <ReceiptStep onSave={saveSettings} onNext={() => { mark(2); setStep(3) }} onBack={() => setStep(1)} busy={busy} />}
          {step === 3 && <PrinterStep onSave={saveSettings} onNext={() => { mark(3); setStep(4) }} onBack={() => setStep(2)} busy={busy} />}
          {step === 4 && <StaffStep
            adminId={user?.id ?? 0}
            onNext={() => { mark(4); finish() }}
            onBack={() => setStep(3)}
            busy={busy}
            setBusy={setBusy}
          />}
        </div>
      </Card>
    </div>
  )
}

function Nav({ onBack, onNext, nextLabel, disabled }: { onBack?: () => void; onNext: () => void; nextLabel: string; disabled?: boolean }) {
  return (
    <div className="flex gap-2 mt-4">
      {onBack && <Button variant="ghost" onClick={onBack}><ChevronLeft size={15} strokeWidth={2.25} aria-hidden />Back</Button>}
      <Button variant="primary" className="flex-1" onClick={onNext} disabled={disabled}>
        {nextLabel}<ChevronRight size={15} strokeWidth={2.25} aria-hidden />
      </Button>
    </div>
  )
}

function StoreStep({ onSave, onNext, busy }: { onSave: (v: Record<string, string>) => Promise<boolean>; onNext: () => void; busy: boolean }) {
  const [name, setName] = useState('')
  const [address, setAddress] = useState('')
  const [phone, setPhone] = useState('')
  useEffect(() => {
    api.get<Record<string, string>>('/api/v1/settings').then((s) => {
      if (!name && s.store_name) setName(s.store_name)
      if (!address && s.store_address) setAddress(s.store_address)
      if (!phone && s.store_phone) setPhone(s.store_phone)
    }).catch(() => undefined)
  }, [])
  return (
    <div className="space-y-3">
      <Field label="Store name" hint="Jina la duka — Printed on receipts and shown in the topbar.">
        <Input value={name} onChange={(e) => setName(e.target.value)} autoFocus placeholder="e.g. Zawadi Prints" />
      </Field>
      <Field label="Address / Anwani"><Input value={address} onChange={(e) => setAddress(e.target.value)} placeholder="Street, town — Mtaa, mji" /></Field>
      <Field label="Phone / Simu"><Input value={phone} onChange={(e) => setPhone(e.target.value)} inputMode="tel" placeholder="07XX XXX XXX" /></Field>
      <Nav nextLabel={busy ? 'Saving…' : 'Save & continue'} disabled={busy || !name.trim()} onNext={async () => {
        if (await onSave({ store_name: name.trim(), store_address: address.trim(), store_phone: phone.trim() })) onNext()
      }} />
    </div>
  )
}

function LogoStep({ onNext, onBack }: { onNext: () => void; onBack: () => void }) {
  const reloadBranding = useBranding((s) => s.load)
  const fileRef = useRef<HTMLInputElement>(null)
  const [busy, setBusy] = useState(false)
  const upload = async (file: File) => {
    setBusy(true)
    try {
      const form = new FormData()
      form.append('logo', file)
      await api.form('/api/v1/settings/logo', form)
      await reloadBranding()
      toast.success('Logo uploaded')
    } catch (e: any) {
      toast.error('Upload failed', e?.message)
    } finally {
      setBusy(false)
      if (fileRef.current) fileRef.current.value = ''
    }
  }
  return (
    <div className="space-y-3">
      <p className="text-[13px] text-ink-muted flex items-center gap-2"><Store size={15} strokeWidth={2.25} aria-hidden />Shown on login, the topbar, and (optionally) printed receipts. PNG or JPEG.</p>
      <div className="flex gap-2">
        <Button variant="secondary" onClick={() => fileRef.current?.click()} disabled={busy}>
          {busy ? <Spinner /> : 'Upload logo'}
        </Button>
        <Button variant="ghost" onClick={onNext}>Skip for now</Button>
      </div>
      <input ref={fileRef} type="file" accept="image/png,image/jpeg" className="hidden" aria-label="Brand logo file"
        onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])} />
      <Nav onBack={onBack} nextLabel="Continue" onNext={onNext} />
    </div>
  )
}

function ReceiptStep({ onSave, onNext, onBack, busy }: { onSave: (v: Record<string, string>) => Promise<boolean>; onNext: () => void; onBack: () => void; busy: boolean }) {
  const [footer, setFooter] = useState('Thank you for your business!')
  const [currency, setCurrency] = useState('KES')
  const [tax, setTax] = useState('16')
  return (
    <div className="space-y-3">
      <Field label="Receipt footer"><Input value={footer} onChange={(e) => setFooter(e.target.value)} /></Field>
      <div className="grid grid-cols-2 gap-2">
        <Field label="Currency">
          <Select value={currency} onChange={(e) => setCurrency(e.target.value)}>
            <option value="KES">KES — Kenyan shilling</option>
            <option value="USD">USD — US dollar</option>
            <option value="TZS">TZS — Tanzanian shilling</option>
            <option value="UGX">UGX — Ugandan shilling</option>
          </Select>
        </Field>
        <Field label="VAT %"><Input value={tax} onChange={(e) => setTax(e.target.value.replace(/[^\d.]/g, ''))} inputMode="decimal" /></Field>
      </div>
      <Nav onBack={onBack} nextLabel={busy ? 'Saving…' : 'Save & continue'} disabled={busy} onNext={async () => {
        if (await onSave({ receipt_footer: footer, currency_code: currency, currency_symbol: currency, tax_percent: tax || '0' })) onNext()
      }} />
    </div>
  )
}

function PrinterStep({ onSave, onNext, onBack, busy }: { onSave: (v: Record<string, string>) => Promise<boolean>; onNext: () => void; onBack: () => void; busy: boolean }) {
  const [target, setTarget] = useState('')
  const [width, setWidth] = useState('80')
  const test = async () => {
    try {
      await api.post('/api/v1/settings/test-print')
      toast.success('Test receipt sent')
    } catch (e: any) {
      toast.error('Test print failed', e?.message)
    }
  }
  return (
    <div className="space-y-3">
      <Field label="Printer target" hint="tcp://192.168.1.200:9100 for network, file:///dev/usb/lp0 for USB. Blank = browser printing.">
        <Input value={target} onChange={(e) => setTarget(e.target.value.trim())} placeholder="tcp://192.168.1.200:9100" className="font-mono" />
      </Field>
      <Field label="Paper width">
        <Select value={width} onChange={(e) => setWidth(e.target.value)}>
          <option value="80">80mm (48 columns)</option>
          <option value="58">58mm (32 columns)</option>
        </Select>
      </Field>
      <div className="flex gap-2">
        <Button variant="secondary" onClick={async () => {
          if (await onSave({ printer_target: target, printer_width: width })) test()
        }} disabled={busy}>Save & test print</Button>
      </div>
      <Nav onBack={onBack} nextLabel={busy ? 'Saving…' : 'Save & continue'} disabled={busy} onNext={async () => {
        if (await onSave({ printer_target: target, printer_width: width })) onNext()
      }} />
    </div>
  )
}

function StaffStep({ adminId, onNext, onBack, busy, setBusy }: { adminId: number; onNext: () => void; onBack: () => void; busy: boolean; setBusy: (b: boolean) => void }) {
  const user = useAuth((s) => s.user)
  const [password, setPassword] = useState('')
  const [pin, setPin] = useState('')
  const [cashierName, setCashierName] = useState('')
  const [cashierPin, setCashierPin] = useState('')
  const [roles, setRoles] = useState<Role[]>([])
  useEffect(() => {
    api.get<Role[]>('/api/v1/roles').then(setRoles).catch(() => setRoles([]))
  }, [])
  const mustRotate = !!user?.mustRotate
  const cashierRole = roles.find((r) => r.name === 'Cashier')?.id

  const save = async () => {
    if (mustRotate && (password.length < 6 || !/^\d{4}$/.test(pin))) {
      toast.error('Set a 6+ character password and a 4-digit PIN for yourself')
      return
    }
    setBusy(true)
    try {
      if (mustRotate) {
        // Take over the seeded admin account (clears rotation by construction).
        // The password save kills this session by design, so re-login before
        // setting the PIN.
        const me = await api.get<{ username: string }>('/api/v1/me')
        await api.put(`/api/v1/users/${adminId}/password`, { password })
        const res = await api.post<{ token: string }>('/api/v1/auth/login', {
          username: me.username,
          password,
        })
        localStorage.setItem('pos_token', res.token)
        await api.put(`/api/v1/users/${adminId}/pin`, { pin })
        const fresh = await api.post<{ token: string }>('/api/v1/auth/login', {
          username: me.username,
          password,
        })
        localStorage.setItem('pos_token', fresh.token)
      }
      if (cashierName.trim() && cashierRole) {
        await api.post('/api/v1/users', {
          username: cashierName.trim().toLowerCase().replace(/\s+/g, ''),
          fullName: cashierName.trim(),
          password: `temp-${Date.now().toString(36)}`,
          pin: /^\d{4}$/.test(cashierPin) ? cashierPin : '0000',
          roleId: cashierRole,
        })
        toast.success('Cashier created', 'They set their own password on first login.')
      }
      onNext()
    } catch (e: any) {
      toast.error('Save failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-3">
      {mustRotate ? (
        <>
          <p className="text-[13px] text-ink-muted">
            You're signed in as <strong className="text-ink">{user?.username}</strong> — set your own password + PIN:
          </p>
          <div className="grid grid-cols-2 gap-2">
            <Field label="Admin password"><Input value={password} onChange={(e) => setPassword(e.target.value)} type="password" autoFocus placeholder="6+ characters" /></Field>
            <Field label="Admin PIN"><Input value={pin} onChange={(e) => setPin(e.target.value.replace(/\D/g, '').slice(0, 4))} inputMode="numeric" placeholder="4 digits" /></Field>
          </div>
        </>
      ) : (
        <p className="text-[13px] text-ink-muted">
          Signed in as <strong className="text-ink">{user?.username}</strong> — you're all set. Add your first cashier below, or open the shop now.
        </p>
      )}
      <p className="text-[13px] text-ink-muted pt-1">First cashier (optional — they set their own password on first login):</p>
      <div className="grid grid-cols-2 gap-2">
        <Field label="Name"><Input value={cashierName} onChange={(e) => setCashierName(e.target.value)} placeholder="e.g. Brian" /></Field>
        <Field label="PIN"><Input value={cashierPin} onChange={(e) => setCashierPin(e.target.value.replace(/\D/g, '').slice(0, 4))} inputMode="numeric" placeholder="4 digits" /></Field>
      </div>
      <div className="flex gap-2 mt-4">
        <Button variant="ghost" onClick={onBack}><ChevronLeft size={15} strokeWidth={2.25} aria-hidden />Back</Button>
        <Button variant="primary" className="flex-1" onClick={save} disabled={busy}>
          {busy ? <Spinner /> : <span className="inline-flex items-center gap-2"><Check size={15} strokeWidth={2.5} aria-hidden />Open shop</span>}
        </Button>
      </div>
    </div>
  )
}
