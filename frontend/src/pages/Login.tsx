import { useEffect, useState } from 'react'
import { useAuth } from '../stores/auth'
import { useBranding } from '../stores/branding'
import { Button, Input, Field, Spinner } from '../components/ui'
import { navigate, homeFor } from '../lib/router'
import { reconnectWs } from '../ws/client'
import { toast } from '../stores/toasts'
import { api, backendMode } from '../lib/api'
import { ShieldCheck, User, Palette, KeyRound, LifeBuoy } from 'lucide-react'

const DEMO_ACCOUNTS = [
  { username: 'admin', password: '0000', label: 'Admin', hint: 'Full control — settings, stock, KRA reports', icon: ShieldCheck },
  { username: 'cashier', password: '0000', label: 'Cashier', hint: 'POS terminal, shifts, M-Pesa entry', icon: User },
  { username: 'designer', password: '0000', label: 'Designer', hint: 'Design board, production queue', icon: Palette },
]

type Mode = 'login' | 'setup' | 'forgot'

export function Login() {
  const { login } = useAuth()
  const branding = useBranding((s) => s.branding)
  const [mode, setMode] = useState<Mode>('login')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [demo, setDemo] = useState(false)
  const [needsSetup, setNeedsSetup] = useState(false)

  // First run with zero accounts (demo accounts no longer ship): the
  // database is fresh — the very first thing it needs is its owner.
  useEffect(() => {
    let alive = true
    backendMode().then(async (m) => {
      if (!alive) return
      setDemo(m === 'demo')
      if (m === 'demo') return
      try {
        const r = await api.get<{ hasUsers: boolean }>('/api/v1/auth/has-users')
        if (alive && !r.hasUsers) setNeedsSetup(true)
      } catch {
        /* the login attempt below will surface real errors */
      }
    }).catch(() => undefined)
    return () => { alive = false }
  }, [])

  useEffect(() => {
    if (needsSetup) setMode('setup')
  }, [needsSetup])

  const land = () => {
    reconnectWs()
    navigate(homeFor(useAuth.getState().user))
  }

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await login(username.trim(), password)
      toast.success(`Welcome back, ${username.trim()}`)
      land()
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
      toast.success(`Welcome back, ${u}`)
      land()
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
          {branding.brand_logo_url ? (
            <img src={branding.brand_logo_url} alt={branding.store_name || branding.app_name} className="w-16 h-16 rounded-card object-contain bg-surface border border-line shadow-brutal mb-4" />
          ) : (
            <div className="w-16 h-16 rounded-card bg-brand shadow-brutal-brand flex items-center justify-center text-white font-black text-2xl mb-4">
              {(branding.store_name || branding.app_name || 'P').slice(0, 1).toUpperCase()}
            </div>
          )}
          <h1 className="text-ink text-xl font-bold text-center">
            {branding.store_name || branding.app_name}
          </h1>
          <p className="text-ink-subtle text-sm mt-0.5">{branding.app_name}</p>
        </div>

        {mode === 'setup' && <SetupOwner onDone={land} />}
        {mode === 'login' && (
          <form
            onSubmit={submit}
            className="bg-surface border border-line rounded-card shadow-brutal p-5 space-y-4"
          >
            <Field label="Username">
              <Input
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoComplete="username"
                autoFocus
                required
                placeholder="your username"
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
              {busy ? <Spinner className="border-t-white" /> : 'Sign in'}
            </Button>
            <div className="flex items-center justify-between gap-2">
              <button
                type="button"
                onClick={() => { setMode('forgot'); setError('') }}
                className="text-[12.5px] font-semibold text-ink-muted hover:text-ink underline decoration-line hover:decoration-line-strong"
              >
                Forgot password?
              </button>
              <button
                type="button"
                onClick={() => navigate('/pin')}
                className="text-[12.5px] font-semibold text-ink-muted hover:text-ink underline decoration-line hover:decoration-line-strong"
                title="Shared-terminal PIN pad for shift handoffs"
              >
                PIN pad →
              </button>
            </div>
            <button
              type="button"
              onClick={() => navigate('/join')}
              className="w-full min-h-11 flex items-center justify-center text-center text-sm font-semibold text-ink-muted hover:text-ink underline decoration-line hover:decoration-line-strong"
            >
              Joining a team? Paste your join link →
            </button>
          </form>
        )}
        {mode === 'forgot' && (
          <ForgotPassword
            onBack={() => { setMode('login'); setError('') }}
            onDone={() => { setMode('login'); setError('') }}
          />
        )}

        {demo && mode === 'login' && (
          <div className="mt-4 bg-surface border border-line rounded-card shadow-brutal p-4">
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
                    className="w-full flex items-center gap-3 p-2.5 rounded-input border border-line bg-surface-muted hover:border-line-strong hover:bg-surface text-left transition-colors"
                  >
                    <span className="w-9 h-9 rounded-input bg-surface border border-line flex items-center justify-center text-ink shrink-0" aria-hidden>
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

// ---- First-run owner setup (replaces the old seeded demo accounts) ----

function SetupOwner({ onDone }: { onDone: () => void }) {
  const [fullName, setFullName] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [pin, setPin] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [recoveryCode, setRecoveryCode] = useState('')

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (busy) return
    if (password !== confirm) {
      setError('Passwords do not match')
      return
    }
    if (pin && !/^\d{4}$/.test(pin)) {
      setError('PIN must be exactly 4 digits')
      return
    }
    setBusy(true)
    setError('')
    try {
      const res = await api.post<{ token: string; user: any; recoveryCode: string }>(
        '/api/v1/auth/bootstrap',
        { username: username.trim(), fullName: fullName.trim(), password, pin },
      )
      localStorage.setItem('pos_token', res.token)
      await useAuth.getState().refresh()
      setRecoveryCode(res.recoveryCode)
    } catch (err: any) {
      setError(err?.message || 'Setup failed')
    } finally {
      setBusy(false)
    }
  }

  if (recoveryCode) {
    return (
      <div className="bg-surface border border-brand rounded-card shadow-brutal-brand p-5 space-y-4">
        <div className="flex items-center gap-2.5">
          <span className="w-9 h-9 rounded-input bg-brand flex items-center justify-center text-white shrink-0" aria-hidden>
            <KeyRound size={17} strokeWidth={2.25} />
          </span>
          <h2 className="text-ink font-bold">Owner account created</h2>
        </div>
        <p className="text-[13px] text-ink-muted">
          This is your <span className="font-bold text-ink">recovery code</span> — the only way back in if
          the password is ever forgotten. Write it down and store it away from this machine.
          It is shown <span className="font-bold text-ink">only once</span>.
        </p>
        <p className="text-center text-2xl font-mono font-bold tracking-widest text-ink bg-surface-muted border border-line rounded-input py-3 select-all">
          {recoveryCode}
        </p>
        <Button
          variant="primary"
          size="lg"
          className="w-full"
          onClick={() => {
            // Session is already active (bootstrap returned a token).
            toast.success('Welcome to your store')
            onDone()
          }}
        >
          I saved it — open my store
        </Button>
      </div>
    )
  }

  return (
    <form onSubmit={submit} className="bg-surface border border-line rounded-card shadow-brutal p-5 space-y-4">
      <div className="flex items-center gap-2.5">
        <span className="w-9 h-9 rounded-input bg-brand flex items-center justify-center text-white shrink-0" aria-hidden>
          <ShieldCheck size={17} strokeWidth={2.25} />
        </span>
        <div>
          <h2 className="text-ink font-bold leading-tight">Set up your store's owner</h2>
          <p className="text-[12px] text-ink-subtle">This machine has no accounts yet — create the first one.</p>
        </div>
      </div>
      <Field label="Your name">
        <Input value={fullName} onChange={(e) => setFullName(e.target.value)} placeholder="e.g. Jane Njeri" autoFocus />
      </Field>
      <Field label="Username">
        <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" required placeholder="e.g. jane" />
      </Field>
      <Field label="Password (6+ characters)">
        <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" required placeholder="••••••••" />
      </Field>
      <Field label="Confirm password">
        <Input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" required placeholder="••••••••" />
      </Field>
      <Field label="Quick PIN (optional — 4 digits)">
        <Input
          value={pin}
          onChange={(e) => setPin(e.target.value.replace(/\D/g, '').slice(0, 4))}
          inputMode="numeric"
          placeholder="e.g. 7942"
        />
      </Field>
      {error && (
        <p role="alert" className="text-danger-text text-sm font-semibold bg-danger-bg border border-danger-text/30 rounded-input px-3 py-2">
          {error}
        </p>
      )}
      <Button type="submit" variant="primary" size="lg" className="w-full" disabled={busy}>
        {busy ? <Spinner className="border-t-white" /> : 'Create owner account'}
      </Button>
    </form>
  )
}

// ---- Forgot password (recovery code redemption) ----

function ForgotPassword({ onBack, onDone }: { onBack: () => void; onDone: () => void }) {
  const [username, setUsername] = useState('')
  const [code, setCode] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await api.post('/api/v1/auth/forgot-password', {
        username: username.trim(),
        recoveryCode: code.trim(),
        newPassword,
      })
      toast.success('Password reset', 'Sign in with the new password.')
      onDone()
    } catch (err: any) {
      setError(err?.message || 'Invalid details')
    } finally {
      setBusy(false)
    }
  }

  return (
    <form onSubmit={submit} className="bg-surface border border-line rounded-card shadow-brutal p-5 space-y-4">
      <div className="flex items-center gap-2.5">
        <span className="w-9 h-9 rounded-input bg-surface-muted border border-line flex items-center justify-center text-ink shrink-0" aria-hidden>
          <LifeBuoy size={17} strokeWidth={2.25} />
        </span>
        <div>
          <h2 className="text-ink font-bold leading-tight">Reset your password</h2>
          <p className="text-[12px] text-ink-subtle">Use the recovery code you saved at setup.</p>
        </div>
      </div>
      <Field label="Username">
        <Input value={username} onChange={(e) => setUsername(e.target.value)} required placeholder="your username" autoFocus />
      </Field>
      <Field label="Recovery code">
        <Input
          value={code}
          onChange={(e) => setCode(e.target.value)}
          required
          placeholder="XXXX-XXXX-XXXX-XXXX"
          className="font-mono"
        />
      </Field>
      <Field label="New password (6+ characters)">
        <Input type="password" value={newPassword} onChange={(e) => setNewPassword(e.target.value)} autoComplete="new-password" required placeholder="••••••••" />
      </Field>
      {error && (
        <p role="alert" className="text-danger-text text-sm font-semibold bg-danger-bg border border-danger-text/30 rounded-input px-3 py-2">
          {error}
        </p>
      )}
      <Button type="submit" variant="primary" size="lg" className="w-full" disabled={busy}>
        {busy ? <Spinner className="border-t-white" /> : 'Reset password'}
      </Button>
      <button
        type="button"
        onClick={onBack}
        className="w-full text-center text-sm font-semibold text-ink-muted hover:text-ink underline decoration-line hover:decoration-line-strong"
      >
        Back to sign in
      </button>
      <p className="text-[11.5px] text-ink-subtle">
        No recovery code? Run <span className="font-mono font-bold text-ink">ledgerpos reset-owner --username you</span> on
        the till machine to mint a fresh one, or ask whoever set the store up.
      </p>
    </form>
  )
}
