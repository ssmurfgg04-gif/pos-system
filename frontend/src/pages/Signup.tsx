// Signup — open a new shop (server mode with ALLOW_SIGNUP only). Creates
// the shop, its admin, and logs straight in. Usernames are unique per box.

import { useState } from 'react'
import { api } from '../lib/api'
import { useAuth } from '../stores/auth'
import { navigate } from '../lib/router'
import { Button, Field, Input, Spinner } from '../components/ui'

export function Signup() {
  const refresh = useAuth((s) => s.refresh)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [pin, setPin] = useState('')
  const [shopName, setShopName] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setError('')
    if (pin && !/^\d{4}$/.test(pin)) {
      setError('PIN must be exactly 4 digits')
      return
    }
    setBusy(true)
    try {
      const res = await api.post<{ token: string; user: { id: number } }>('/api/v1/auth/signup', {
        username: username.trim(),
        password,
        shopName: shopName.trim(),
      })
      localStorage.setItem('pos_token', res.token)
      if (pin) {
        await api.put(`/api/v1/users/${res.user.id}/pin`, { pin })
      }
      await refresh()
      navigate('/')
    } catch (err: any) {
      setError(err?.message || 'Signup failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-h-dvh bg-surface-muted flex items-center justify-center p-4">
      <div className="w-full max-w-sm">
        <form
          onSubmit={submit}
          className="bg-surface border-2 border-line-strong rounded-card shadow-brutal p-5 space-y-4"
        >
          <div>
            <h1 className="font-black text-xl text-ink">Open a new shop</h1>
            <p className="text-[13px] text-ink-muted mt-0.5">Your own database, your own login — nobody sees anyone else's shop.</p>
          </div>
          <Field label="Username">
            <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoFocus required placeholder="e.g. wanjiku" />
          </Field>
          <Field label="Password" hint="At least 6 characters.">
            <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" required placeholder="••••••••" />
          </Field>
          <Field label="Shop name">
            <Input value={shopName} onChange={(e) => setShopName(e.target.value)} required placeholder="e.g. Zawadi Prints" />
          </Field>
          <Field label="PIN (optional)" hint="For fast sign-in at the till. You can set it later.">
            <Input value={pin} onChange={(e) => setPin(e.target.value.replace(/\D/g, '').slice(0, 4))} inputMode="numeric" placeholder="4 digits" />
          </Field>
          {error && (
            <p role="alert" className="text-danger-text text-sm font-semibold bg-danger-bg border border-danger-text/30 rounded-input px-3 py-2">
              {error}
            </p>
          )}
          <Button type="submit" variant="primary" size="lg" className="w-full" disabled={busy}>
            {busy ? <Spinner className="border-t-brand-ink" /> : 'Create shop & sign in'}
          </Button>
          <button
            type="button"
            onClick={() => navigate('/login')}
            className="w-full min-h-11 flex items-center justify-center text-center text-sm font-semibold text-ink-muted hover:text-ink underline decoration-line hover:decoration-line-strong"
          >
            Already have a shop? Sign in →
          </button>
        </form>
      </div>
    </div>
  )
}
