import { useEffect } from 'react'
import { useRoute } from './lib/router'
import { useAuth } from './stores/auth'
import { useBranding } from './stores/branding'
import { AppShell, Booting } from './components/shell'

import { Login } from './pages/Login'
import { Pin } from './pages/Pin'
import { Pos } from './pages/Pos'
import { Inventory } from './pages/Inventory'
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

  const page = (() => {
    switch (true) {
      case path === '/':
        return user.permissions.includes('pos.sell') ? <Pos /> : <NoPerm perm="pos.sell" />
      case path.startsWith('/orders'):
        return user.permissions.includes('orders.view') ? <Orders /> : <NoPerm perm="orders.view" />
      case path.startsWith('/design'):
        return user.permissions.includes('design.view') ? <DesignBoard /> : <NoPerm perm="design.view" />
      case path.startsWith('/inventory'):
        return user.permissions.includes('products.view') ? <Inventory /> : <NoPerm perm="products.view" />
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

  return <AppShell current={path}>{page}</AppShell>
}

function NoPerm({ perm }: { perm: string }) {
  return (
    <div className="bg-surface border-2 border-line-strong rounded-card shadow-brutal p-8 text-center">
      <p className="text-2xl mb-2" aria-hidden>🔒</p>
      <h2 className="font-bold text-ink">Not available for your role</h2>
      <p className="text-sm text-ink-muted mt-1">
        This screen requires the <code className="bg-surface-muted px-1.5 py-0.5 rounded text-xs">{perm}</code> permission.
        Ask an administrator to grant it via People &amp; Roles.
      </p>
    </div>
  )
}

function NotFound() {
  return (
    <div className="bg-surface border-2 border-line-strong rounded-card shadow-brutal p-8 text-center">
      <h2 className="font-bold text-ink">Page not found</h2>
    </div>
  )
}
