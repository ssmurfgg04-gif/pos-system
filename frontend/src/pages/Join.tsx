// Join — redeem a team join link on a fresh machine. The owner minted the
// link (Settings → Team → Invite a worker); it carries a single-use token
// and the role. The worker pastes it, picks a name + PIN, and lands in the
// POS — the shop's config (store name, currency, receipt) syncs from the
// team in the background. No admin onboarding, no keys, nothing to type.

import { useState } from 'react'
import { api, TeamJoinResult } from '../lib/api'
import { useAuth } from '../stores/auth'
import { useBranding } from '../stores/branding'
import { navigate, homeFor } from '../lib/router'
import { reconnectWs } from '../ws/client'
import { Button, Field, Input, Spinner } from '../components/ui'
import { toast } from '../stores/toasts'
import { Link2, Users } from 'lucide-react'

export function Join() {
  const [link, setLink] = useState('')
  const [name, setName] = useState('')
  const [pin, setPin] = useState('')
  const [pin2, setPin2] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [joined, setJoined] = useState<TeamJoinResult | null>(null)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (busy) return
    if (!/^\d{4}$/.test(pin) || pin === '0000' || pin === '1234') {
      setError('Pick a 4-digit PIN that isn\'t 0000 or 1234')
      return
    }
    if (pin !== pin2) {
      setError('The two PINs don\'t match')
      return
    }
    setBusy(true)
    setError('')
    try {
      const res = await api.post<TeamJoinResult>('/api/v1/auth/team-join', {
        link: link.trim(), name: name.trim(), pin,
      })
      localStorage.setItem('pos_token', res.token)
      await useAuth.getState().refresh()
      await useBranding.getState().load()
      reconnectWs()
      setJoined(res)
      toast.success(`Welcome to ${res.storeName || 'the team'}`,
        `You're in as ${res.roleName} — the shop's products and settings are syncing now.`)
      navigate(homeFor(useAuth.getState().user))
    } catch (err: any) {
      setError(err?.message || 'Could not join with that link')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-h-full flex flex-col items-center justify-center bg-shell px-4 py-10 overflow-y-auto">
      <div className="w-full max-w-md">
        <div className="flex flex-col items-center mb-6">
          <div className="w-16 h-16 rounded-card bg-brand border-2 border-brand-strong shadow-brutal-brand flex items-center justify-center text-brand-ink mb-4" aria-hidden>
            <Users size={28} strokeWidth={2.25} />
          </div>
          <h1 className="text-on-shell text-xl font-bold text-center">Join a team</h1>
          <p className="text-on-shell-muted text-sm mt-0.5">Paste the link the owner sent you</p>
        </div>

        <form onSubmit={submit} className="bg-surface border-2 border-line-strong rounded-card shadow-brutal p-5 space-y-4">
          <Field label="Team join link" hint="Looks like https://awesomeposs.netlify.app/join#t=…&r=…&k=… — paste the whole thing.">
            <textarea
              value={link}
              onChange={(e) => setLink(e.target.value)}
              required
              autoFocus
              rows={2}
              placeholder="Paste the join link here"
              className="w-full min-h-11 rounded-input border-2 border-line bg-surface px-3 py-2 text-sm text-ink placeholder:text-ink-subtle focus:border-brand focus:outline-none font-mono text-[12px]"
            />
          </Field>
          <Field label="Your name">
            <Input value={name} onChange={(e) => setName(e.target.value)} required placeholder="e.g. Brian" />
          </Field>
          <div className="grid grid-cols-2 gap-2">
            <Field label="Your PIN" hint="You'll sign in with this.">
              <Input value={pin} onChange={(e) => setPin(e.target.value.replace(/\D/g, '').slice(0, 4))} inputMode="numeric" type="password" required placeholder="4 digits" />
            </Field>
            <Field label="Repeat PIN">
              <Input value={pin2} onChange={(e) => setPin2(e.target.value.replace(/\D/g, '').slice(0, 4))} inputMode="numeric" type="password" required placeholder="4 digits" />
            </Field>
          </div>
          {error && (
            <p role="alert" className="text-danger-text text-sm font-semibold bg-danger-bg border border-danger-text/30 rounded-input px-3 py-2">
              {error}
            </p>
          )}
          <Button type="submit" variant="primary" size="lg" className="w-full" disabled={busy || !link.trim() || !name.trim() || pin.length !== 4}>
            {busy ? <Spinner className="border-t-brand-ink" /> : <span className="inline-flex items-center gap-2"><Link2 size={16} strokeWidth={2.5} aria-hidden />Join the team</span>}
          </Button>
          <button
            type="button"
            onClick={() => navigate('/login')}
            className="w-full min-h-11 flex items-center justify-center text-center text-sm font-semibold text-ink-muted hover:text-ink underline decoration-line hover:decoration-line-strong"
          >
            I have my own login instead →
          </button>
        </form>

        {joined && (
          <div className="mt-4 bg-paid-bg border-2 border-paid-text/40 rounded-card p-4 text-[13px] font-semibold text-paid-text">
            Joined {joined.storeName || joined.teamCode} as {joined.roleName}. Syncing the shop…
          </div>
        )}
      </div>
    </div>
  )
}
