import { useEffect, useState } from 'react'
import { useRoute, homeFor, withBase } from './lib/router'
import { useAuth } from './stores/auth'
import { useBranding } from './stores/branding'
import { AppShell, Booting } from './components/shell'
import { Lock } from 'lucide-react'

import { api, User } from './lib/api'
import { Login } from './pages/Login'
import { Pin } from './pages/Pin'
import { Rotate } from './pages/Rotate'
import { Onboarding } from './pages/Onboarding'
import { Pos } from './pages/Pos'
import { Customers } from './pages/Customers'
import { Inventory } from './pages/Inventory'
import { Suppliers } from './pages/Suppliers'
import { Orders } from './pages/Orders'
import { DesignBoard } from './pages/DesignBoard'
import { Shifts } from './pages/Shifts'
import { Reports } from './pages/Reports'
import { Users } from './pages/Users'
import { Settings } from './pages/Settings'

export function App() {
  const [path] = useRoute()
  const { user, ready, refresh } = useAuth()
  const { loaded, load } = useBranding()

  useEffect(() => {
    refresh()
    load()
  }, [])

  if (!ready || !loaded) return <Booting />

  // Public routes.
  if (!user) {
    if (path === '/pin') return <Pin />
    return <Login />
  }

  // Seeded defaults stop here until rotated (server enforces the same gate).
  if (user.mustRotate) return <Rotate onDone={refresh} />

  return <GatedApp path={path} user={user} />
}

function GatedApp({ path, user }: { path: string; user: User }) {
  const [onboarded, setOnboarded] = useState<boolean | null>(null)
  const isAdmin = user.permissions.includes('users.manage')

  useEffect(() => {
    if (!isAdmin) {
      setOnboarded(true)
      return
    }
    let live = true
    api.get<Record<string, string>>('/api/v1/settings')
      .then((s) => { if (live) setOnboarded(s.onboarding_done === 'true') })
      .catch(() => { if (live) setOnboarded(true) })
    return () => { live = false }
  }, [isAdmin])

  // Guided first run for admins; everyone else (and errors) skips it.
  if (isAdmin && onboarded === false) return <Onboarding onDone={() => setOnboarded(true)} />
  if (isAdmin && onboarded === null) return <Booting />

  const page = (() => {
    switch (true) {
      case path === '/':
        return user.permissions.includes('pos.sell') ? <Pos /> : <NoPerm perm="pos.sell" />
      case path.startsWith('/orders'):
        return user.permissions.includes('orders.view') ? <Orders /> : <NoPerm perm="orders.view" />
      case path.startsWith('/customers'):
        return user.permissions.includes('customers.view') ? <Customers /> : <NoPerm perm="customers.view" />
      case path.startsWith('/design'):
        return user.permissions.includes('design.view') ? <DesignBoard /> : <NoPerm perm="design.view" />
      case path.startsWith('/inventory'):
        return user.permissions.includes('products.view') ? <Inventory /> : <NoPerm perm="products.view" />
      case path.startsWith('/suppliers'):
        return user.permissions.includes('suppliers.view') ? <Suppliers /> : <NoPerm perm="suppliers.view" />
      case path.startsWith('/shifts'):
        return user.permissions.includes('shifts.manage') ? <Shifts /> : <NoPerm perm="shifts.manage" />
      case path.startsWith('/reports'):
        return user.permissions.includes('reports.view') ? <Reports /> : <NoPerm perm="reports.view" />
      case path.startsWith('/users'):
        return user.permissions.includes('users.manage') ? <Users /> : <NoPerm perm="users.manage" />
      case path.startsWith('/settings'):
        return user.permissions.includes('settings.manage') ? <Settings /> : <NoPerm perm="settings.manage" />
      default:
        return <NotFound />
    }
  })()

  // Users without the sell permission (e.g. a pure designer) never sit on
  // a "not available" screen — redirect to their role's home instead.
  if (path === '/' && !user.permissions.includes('pos.sell')) {
    const home = homeFor(user)
    if (home !== '/') return <Redirect to={home} />
  }

  return <AppShell current={path}>{page}</AppShell>
}

function NoPerm({ perm }: { perm: string }) {
  return (
    <div className="bg-surface border-2 border-line-strong rounded-card shadow-brutal p-8 text-center">
      <div className="w-14 h-14 mx-auto rounded-card border-2 border-line-strong bg-surface-muted flex items-center justify-center text-ink-muted mb-3" aria-hidden>
        <Lock size={26} strokeWidth={2.25} />
      </div>
      <h2 className="font-bold text-ink">Not available for your role</h2>
      <p className="text-sm text-ink-muted mt-1">
        This screen requires the <code className="bg-surface-muted px-1.5 py-0.5 rounded text-xs">{perm}</code> permission.
        Ask an administrator to grant it via People &amp; Roles.
      </p>
    </div>
  )
}

function Redirect({ to }: { to: string }) {
  useEffect(() => {
    history.replaceState({}, '', withBase(to))
    window.dispatchEvent(new PopStateEvent('popstate'))
  }, [to])
  return <Booting />
}

function NotFound() {
  return (
    <div className="bg-surface border-2 border-line-strong rounded-card shadow-brutal p-8 text-center">
      <h2 className="font-bold text-ink">Page not found</h2>
    </div>
  )
}
