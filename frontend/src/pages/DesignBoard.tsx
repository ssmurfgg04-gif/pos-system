// Design board — the designer role's kanban: queue → in progress → ready →
// delivered. design.view to see; design.manage to move/create. Columns are
// always rendered (even empty) so the workflow shape is visible from day one.
// Each job carries attachments (≤5 MB) the team can upload, download and
// delete — artwork, briefs, reference photos travel with the job.

import { useEffect, useRef, useState } from 'react'
import { api, ApiError, backendMode, DesignFile, DesignJob, token } from '../lib/api'
import { useAuth } from '../stores/auth'
import { Button, Card, EmptyState, Field, Input, Modal, Spinner, Textarea } from '../components/ui'
import { toast } from '../stores/toasts'
import { onWsEvent } from '../ws/client'
import { Palette, Paperclip, UploadCloud, Download, Trash2 } from 'lucide-react'

const MAX_ATTACHMENT_BYTES = 5 * 1024 * 1024 // matches the server cap (5 MB)

/**
 * Authenticated binary download — mirrors the raw()/downloadFile() pattern
 * from lib/api (Bearer header, then blob → object URL → a.click) but keeps
 * the bytes binary-safe: design attachments are arbitrary files, not CSVs.
 */
async function downloadAttachment(jobId: number, f: DesignFile): Promise<void> {
  if ((await backendMode()) === 'demo') {
    throw new ApiError(501, 'attachment downloads are not available in the demo build')
  }
  const base = (import.meta.env.VITE_API_URL as string | undefined) ?? ''
  const headers: Record<string, string> = {}
  const t = token()
  if (t) headers['Authorization'] = 'Bearer ' + t
  const res = await fetch(`${base}/api/v1/design/${jobId}/files/${f.id}`, { headers })
  if (!res.ok) {
    let msg = `Download failed (${res.status})`
    try {
      const j = await res.json()
      if (j?.error) msg = j.error
    } catch { /* not JSON */ }
    throw new ApiError(res.status, msg)
  }
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = f.filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  window.setTimeout(() => URL.revokeObjectURL(url), 4000)
}

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
  const [filesByJob, setFilesByJob] = useState<Record<number, DesignFile[]>>({})
  const [attachFor, setAttachFor] = useState<DesignJob | null>(null)

  const load = async () => {
    try {
      const list = await api.get<DesignJob[]>('/api/v1/design')
      setJobs(list)
      // Attachment counts for every card (silent on old backends that don't
      // serve them yet — the chip just reads 0).
      list.forEach(async (j) => {
        try {
          const files = await api.get<DesignFile[]>(`/api/v1/design/${j.id}/files`)
          setFilesByJob((m) => ({ ...m, [j.id]: files }))
        } catch { /* attachments unavailable — leave count as-is */ }
      })
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
                        <span className="flex items-center gap-1.5">
                          <button
                            type="button"
                            onClick={() => setAttachFor(j)}
                            aria-label={`Attachments — ${j.title}`}
                            title="Attachments"
                            className="inline-flex items-center gap-1 text-[11px] font-semibold text-ink-muted border-2 border-line-strong rounded-input px-1.5 py-1 bg-surface hover:bg-surface-muted hover:text-ink"
                          >
                            <Paperclip size={12} strokeWidth={2.25} aria-hidden />
                            {filesByJob[j.id]?.length ?? 0}
                          </button>
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
                        </span>
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

      {attachFor && (
        <AttachmentsModal
          job={attachFor}
          initialFiles={filesByJob[attachFor.id]}
          canManage={canManage}
          onClose={() => setAttachFor(null)}
          onChanged={(files) => setFilesByJob((m) => ({ ...m, [attachFor.id]: files }))}
        />
      )}
    </div>
  )
}

function JobModal({ onClose, onSaved }: { onClose: () => void; onSaved: () => void }) {
  const [form, setForm] = useState({ title: '', productName: '', customerName: '', notes: '', status: 'queue' })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [custQuery, setCustQuery] = useState('')
  const [custOptions, setCustOptions] = useState<{ id: number; name: string; phone?: string }[]>([])
  useEffect(() => {
    const q = custQuery.trim()
    if (!q) { setCustOptions([]); return }
    const t = window.setTimeout(async () => {
      try {
        const res = await api.get<{ id: number; name: string; phone?: string }[]>(`/api/v1/customers?search=${encodeURIComponent(q)}`)
        setCustOptions(res.slice(0, 6))
      } catch { /* offline */ }
    }, 250)
    return () => window.clearTimeout(t)
  }, [custQuery])

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
        <Field label="Customer" hint="Pick from Customers tab for linked ledger — or type a new name.">
          <Input
            value={form.customerName}
            onChange={(e) => { setForm({ ...form, customerName: e.target.value }); setCustQuery(e.target.value) }}
            placeholder="Type to search existing customers…"
          />
          {custQuery && custOptions.length > 0 && (
            <div className="border-2 border-line rounded-input overflow-hidden mt-1">
              {custOptions.map((c) => (
                <button key={c.id} type="button" onClick={() => { setForm({ ...form, customerName: c.name }); setCustQuery(''); setCustOptions([]) }} className="w-full text-left px-3 py-2 hover:bg-surface-muted text-[13px]">
                  {c.name} <span className="text-ink-subtle">{c.phone || ''}</span>
                </button>
              ))}
            </div>
          )}
        </Field>
        <Field label="Notes">
          <Textarea value={form.notes} onChange={(e) => setForm({ ...form, notes: e.target.value })} placeholder="Colors, placement, deadline…" />
        </Field>
        {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
      </div>
    </Modal>
  )
}

