import { useEffect, useState } from 'react'
import { api, PinUser } from '../lib/api'
import { useAuth } from '../stores/auth'
import { useBranding } from '../stores/branding'
import { Keypad, Spinner } from '../components/ui'
import { navigate } from '../lib/router'
import { reconnectWs } from '../ws/client'
import { toast } from '../stores/toasts'

// Shift-handoff screen: pick your face, tap your 4-digit PIN. PINs are
// bcrypt-hashed server-side with escalating lockout — brute force is slow.
export function Pin() {
  const { pinLogin } = useAuth()
  const branding = useBranding((s) => s.branding)
  const [users, setUsers] = useState<PinUser[] | null>(null)
  const [userId, setUserId] = useState<number | null>(null)
  const [pin, setPin] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [lockNote, setLockNote] = useState('')

  useEffect(() => {
    api.get<PinUser[]>('/api/v1/auth/pin-users').then(setUsers).catch(() => setUsers([]))
  }, [])

  const submit = async (code: string) => {
    if (busy || !userId) return
    setBusy(true)
    setError('')
    setLockNote('')
    try {
      await pinLogin(userId, code)
      reconnectWs()
      toast.success('Signed in')
      navigate('/')
    } catch (err: any) {
      setPin('')
      const msg = err?.message || 'Wrong PIN'
      setError(msg)
      if (/locked/i.test(msg)) setLockNote('Too many attempts — the account is temporarily locked.')
    } finally {
      setBusy(false)
    }
  }

  const onDigit = (d: string) => {
    if (busy) return
    const next = (pin + d).slice(0, 4)
    setPin(next)
    setError('')
    if (next.length === 4) setTimeout(() => submit(next), 120)
  }

  const selected = users?.find((u) => u.id === userId)

  return (
    <div className="min-h-full flex flex-col items-center justify-center bg-shell px-4 py-8">
      <div className="w-full max-w-sm">
        <h1 className="text-on-shell text-lg font-bold text-center mb-1">
          {selected ? `Hello, ${selected.fullName}` : branding.store_name || branding.app_name}
        </h1>
        <p className="text-on-shell-muted text-sm text-center mb-5">
          {selected ? 'Enter your 4-digit PIN' : 'Who is starting this shift?'}
        </p>

        <div className="bg-surface border-2 border-line-strong rounded-card shadow-brutal p-5">
          {!users ? (
            <div className="py-10 flex justify-center"><Spinner /></div>
          ) : !userId ? (
            <div className="grid grid-cols-2 gap-2">
              {users.map((u) => (
                <button
                  key={u.id}
                  onClick={() => setUserId(u.id)}
                  className="min-h-20 px-3 py-3 bg-surface-muted border-2 border-line-strong rounded-input shadow-brutal-sm
                    active:translate-x-[2px] active:translate-y-[2px] active:shadow-none transition-transform duration-75 text-left"
                >
                  <p className="font-bold text-ink text-sm leading-tight">{u.fullName}</p>
                  <p className="text-ink-muted text-xs mt-0.5">{u.roleName}</p>
                </button>
              ))}
            </div>
          ) : (
            <>
              <div className="flex justify-center gap-3 mb-5" aria-label={`PIN: ${pin.length} of 4 digits`}>
                {[0, 1, 2, 3].map((i) => (
                  <span
                    key={i}
                    className={`w-12 h-12 rounded-input border-2 flex items-center justify-center text-xl font-bold ${
                      pin.length > i ? 'bg-brand border-brand-strong text-brand-ink' : 'bg-surface-muted border-line text-ink-subtle'
                    }`}
                  >
                    {pin.length > i ? '•' : ''}
                  </span>
                ))}
              </div>
              {error && (
                <p role="alert" className="text-center text-danger-text text-sm font-semibold mb-3">
                  {error}
                </p>
              )}
              {lockNote && <p className="text-center text-pending-text text-xs mb-3">{lockNote}</p>}
              <Keypad onDigit={onDigit} onBack={() => setPin(pin.slice(0, -1))} onClear={() => setPin('')} />
              <button
                onClick={() => { setUserId(null); setPin(''); setError('') }}
                className="w-full mt-4 min-h-11 flex items-center justify-center text-center text-sm font-semibold text-ink-muted hover:text-ink"
              >
                ← Pick someone else
              </button>
            </>
          )}
        </div>

        <button
          onClick={() => navigate('/login')}
          className="w-full mt-4 min-h-11 flex items-center justify-center text-center text-sm font-semibold text-on-shell-muted hover:text-on-shell"
        >
          Password sign-in instead
        </button>
      </div>
    </div>
  )
}
