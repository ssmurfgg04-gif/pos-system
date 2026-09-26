// Settings — the white-label control panel. Every string the customer sees
// lives here. Secrets are masked (__SET__ keeps the stored value). Includes
// Paystack + M-Pesa payments, team sync (multi-till linking), void reason
// admin, printer target + test print, the audit log, and System (backups
// + demo data reset).

import { useEffect, useRef, useState } from 'react'
import {
  api,
  AuditEntry,
  BackupResult,
  OffsiteStatus,
  PaymentConfig,
  TeamSyncStatus,
  UpdateStatus,
  VoidReason,
  backendMode,
} from '../lib/api'
import { resetDemo } from '../demo/backend'
import { useBranding } from '../stores/branding'
import { Button, Card, EmptyState, Field, Input, Select, Spinner, StatusPill, Table, Tabs, Textarea } from '../components/ui'
import { toast } from '../stores/toasts'
import { Printer, DatabaseBackup, HardDriveDownload, RotateCcw, RefreshCw, CreditCard, MonitorSmartphone, Cloud, AlertTriangle } from 'lucide-react'

type SettingsMap = Record<string, string>

// Secret values are echoed masked by the API; sending the mask back keeps
// the stored secret. An empty field never clears a configured secret.
const MASK = '__SET__'

