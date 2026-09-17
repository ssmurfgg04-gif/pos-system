// Minimal history-API router (no dependency — keeps the bundle lean).
//
// BASE_URL support: when the app is built for a subdirectory deploy
// (VITE_BASE=/demo/ — the Netlify download site serves the in-browser
// demo under /demo/), routes are pushed WITH the base and matched
// WITHOUT it, so every page/component keeps using clean paths like
// '/reports'.

import { useEffect, useState, useCallback } from 'react'

const BASE = ((import.meta.env.BASE_URL as string | undefined) ?? '/')

/** '/demo/reports' -> '/reports' (BASE='/' build: identity). */
export function stripBase(p: string): string {
  if (BASE !== '/' && p.startsWith(BASE)) {
    const rest = p.slice(BASE.length - 1)
    return rest || '/'
  }
  return p || '/'
}

/** '/reports' -> '/demo/reports' (BASE='/' build: identity). */
export function withBase(to: string): string {
  if (BASE === '/') return to
  return BASE + to.replace(/^\/+/, '')
}

export function useRoute(): [string, (to: string) => void] {
  const [path, setPath] = useState(stripBase(location.pathname))
  useEffect(() => {
    const onPop = () => setPath(stripBase(location.pathname))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])
  const navigate = useCallback((to: string) => {
    history.pushState({}, '', withBase(to))
    setPath(to)
  }, [])
  return [path, navigate]
}

export function navigate(to: string) {
  history.pushState({}, '', withBase(to))
  window.dispatchEvent(new PopStateEvent('popstate'))
}

/**
 * Landing page after login. Everyone trades: admins open the till and
 * then navigate to Reports when they need oversight; roles that can't
 * sell land on their own board or list.
 */
export function homeFor(user: { permissions: string[] } | null | undefined): string {
  const p = user?.permissions ?? []
  if (p.includes('pos.sell')) return '/'
  if (p.includes('design.view')) return '/design'
  if (p.includes('reports.view')) return '/reports'
  if (p.includes('orders.view')) return '/orders'
  if (p.includes('products.view')) return '/inventory'
  return '/'
}
