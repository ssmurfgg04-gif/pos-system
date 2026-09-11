import { useEffect } from 'react'
import { useAuth } from '../stores/auth'
import { useBranding } from '../stores/branding'
import { navigate } from '../lib/router'
import { Spinner, OfflineBanner } from './ui'
import { startHeartbeat, flushQueue } from '../offline/heartbeat'
import { connectWs, disconnectWs, onWsEvent } from '../ws/client'
import { toast } from '../stores/toasts'
import { useNet } from '../offline/heartbeat'

// Nav items: shown strictly by permission (server enforces regardless).
const NAV: { to: string; label: string; perm: string; icon: string }[] = [
  { to: '/', label: 'Sell', perm: 'pos.sell', icon: '🛒' },
  { to: '/orders', label: 'Orders', perm: 'orders.view', icon: '🧾' },
  { to: '/design', label: 'Design', perm: 'design.view', icon: '🎨' },
  { to: '/inventory', label: 'Inventory', perm: 'products.view', icon: '📦' },
  { to: '/shifts', label: 'Shifts', perm: 'shifts.manage', icon: '💰' },
  { to: '/reports', label: 'Reports', perm: 'reports.view', icon: '📊' },
  { to: '/users', label: 'People', perm: 'users.manage', icon: '👥' },
  { to: '/settings', label: 'Settings', perm: 'settings.manage', icon: '⚙️' },
]

export function AppShell({ current, children }: { current: string; children: React.ReactNode }) {
  const { user, logout } = useAuth()
  const branding = useBranding((s) => s.branding)
  const net = useNet()

  useEffect(() => {
    startHeartbeat()
    connectWs()
    const off = onWsEvent('SETTINGS_UPDATED', () => useBranding.getState().load())
    const offPaid = onWsEvent('ORDER_PAID', (o: any) => {
      // Another terminal completed a sale — surface it.
      toast.success(`Order ${o?.number} paid`)
    })
    const offOffline = onWsEvent('ORDER_CREATED', () => undefined)
    const offNet = () => undefined
    return () => {
      off()
      offPaid()
      offOffline()
      offNet()
      disconnectWs()
    }
  }, [])

  const doLogout = () => {
    logout()
    disconnectWs()
    navigate('/login')
  }

  const items = NAV.filter((n) => user?.permissions.includes(n.perm))
  const active = (to: string) => (to === '/' ? current === '/' : current.startsWith(to))

  return (
    <div className="h-full flex flex-col bg-shell">
      {/* Topbar */}
      <header className="bg-shell border-b border-shell-edge px-3 sm:px-4 h-16 flex items-center gap-3 shrink-0">
        <div className="flex items-center gap-2.5 min-w-0">
          <div className="w-9 h-9 rounded-input bg-brand border-2 border-brand-strong flex items-center justify-center text-brand-ink font-black text-lg shrink-0">
            {(branding.store_name || branding.app_name || 'P').slice(0, 1).toUpperCase()}
          </div>
          <div className="hidden sm:block min-w-0">
            <p className="text-on-shell font-bold text-sm truncate leading-tight">
              {branding.store_name || branding.app_name}
            </p>
            <p className="text-on-shell-muted text-[11px] leading-tight">{branding.app_name}</p>
          </div>
        </div>

        <nav className="flex-1 flex items-center gap-1 overflow-x-auto" aria-label="Main">
          {items.map((n) => (
            <button
              key={n.to}
              onClick={() => navigate(n.to)}
              aria-current={active(n.to) ? 'page' : undefined}
              className={`min-h-11 px-3 rounded-input text-[13px] font-semibold whitespace-nowrap transition-colors ${
                active(n.to)
                  ? 'bg-shell-edge text-on-shell border border-on-shell-muted/40'
                  : 'text-on-shell-muted hover:text-on-shell hover:bg-shell-edge/60'
              }`}
            >
              <span aria-hidden className="mr-1">{n.icon}</span>
              {n.label}
            </button>
          ))}
        </nav>

        <div className="flex items-center gap-2 shrink-0">
          {net.pending > 0 && (
            <button
              onClick={() => flushQueue()}
              title="Sync queued offline sales"
              className="min-h-11 px-3 rounded-input bg-pending-bg text-pending-text text-[12px] font-bold border border-pending-text"
            >
              ↻ {net.pending}
            </button>
          )}
          <div className="hidden md:flex flex-col items-end leading-tight mr-1">
            <span className="text-on-shell text-[13px] font-bold">{user?.fullName || user?.username}</span>
            <span className="text-on-shell-muted text-[11px]">{user?.roleName}</span>
          </div>
          <button
            onClick={doLogout}
            className="min-h-11 min-w-11 rounded-input text-on-shell-muted hover:text-on-shell hover:bg-shell-edge/60 flex items-center justify-center text-lg"
            aria-label="Log out"
            title="Log out"
          >
            ⎋
          </button>
        </div>
      </header>

      <OfflineBanner />

      {/* Content canvas */}
      <main className="flex-1 min-h-0 overflow-y-auto bg-shell">
        <div className="p-3 sm:p-4 lg:p-5 max-w-7xl mx-auto">{children}</div>
      </main>
    </div>
  )
}

export function Booting() {
  return (
    <div className="h-full flex flex-col items-center justify-center gap-3 bg-shell">
      <Spinner className="w-7 h-7 border-4 border-shell-edge border-t-brand" />
      <p className="text-on-shell-muted text-sm">Loading…</p>
    </div>
  )
}
