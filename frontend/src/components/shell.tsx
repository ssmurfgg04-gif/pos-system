import { useEffect, useState } from 'react'
import { useAuth } from '../stores/auth'
import { useBranding } from '../stores/branding'
import { usePosSearch } from '../stores/search'
import { navigate } from '../lib/router'
import { Spinner, OfflineBanner } from './ui'
import { startHeartbeat, flushQueue } from '../offline/heartbeat'
import { connectWs, disconnectWs, onWsEvent, onWsReconnect } from '../ws/client'
import { toast } from '../stores/toasts'
import { useNet } from '../offline/heartbeat'
import { backendMode, isDemoSync, getDesktopStatus, api, type DesktopStatus } from '../lib/api'
import {
  ShoppingCart, ReceiptText, Palette, Package, Coins, BarChart3, Users, Settings,
  LogOut, RefreshCw, FlaskConical, Power, PowerOff, BookUser, Truck, Search, AlertTriangle,
} from 'lucide-react'

// Nav items: shown strictly by permission (server enforces regardless).
// Lucide icons (shadcn's icon set) — no emoji anywhere in the chrome.
const NAV: { to: string; label: string; perm: string; icon: typeof ShoppingCart }[] = [
  { to: '/', label: 'Sell', perm: 'pos.sell', icon: ShoppingCart },
  { to: '/orders', label: 'Orders', perm: 'orders.view', icon: ReceiptText },
  { to: '/customers', label: 'Customers', perm: 'customers.view', icon: BookUser },
  { to: '/design', label: 'Design', perm: 'design.view', icon: Palette },
  { to: '/inventory', label: 'Inventory', perm: 'products.view', icon: Package },
  { to: '/suppliers', label: 'Suppliers', perm: 'suppliers.view', icon: Truck },
  { to: '/shifts', label: 'Shifts', perm: 'shifts.manage', icon: Coins },
  { to: '/reports', label: 'Reports', perm: 'reports.view', icon: BarChart3 },
  { to: '/users', label: 'People', perm: 'users.manage', icon: Users },
  { to: '/settings', label: 'Settings', perm: 'settings.manage', icon: Settings },
]