// Per-job attachments: upload (design.manage, ≤5 MB), download for anyone
// who can see the board, delete (design.manage). The list refreshes after
// every change and reports back so the card's paperclip count stays true.
function AttachmentsModal({
  job,
  initialFiles,
  canManage,
  onClose,
  onChanged,
}: {
  job: DesignJob
  initialFiles: DesignFile[] | undefined
  canManage: boolean
  onClose: () => void
  onChanged: (files: DesignFile[]) => void
}) {
  const [files, setFiles] = useState<DesignFile[] | null>(initialFiles ?? null)
  const [uploading, setUploading] = useState(false)
  const [busyId, setBusyId] = useState<number | null>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  const refresh = async () => {
    try {
      const list = await api.get<DesignFile[]>(`/api/v1/design/${job.id}/files`)
      setFiles(list)
      onChanged(list)
    } catch (e: any) {
      toast.error('Attachments failed to load', e?.message)
    }
  }

  const upload = async (file: File) => {
    if (file.size > MAX_ATTACHMENT_BYTES) {
      toast.error('File too large', `${file.name} is ${(file.size / (1024 * 1024)).toFixed(1)} MB — the cap is 5 MB`)
      if (fileRef.current) fileRef.current.value = ''
      return
    }
    setUploading(true)
    try {
      const form = new FormData()
      form.append('file', file)
      await api.form(`/api/v1/design/${job.id}/files`, form)
      toast.success('File attached', file.name)
      await refresh()
    } catch (e: any) {
      toast.error('Upload failed', e?.message)
    } finally {
      setUploading(false)
      if (fileRef.current) fileRef.current.value = ''
    }
  }

  const download = async (f: DesignFile) => {
    setBusyId(f.id)
    try {
      await downloadAttachment(job.id, f)
    } catch (e: any) {
      toast.error('Download failed', e?.message)
    } finally {
      setBusyId(null)
    }
  }

  const remove = async (f: DesignFile) => {
    if (!confirm(`Delete ${f.filename}? This cannot be undone.`)) return
    setBusyId(f.id)
    try {
      await api.del(`/api/v1/design/${job.id}/files/${f.id}`)
      toast.success('Attachment deleted', f.filename)
      await refresh()
    } catch (e: any) {
      toast.error('Delete failed', e?.message)
    } finally {
      setBusyId(null)
    }
  }

  return (
    <Modal
      open
      onClose={onClose}
      title={`Attachments — ${job.title}`}
      size="md"
      footer={<Button variant="primary" onClick={onClose}>Close</Button>}
    >
      {canManage && (
        <div className="flex flex-wrap items-center gap-2 mb-3">
          <Button variant="primary" size="sm" onClick={() => fileRef.current?.click()} disabled={uploading}>
            {uploading ? <Spinner className="border-t-brand-ink" /> : <UploadCloud size={15} strokeWidth={2.25} aria-hidden />}
            Attach file
          </Button>
          <span className="text-[12px] text-ink-subtle">Any file type, up to 5 MB — travels with the job for the whole team.</span>
          <input
            ref={fileRef}
            type="file"
            className="hidden"
            aria-label="Attachment file"
            onChange={(e) => e.target.files?.[0] && upload(e.target.files[0])}
          />
        </div>
      )}

      {!files ? (
        <div className="py-8 flex justify-center"><Spinner /></div>
      ) : files.length === 0 ? (
        <EmptyState
          icon={<Paperclip size={24} strokeWidth={2.25} />}
          title="No attachments yet"
          body={canManage ? 'Attach artwork, briefs or reference photos — everyone on the board sees them.' : 'Files attached to this job will appear here.'}
        />
      ) : (
        <ul className="divide-y divide-line border-2 border-line rounded-input overflow-hidden">
          {files.map((f) => (
            <li key={f.id} className="flex items-center gap-3 px-3 py-2.5 bg-surface">
              <Paperclip size={14} strokeWidth={2.25} className="text-ink-subtle shrink-0" aria-hidden />
              <span className="min-w-0 flex-1">
                <p className="font-semibold text-ink text-[13px] truncate" title={f.filename}>{f.filename}</p>
                <p className="text-[11px] text-ink-subtle">
                  {(f.size / 1024).toFixed(0)} KB · {f.uploadedByName || `user #${f.uploadedBy}`} ·{' '}
                  {new Date(f.createdAt).toLocaleDateString()}
                </p>
              </span>
              <span className="flex items-center gap-1 shrink-0">
                <Button size="sm" variant="ghost" onClick={() => download(f)} disabled={busyId === f.id}>
                  <Download size={14} strokeWidth={2.25} aria-hidden />
                  Download
                </Button>
                {canManage && (
                  <Button size="sm" variant="ghost" className="text-danger-text" onClick={() => remove(f)} disabled={busyId === f.id}>
                    <Trash2 size={14} strokeWidth={2.25} aria-hidden />
                    Delete
                  </Button>
                )}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Modal>
  )
}
