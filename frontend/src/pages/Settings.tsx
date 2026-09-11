// Settings — the white-label control panel. Every string the customer sees
// lives here. Secrets are masked (__SET__ keeps the stored value). Includes
// M-Pesa (mock/sandbox/production), printer target + test print, the
// audit log, and System (backups + demo data reset).

import { useEffect, useState } from 'react'
import { api, AuditEntry, BackupResult, backendMode } from '../lib/api'
import { resetDemo } from '../demo/backend'
import { useBranding } from '../stores/branding'
import { Button, Card, Field, Input, Select, Spinner, Table, Tabs, Textarea } from '../components/ui'
import { toast } from '../stores/toasts'
import { Printer, DatabaseBackup, HardDriveDownload, RotateCcw } from 'lucide-react'

type SettingsMap = Record<string, string>

export function Settings() {
  const reloadBranding = useBranding((s) => s.load)
  const [tab, setTab] = useState<'store' | 'payments' | 'printer' | 'system' | 'audit'>('store')
  const [values, setValues] = useState<SettingsMap | null>(null)
  const [busy, setBusy] = useState(false)
  const [audit, setAudit] = useState<AuditEntry[] | null>(null)
  const [backups, setBackups] = useState<BackupResult[] | null>(null)
  const [backing, setBacking] = useState(false)
  const [demo, setDemo] = useState(false)

  useEffect(() => {
    let alive = true
    backendMode().then((m) => { if (alive) setDemo(m === 'demo') }).catch(() => undefined)
    return () => { alive = false }
  }, [])

  const load = async () => {
    try {
      setValues(await api.get<SettingsMap>('/api/v1/settings'))
    } catch (e: any) {
      toast.error('Load failed', e?.message)
    }
  }
  useEffect(() => { load() }, [])

  useEffect(() => {
    if (tab === 'audit' && !audit) {
      api.get<AuditEntry[]>('/api/v1/audit').then(setAudit).catch(() => setAudit([]))
    }
    if (tab === 'system' && !backups) {
      api.get<BackupResult[]>('/api/v1/system/backups').then(setBackups).catch(() => setBackups([]))
    }
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
              { key: 'system' as const, label: 'System' },
              { key: 'audit' as const, label: 'Audit log' },
            ]}
            value={tab}
            onChange={setTab}
          />
          {tab !== 'audit' && (
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
          </div>
        )}

        {tab === 'payments' && (
          <div className="px-4 pb-4 sm:px-5 grid sm:grid-cols-2 gap-3">
            <div className="sm:col-span-2 bg-surface-muted border-2 border-line rounded-input p-3 text-[13px] text-ink-muted">
              <p><strong className="text-ink">M-Pesa setup.</strong> The payment provider is swappable:
              run in <strong>mock</strong> for demos/training, switch to <strong>sandbox</strong> to test with Safaricom,
              or <strong>production</strong> when you go live. Manual receipt-code entry always works, even with no provider configured.</p>
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
            <div className="sm:col-span-2">
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
            </div>
          </div>
        )}

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
