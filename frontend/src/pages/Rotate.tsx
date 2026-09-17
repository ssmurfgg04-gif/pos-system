// Rotate — forced credential rotation. Rendered instead of the whole app
// while me.mustRotate is true: set a personal password + PIN, then reload.

import { useState } from 'react'
import { api } from '../lib/api'
import { useAuth } from '../stores/auth'
import { Button, Card, Field, Input, Spinner } from '../components/ui'
import { toast } from '../stores/toasts'
import { Lock } from 'lucide-react'

export function Rotate({ onDone }: { onDone: () => void }) {
  const user = useAuth((s) => s.user)
  const [password, setPassword] = useState('')
  const [pin, setPin] = useState('')
  const [busy, setBusy] = useState(false)
  const validPw = password.length >= 6
  const validPin = /^\d{4}$/.test(pin)

  const save = async () => {
    if (!user || !validPw || !validPin) return
    setBusy(true)
    try {
      // Each credential save kills the session used to perform it (by
      // design), so re-login after each step and land with a live token.
      await api.put(`/api/v1/users/${user.id}/password`, { password })
      const res = await api.post<{ token: string }>('/api/v1/auth/login', {
        username: user.username,
        password,
      })
      localStorage.setItem('pos_token', res.token)
      await api.put(`/api/v1/users/${user.id}/pin`, { pin })
      const fresh = await api.post<{ token: string }>('/api/v1/auth/login', {
        username: user.username,
        password,
      })
      localStorage.setItem('pos_token', fresh.token)
      toast.success('Credentials updated')
      onDone()
    } catch (e: any) {
      toast.error('Save failed', e?.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-h-dvh bg-surface-muted flex items-center justify-center p-4">
      <Card
        title="Set your login details"
        sub={user ? `The installer password is public. Set your own to enter your shop.` : 'First login'}
        pad
      >
        <div className="space-y-3 min-w-72">
          <Field label="New password" hint="At least 6 characters.">
            <Input
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              type="password"
              autoFocus
              placeholder="••••••"
            />
          </Field>
          <Field label="New 4-digit PIN" hint="Used for fast shift switch at this till.">
            <Input
              value={pin}
              onChange={(e) => setPin(e.target.value.replace(/\D/g, '').slice(0, 4))}
              inputMode="numeric"
              placeholder="••••"
            />
          </Field>
          <Button variant="primary" size="lg" className="w-full" onClick={save} disabled={busy || !validPw || !validPin}>
            {busy ? <Spinner /> : <span className="inline-flex items-center gap-2"><Lock size={16} strokeWidth={2.25} aria-hidden />Save & continue</span>}
          </Button>
        </div>
      </Card>
    </div>
  )
}
