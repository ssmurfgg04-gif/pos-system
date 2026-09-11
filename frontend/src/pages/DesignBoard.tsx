// Design board — the designer role's kanban: queue → in progress → ready →
// delivered. design.view to see; design.manage to move/create. Columns are
// always rendered (even empty) so the workflow shape is visible from day one.

import { useEffect, useState } from 'react'
import { api, DesignJob } from '../lib/api'
import { useAuth } from '../stores/auth'
import { Button, Card, EmptyState, Field, Input, Modal, Spinner, Textarea } from '../components/ui'
import { toast } from '../stores/toasts'
import { onWsEvent } from '../ws/client'
import { Palette } from 'lucide-react'

const COLUMNS: { key: DesignJob['status']; label: string }[] = [
  { key: 'queue', label: 'Queue' },
  { key: 'in_progress', label: 'In progress' },
  { key: 'ready', label: 'Ready' },
  { key: 'delivered', label: 'Delivered' },
]

export function DesignBoard() {
  const canManage = useAuth((s) => !!s.user?.permissions.includes('design.manage'))
  const [jobs, setJobs] = useState<DesignJob[] | null>(null)
  const [creating, setCreating] = useState(false)

  const load = async () => {
    try {
      setJobs(await api.get<DesignJob[]>('/api/v1/design'))
    } catch (e: any) {
      toast.error('Load failed', e?.message)
    }
  }
  useEffect(() => {
    load()
    return onWsEvent('DESIGN_JOB_UPDATED', () => load())
  }, [])

  const move = async (job: DesignJob, status: DesignJob['status']) => {
    try {
      await api.post(`/api/v1/design/${job.id}/move`, { status })
      toast.success('Moved', `${job.title} → ${status.replace('_', ' ')}`)
      load()
    } catch (e: any) {
      toast.error('Move failed', e?.message)
    }
  }

  const empty = jobs !== null && jobs.length === 0

  return (
    <div className="space-y-4">
      <Card
        title="Design & production board"
        sub="Custom work: artwork, branding runs, custom merch"
        actions={canManage && <Button variant="primary" size="sm" onClick={() => setCreating(true)}>+ Job</Button>}
      >
        {empty && (
          <div className="mb-3">
            <EmptyState
              icon={<Palette size={24} strokeWidth={2.25} />}
              title="No design jobs yet"
              body={canManage ? 'Create the first job — it will land in the Queue column.' : 'Jobs will appear here as they are created.'}
            />
          </div>
        )}

        <div className={`grid gap-3 sm:grid-cols-2 xl:grid-cols-4 ${empty ? 'opacity-60' : ''}`}>
          {COLUMNS.map((col) => {
            const items = (jobs ?? []).filter((j) => j.status === col.key)
            return (
              <div key={col.key} className="bg-surface-muted border-2 border-line rounded-input p-2 min-h-40">
                <p className="text-[12px] font-bold uppercase tracking-wide text-ink-muted px-1.5 py-1 flex items-center justify-between">
                  {col.label}
                  <span className="tabular bg-surface border border-line rounded-pill px-2">{items.length}</span>
                </p>
                <div className="space-y-2 mt-1">
                  {items.map((j) => (
                    <article key={j.id} className="bg-surface border-2 border-line-strong rounded-input p-2.5 shadow-brutal-sm">
                      <p className="font-bold text-[13px] text-ink leading-snug">{j.title}</p>
                      {j.productName && <p className="text-[11px] text-ink-subtle mt-0.5">on {j.productName}</p>}
                      {j.customerName && <p className="text-[11px] text-ink-muted mt-0.5">for {j.customerName}</p>}
                      {j.notes && <p className="text-[12px] text-ink-muted mt-1 line-clamp-2">{j.notes}</p>}
                      <div className="flex items-center justify-between mt-2">
                        <span className="text-[11px] text-ink-subtle">{j.assigneeName || j.createdBy || '—'}</span>
                        {canManage && (
                          <select
                            value={j.status}
                            onChange={(e) => move(j, e.target.value as DesignJob['status'])}
                            aria-label={`Move ${j.title}`}
                            className="text-[12px] font-semibold border-2 border-line-strong rounded-input px-1.5 py-1 bg-surface"
                          >
                            {COLUMNS.map((c) => <option key={c.key} value={c.key}>{c.label}</option>)}
                          </select>
                        )}
                      </div>
                    </article>
                  ))}
                  {items.length === 0 && <p className="text-[12px] text-ink-subtle text-center py-6">Empty</p>}
                </div>
              </div>
            )
          })}
        </div>
      </Card>

      {creating && <JobModal onClose={() => setCreating(false)} onSaved={() => { setCreating(false); load() }} />}
    </div>
  )
}

function JobModal({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const [form, setForm] = useState({ title: '', productName: '', customerName: '', notes: '', status: 'queue' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const save = async () => {
    setBusy(true)
    setError('')
    try {
      await api.post('/api/v1/design', form)
      toast.success('Design job created', form.title)
      onSaved()
    } catch (e: any) {
      setError(e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open onClose={onClose} title="New design job" footer={
      <>
        <Button variant="ghost" onClick={onClose}>Cancel</Button>
        <Button variant="primary" onClick={save} disabled={busy || !form.title.trim()}>
          {busy ? <Spinner className="border-t-brand-ink" /> : 'Create job'}
        </Button>
      </>
    }>
      <div className="space-y-3">
        <Field label="Title">
          <Input value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} autoFocus placeholder="e.g. Acme FC team jersey artwork" />
        </Field>
        <Field label="Product">
          <Input value={form.productName} onChange={(e) => setForm({ ...form, productName: e.target.value })} placeholder="e.g. Premium Heavyweight Tee" />
        </Field>
        <Field label="Customer">
          <Input value={form.customerName} onChange={(e) => setForm({ ...form, customerName: e.target.value })} />
        </Field>
        <Field label="Notes">
          <Textarea value={form.notes} onChange={(e) => setForm({ ...form, notes: e.target.value })} placeholder="Colors, placement, deadline…" />
        </Field>
        {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
      </div>
    </Modal>
  )
}
