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