export function Settings() {
  const reloadBranding = useBranding((s) => s.load)
  const [tab, setTab] = useState<'store' | 'payments' | 'printer' | 'team' | 'system' | 'audit'>('store')
  const [values, setValues] = useState<SettingsMap | null>(null)
  const [busy, setBusy] = useState(false)
  const [payConfig, setPayConfig] = useState<PaymentConfig | null>(null)
  const [checkingPay, setCheckingPay] = useState(false)
  const [audit, setAudit] = useState<AuditEntry[] | null>(null)
  const [backups, setBackups] = useState<BackupResult[] | null>(null)
  const [backing, setBacking] = useState(false)
  const [offsite, setOffsite] = useState<OffsiteStatus | null>(null)
  const [update, setUpdate] = useState<UpdateStatus | null>(null)
  const [updating, setUpdating] = useState(false)
  const [demo, setDemo] = useState(false)

  useEffect(() => {
    let alive = true
    backendMode().then((m) => { if (alive) setDemo(m === 'demo') }).catch(() => undefined)
    return () => { alive = false }
  }, [])

  const load = async () => {
    try {
      const next = await api.get<SettingsMap>('/api/v1/settings')
      setValues(next)
      paystackSecretOriginal.current = next.paystack_secret_key ?? ''
    } catch (e: any) {
      toast.error('Load failed', e?.message)
    }
  }
  useEffect(() => { load() }, [])

  // Value of paystack_secret_key as the API returned it (MASK when set) —
  // lets a cleared input restore the mask instead of wiping the key.
  const paystackSecretOriginal = useRef('')

  const checkPaystack = async () => {
    setCheckingPay(true)
    try {
      setPayConfig(await api.get<PaymentConfig>('/api/v1/payments/config'))
    } catch { /* demo backend or offline — hint simply stays hidden */ }
    finally { setCheckingPay(false) }
  }

  useEffect(() => {
    if (tab === 'audit' && !audit) {
      api.get<AuditEntry[]>('/api/v1/audit').then(setAudit).catch(() => setAudit([]))
    }
    if (tab === 'system' && !backups) {
      api.get<BackupResult[]>('/api/v1/system/backups').then(setBackups).catch(() => setBackups([]))
    }
    if (tab === 'system' && !offsite) {
      api.get<OffsiteStatus>('/api/v1/system/offsite').then(setOffsite).catch(() => setOffsite(null))
    }
    if (tab === 'system' && !update) {
      api.get<UpdateStatus>('/api/v1/system/update').then(setUpdate).catch(() => setUpdate(null))
    }
    if (tab === 'payments' && !payConfig) checkPaystack()
  }, [tab])

  const set = (k: string, v: string) => setValues((s) => (s ? { ...s, [k]: v } : s))

  const save = async () => {
    if (!values) return
    setBusy(true)
    try {
      const next = await api.put<SettingsMap>('/api/v1/settings', { values })
      setValues(next)
      await reloadBranding()
      toast.success('Settings saved')
    } catch (e: any) {
      toast.error('Save failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  if (!values) {
    return <div className="py-16 flex justify-center"><Spinner className="w-7 h-7 border-4" /></div>
  }

  return (
    <div className="space-y-4">
      <Card title="Settings" sub="Everything here is customer-visible branding or money handling" pad={false}>
        <div className="px-4 py-3 flex flex-wrap gap-2 items-center justify-between">
          <Tabs
            tabs={[
              { key: 'store' as const, label: 'Store' },
              { key: 'payments' as const, label: 'Payments' },
              { key: 'printer' as const, label: 'Printer' },
              { key: 'team' as const, label: 'Team' },
              { key: 'system' as const, label: 'System' },
              { key: 'audit' as const, label: 'Audit log' },
            ]}
            value={tab}
            onChange={setTab}
          />
          {tab !== 'audit' && tab !== 'team' && (
            <Button variant="primary" size="sm" onClick={save} disabled={busy}>
              {busy ? <Spinner className="border-t-brand-ink" /> : 'Save changes'}
            </Button>
          )}
        </div>

        {tab === 'store' && (
          <div className="px-4 pb-4 sm:px-5 grid sm:grid-cols-2 gap-3">
            <Field label="App name" hint="Shown in the browser title and login screen">
              <Input value={values.app_name ?? ''} onChange={(e) => set('app_name', e.target.value)} />
            </Field>
            <Field label="Store name" hint="Printed on receipts and shown in the topbar">
              <Input value={values.store_name ?? ''} onChange={(e) => set('store_name', e.target.value)} />
            </Field>
            <Field label="Store address">
              <Input value={values.store_address ?? ''} onChange={(e) => set('store_address', e.target.value)} />
            </Field>
            <Field label="Store phone">
              <Input value={values.store_phone ?? ''} onChange={(e) => set('store_phone', e.target.value)} />
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Currency symbol">
                <Input value={values.currency_symbol ?? ''} onChange={(e) => set('currency_symbol', e.target.value)} />
              </Field>
              <Field label="Currency code">
                <Input value={values.currency_code ?? ''} onChange={(e) => set('currency_code', e.target.value)} />
              </Field>
              <Field label="VAT percent">
                <Input value={values.tax_percent ?? '16'} onChange={(e) => set('tax_percent', e.target.value.replace(/[^\d.]/g, ''))} inputMode="decimal" />
              </Field>
              <Field label="VAT mode">
                <Select value={values.tax_included ?? 'true'} onChange={(e) => set('tax_included', e.target.value)}>
                  <option value="true">Included in prices</option>
                  <option value="false">Added at checkout</option>
                </Select>
              </Field>
              <Field label="Low-stock alert threshold">
                <Input value={values.low_stock_threshold ?? '5'} onChange={(e) => set('low_stock_threshold', e.target.value.replace(/\D/g, ''))} inputMode="numeric" />
              </Field>
            </div>
            <Field label="Brand color" hint="Buttons and highlights across the app">
              <div className="flex gap-2">
                <input
                  type="color"
                  value={/^#[0-9a-fA-F]{6}$/.test(values.brand_color ?? '') ? values.brand_color : '#10B981'}
                  onChange={(e) => set('brand_color', e.target.value)}
                  className="w-14 h-11 border-2 border-line-strong rounded-input cursor-pointer bg-surface"
                  aria-label="Brand color picker"
                />
                <Input value={values.brand_color ?? ''} onChange={(e) => set('brand_color', e.target.value)} className="flex-1" />
              </div>
            </Field>
            <Field label="Receipt footer" hint="Printed at the bottom of every receipt">
              <Textarea value={values.receipt_footer ?? ''} onChange={(e) => set('receipt_footer', e.target.value)} />
            </Field>
            <Field label="Brand logo" hint="PNG or JPEG, 2 MB max. Shown on login and the topbar.">
              <BrandLogoField />
            </Field>

            <VoidReasonsAdmin />
          </div>
        )}

        {tab === 'payments' && (
          <div className="px-4 pb-4 sm:px-5 grid sm:grid-cols-2 gap-3">
            <div className="sm:col-span-2 flex flex-wrap items-start justify-between gap-2 bg-surface-muted border-2 border-line rounded-input p-3 text-[13px] text-ink-muted">
              <p className="min-w-56 flex-1">
                <strong className="text-ink inline-flex items-center gap-1.5"><CreditCard size={15} strokeWidth={2.25} aria-hidden />Paystack — cards &amp; M-Pesa.</strong>{' '}
                Paste your Paystack keys below and save. Start with <code className="font-mono text-ink">sk_test</code> to trial,
                switch to <code className="font-mono text-ink">sk_live</code> when you go live.
              </p>
              <div className="flex items-center gap-2">
                {payConfig ? (
                  <StatusPill
                    status={payConfig.paystack.configured ? 'paid' : 'pending'}
                    label={payConfig.paystack.configured ? 'Connected — secret key on file' : 'Not configured — no secret key yet'}
                  />
                ) : (
                  <StatusPill status="info" label="Connection state unknown" />
                )}
                <Button size="sm" variant="ghost" onClick={checkPaystack} disabled={checkingPay}>
                  {checkingPay ? <Spinner className="border-t-brand-ink" /> : <RefreshCw size={13} strokeWidth={2.5} aria-hidden />}
                  Check
                </Button>
              </div>
            </div>
            {payConfig && (
              <p className="sm:col-span-2 text-[12px] text-ink-subtle -mt-1">
                Live from the till: Paystack {payConfig.paystack.enabled ? 'enabled' : 'disabled'} at checkout ·
                currency <strong className="text-ink-muted">{payConfig.paystack.currency || '—'}</strong> ·
                callback <span className="font-mono">{payConfig.paystack.callbackUrl || '—'}</span>
              </p>
            )}
            <label className="flex items-center gap-2 min-h-11 text-sm font-semibold text-ink sm:col-span-2">
              <input
                type="checkbox"
                checked={(values.paystack_enabled ?? 'false') === 'true'}
                onChange={(e) => set('paystack_enabled', String(e.target.checked))}
                className="w-5 h-5 accent-[#10B981]"
              />
              Accept card &amp; M-Pesa payments via Paystack
            </label>
            <Field label="Public key" hint="Safe for the browser — starts with pk_">
              <Input value={values.paystack_public_key ?? ''} onChange={(e) => set('paystack_public_key', e.target.value.trim())} placeholder="pk_test_…" className="font-mono" />
            </Field>
            <Field
              label="Secret key"
              hint={values.paystack_secret_key === MASK ? 'A key is stored — type a new one to replace it; clearing keeps it.' : 'Starts with sk_. Stored only on this machine.'}
            >
              <Input
                type="password"
                value={values.paystack_secret_key === MASK ? '' : values.paystack_secret_key ?? ''}
                placeholder={values.paystack_secret_key === MASK ? '__SET__ — configured' : 'not configured — paste sk_ key'}
                onChange={(e) => {
                  const v = e.target.value
                  set('paystack_secret_key', v.trim() === '' ? paystackSecretOriginal.current : v)
                }}
                className="font-mono"
                autoComplete="off"
              />
            </Field>
            <Field label="Currency" hint="Paystack charge currency, e.g. KES, GHS, NGN">
              <Input value={values.paystack_currency ?? 'KES'} onChange={(e) => set('paystack_currency', e.target.value.toUpperCase())} placeholder="KES" className="font-mono uppercase" />
            </Field>
            <Field label="Callback URL" hint="Where the customer lands after paying online">
              <Input value={values.paystack_callback_url ?? 'https://awesomeposs.netlify.app/'} onChange={(e) => set('paystack_callback_url', e.target.value.trim())} placeholder="https://awesomeposs.netlify.app/" />
            </Field>
            <div className="sm:col-span-2 flex items-start gap-2 bg-info-bg border-2 border-info-text/30 rounded-input p-3 text-[12px] text-info-text font-semibold">
              <MonitorSmartphone size={15} strokeWidth={2.25} className="shrink-0 mt-0.5" aria-hidden />
              <p>
                The secret key is stored only on this machine and never synced or exported.
                Cards &amp; M-Pesa via Paystack are charged from this till.
              </p>
            </div>

            <div className="sm:col-span-2 border-t-2 border-line pt-3 mt-1">
              <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">M-Pesa (Daraja)</p>
              <p className="text-[13px] text-ink-muted">
                <strong className="text-ink">M-Pesa setup.</strong> The payment provider is swappable:
                run in <strong>mock</strong> for demos/training, switch to <strong>sandbox</strong> to test with Safaricom,
                or <strong>production</strong> when you go live. Manual receipt-code entry always works, even with no provider configured.
              </p>
            </div>
            <Field label="Provider environment">
              <Select value={values.mpesa_env ?? 'mock'} onChange={(e) => set('mpesa_env', e.target.value)}>
                <option value="mock">Mock (demo / training)</option>
                <option value="sandbox">Daraja sandbox</option>
                <option value="production">Daraja production</option>
              </Select>
            </Field>
            <Field label="Default M-Pesa mode at checkout">
              <Select value={values.payment_mode ?? 'auto'} onChange={(e) => set('payment_mode', e.target.value)}>
                <option value="auto">Auto — STK push with manual fallback</option>
                <option value="stk">STK push only</option>
                <option value="manual">Manual receipt code only</option>
              </Select>
            </Field>
            <Field label="Till number" hint="Shown to customers when paying manually">
              <Input value={values.till_number ?? ''} onChange={(e) => set('till_number', e.target.value)} inputMode="numeric" />
            </Field>
            <Field label="Paybill number">
              <Input value={values.paybill_number ?? ''} onChange={(e) => set('paybill_number', e.target.value)} inputMode="numeric" />
            </Field>
            <Field label="Business shortcode (Daraja)">
              <Input value={values.mpesa_shortcode ?? ''} onChange={(e) => set('mpesa_shortcode', e.target.value)} inputMode="numeric" />
            </Field>
            <Field label="Passkey" hint="Daraja Lipa Na M-Pesa passkey">
              <Input type="password" value={values.mpesa_passkey ?? ''} onChange={(e) => set('mpesa_passkey', e.target.value)} />
            </Field>
            <Field label="Consumer key">
              <Input value={values.mpesa_consumer_key ?? ''} onChange={(e) => set('mpesa_consumer_key', e.target.value)} />
            </Field>
            <Field label="Consumer secret">
              <Input type="password" value={values.mpesa_consumer_secret ?? ''} onChange={(e) => set('mpesa_consumer_secret', e.target.value)} />
            </Field>
            <Field label="Callback URL" hint="Optional on LAN — the server also polls stkpushquery every 5s">
              <Input value={values.mpesa_callback_url ?? ''} onChange={(e) => set('mpesa_callback_url', e.target.value)} placeholder="https://yourdomain.example/api/v1/payments/mpesa/callback" />
            </Field>
            <div className="grid grid-cols-2 gap-3">
              <Field label="Mock delay (ms)" hint="Demo time before auto-success">
                <Input value={values.mpesa_mock_delay_ms ?? '4000'} onChange={(e) => set('mpesa_mock_delay_ms', e.target.value.replace(/\D/g, ''))} inputMode="numeric" />
              </Field>
              <Field label="Mock result code" hint="0 = success; e.g. 1032 simulates cancel">
                <Input value={values.mpesa_mock_result_code ?? '0'} onChange={(e) => set('mpesa_mock_result_code', e.target.value.replace(/\D/g, ''))} inputMode="numeric" />
              </Field>
            </div>
            <p className="sm:col-span-2 text-[12px] text-ink-subtle">
              Secrets show as <code className="font-mono">__SET__</code> after saving — that value means “keep the stored secret”.
            </p>
          </div>
        )}

        {tab === 'printer' && (
          <div className="px-4 pb-4 sm:px-5 grid sm:grid-cols-2 gap-3">
            <div className="sm:col-span-2 bg-surface-muted border-2 border-line rounded-input p-3 text-[13px] text-ink-muted">
              <p>Network thermal printers: <code className="font-mono text-ink">tcp://192.168.1.200:9100</code>. USB: <code className="font-mono text-ink">file:///dev/usb/lp0</code>. Leave blank to disable printing (receipts remain printable from the browser).</p>
            </div>
            <Field label="Printer target">
              <Input value={values.printer_target ?? ''} onChange={(e) => set('printer_target', e.target.value)} placeholder="tcp://192.168.1.200:9100" />
            </Field>
            <Field label="Paper width">
              <Select value={values.printer_width ?? '80'} onChange={(e) => set('printer_width', e.target.value)}>
                <option value="80">80mm (48 columns)</option>
                <option value="58">58mm (32 columns)</option>
              </Select>
            </Field>
            <label className="flex items-center gap-2 min-h-11 text-sm font-semibold text-ink sm:col-span-2">
              <input
                type="checkbox"
                checked={(values.auto_print_receipts ?? 'true') === 'true'}
                onChange={(e) => set('auto_print_receipts', String(e.target.checked))}
                className="w-5 h-5 accent-[#10B981]"
              />
              Auto-print receipts when payment completes
            </label>
            <div className="sm:col-span-2 flex flex-wrap gap-2">
              <Button
                variant="secondary"
                onClick={async () => {
                  try {
                    await api.post('/api/v1/settings/test-print')
                    toast.success('Test receipt sent', values.printer_target)
                  } catch (e: any) {
                    toast.error('Test print failed', e?.message)
                  }
                }}
              >
                <Printer size={14} strokeWidth={2.5} aria-hidden />
                Send test print
              </Button>
              <Button
                variant="secondary"
                onClick={async () => {
                  try {
                    await api.post('/api/v1/printer/kick')
                    toast.success('Drawer kicked', 'Till should have popped')
                  } catch (e: any) {
                    toast.error('Kick failed', e?.message)
                  }
                }}
              >
                Kick cash drawer
              </Button>
            </div>
            <label className="flex items-center gap-2 min-h-11 text-sm font-semibold text-ink sm:col-span-2">
              <input
                type="checkbox"
                checked={(values.receipt_logo ?? 'true') === 'true'}
                onChange={(e) => set('receipt_logo', String(e.target.checked))}
                className="w-5 h-5 accent-[#10B981]"
              />
              Print brand logo on receipts (when a logo is uploaded)
            </label>
          </div>
        )}

        {tab === 'team' && <TeamSyncPanel />}

        {tab === 'system' && (
          <div className="px-4 pb-4 sm:px-5 grid sm:grid-cols-2 gap-3">
            <div className="sm:col-span-2 bg-surface-muted border-2 border-line rounded-input p-3 text-[13px] text-ink-muted">
              <p><strong className="text-ink">Backups.</strong> A consistent snapshot of the database is written to the
              <code className="font-mono text-ink"> backups/</code> folder daily at 02:00 (SQLite <code className="font-mono text-ink">VACUUM INTO</code> —
              safe while sales are running). Copy the folder to a USB drive or cloud folder for off-site protection.</p>
            </div>
            <Field label="Automatic daily backup">
              <Select value={values.backup_auto ?? 'true'} onChange={(e) => set('backup_auto', e.target.value)}>
                <option value="true">Enabled — daily at 02:00</option>
                <option value="false">Disabled</option>
              </Select>
            </Field>
            <Field label="Keep last N snapshots">
              <Input value={values.backup_keep ?? '7'} onChange={(e) => set('backup_keep', e.target.value.replace(/\D/g, ''))} inputMode="numeric" />
            </Field>
            <div className="sm:col-span-2 flex flex-wrap gap-2">
              <Button
                variant="primary"
                disabled={backing}
                onClick={async () => {
                  setBacking(true)
                  try {
                    const res = await api.post<BackupResult>('/api/v1/system/backup')
                    toast.success('Backup created', `${res.file} (${(res.bytes / 1024).toFixed(0)} KB)`)
                    setBackups(await api.get<BackupResult[]>('/api/v1/system/backups'))
                  } catch (e: any) {
                    toast.error('Backup failed', e?.message)
                  } finally {
                    setBacking(false)
                  }
                }}
              >
                {backing ? <Spinner className="border-t-brand-ink" /> : <DatabaseBackup size={15} strokeWidth={2.5} aria-hidden />}
                Back up now
              </Button>
              {demo && (
                <Button
                  variant="ghost"
                  className="text-danger-text border-2 border-danger-text/40 hover:bg-danger-bg ml-2"
                  onClick={() => {
                    if (!confirm('Reset all demo data back to the seeded shop? Your demo orders and changes will be lost.')) return
                    resetDemo()
                    toast.success('Demo data reset', 'Reloading…')
                    window.setTimeout(() => location.reload(), 700)
                  }}
                >
                  <RotateCcw size={15} strokeWidth={2.5} aria-hidden />
                  Reset demo data
                </Button>
              )}
            </div>
            <div className="sm:col-span-2">
              <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">Stored snapshots</p>
              {!backups ? (
                <div className="py-6 flex justify-center"><Spinner /></div>
              ) : backups.length === 0 ? (
                <p className="text-sm text-ink-muted">No snapshots yet — take one now or wait for the nightly run.</p>
              ) : (
                <Table head={['Snapshot', 'Size', 'When']}>
                  {backups.map((b) => (
                    <tr key={b.file}>
                      <td className="px-3 py-2 font-mono text-[12px] text-ink">{b.file}</td>
                      <td className="px-3 py-2 tabular text-ink-muted">{(b.bytes / 1024).toFixed(0)} KB</td>
                      <td className="px-3 py-2 text-[12px] text-ink-subtle">{new Date(b.at).toLocaleString()}</td>
                    </tr>
                  ))}
                </Table>
              )}
            </div>
            <p className="sm:col-span-2 text-[12px] text-ink-subtle flex items-start gap-1.5">
              <HardDriveDownload size={13} strokeWidth={2.5} className="shrink-0 mt-0.5" aria-hidden />
              Snapshots are full SQLite databases — copy the backups folder anywhere and the app can restore from it directly.
            </p>
            <div className="sm:col-span-2 border-t-2 border-line pt-3">
              <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">Software updates</p>
              {!update ? (
                <div className="py-4 flex justify-center"><Spinner /></div>
              ) : update.updateAvailable ? (
                <div className="space-y-2">
                  <p className="text-[13px] font-bold text-pending-text">Version {update.latest} is ready (this box: {update.current || 'dev'}).</p>
                  {update.notes && <p className="text-[12px] text-ink-muted whitespace-pre-wrap">{update.notes.slice(0, 500)}</p>}
                  <div className="flex flex-wrap gap-2">
                    <Button size="sm" variant="primary" disabled={updating} onClick={async () => {
                      setUpdating(true)
                      try {
                        await api.post('/api/v1/system/update/download')
                        setUpdate(await api.get<UpdateStatus>('/api/v1/system/update'))
                        toast.success('Update downloaded', 'Install when the till is quiet.')
                      } catch (e: any) {
                        toast.error('Download failed', e?.message)
                      } finally {
                        setUpdating(false)
                      }
                    }}>
                      {updating ? <Spinner /> : 'Download'}
                    </Button>
                    {update.staged && (
                      <Button size="sm" variant="secondary" disabled={updating} onClick={async () => {
                        if (!confirm('Install the update now? The app will close to finish installing — finish the current sale first.')) return
                        setUpdating(true)
                        try {
                          await api.post('/api/v1/system/update/install')
                          toast.success('Installing', 'The app is closing to finish the update.')
                        } catch (e: any) {
                          toast.error('Install failed', e?.message)
                        } finally {
                          setUpdating(false)
                        }
                      }}>
                        Install now
                      </Button>
                    )}
                    <Button size="sm" variant="ghost" disabled={updating} onClick={async () => {
                      setUpdating(true)
                      try {
                        setUpdate(await api.post<UpdateStatus>('/api/v1/system/update/refresh'))
                      } catch (e: any) {
                        toast.error('Check failed', e?.message)
                      } finally {
                        setUpdating(false)
                      }
                    }}>
                      Check again
                    </Button>
                  </div>
                </div>
              ) : (
                <div className="flex items-center gap-2">
                  <p className="text-[13px] text-ink-muted">Up to date{update.latest ? ` (${update.latest})` : ''}.</p>
                  <Button size="sm" variant="ghost" disabled={updating} onClick={async () => {
                    setUpdating(true)
                    try {
                      setUpdate(await api.post<UpdateStatus>('/api/v1/system/update/refresh'))
                    } catch (e: any) {
                      toast.error('Check failed', e?.message)
                    } finally {
                      setUpdating(false)
                    }
                  }}>
                    Check again
                  </Button>
                </div>
              )}
              {update && update.lastError && <p className="text-[12px] text-danger-text mt-1">Last check error: {update.lastError}</p>}
            </div>
            <div className="sm:col-span-2 border-t-2 border-line pt-3">
              <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">Off-site backup (encrypted, automatic)</p>
              <p className="text-[13px] text-ink-muted mb-3">
                Every snapshot is encrypted on this machine and pushed to your own Supabase project
                (free tier). Uploads retry by themselves — nobody has to be around.
                Use one project per shop, so a leaked key only ever opens that shop.
                <strong className="text-danger-text"> Keep the passphrase somewhere safe: without it the copies cannot be opened.</strong>
              </p>
            </div>
            <Field label="Off-site backup">
              <Select value={values.offsite_enabled ?? 'false'} onChange={(e) => set('offsite_enabled', e.target.value)}>
                <option value="false">Disabled</option>
                <option value="true">Enabled — push after every snapshot</option>
              </Select>
            </Field>
            <Field label="Keep last N remote copies">
              <Input value={values.offsite_keep ?? '14'} onChange={(e) => set('offsite_keep', e.target.value.replace(/\D/g, ''))} inputMode="numeric" />
            </Field>
            <Field label="Project URL">
              <Input value={values.offsite_endpoint ?? ''} onChange={(e) => set('offsite_endpoint', e.target.value.trim())} placeholder="https://xyzcompany.supabase.co" className="font-mono" />
            </Field>
            <Field label="Bucket">
              <Input value={values.offsite_bucket ?? ''} onChange={(e) => set('offsite_bucket', e.target.value.trim())} placeholder="ledgerpos" className="font-mono" />
            </Field>
            <Field label="Key prefix">
              <Input value={values.offsite_prefix ?? ''} onChange={(e) => set('offsite_prefix', e.target.value.trim())} placeholder="Defaults to this machine's name" className="font-mono" />
            </Field>
            <Field label="Service role key">
              <Input value={values.offsite_secret_key ?? ''} onChange={(e) => set('offsite_secret_key', e.target.value)} type="password" className="font-mono" />
            </Field>
            <Field label="Backup passphrase" hint="Encrypts every copy. Shows as __SET__ once saved — write it down now.">
              <Input value={values.offsite_passphrase ?? ''} onChange={(e) => set('offsite_passphrase', e.target.value)} type="password" className="font-mono" />
            </Field>
            <div className="sm:col-span-2">
              <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">Upload status</p>
              {!offsite ? (
                <div className="py-4 flex justify-center"><Spinner /></div>
              ) : !offsite.enabled ? (
                <p className="text-sm text-ink-muted">Off-site is disabled — enable it above and save.</p>
              ) : (
                <div className="text-[13px] space-y-1">
                  {offsite.pending > 0 && <p className="font-bold text-pending-text">{offsite.pending} upload(s) queued — retrying automatically.</p>}
                  {offsite.lastOk && <p className="text-paid-text">Last upload: <span className="font-mono text-[12px]">{offsite.lastOk}</span> ({offsite.lastAt ? new Date(offsite.lastAt).toLocaleString() : ''})</p>}
                  {offsite.lastError && <p className="text-danger-text font-semibold">Last error: {offsite.lastError}</p>}
                  {!offsite.lastOk && !offsite.lastError && offsite.pending === 0 && <p className="text-ink-muted">No uploads yet — take a backup above.</p>}
                  <Button size="sm" variant="ghost" onClick={() => api.get<OffsiteStatus>('/api/v1/system/offsite').then(setOffsite).catch(() => undefined)}>Refresh status</Button>
                </div>
              )}
            </div>
          </div>
        )}

        {tab === 'audit' && (
          !audit ? (
            <div className="py-12 flex justify-center"><Spinner /></div>
          ) : audit.length === 0 ? (
            <p className="px-4 pb-4 text-sm text-ink-muted">No audit entries yet.</p>
          ) : (
            <Table head={['When', 'Who', 'Action', 'Entity', 'Details']}>
              {audit.map((a) => (
                <tr key={a.id}>
                  <td className="px-3 py-2 text-[12px] text-ink-subtle whitespace-nowrap">{new Date(a.createdAt).toLocaleString()}</td>
                  <td className="px-3 py-2 text-[13px] font-semibold text-ink">{a.username || 'system'}</td>
                  <td className="px-3 py-2"><span className="font-mono text-[11px] bg-surface-muted border border-line rounded px-1.5 py-0.5">{a.action}</span></td>
                  <td className="px-3 py-2 text-[12px] text-ink-muted">{a.entity}{a.entityId ? ` #${a.entityId}` : ''}</td>
                  <td className="px-3 py-2 text-[12px] text-ink-muted max-w-64 truncate">{a.details}</td>
                </tr>
              ))}
            </Table>
          )
        )}
      </Card>
    </div>
  )
}

// Brand logo upload (Store tab). PNG/JPEG ≤2 MB; preview busts cache per
// upload so the new mark shows immediately. Server validates magic bytes.
function BrandLogoField() {
  const branding = useBranding((s) => s.branding)
  const reloadBranding = useBranding((s) => s.load)
  const [busy, setBusy] = useState(false)
  const [tick, setTick] = useState(0)
  const fileRef = useRef<HTMLInputElement>(null)
  const url = branding.brand_logo_url ? `${branding.brand_logo_url}?t=${tick}` : ''
  const upload = async (file: File) => {
    setBusy(true)
    try {
      const form = new FormData()
      form.append('logo', file)
      await api.form('/api/v1/settings/logo', form)
      await reloadBranding()
      setTick((t) => t + 1)
      toast.success('Brand logo updated')
    } catch (e: any) {
      toast.error('Logo upload failed', e?.message)
    } finally {
      setBusy(false)
      if (fileRef.current) fileRef.current.value = ''
    }
  }
  const remove = async () => {
    setBusy(true)
    try {
      await api.del('/api/v1/settings/logo')
      await reloadBranding()
      setTick((t) => t + 1)
      toast.success('Brand logo removed')
    } catch (e: any) {
      toast.error('Remove failed', e?.message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="flex items-center gap-3">
      <div className="w-14 h-14 rounded-input bg-surface-muted border-2 border-line-strong flex items-center justify-center overflow-hidden shrink-0">
        {url
          ? <img src={url} alt="Brand logo" className="w-full h-full object-contain" />
          : <span className="text-ink-subtle text-xs font-bold">none</span>}
      </div>
      <div className="flex flex-wrap gap-2">
        <Button variant="secondary" size="sm" onClick={() => fileRef.current?.click()} disabled={busy}>
          Upload
        </Button>
        {url && (
          <Button variant="secondary" size="sm" onClick={remove} disabled={busy}>
            Remove
          </Button>
        )}
        <input
          ref={fileRef}
          type="file"
          accept="image/png,image/jpeg"
          className="hidden"
          aria-label="Brand logo file"
          onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])}
        />
      </div>
    </div>
  )
}

// ---- Void reasons admin (Store tab) ----
// The catalogue cashiers pick from when voiding an order. Lives in the
// settings API (syncs to every till) — add / rename / toggle, no delete so
// audit-trail labels never disappear from old orders.

function VoidReasonsAdmin() {
  const [reasons, setReasons] = useState<VoidReason[] | null>(null)
  const [label, setLabel] = useState('')
  const [editingId, setEditingId] = useState<number | null>(null)
  const [editLabel, setEditLabel] = useState('')
  const [busy, setBusy] = useState(false)

  const load = async () => {
    try {
      setReasons(await api.get<VoidReason[]>('/api/v1/void-reasons?all=true'))
    } catch (e: any) {
      setReasons([])
      toast.error('Void reasons failed to load', e?.message)
    }
  }
  useEffect(() => { load() }, [])

  const add = async () => {
    const v = label.trim()
    if (!v) return
    setBusy(true)
    try {
      await api.post('/api/v1/void-reasons', { label: v })
      toast.success('Reason added', v)
      setLabel('')
      await load()
    } catch (e: any) {
      toast.error('Add failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  const rename = async (r: VoidReason) => {
    const v = editLabel.trim()
    if (!v) return
    setBusy(true)
    try {
      await api.put(`/api/v1/void-reasons/${r.id}`, { label: v, active: r.active })
      toast.success('Reason renamed', `${r.label} → ${v}`)
      setEditingId(null)
      await load()
    } catch (e: any) {
      toast.error('Rename failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  const toggle = async (r: VoidReason) => {
    setBusy(true)
    try {
      await api.put(`/api/v1/void-reasons/${r.id}`, { label: r.label, active: !r.active })
      toast.success(r.active ? 'Reason hidden' : 'Reason shown', r.label)
      await load()
    } catch (e: any) {
      toast.error('Update failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="sm:col-span-2 border-t-2 border-line pt-3 mt-1">
      <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">Void reasons</p>
      <p className="text-[13px] text-ink-muted mb-3">
        The list cashiers pick from when voiding an order. Inactive reasons stay on past orders but
        can&apos;t be chosen at the till. Changes sync to every linked till.
      </p>
      {!reasons ? (
        <div className="py-4 flex justify-center"><Spinner /></div>
      ) : reasons.length === 0 ? (
        <p className="text-sm text-ink-muted mb-3">No reasons yet — add the first one below.</p>
      ) : (
        <Table head={['Reason', 'Status', '']}>
          {reasons.map((r) => (
            <tr key={r.id} className={r.active ? '' : 'opacity-60'}>
              <td className="px-3 py-2">
                {editingId === r.id ? (
                  <Input
                    value={editLabel}
                    onChange={(e) => setEditLabel(e.target.value)}
                    onKeyDown={(e) => { if (e.key === 'Enter') rename(r); if (e.key === 'Escape') setEditingId(null) }}
                    autoFocus
                    aria-label={`Rename reason ${r.label}`}
                  />
                ) : (
                  <span className="text-[13px] font-semibold text-ink">{r.label}</span>
                )}
              </td>
              <td className="px-3 py-2">
                <StatusPill status={r.active ? 'paid' : 'void'} label={r.active ? 'Active' : 'Hidden'} />
              </td>
              <td className="px-3 py-2 text-right whitespace-nowrap">
                {editingId === r.id ? (
                  <span className="inline-flex items-center gap-1">
                    <Button size="sm" variant="primary" onClick={() => rename(r)} disabled={busy || !editLabel.trim()}>Save</Button>
                    <Button size="sm" variant="ghost" onClick={() => setEditingId(null)}>Cancel</Button>
                  </span>
                ) : (
                  <span className="inline-flex items-center gap-1">
                    <Button size="sm" variant="ghost" onClick={() => { setEditingId(r.id); setEditLabel(r.label) }}>Rename</Button>
                    <Button size="sm" variant="ghost" onClick={() => toggle(r)} disabled={busy}>
                      {r.active ? 'Hide' : 'Show'}
                    </Button>
                  </span>
                )}
              </td>
            </tr>
          ))}
        </Table>
      )}
      <form
        className="flex flex-wrap gap-2 mt-3"
        onSubmit={(e) => { e.preventDefault(); add() }}
      >
        <Input
          value={label}
          onChange={(e) => setLabel(e.target.value)}
          placeholder="e.g. Wrong item scanned, Customer changed mind…"
          className="flex-1 min-w-56"
          aria-label="New void reason"
        />
        <Button type="submit" variant="primary" disabled={busy || !label.trim()}>Add reason</Button>
      </form>
    </div>
  )
}

// ---- Team sync (Team tab) ----
// Links this till to a Supabase project so catalogues, customers, orders and
// settings flow between tills automatically. Config lives on this device;
// the team code is the shared secret other tills join with.

function TeamSyncPanel() {
  const [status, setStatus] = useState<TeamSyncStatus | null>(null)
  const [loadError, setLoadError] = useState('')
  const [projectUrl, setProjectUrl] = useState('')
  const [serviceKey, setServiceKey] = useState('')
  const [teamCode, setTeamCode] = useState('')
  const [enabled, setEnabled] = useState(false)
  const [busy, setBusy] = useState(false)
  const [creating, setCreating] = useState(false)
  const [syncing, setSyncing] = useState(false)
  const [switching, setSwitching] = useState(false)

  const load = async () => {
    try {
      const s = await api.get<TeamSyncStatus>('/api/v1/team-sync')
      setStatus(s)
      setTeamCode(s.teamCode || '')
      setEnabled(s.enabled)
      setLoadError('')
    } catch (e: any) {
      setStatus(null)
      setLoadError(e?.message || 'Team sync status unavailable')
    }
  }
  useEffect(() => { load() }, [])

  const save = async () => {
    setBusy(true)
    try {
      // Empty project URL / service key / team code keep the stored values
      // (the API only overwrites fields that are sent non-empty).
      await api.put('/api/v1/team-sync', {
        projectUrl: projectUrl.trim(),
        serviceKey: serviceKey.trim() === '' ? '__SET__' : serviceKey.trim(),
        teamCode: teamCode.trim(),
        enabled,
      })
      toast.success('Team sync saved', enabled ? 'Sync is on' : 'Sync is off')
      setProjectUrl('')
      setServiceKey('')
      await load()
    } catch (e: any) {
      toast.error('Save failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  const createCode = async () => {
    setCreating(true)
    try {
      const res = await api.post<{ teamCode: string }>('/api/v1/team-sync/create')
      setTeamCode(res.teamCode)
      toast.success('Team code created', `${res.teamCode} — enter it on your other tills`)
    } catch (e: any) {
      toast.error('Could not create code', e?.message)
    } finally {
      setCreating(false)
    }
  }

  const syncNow = async () => {
    setSyncing(true)
    try {
      const res = await api.post<{ pushed: number; applied: number }>('/api/v1/team-sync/now')
      toast.success('Sync finished', `pushed ${res.pushed}, applied ${res.applied}`)
      await load()
    } catch (e: any) {
      toast.error('Sync failed', e?.message)
    } finally {
      setSyncing(false)
    }
  }

  // Revert a hand-configured till to the automatic cloud identity
  // (no project URL / service key / team code needed on this device).
  const switchToCloud = async () => {
    setSwitching(true)
    try {
      await api.post('/api/v1/team-sync/use-cloud')
      toast.success('Switched to LedgerPOS Cloud', 'This till now finds its team automatically.')
      await load()
    } catch (e: any) {
      toast.error('Could not switch to cloud', e?.message)
    } finally {
      setSwitching(false)
    }
  }

  if (loadError && !status) {
    return (
      <div className="px-4 pb-4 sm:px-5">
        <EmptyState
          icon={<MonitorSmartphone size={24} strokeWidth={2.25} />}
          title="Team sync unavailable"
          body={loadError}
          action={<Button variant="secondary" size="sm" onClick={load}>Try again</Button>}
        />
      </div>
    )
  }

  return (
    <div className="px-4 pb-4 sm:px-5 space-y-4">
      <div className="bg-surface-muted border-2 border-line rounded-input p-3 text-[13px] text-ink-muted">
        <p><strong className="text-ink">Multi-till syncing.</strong> Every till links itself to the team
        automatically — it finds its team in the cloud database the moment it gets online. No codes to send,
        no keys to paste. Sell on one till, see it on all of them.</p>
      </div>

      <div>
        <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">Status</p>
        {!status ? (
          <div className="py-6 flex justify-center"><Spinner /></div>
        ) : (
          <div className="space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <StatusPill status={status.enabled ? 'paid' : 'pending'} label={status.enabled ? 'Sync on' : 'Sync off'} />
              {status.pending > 0 && (
                <StatusPill status="info" label={`${status.pending} change${status.pending === 1 ? '' : 's'} waiting to push`} />
              )}
              {status.source === 'cloud' && (
                <span className="inline-flex flex-col gap-1">
                  <StatusPill status="paid" label="LedgerPOS Cloud — automatic" />
                  <span className="text-[12px] text-ink-muted">This till found its team in the cloud database. No codes, no keys.</span>
                </span>
              )}
              {status.source === 'manual' && (
                <span className="inline-flex flex-col gap-1">
                  <StatusPill status="pending" label="Manual configuration" />
                  <span>
                    <Button size="sm" variant="secondary" onClick={switchToCloud} disabled={switching} title="Drop the hand-entered keys and use the automatic cloud identity">
                      {switching ? <Spinner className="border-t-brand-ink" /> : <Cloud size={14} strokeWidth={2.5} aria-hidden />}
                      Switch to LedgerPOS Cloud (automatic)
                    </Button>
                  </span>
                </span>
              )}
              {status.teamCode && (
                <span className="text-[12px] text-ink-muted">
                  Team code <span className="font-mono font-bold text-ink tracking-wider">{status.teamCode}</span>
                </span>
              )}
            </div>
            {status.registered && !status.approved && (
              <div className="flex items-start gap-2 bg-pending-bg border-2 border-pending-text/30 rounded-input p-3 text-[13px] font-bold text-pending-text">
                <AlertTriangle size={15} strokeWidth={2.5} className="shrink-0 mt-0.5" aria-hidden />
                <span>
                  This till is registered but awaiting approval — it cannot push or pull events until another
                  approved device on the team accepts it{status.autoApprove ? ' (auto-approve is on, this should clear shortly)' : ''}.
                </span>
              </div>
            )}
            <div className="grid sm:grid-cols-2 gap-x-4 gap-y-1 text-[12px] text-ink-muted">
              <span>Last push: <span className="tabular text-ink">{status.lastPush ? new Date(status.lastPush).toLocaleString() : 'never'}</span></span>
              <span>Last pull: <span className="tabular text-ink">{status.lastPull ? new Date(status.lastPull).toLocaleString() : 'never'}</span></span>
            </div>
            {status.lastError && (
              <p className="text-[12px] font-bold text-danger-text">Last error: {status.lastError}</p>
            )}
            {status.devices.length > 0 && (
              <Table head={['Device', 'Version', 'Last seen', 'Approval', '']}>
                {status.devices.map((d) => (
                  <tr key={d.deviceId} className={d.thisDevice ? 'bg-brand/5' : ''}>
                    <td className="px-3 py-2">
                      <span className="inline-flex items-center gap-1.5 text-[13px] font-semibold text-ink">
                        <MonitorSmartphone size={14} strokeWidth={2.25} aria-hidden />
                        {d.deviceName || d.deviceId.slice(0, 8)}
                      </span>
                    </td>
                    <td className="px-3 py-2 font-mono text-[12px] text-ink-muted">{d.appVersion || '—'}</td>
                    <td className="px-3 py-2 text-[12px] text-ink-subtle tabular">
                      {d.lastSeen ? new Date(d.lastSeen).toLocaleString() : 'never'}
                    </td>
                    <td className="px-3 py-2">
                      <StatusPill status={d.approved ? 'paid' : 'pending'} label={d.approved ? 'Approved' : 'Pending'} />
                    </td>
                    <td className="px-3 py-2 text-right">
                      {d.thisDevice && <StatusPill status="info" label="This device" />}
                    </td>
                  </tr>
                ))}
              </Table>
            )}
          </div>
        )}
      </div>

      <div className="border-t-2 border-line pt-3">
        <p className="text-[12px] uppercase font-bold text-ink-muted mb-1.5">Link this till</p>
        <div className="grid sm:grid-cols-2 gap-3">
          <Field label="Supabase project URL" hint="Leave blank to keep the stored one.">
            <Input value={projectUrl} onChange={(e) => setProjectUrl(e.target.value)} placeholder="https://xyzcompany.supabase.co" className="font-mono" />
          </Field>
          <Field label="Service key" hint="service_role key. Leave blank to keep the stored one.">
            <Input type="password" value={serviceKey} onChange={(e) => setServiceKey(e.target.value)} className="font-mono" autoComplete="off" />
          </Field>
          <Field label="Team code" hint="Shared by all tills in this team.">
            <div className="flex gap-2">
              <Input
                value={teamCode}
                onChange={(e) => setTeamCode(e.target.value.toUpperCase())}
                placeholder="e.g. KQ7-P2MX-91"
                className="font-mono uppercase flex-1"
              />
              <Button variant="secondary" onClick={createCode} disabled={creating}>
                {creating ? <Spinner className="border-t-brand-ink" /> : 'Create team code'}
              </Button>
            </div>
          </Field>
          <div className="flex items-end">
            <label className="flex items-center gap-2 min-h-11 text-sm font-semibold text-ink">
              <input
                type="checkbox"
                checked={enabled}
                onChange={(e) => setEnabled(e.target.checked)}
                className="w-5 h-5 accent-[#10B981]"
              />
              Sync with the team
            </label>
          </div>
        </div>
        <div className="flex flex-wrap gap-2 mt-3">
          <Button variant="primary" onClick={save} disabled={busy}>
            {busy ? <Spinner className="border-t-brand-ink" /> : 'Save team sync'}
          </Button>
          <Button variant="secondary" onClick={syncNow} disabled={syncing}>
            {syncing ? <Spinner /> : <RefreshCw size={15} strokeWidth={2.5} aria-hidden />}
            Sync now
          </Button>
          <Button variant="ghost" onClick={load} disabled={syncing || busy}>Refresh status</Button>
        </div>
      </div>

      <div className="bg-surface-muted border-2 border-line rounded-input p-3 text-[13px] text-ink-muted">
        <p>
          Changes to products, prices, customers, orders, void reasons and store settings sync to every
          linked till automatically — no reinstalling. Binary updates still arrive via Settings → Updates.
        </p>
      </div>
    </div>
  )
}
