import { useState } from 'react'
import { useAuth } from '../stores/auth'
import { useBranding } from '../stores/branding'
import { Button, Input, Field, Spinner } from '../components/ui'
import { navigate, homeFor } from '../lib/router'
import { reconnectWs } from '../ws/client'
import { toast } from '../stores/toasts'
import { backendMode } from '../lib/api'
import { ShieldCheck, User, Palette } from 'lucide-react'

const DEMO_ACCOUNTS = [
  { username: 'admin', password: 'admin123', label: 'Admin', hint: 'Full control — settings, stock, KRA reports', icon: ShieldCheck },
  { username: 'cashier', password: 'cashier123', label: 'Cashier', hint: 'POS terminal, shifts, M-Pesa entry', icon: User },
  { username: 'designer', password: 'designer123', label: 'Designer', hint: 'Design board, production queue', icon: Palette },
]

export function Login() {
  const { login } = useAuth()
  const branding = useBranding((s) => s.branding)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [demo, setDemo] = useState(false)

  // Demo hint chips appear only when the backend probe settled on demo
  // (static deploy). serverMode() distinguishes probe-forced demo from real.
  backendMode().then((m) => setDemo(m === 'demo')).catch(() => undefined)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await login(username.trim(), password)
      reconnectWs()
      toast.success(`Welcome back, ${username.trim()}`)
      navigate(homeFor(useAuth.getState().user))
    } catch (err: any) {
      setError(err?.message || 'Login failed')
    } finally {
      setBusy(false)
    }
  }

  const quickLogin = async (u: string, p: string) => {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await login(u, p)
      reconnectWs()
      toast.success(`Welcome back, ${u}`)
      navigate(homeFor(useAuth.getState().user))
    } catch (err: any) {
      setError(err?.message || 'Login failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-h-full flex flex-col items-center justify-center bg-shell px-4 py-10 overflow-y-auto">
      <div className="w-full max-w-sm">
        <div className="flex flex-col items-center mb-6">
          <div className="w-16 h-16 rounded-card bg-brand border-2 border-brand-strong shadow-brutal-brand flex items-center justify-center text-brand-ink font-black text-2xl mb-4">
            {(branding.store_name || branding.app_name || 'P').slice(0, 1).toUpperCase()}
          </div>
          <h1 className="text-on-shell text-xl font-bold text-center">
            {branding.store_name || branding.app_name}
          </h1>
          <p className="text-on-shell-muted text-sm mt-0.5">{branding.app_name}</p>
        </div>

        <form
          onSubmit={submit}
          className="bg-surface border-2 border-line-strong rounded-card shadow-brutal p-5 space-y-4"
        >
          <Field label="Username">
            <Input
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              autoComplete="username"
              autoFocus
              required
              placeholder="e.g. admin"
            />
          </Field>
          <Field label="Password">
            <Input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="current-password"
              required
              placeholder="••••••••"
            />
          </Field>
          {error && (
            <p role="alert" className="text-danger-text text-sm font-semibold bg-danger-bg border border-danger-text/30 rounded-input px-3 py-2">
              {error}
            </p>
          )}
          <Button type="submit" variant="primary" size="lg" className="w-full" disabled={busy}>
            {busy ? <Spinner className="border-t-brand-ink" /> : 'Sign in'}
          </Button>
          <button
            type="button"
            onClick={() => navigate('/pin')}
            className="w-full min-h-11 flex items-center justify-center text-center text-sm font-semibold text-ink-muted hover:text-ink underline decoration-line hover:decoration-line-strong"
          >
            Quick PIN switch instead →
          </button>
        </form>

        {demo && (
          <div className="mt-4 bg-surface border-2 border-line-strong rounded-card shadow-brutal p-4">
            <p className="text-[12px] uppercase font-bold text-ink-muted mb-2.5">
              Demo mode — try a role
            </p>
            <div className="space-y-2">
              {DEMO_ACCOUNTS.map((a) => {
                const Icon = a.icon
                return (
                  <button
                    key={a.username}
                    type="button"
                    onClick={() => quickLogin(a.username, a.password)}
                    disabled={busy}
                    className="w-full flex items-center gap-3 p-2.5 rounded-input border-2 border-line bg-surface-muted hover:border-line-strong hover:bg-surface text-left transition-colors"
                  >
                    <span className="w-9 h-9 rounded-input bg-surface border-2 border-line-strong flex items-center justify-center text-ink shrink-0" aria-hidden>
                      <Icon size={17} strokeWidth={2.25} />
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="block text-[13px] font-bold text-ink">
                        {a.label} <span className="font-mono font-normal text-ink-subtle text-[11px]">{a.username} / {a.password}</span>
                      </span>
                      <span className="block text-[11px] text-ink-muted truncate">{a.hint}</span>
                    </span>
                  </button>
                )
              })}
            </div>
            <p className="text-[11px] text-ink-subtle mt-2.5">
              Data lives in this browser only — a demo of the full POS. Reset it anytime in Settings → System.
            </p>
          </div>
        )}
      </div>
    </div>
  )
}
