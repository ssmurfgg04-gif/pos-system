// Stock transfers (v1.12) — move stock between the shops of one business.
// Sending takes the goods off this shelf at once (works offline: it goes
// when the internet is back). The other shop receives what actually
// arrived; anything short is shown. A transfer nobody has received yet
// can be cancelled and the goods come back.

import { useEffect, useMemo, useState } from 'react'
import { api, Product } from '../lib/api'
import { Button, Card, EmptyState, Field, Input, Modal, Select, Spinner, StatusPill, Table, Textarea } from '../components/ui'
import { toast } from '../stores/toasts'
import { ArrowRightLeft, PackageCheck, Plus, Send, Trash2, Undo2, WifiOff } from 'lucide-react'
import { Transfer, TransferList, qty } from '../lib/v112'

const statusPill = (t: Transfer) => {
  switch (t.status) {
    case 'queued': return <StatusPill status="queued" label="Waiting for internet" />
    case 'sent': return <StatusPill status="progress" label={t.direction === 'in' ? 'To receive' : 'On the way'} />
    case 'received': return <StatusPill status="paid" label={t.shortMilli > 0 ? `Received, ${qty(t.shortMilli)} short` : 'Received'} />
    case 'cancelled': return <StatusPill status="void" label="Cancelled" />
    case 'rejected': return <StatusPill status="danger" label="Refused" />
  }
}

