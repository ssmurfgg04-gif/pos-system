import { useEffect, useState } from 'react'
import { useAuth } from '../stores/auth'
import { useBranding } from '../stores/branding'
import { navigate } from '../lib/router'
import { Spinner, OfflineBanner } from './ui'
import { startHeartbeat, flushQueue } from '../offline/heartbeat'
import { connectWs, disconnectWs, onWsEvent } from '../ws/client'
import { toast } from '../stores/toasts'
import { useNet } from '../offline/heartbeat'
import { backendMode, isDemoSync, getDesktopStatus, api, type DesktopStatus } from '../lib/api'
import {
  ShoppingCart, ReceiptText, Palette, Package, Coins, BarChart3, Users, Settings,
  LogOut, RefreshCw, FlaskConical, Power, PowerOff, BookUser,
} from 'lucide-react'

// Nav items: shown strictly by permission (server enforces regardless).
// Lucide icons (shadcn's icon set) — no emoji anywhere in the chrome.
const NAV: { to: string; label: string; perm: string; icon: typeof ShoppingCart }[] = [
  { to: '/', label: 'Sell', perm: 'pos.sell', icon: ShoppingCart },
  { to: '/orders', label: 'Orders', perm: 'orders.view', icon: ReceiptText },
  { to: '/customers', label: 'Customers', perm: 'customers.view', icon: BookUser },
  { to: '/design', label: 'Design', perm: 'design.view', icon: Palette },
  { to: '/inventory', label: 'Inventory', perm: 'products.view', icon: Package },
  { to: '/shifts', label: 'Shifts', perm: 'shifts.manage', icon: Coins },
  { to: '/reports', label: 'Reports', perm: 'reports.view', icon: BarChart3 },
  { to: '/users', label: 'People', perm: 'users.manage', icon: Users },
  { to: '/settings', label: 'Settings', perm: 'settings.manage', icon: Settings },
]

