import { useState } from 'react'
import { useAuth } from '../stores/auth'
import { useBranding } from '../stores/branding'
import { Button, Input, Field, Spinner } from '../components/ui'
import { navigate } from '../lib/router'
import { reconnectWs } from '../ws/client'
import { toast } from '../stores/toasts'

export function Login() {
  const { login } = useAuth()
  const branding = useBranding((s) => s.branding)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await login(username.trim(), password)
      reconnectWs()
      toast.success(`Welcome back, ${username.trim()}`)
      navigate('/')
    } catch (err: any) {
      setError(err?.message || 'Login failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-h-full flex flex-col items-center justify-center bg-shell px-4 py-10">
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
            className="w-full min-h-11 flex items-center justify-center text-center text-sm font-semibold text-on-shell-muted hover:text-on-shell"
          >
            Quick PIN switch instead →
          </button>
        </form>
      </div>
    </div>
  )
}