const when = (s: string) => {
  if (!s) return ''
  const d = new Date(s)
  return isNaN(d.getTime()) ? s : d.toLocaleString('en-KE', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit', timeZone: 'Africa/Nairobi' })
}

export function Transfers() {
  const [data, setData] = useState<TransferList | null>(null)
  const [sending, setSending] = useState(false)
  const [receiving, setReceiving] = useState<Transfer | null>(null)
  const load = async () => {
    try {
      setData(await api.get<TransferList>('/api/v1/transfers'))
    } catch (e: any) {
      toast.error('Could not load transfers', e?.message)
    }
  }
  useEffect(() => { load() }, [])

  const cancel = async (t: Transfer) => {
    if (!window.confirm(`Cancel the transfer to ${t.toName || t.toTeam}? The goods go back on this shop's shelf.`)) return
    try {
      await api.post('/api/v1/transfers/cancel', { clientRef: t.clientRef, toTeam: t.toTeam })
      toast.success('Cancelled', 'The stock is back on the shelf.')
      load()
    } catch (e: any) {
      toast.error('Could not cancel', e?.message)
    }
  }

  const incoming = (data?.transfers ?? []).filter((t) => t.direction === 'in' && t.status === 'sent')

  return (
    <div className="space-y-4">
      {data && !data.online && (
        <div className="flex items-start gap-2 bg-pending-bg text-pending-text rounded-input px-3 py-2.5 text-sm font-semibold">
          <WifiOff size={16} className="mt-0.5 shrink-0" aria-hidden />
          <span>{data.error || 'Offline'}. Transfers you send now wait here and go when the internet is back.</span>
        </div>
      )}
      {incoming.length > 0 && (
        <Card title={`To receive (${incoming.length})`} sub="Count what arrived, then receive it into stock.">
          <div className="space-y-2">
            {incoming.map((t) => (
              <div key={t.id} className="flex flex-wrap items-center justify-between gap-2 border border-line rounded-input px-3 py-2">
                <div>
                  <p className="font-semibold text-ink">From {t.fromName || t.fromTeam}: {t.lines.length} item(s)</p>
                  <p className="text-xs text-ink-muted">Sent {when(t.sentAt)} by {t.sentBy}{t.note ? ` · ${t.note}` : ''}</p>
                </div>
                <Button variant="primary" onClick={() => setReceiving(t)}><PackageCheck size={15} aria-hidden />Receive</Button>
              </div>
            ))}
          </div>
        </Card>
      )}
      <Card
        title="Stock transfers"
        sub="Move stock between the shops of your business."
        actions={<Button variant="primary" disabled={!data || data.shops.length === 0 && data.online} onClick={() => setSending(true)}><Send size={15} aria-hidden />Send stock</Button>}
        pad={false}
      >
        {!data ? <div className="py-10 flex justify-center"><Spinner /></div> : data.transfers.length === 0 ? (
          <EmptyState
            icon={<ArrowRightLeft size={24} />}
            title="No transfers yet"
            body={data.online && data.shops.length === 0
              ? 'Your business has one shop. Add another shop (Settings → Team) to move stock between them.'
              : 'Send stock to another of your shops; it arrives in their stock when they receive it.'}
          />
        ) : (
          <Table head={['', 'Shop', 'Items', 'Status', 'When', '']}>
            {data.transfers.map((t) => (
              <tr key={t.clientRef + t.direction}>
                <td className="px-3 py-2.5 text-[12px] font-bold text-ink-muted uppercase">{t.direction === 'out' ? 'Out' : 'In'}</td>
                <td className="px-3 py-2.5 font-semibold">{t.direction === 'out' ? `To ${t.toName || t.toTeam}` : `From ${t.fromName || t.fromTeam}`}</td>
                <td className="px-3 py-2.5 text-[13px] text-ink-muted max-w-80">
                  {t.lines.slice(0, 3).map((l) => `${qty(l.qtyMilli)} × ${l.name}`).join(', ')}{t.lines.length > 3 ? ` +${t.lines.length - 3} more` : ''}
                  {t.lastError && <span className="block text-danger-text text-[12px]">{t.lastError}</span>}
                </td>
                <td className="px-3 py-2.5">{statusPill(t)}</td>
                <td className="px-3 py-2.5 text-[12px] text-ink-subtle">{when(t.receivedAt || t.sentAt)}</td>
                <td className="px-3 py-2.5">
                  {t.direction === 'out' && (t.status === 'sent' || t.status === 'queued') && data.online && (
                    <Button size="sm" variant="ghost" onClick={() => cancel(t)}><Undo2 size={14} aria-hidden />Cancel</Button>
                  )}
                </td>
              </tr>
            ))}
          </Table>
        )}
      </Card>
      {sending && data && <SendModal shops={data.shops} onClose={() => setSending(false)} onDone={() => { setSending(false); load() }} />}
      {receiving && <ReceiveModal t={receiving} onClose={() => setReceiving(null)} onDone={() => { setReceiving(null); load() }} />}
    </div>
  )
}

function SendModal({ shops, onClose, onDone }: { shops: { teamCode: string; name: string }[]; onClose: () => void; onDone: () => void }) {
  const [products, setProducts] = useState<Product[]>([])
  const [toTeam, setToTeam] = useState(shops[0]?.teamCode ?? '')
  const [search, setSearch] = useState('')
  const [lines, setLines] = useState<{ p: Product; qty: string }[]>([])
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => { api.get<Product[]>('/api/v1/products').then(setProducts).catch(() => setProducts([])) }, [])
  const matches = useMemo(() => {
    const q = search.trim().toLowerCase()
    if (!q) return []
    return products.filter((p) => p.trackStock && !lines.some((l) => l.p.id === p.id) &&
      (p.name.toLowerCase().includes(q) || p.sku.toLowerCase().includes(q) || (p.barcode || '') === q)).slice(0, 8)
  }, [search, products, lines])
  const milli = (s: string) => Math.round(Number(s) * 1000)
  const valid = toTeam && lines.length > 0 && lines.every((l) => milli(l.qty) > 0 && milli(l.qty) <= l.p.stockQtyMilli)
  const send = async () => {
    setBusy(true)
    setError('')
    try {
      const t = await api.post<Transfer>('/api/v1/transfers', { toTeam, note, lines: lines.map((l) => ({ productId: l.p.id, qtyMilli: milli(l.qty) })) })
      toast.success(t.status === 'queued' ? 'Saved, sends when online' : 'Sent', 'The stock left this shop.')
      onDone()
    } catch (e: any) {
      setError(e?.message || 'Could not send')
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal open onClose={onClose} title="Send stock to another shop" size="lg"
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" disabled={busy || !valid} onClick={send}><Send size={15} aria-hidden />Send</Button></>}>
      <div className="space-y-3">
        <Field label="To">
          {shops.length > 0 ? (
            <Select value={toTeam} onChange={(e) => setToTeam(e.target.value)}>
              {shops.map((s) => <option key={s.teamCode} value={s.teamCode}>{s.name}</option>)}
            </Select>
          ) : (
            <Input value={toTeam} onChange={(e) => setToTeam(e.target.value.trim().toUpperCase())} placeholder="Shop code (offline: checked when sent)" />
          )}
        </Field>
        <Field label="Add products">
          <Input value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Name, SKU or scan a barcode" autoFocus />
        </Field>
        {matches.length > 0 && (
          <div className="border border-line rounded-input divide-y divide-line">
            {matches.map((p) => (
              <button key={p.id} className="w-full text-left px-3 py-2 hover:bg-surface-muted flex justify-between" onClick={() => { setLines([...lines, { p, qty: '1' }]); setSearch('') }}>
                <span className="font-semibold">{p.name}</span><span className="text-xs text-ink-muted">{qty(p.stockQtyMilli)} in stock <Plus size={12} className="inline" aria-hidden /></span>
              </button>
            ))}
          </div>
        )}
        {lines.length > 0 && (
          <Table head={['Product', 'In stock', 'Send', '']}>
            {lines.map((l, i) => {
              const bad = milli(l.qty) <= 0 || milli(l.qty) > l.p.stockQtyMilli
              return (
                <tr key={l.p.id}>
                  <td className="px-3 py-2 font-semibold">{l.p.name}</td>
                  <td className="px-3 py-2 tabular">{qty(l.p.stockQtyMilli)}</td>
                  <td className="px-3 py-2 w-28">
                    <Input inputMode="decimal" value={l.qty} aria-invalid={bad} className={bad ? 'border-danger-text' : ''}
                      onChange={(e) => setLines(lines.map((x, j) => j === i ? { ...x, qty: e.target.value } : x))} />
                  </td>
                  <td className="px-3 py-2"><Button size="sm" variant="ghost" aria-label="Remove" onClick={() => setLines(lines.filter((_, j) => j !== i))}><Trash2 size={14} aria-hidden /></Button></td>
                </tr>
              )
            })}
          </Table>
        )}
        <Field label="Note (optional)"><Textarea value={note} onChange={(e) => setNote(e.target.value)} maxLength={300} /></Field>
        {error && <p role="alert" className="text-danger-text text-sm font-semibold">{error}</p>}
      </div>
    </Modal>
  )
}

function ReceiveModal({ t, onClose, onDone }: { t: Transfer; onClose: () => void; onDone: () => void }) {
  const [got, setGot] = useState<string[]>(t.lines.map((l) => qty(l.qtyMilli)))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const milli = (s: string) => Math.round(Number(s) * 1000)
  const valid = got.every((g, i) => g.trim() !== '' && milli(g) >= 0 && milli(g) <= t.lines[i].qtyMilli)
  const receive = async () => {
    setBusy(true)
    setError('')
    try {
      await api.post(`/api/v1/transfers/${t.id}/receive`, { lines: t.lines.map((l, i) => ({ sku: l.sku, qtyMilli: milli(got[i]) })) })
      toast.success('Received', 'The stock is on your shelf.')
      onDone()
    } catch (e: any) {
      setError(e?.message || 'Could not receive')
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal open onClose={onClose} title={`Receive from ${t.fromName || t.fromTeam}`} size="lg"
      footer={<><Button onClick={onClose}>Not now</Button><Button variant="primary" disabled={busy || !valid} onClick={receive}><PackageCheck size={15} aria-hidden />Receive into stock</Button></>}>
      <p className="text-sm text-ink-muted mb-3">Type what actually arrived. Anything short stays on the record as missing.</p>
      <Table head={['Product', 'Sent', 'Arrived']}>
        {t.lines.map((l, i) => (
          <tr key={l.sku}>
            <td className="px-3 py-2 font-semibold">{l.name}<span className="block text-xs text-ink-subtle">{l.sku}</span></td>
            <td className="px-3 py-2 tabular">{qty(l.qtyMilli)}</td>
            <td className="px-3 py-2 w-28"><Input inputMode="decimal" value={got[i]} onChange={(e) => setGot(got.map((g, j) => j === i ? e.target.value : g))} /></td>
          </tr>
        ))}
      </Table>
      {error && <p role="alert" className="text-danger-text text-sm font-semibold mt-3">{error}</p>}
    </Modal>
  )
}