export function AppShell({ current, children }: { current: string; children: React.ReactNode }) {
  const { user, logout } = useAuth()
  const branding = useBranding((s) => s.branding)
  const net = useNet()
  const [demo, setDemo] = useState(isDemoSync())
  const [desk, setDesk] = useState<DesktopStatus | null>(null)
  const [stopping, setStopping] = useState(false)

  useEffect(() => {
    startHeartbeat()
    connectWs()
    let mounted = true
    backendMode().then((m) => { if (mounted) setDemo(m === 'demo') })
    getDesktopStatus().then((d) => { if (mounted) setDesk(d) }).catch(() => undefined)
    const off = onWsEvent('SETTINGS_UPDATED', () => useBranding.getState().load())
    const offPaid = onWsEvent('ORDER_PAID', (o: any) => {
      // Another terminal completed a sale — surface it.
      toast.success(`Order ${o?.number} paid`)
    })
    const offOffline = onWsEvent('ORDER_CREATED', () => undefined)
    return () => {
      mounted = false
      off()
      offPaid()
      offOffline()
      disconnectWs()
    }
  }, [])

  const doLogout = () => {
    logout()
    disconnectWs()
    navigate('/login')
  }

  // Desktop app: admin can stop the whole local app from the toolbar.
  const canQuit = !!(desk?.desktop && user?.permissions.includes('settings.manage'))
  const quitApp = async () => {
    if (stopping) return
    if (!window.confirm(`Quit ${branding.app_name || 'the app'}? The local app and its server will stop — your data is saved on this machine.`)) return
    setStopping(true)
    try { await api.post('/system/quit') } catch { /* server is going away — expected */ }
    logout()
    disconnectWs()
  }

  const items = NAV.filter((n) => user?.permissions.includes(n.perm))
  const active = (to: string) => (to === '/' ? current === '/' : current.startsWith(to))

  return (
    <div className="h-full flex flex-col bg-shell">
      {/* Topbar */}
      <header className="bg-shell border-b border-shell-edge px-3 sm:px-4 h-16 flex items-center gap-3 shrink-0">
        <div className="flex items-center gap-2.5 min-w-0">
          {branding.brand_logo_url ? (
            <img src={branding.brand_logo_url} alt="" className="w-9 h-9 rounded-input object-contain bg-surface border-2 border-line-strong shrink-0" />
          ) : (
            <div className="w-9 h-9 rounded-input bg-brand border-2 border-brand-strong flex items-center justify-center text-brand-ink font-black text-lg shrink-0">
              {(branding.store_name || branding.app_name || 'P').slice(0, 1).toUpperCase()}
            </div>
          )}
          <div className="hidden sm:block min-w-0">
            <p className="text-on-shell font-bold text-sm truncate leading-tight">
              {branding.store_name || branding.app_name}
            </p>
            <p className="text-on-shell-muted text-[11px] leading-tight">{branding.app_name}</p>
          </div>
        </div>

        <nav className="flex-1 flex items-center gap-1 overflow-x-auto" aria-label="Main">
          {items.map((n) => {
            const Icon = n.icon
            return (
              <button
                key={n.to}
                onClick={() => navigate(n.to)}
                aria-current={active(n.to) ? 'page' : undefined}
                className={`min-h-11 px-3 rounded-input text-[13px] font-semibold whitespace-nowrap transition-colors inline-flex items-center gap-1.5 ${
                  active(n.to)
                    ? 'bg-shell-edge text-on-shell border border-on-shell-muted/40'
                    : 'text-on-shell-muted hover:text-on-shell hover:bg-shell-edge/60'
                }`}
              >
                <Icon size={15} strokeWidth={2.25} aria-hidden />
                {n.label}
              </button>
            )
          })}
        </nav>

        <div className="flex items-center gap-2 shrink-0">
          {demo && (
            <span
              className="min-h-8 px-2.5 rounded-pill bg-info-bg text-info-text border border-info-text/40 text-[11px] font-bold inline-flex items-center gap-1.5 whitespace-nowrap"
              title="No server behind this build — data lives in your browser only"
            >
              <FlaskConical size={12} aria-hidden />
              Demo mode
            </span>
          )}
          {net.pending > 0 && (
            <button
              onClick={() => flushQueue()}
              title="Sync queued offline sales"
              className="min-h-11 px-3 rounded-input bg-pending-bg text-pending-text text-[12px] font-bold border border-pending-text inline-flex items-center gap-1.5"
            >
              <RefreshCw size={13} aria-hidden />
              {net.pending}
            </button>
          )}
          <div className="hidden md:flex flex-col items-end leading-tight mr-1">
            <span className="text-on-shell text-[13px] font-bold">{user?.fullName || user?.username}</span>
            <span className="text-on-shell-muted text-[11px]">{user?.roleName}</span>
          </div>
          <button
            onClick={doLogout}
            className="min-h-11 min-w-11 rounded-input text-on-shell-muted hover:text-on-shell hover:bg-shell-edge/60 flex items-center justify-center"
            aria-label="Log out"
            title="Log out"
          >
            <LogOut size={18} aria-hidden />
          </button>
          {canQuit && (
            <button
              onClick={quitApp}
              className="min-h-11 min-w-11 rounded-input text-on-shell-muted hover:text-danger-text hover:bg-shell-edge/60 flex items-center justify-center"
              aria-label="Quit application"
              title={`Quit ${branding.app_name || 'app'} (stops the local app)`}
            >
              <Power size={18} aria-hidden />
            </button>
          )}
        </div>
      </header>

      <OfflineBanner />

      {/* Desktop app stopped — safe-to-close takeover */}
      {stopping && (
        <div className="fixed inset-0 z-70 bg-shell flex items-center justify-center p-4" role="status">
          <div className="bg-surface border-2 border-line-strong rounded-card shadow-brutal p-6 max-w-sm w-full text-center">
            <div className="w-12 h-12 mx-auto rounded-input bg-surface-muted border-2 border-line-strong flex items-center justify-center mb-3 text-ink-subtle">
              <PowerOff size={22} aria-hidden />
            </div>
            <h2 className="text-ink font-bold text-lg">
              {branding.app_name || 'The app'} has stopped
            </h2>
            <p className="text-ink-muted text-sm mt-1.5">
              All sales are saved on this machine. You can close this browser tab — start the app again from its shortcut whenever you need it.
            </p>
          </div>
        </div>
      )}

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
