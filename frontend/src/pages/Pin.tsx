import { useEffect, useRef, useState } from 'react'
import { api, PinUser } from '../lib/api'
import { useAuth } from '../stores/auth'
import { useBranding } from '../stores/branding'
import { Keypad, Spinner } from '../components/ui'
import { navigate, homeFor } from '../lib/router'
import { reconnectWs } from '../ws/client'
import { toast } from '../stores/toasts'
import { LifeBuoy } from 'lucide-react'

// Shift-handoff screen: pick your face, tap your 4-digit PIN. PINs are
// bcrypt-hashed server-side with escalating lockout — brute force is slow.
//
// Forgot PIN? A cloud-mediated recovery rides right here on the pad: the
// till files a request with the team cloud, the owner taps Approve on any
// other till (Settings → Team) and reads out a one-time 8-digit code, the
// worker types it + picks a new PIN and lands straight in the POS. No
// email, no CLI, no admin session required on this machine.
export function Pin() {
  const { pinLogin } = useAuth()
  const branding = useBranding((s) => s.branding)
  const [users, setUsers] = useState<PinUser[] | null>(null)
  const [userId, setUserId] = useState<number | null>(null)
  const [pin, setPin] = useState('')
  const [busy, setBusy] = useState(false)
  const [recover, setRecover] = useState<null | { step: 'request' | 'code'; requestId: number; username: string }>(null)
  const busyRef = useRef(false)
  // The 4th digit auto-submits after a short beat; any other submit path
  // (Enter) or reset (Backspace/Escape/Clear/switch user) cancels it —
  // otherwise a stale timer logs you in AFTER you cleared the code.
  const submitTimer = useRef<ReturnType<typeof setTimeout> | null>(null)
  const cancelPendingSubmit = () => {
    if (submitTimer.current) {
      clearTimeout(submitTimer.current)
      submitTimer.current = null
    }
  }
  const [error, setError] = useState('')
  const [lockNote, setLockNote] = useState('')

  useEffect(() => {
    api.get<PinUser[]>('/api/v1/auth/pin-users').then(setUsers).catch(() => setUsers([]))
  }, [])

  const submit = async (code: string) => {
    // busyRef: synchronous guard — whichever submit path wins (auto or
    // Enter), the other must not fire a second login.
    if (busyRef.current || busy || !userId) return
    cancelPendingSubmit()
    busyRef.current = true
    setBusy(true)
    setError('')
    setLockNote('')
    try {
      // v1.3.2: bind the sign-in to the shop this user list came from —
      // per-shop user ids collide, and the shop picker already made the choice.
      const u = users?.find((x) => x.id === userId)
      await pinLogin(userId, code, u?.shopId)
      reconnectWs()
      toast.success('Signed in')
      navigate(homeFor(useAuth.getState().user))
    } catch (err: any) {
      setPin('')
      const msg = err?.message || 'Wrong PIN'
      setError(msg)
      if (/locked/i.test(msg)) setLockNote('Too many attempts — the account is temporarily locked.')
    } finally {
      busyRef.current = false
      setBusy(false)
    }
  }

  const onDigit = (d: string) => {
    if (busy) return
    const next = (pin + d).slice(0, 4)
    setPin(next)
    setError('')
    if (next.length === 4) {
      cancelPendingSubmit()
      submitTimer.current = setTimeout(() => submit(next), 120)
    }
  }

  // Physical keyboard alongside the touch pad: digits type the PIN,
  // Backspace erases, Escape clears, Enter submits. The picker accepts
  // 1–9 as a shortcut for the Nth face. Touch tills keep tapping —
  // nobody has to reach for the mouse to start a shift.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (busy || recover) return // recovery panel owns the keyboard
      const tag = (e.target as HTMLElement | null)?.tagName
      if (tag === 'INPUT' || tag === 'TEXTAREA') return // never hijack typing
      if (!userId) {
        // Picker: 1–9 selects the Nth user in grid order.
        if (/^[1-9]$/.test(e.key)) {
          const idx = Number(e.key) - 1
          if (users && users[idx]) {
            e.preventDefault()
            setUserId(users[idx].id)
          }
        }
        return
      }
      if (/^[0-9]$/.test(e.key)) {
        e.preventDefault()
        ;(document.activeElement as HTMLElement | null)?.blur?.()
        onDigit(e.key)
      } else if (e.key === 'Backspace') {
        e.preventDefault()
        cancelPendingSubmit()
        setPin((p) => p.slice(0, -1))
      } else if (e.key === 'Escape') {
        e.preventDefault()
        cancelPendingSubmit()
        setPin('')
      } else if (e.key === 'Enter' || e.key === ' ') {
        // On a focused pad key (or nowhere) Enter/Space just submits —
        // it also cancels the 4th-digit auto-submit so exactly one
        // login fires. On other buttons ("Pick someone else") keep
        // native activation.
        const el = document.activeElement as HTMLElement | null
        const onPad = !el || el === document.body || el.closest?.('[data-keypad]')
        if (!onPad) return
        e.preventDefault()
        cancelPendingSubmit()
        if (pin.length === 4) submit(pin)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  const selected = users?.find((u) => u.id === userId)

  return (
    <div className="min-h-full flex flex-col items-center justify-center bg-shell px-4 py-8">
      <div className="w-full max-w-sm">
        <h1 className="text-on-shell text-lg font-bold text-center mb-1">
          {recover ? 'Forgot your PIN?' : selected ? `Hello, ${selected.fullName}` : branding.store_name || branding.app_name}
        </h1>
        <p className="text-on-shell-muted text-sm text-center mb-5">
          {recover ? 'The owner can get you back in — takes a minute' : selected ? 'Enter your 4-digit PIN' : 'Who is starting this shift?'}
        </p>

        {recover ? (
          <ForgotPinPanel
            preselect={selected}
            state={recover}
            setState={setRecover}
            onDone={(uid, newPin) => {
              // Fresh PIN is set — walk straight in with it.
              setRecover(null)
              const finish = async () => {
                try {
                  const uu = users?.find((x) => x.id === uid)
                  await pinLogin(uid, newPin, uu?.shopId)
                  reconnectWs()
                  toast.success('Signed in with your new PIN')
                  navigate(homeFor(useAuth.getState().user))
                } catch {
                  // Extremely unlikely (pin just set) — fall back to the pad.
                  setUserId(uid)
                  toast.success('PIN changed — sign in with it now')
                }
              }
              finish()
            }}
            onCancel={() => { setRecover(null); setError('') }}
          />
        ) : (
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
                <p className="col-span-2 text-center text-ink-subtle text-xs mt-1">
                  Tip: press 1–{Math.min(users.length, 9)} on the keyboard to pick
                </p>
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
                {lockNote && (
                  <div className="text-center text-pending-text text-xs mb-3 space-y-1">
                    <p className="font-semibold">{lockNote}</p>
                    <p className="text-ink-muted">
                      Locked out? Tap <strong className="text-ink">Forgot PIN?</strong> below — the owner can reset it from
                      any other till in about a minute. No data is lost.
                    </p>
                  </div>
                )}
                <Keypad
                  onDigit={onDigit}
                  onBack={() => { cancelPendingSubmit(); setPin(pin.slice(0, -1)) }}
                  onClear={() => { cancelPendingSubmit(); setPin('') }}
                />
                <p className="text-center text-ink-subtle text-xs mt-3">
                  Type 0–9 on the keyboard · Backspace erases
                </p>
                <button
                  onClick={() => {
                    cancelPendingSubmit()
                    setUserId(null); setPin(''); setError('')
                  }}
                  className="w-full mt-4 min-h-11 flex items-center justify-center text-center text-sm font-semibold text-ink-muted hover:text-ink"
                >
                  ← Pick someone else
                </button>
                <button
                  onClick={() => {
                    setError('')
                    // Send the LOGIN NAME, not the display name — the reset
                    // binds to users.username (the review caught this).
                    setRecover({ step: 'request', requestId: 0, username: selected?.username || '' })
                  }}
                  className="w-full min-h-11 flex items-center justify-center gap-1.5 text-center text-sm font-semibold text-ink-muted hover:text-ink underline decoration-line hover:decoration-line-strong"
                >
                  <LifeBuoy size={14} strokeWidth={2.25} aria-hidden />Forgot PIN?
                </button>
              </>
            )}
          </div>
        )}

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

// ---- Forgot PIN: request → owner approves elsewhere → redeem code ----

function ForgotPinPanel({
  preselect,
  state,
  setState,
  onDone,
  onCancel,
}: {
  preselect?: PinUser
  state: { step: 'request' | 'code'; requestId: number; username: string }
  setState: (s: { step: 'request' | 'code'; requestId: number; username: string }) => void
  onDone: (userId: number, newPin: string) => void
  onCancel: () => void
}) {
  const [username, setUsername] = useState(preselect?.fullName || '')
  const [code, setCode] = useState('')
  const [pin, setPin] = useState('')
  const [pin2, setPin2] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  // Resume an existing request (e.g. the app was closed mid-recovery).
  useEffect(() => {
    if (state.step === 'code' || state.requestId > 0) return
    api.get<{ active: boolean; request?: { requestId: number; username: string } }>('/api/v1/auth/forgot-pin/status')
      .then((r) => {
        if (r.active && r.request) setState({ step: 'code', requestId: r.request.requestId, username: r.request.username })
      })
      .catch(() => undefined)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const sendRequest = async () => {
    if (busy) return
    if (!username.trim()) {
      setError('Type your name exactly as it appears on your sign-in button')
      return
    }
    setBusy(true)
    setError('')
    try {
      const r = await api.post<{ requestId: number; username: string; reused: boolean }>('/api/v1/auth/forgot-pin', {
        username: username.trim(),
        // deviceName deliberately omitted — the cloud already knows this
        // till's real name; sending a literal overwrote it in the roster.
      })
      setState({ step: 'code', requestId: r.requestId, username: r.username })
    } catch (err: any) {
      setError(err?.message || 'Could not send the request — check the internet')
    } finally {
      setBusy(false)
    }
  }

  const redeem = async () => {
    if (busy) return
    if (!/^\d{8}$/.test(code)) {
      setError('Enter the 8-digit code the owner read to you')
      return
    }
    if (!/^\d{4}$/.test(pin) || pin === '0000' || pin === '1234') {
      setError('Pick a 4-digit PIN that isn\u2019t 0000 or 1234')
      return
    }
    if (pin !== pin2) {
      setError('The two PINs don\u2019t match')
      return
    }
    setBusy(true)
    setError('')
    try {
      const r = await api.post<{ username: string; userId: number }>('/api/v1/auth/forgot-pin/redeem', {
        requestId: state.requestId, code: code.trim(), pin,
      })
      toast.success('PIN changed', 'You\u2019re signed in — welcome back.')
      onDone(r.userId, pin)
    } catch (err: any) {
      setError(err?.message || 'That code didn\u2019t work — check the digits')
    } finally {
      setBusy(false)
    }
  }

  if (state.step === 'request') {
    return (
      <div className="bg-surface border-2 border-line-strong rounded-card shadow-brutal p-5 space-y-4">
        <p className="text-[13px] text-ink-muted">
          Check the login name shown above your PIN pad ({state.username ? <span className="font-bold text-ink">{state.username}</span> : 'pick your name first'}),
          then send a reset request. The owner approves it from any other till and reads you a short code.
        </p>
        <label className="block">
          <span className="block text-[12px] font-bold text-ink-muted uppercase tracking-wide mb-1">Your login name</span>
          <input
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            autoFocus
            placeholder="e.g. salma-pc"
            className="w-full min-h-11 rounded-input border-2 border-line bg-surface px-3 text-sm text-ink placeholder:text-ink-subtle focus:border-brand focus:outline-none"
          />
        </label>
        {error && (
          <p role="alert" className="text-danger-text text-sm font-semibold bg-danger-bg border border-danger-text/30 rounded-input px-3 py-2">
            {error}
          </p>
        )}
        <button
          onClick={sendRequest}
          disabled={busy || !username.trim()}
          className="w-full min-h-12 rounded-input bg-brand border-2 border-brand-strong text-brand-ink font-bold text-sm shadow-brutal-sm active:translate-x-[2px] active:translate-y-[2px] active:shadow-none transition-transform duration-75 disabled:opacity-50 inline-flex items-center justify-center gap-2"
        >
          {busy ? <Spinner className="border-t-brand-ink" /> : 'Send reset request'}
        </button>
        <p className="text-[11.5px] text-ink-subtle">
          Nothing is deleted and no money is touched — this only lets the owner hand you a code.
        </p>
        <button
          onClick={onCancel}
          className="w-full text-center text-sm font-semibold text-ink-muted hover:text-ink underline decoration-line hover:decoration-line-strong"
        >
          ← Back to the PIN pad
        </button>
      </div>
    )
  }

  return (
    <div className="bg-surface border-2 border-line-strong rounded-card shadow-brutal p-5 space-y-4">
      <div className="bg-brand-soft border border-brand/30 rounded-input px-3 py-2.5 text-[12.5px] text-brand font-semibold">
        Request sent. Now call or message the owner — on their till they open{' '}
        <span className="font-bold">Settings → Team → PIN reset requests</span>, tap{' '}<span className="font-bold">Approve</span>{' '}
        (or <span className="font-bold">New code</span> if one expired) next to "{state.username}" and read you the 8-digit code.
      </div>
      <label className="block">
        <span className="block text-[12px] font-bold text-ink-muted uppercase tracking-wide mb-1">The 8-digit code</span>
        <input
          value={code}
          onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 8))}
          inputMode="numeric"
          autoFocus
          placeholder="e.g. 48291305"
          className="w-full min-h-12 rounded-input border-2 border-line bg-surface px-3 text-lg font-mono tracking-widest text-ink placeholder:text-ink-subtle focus:border-brand focus:outline-none"
        />
      </label>
      <div className="grid grid-cols-2 gap-2">
        <label className="block">
          <span className="block text-[12px] font-bold text-ink-muted uppercase tracking-wide mb-1">New PIN</span>
          <input
            value={pin}
            onChange={(e) => setPin(e.target.value.replace(/\D/g, '').slice(0, 4))}
            inputMode="numeric"
            type="password"
            placeholder="4 digits"
            className="w-full min-h-11 rounded-input border-2 border-line bg-surface px-3 text-sm text-ink placeholder:text-ink-subtle focus:border-brand focus:outline-none"
          />
        </label>
        <label className="block">
          <span className="block text-[12px] font-bold text-ink-muted uppercase tracking-wide mb-1">Repeat PIN</span>
          <input
            value={pin2}
            onChange={(e) => setPin2(e.target.value.replace(/\D/g, '').slice(0, 4))}
            inputMode="numeric"
            type="password"
            placeholder="4 digits"
            className="w-full min-h-11 rounded-input border-2 border-line bg-surface px-3 text-sm text-ink placeholder:text-ink-subtle focus:border-brand focus:outline-none"
          />
        </label>
      </div>
      {error && (
        <p role="alert" className="text-danger-text text-sm font-semibold bg-danger-bg border border-danger-text/30 rounded-input px-3 py-2">
          {error}
        </p>
      )}
      <button
        onClick={redeem}
        disabled={busy || code.length !== 8 || pin.length !== 4 || pin !== pin2}
        className="w-full min-h-12 rounded-input bg-brand border-2 border-brand-strong text-brand-ink font-bold text-sm shadow-brutal-sm active:translate-x-[2px] active:translate-y-[2px] active:shadow-none transition-transform duration-75 disabled:opacity-50"
      >
        {busy ? <Spinner className="border-t-brand-ink" /> : 'Set my new PIN'}
      </button>
      <p className="text-[11.5px] text-ink-subtle">
        The code works for 15 minutes, on this till only. Expired or mistyped past the retries? Ask the
        owner to tap Approve again — they'll get a fresh code for the same request.
      </p>
      <button
        onClick={onCancel}
        className="w-full text-center text-sm font-semibold text-ink-muted hover:text-ink underline decoration-line hover:decoration-line-strong"
      >
        ← Back to the PIN pad
      </button>
    </div>
  )
}
