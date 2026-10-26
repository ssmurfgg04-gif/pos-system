// ConnectGate — the Android client's first-launch screen. The APK bundles
// this SPA and talks to a till server on the shop LAN; the till address is
// captured once here, probed, and stored (api.ts serverUrl()). Desktop and
// web deploys never see this gate: it only mounts when the app runs inside
// the Capacitor shell (window.Capacitor present) or a till address was
// already saved.
//
// Anti-demo rule: when a till address is configured, a failed probe shows
// this screen again — the app NEVER falls back to the in-browser demo with
// real-looking money.

import { useEffect, useState } from 'react'
import { serverUrl, setServerUrl, clearServerUrl } from '../lib/api'
import { Wifi, ArrowRight, AlertTriangle, Store } from 'lucide-react'

function inApk(): boolean {
  return typeof (window as unknown as { Capacitor?: unknown }).Capacitor !== 'undefined'
}

async function probeHealth(base: string): Promise<boolean> {
  try {
    const ctl = new AbortController()
    const t = window.setTimeout(() => ctl.abort(), 2500)
    const res = await fetch(`${base}/api/v1/health`, { cache: 'no-store', signal: ctl.signal })
    window.clearTimeout(t)
    return res.ok && (res.headers.get('content-type') || '').includes('json')
  } catch {
    return false
  }
}

export function useConnectGate(): boolean {
  const [needed, setNeeded] = useState<boolean | null>(null)
  useEffect(() => {
    ;(async () => {
      const stored = serverUrl()
      if (!stored && !inApk()) {
        setNeeded(false)
        return
      }
      // Desktop/web with a stored URL (rare) still probes once.
      if (!stored && inApk()) {
        setNeeded(true)
        return
      }
      setNeeded(!(await probeHealth(stored)))
    })()
  }, [])
  return needed === true
}

export function ConnectScreen({ onConnected }: { onConnected: () => void }) {
  const [url, setUrl] = useState('')
  const [state, setState] = useState<'idle' | 'checking' | 'error'>('idle')
  const [message, setMessage] = useState('')

  const connect = async () => {
    const raw = url.trim().replace(/\/+$/, '')
    if (!raw) return
    const base = /^https?:\/\//.test(raw) ? raw : `http://${raw}`
    setState('checking')
    if (await probeHealth(base)) {
      setServerUrl(base)
      onConnected()
      // Full reload so every cached module picks up the new base.
      window.location.reload()
      return
    }
    setState('error')
    setMessage(`No LedgerPOS till answered at ${base}. Check the address and that the till computer is running (and this device is on the same Wi-Fi).`)
  }

  return (
    <div className="min-h-screen bg-shell flex items-center justify-center p-6">
      <div className="w-full max-w-md space-y-4 bg-surface border-2 border-line rounded-card p-6 shadow-card">
        <div className="flex items-center gap-3">
          <span className="w-11 h-11 rounded-input bg-brand-soft text-brand flex items-center justify-center" aria-hidden>
            <Store size={20} strokeWidth={2.25} />
          </span>
          <div>
            <h1 className="font-black text-ink text-lg leading-tight">Connect to your till</h1>
            <p className="text-[12.5px] text-ink-muted">One-time setup — the address is remembered on this device.</p>
          </div>
        </div>

        {state === 'error' && (
          <div role="alert" className="flex items-start gap-2 bg-pending-bg border border-pending-text/30 rounded-input p-3 text-[13px] text-pending-text font-semibold">
            <AlertTriangle size={16} strokeWidth={2.5} className="shrink-0 mt-0.5" aria-hidden />
            <span>{message}</span>
          </div>
        )}

        <label className="block space-y-1">
          <span className="text-[12px] uppercase font-bold text-ink-muted">Till server address</span>
          <input
            value={url}
            onChange={(e) => { setUrl(e.target.value); setState('idle') }}
            onKeyDown={(e) => e.key === 'Enter' && connect()}
            placeholder="http://192.168.1.20:8800"
            inputMode="url"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            className="w-full min-h-12 px-3 rounded-input border-2 border-line-strong bg-surface text-ink font-semibold focus:border-brand outline-none"
          />
        </label>
        <p className="text-[12px] text-ink-muted leading-relaxed">
          Find it on the till computer: the server window shows the address, or Settings → Network on any already-connected terminal.
        </p>

        <button
          type="button"
          onClick={connect}
          disabled={state === 'checking' || !url.trim()}
          className="w-full min-h-12 rounded-input bg-brand text-brand-ink font-black inline-flex items-center justify-center gap-2 disabled:opacity-50"
        >
          <Wifi size={16} strokeWidth={2.5} aria-hidden />
          {state === 'checking' ? 'Checking…' : 'Connect'}
          {state !== 'checking' && <ArrowRight size={16} strokeWidth={2.5} aria-hidden />}
        </button>

        {serverUrl() && !inApk() && (
          <button
            type="button"
            onClick={() => { clearServerUrl(); window.location.reload() }}
            className="w-full text-[12px] text-ink-muted underline"
          >
            Forget this till and use the browser address instead
          </button>
        )}
      </div>
    </div>
  )
}
