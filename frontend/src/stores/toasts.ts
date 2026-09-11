import { create } from 'zustand'

export type ToastKind = 'success' | 'error' | 'info'

export interface Toast {
  id: number
  kind: ToastKind
  title: string
  body?: string
}

interface ToastState {
  toasts: Toast[]
  push: (kind: ToastKind, title: string, body?: string) => void
  dismiss: (id: number) => void
}

let seq = 1

export const useToasts = create<ToastState>((set) => ({
  toasts: [],
  push: (kind, title, body) => {
    const id = seq++
    set((s) => ({ toasts: [...s.toasts.slice(-3), { id, kind, title, body }] }))
    // Bottom-center toasts never cover the topbar barcode search (the
    // highest-frequency target on the POS screen).
    setTimeout(() => {
      set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) }))
    }, kind === 'error' ? 6000 : 3500)
  },
  dismiss: (id) => set((s) => ({ toasts: s.toasts.filter((t) => t.id !== id) })),
}))

export const toast = {
  success: (title: string, body?: string) => useToasts.getState().push('success', title, body),
  error: (title: string, body?: string) => useToasts.getState().push('error', title, body),
  info: (title: string, body?: string) => useToasts.getState().push('info', title, body),
}