// Desktop composition: 72px header on top; below it a dark 190px navigation
// rail (light workspace starts after it) — the POS adds the 430px checkout
// rail on the right. The zones never collapse or overlap at desktop sizes.
export function AppShell({ current, children }: { current: string; children: React.ReactNode }) {
  const { user, logout } = useAuth()
  const branding = useBranding((s) => s.branding)
  const net = useNet()
  const search = usePosSearch((s) => s.query)
  const setSearch = usePosSearch((s) => s.setQuery)
  const [demo, setDemo] = useState(isDemoSync())
  const [desk, setDesk] = useState<DesktopStatus | null>(null)
  const [stopping, setStopping] = useState(false)
  const [updateNote, setUpdateNote] = useState<{ latest: string } | null>(null)
  const [shops, setShops] = useState<{ id: string; name: string }[] | null>(null)
  const [clock, setClock] = useState(() => new Date())
  useEffect(() => {
    const t = window.setInterval(() => setClock(new Date()), 20000)
    return () => window.clearInterval(t)
  }, [])
  useEffect(() => {
    if (!user) return
    let live = true
    api.get<{ id: string; name: string }[]>('/api/v1/shops').then((list) => {
      if (live && list.length > 1) setShops(list)
    }).catch(() => undefined)
    return () => { live = false }
  }, [user?.id])
  useEffect(() => {
    if (!user?.permissions.includes('settings.manage')) return
    let live = true
    api.get<{ updateAvailable: boolean; latest: string }>('/api/v1/system/update')
      .then((s) => { if (live && s.updateAvailable) setUpdateNote({ latest: s.latest }) })
      .catch(() => undefined)
    return () => { live = false }
  }, [user?.id])

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
    // Socket back after a gap: pages may have missed broadcasts — tell them
    // to reload their data, and refresh the offline-queue badge.
    const offReconnect = onWsReconnect(() => {
      useNet.getState().refreshPending()
      window.dispatchEvent(new CustomEvent('ws-reconnected'))
    })
    return () => {
      mounted = false
      off()
      offPaid()
      offOffline()
      offReconnect()
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
    try { await api.post('/api/v1/system/quit') } catch { /* server is going away — expected */ }
    logout()
    disconnectWs()
  }

  const items = NAV.filter((n) => user?.permissions.includes(n.perm))
  const active = (to: string) => (to === '/' ? current === '/' : current.startsWith(to))

  return (
    <div className="h-full flex flex-col bg-shell">
      {updateNote && (
        <button
          onClick={() => navigate('/settings')}
          className="bg-pending-bg border-b border-pending-text/30 px-3 py-2 text-[13px] font-bold text-pending-text text-center hover:underline"
        >
          Update {updateNote.latest} is ready — install it in Settings → System
        </button>
      )}
      {/* Header — 72px, logo left, global search centered, status right */}
      <header className="bg-surface border-b border-line h-[72px] px-4 flex items-center gap-4 shrink-0">
        <div className="flex items-center gap-2.5 min-w-0 w-52 shrink-0">
          {branding.brand_logo_url ? (
            <img src={branding.brand_logo_url} alt="" className="w-9 h-9 rounded-input object-contain bg-surface border border-line shrink-0" />
          ) : (
            <div className="w-9 h-9 rounded-input bg-brand flex items-center justify-center text-white font-black text-lg shrink-0">
              {(branding.store_name || branding.app_name || 'P').slice(0, 1).toUpperCase()}
            </div>
          )}
          <div className="hidden sm:block min-w-0">
            <p className="text-ink font-bold text-sm truncate leading-tight">
              {branding.store_name || branding.app_name}
            </p>
            <p className="text-ink-subtle text-[11px] leading-tight">{branding.app_name}</p>
          </div>
        </div>
        {shops && shops.length > 1 && (
          <select
            value={user?.shopId || ''}
            onChange={async (e) => {
              const sid = e.target.value
              if (!sid || sid === user?.shopId) return
              try {
                const res = await api.post<{ token: string }>('/api/v1/auth/switch', { shopId: sid })
                localStorage.setItem('pos_token', res.token)
                location.reload()
              } catch (err: any) {
                toast.error('Switch failed', err?.message)
              }
            }}
            className="hidden sm:block min-h-9 px-2 rounded-input bg-surface text-ink text-[12px] font-semibold border border-line-strong"
            title="Switch shop"
          >
            {shops.map((s) => (
              <option key={s.id} value={s.id}>{s.name}</option>
            ))}
          </select>
        )}

        {/* Global catalog search — drives the Sell screen filter */}
        <div className="hidden md:block flex-1 max-w-xl mx-auto">
          <div className="relative">
            <Search size={15} strokeWidth={2.5} className="absolute left-3 top-1/2 -translate-y-1/2 text-ink-subtle pointer-events-none" aria-hidden />
            <input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              onKeyDown={(e) => { if (e.key === 'Enter') navigate('/') }}
              placeholder="Search products…"
              aria-label="Search products"
              className="w-full min-h-10 pl-9 pr-3 bg-surface-muted border border-line rounded-input text-ink text-[13px] placeholder:text-ink-subtle focus:outline-none focus-visible:outline-2 focus-visible:outline-brand focus-visible:-outline-offset-1"
            />
          </div>
        </div>

        <div className="flex items-center gap-2 shrink-0 ml-auto">
          <span className="hidden lg:block text-[12.5px] font-semibold text-ink-muted tabular whitespace-nowrap" title="Local time">
            {clock.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
          </span>
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
              title={
                net.failed > 0
                  ? `${net.pending} offline sale(s) queued — ${net.failed} hit errors and will keep retrying. Click to sync now.`
                  : 'Sync queued offline sales'
              }
              className={`min-h-9 px-2.5 rounded-input text-[12px] font-bold border inline-flex items-center gap-1.5 ${
                net.failed > 0
                  ? 'bg-danger-bg text-danger-text border-danger-text/40'
                  : 'bg-pending-bg text-pending-text border-pending-text/40'
              }`}
            >
              <RefreshCw size={13} aria-hidden />
              {net.pending}
              {net.failed > 0 && <AlertTriangle size={12} aria-label="Some queued sales hit errors" />}
            </button>
          )}
          <div className="hidden md:flex flex-col items-end leading-tight mr-1">
            <span className="text-ink text-[13px] font-bold">{user?.fullName || user?.username}</span>
            <span className="text-ink-subtle text-[11px]">{user?.roleName}</span>
          </div>
          <button
            onClick={doLogout}
            className="min-h-10 min-w-10 rounded-input text-ink-muted hover:text-ink hover:bg-surface-muted flex items-center justify-center"
            aria-label="Log out"
            title="Log out"
          >
            <LogOut size={17} aria-hidden />
          </button>
          {canQuit && (
            <button
              onClick={quitApp}
              className="min-h-10 min-w-10 rounded-input text-ink-muted hover:text-danger-text hover:bg-danger-bg flex items-center justify-center"
              aria-label="Quit application"
              title={`Quit ${branding.app_name || 'app'} (stops the local app)`}
            >
              <Power size={17} aria-hidden />
            </button>
          )}
        </div>
      </header>

      <OfflineBanner />

      <div className="flex-1 min-h-0 flex">
        {/* Dark slim navigation rail — 190px at desktop, icon strip below lg */}
        <nav
          className="bg-sidebar flex lg:flex-col items-center lg:items-stretch gap-1 p-2 lg:p-2.5 lg:w-[190px] shrink-0 overflow-x-auto lg:overflow-y-auto lg:overflow-x-hidden"
          aria-label="Main"
        >
          {items.map((n) => {
            const Icon = n.icon
            const isActive = active(n.to)
            return (
              <button
                key={n.to}
                onClick={() => navigate(n.to)}
                aria-current={isActive ? 'page' : undefined}
                className={`min-h-11 lg:min-h-12 px-3 lg:px-3.5 rounded-input text-[13.5px] font-semibold whitespace-nowrap transition-colors inline-flex items-center gap-2.5 relative ${
                  isActive
                    ? 'bg-sidebar-raised text-sidebar-ink'
                    : 'text-sidebar-muted hover:text-sidebar-ink hover:bg-sidebar-raised/60'
                }`}
              >
                {isActive && <span className="absolute left-0 top-1/2 -translate-y-1/2 hidden lg:block w-1 h-6 rounded-pill bg-brand" aria-hidden />}
                <Icon size={17} strokeWidth={2.25} aria-hidden />
                {n.label}
              </button>
            )
          })}
        </nav>

        {/* Content canvas — light workspace, full bleed at desktop */}
        <main className="flex-1 min-w-0 min-h-0 overflow-y-auto bg-shell">
          <div className={`p-4 lg:p-5 ${current === '/' ? 'h-full' : 'max-w-7xl mx-auto'}`}>{children}</div>
        </main>
      </div>

      {/* Desktop app stopped — safe-to-close takeover */}
      {stopping && (
        <div className="fixed inset-0 z-70 bg-shell flex items-center justify-center p-4" role="status">
          <div className="bg-surface border border-line rounded-card shadow-brutal p-6 max-w-sm w-full text-center">
            <div className="w-12 h-12 mx-auto rounded-input bg-surface-muted border border-line flex items-center justify-center mb-3 text-ink-subtle">
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
    </div>
  )
}

export function Booting() {
  return (
    <div className="h-full flex flex-col items-center justify-center gap-3 bg-shell">
      <Spinner className="w-7 h-7 border-4 border-line border-t-brand" />
      <p className="text-ink-muted text-sm">Loading…</p>
    </div>
  )
}
