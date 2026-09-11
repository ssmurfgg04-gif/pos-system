// Minimal history-API router (no dependency — keeps the bundle lean).

import { useEffect, useState, useCallback } from 'react'

export function useRoute(): [string, (to: string) => void] {
  const [path, setPath] = useState(location.pathname)
  useEffect(() => {
    const onPop = () => setPath(location.pathname)
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])
  const navigate = useCallback((to: string) => {
    history.pushState({}, '', to)
    setPath(to)
  }, [])
  return [path, navigate]
}

export function navigate(to: string) {
  history.pushState({}, '', to)
  window.dispatchEvent(new PopStateEvent('popstate'))
}

/**
 * Role-appropriate landing page after login. Cashiers land on the terminal;
 * designers on their board; admins/managers land on Reports — the admin's
 * job is oversight (stock, passwords, KRA returns, fixing problems), with
 * the sell screen one click away when a sale needs rescuing.
 */
export function homeFor(user: { permissions: string[] } | null | undefined): string {
  const p = user?.permissions ?? []
  if (p.includes('reports.view') && p.includes('settings.manage')) return '/reports'
  if (p.includes('design.view')) return '/design'
  if (p.includes('pos.sell')) return '/'
  if (p.includes('orders.view')) return '/orders'
  if (p.includes('products.view')) return '/inventory'
  return '/'
}
