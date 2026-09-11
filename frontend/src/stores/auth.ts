import { create } from 'zustand'
import { api, User } from '../lib/api'

interface AuthState {
  user: User | null
  ready: boolean
  login: (username: string, password: string) => Promise<void>
  pinLogin: (userId: number, pin: string) => Promise<void>
  logout: () => void
  refresh: () => Promise<void>
  can: (perm: string) => boolean
}

export const useAuth = create<AuthState>((set, get) => ({
  user: null,
  ready: false,
  login: async (username, password) => {
    const res = await api.post<{ token: string; user: User }>('/api/v1/auth/login', {
      username,
      password,
    })
    localStorage.setItem('pos_token', res.token)
    set({ user: res.user, ready: true })
  },
  pinLogin: async (userId, pin) => {
    const res = await api.post<{ token: string; user: User }>('/api/v1/auth/pin', { userId, pin })
    localStorage.setItem('pos_token', res.token)
    set({ user: res.user, ready: true })
  },
  logout: () => {
    localStorage.removeItem('pos_token')
    set({ user: null, ready: true })
  },
  refresh: async () => {
    if (!localStorage.getItem('pos_token')) {
      set({ user: null, ready: true })
      return
    }
    try {
      const user = await api.get<User>('/api/v1/me')
      set({ user, ready: true })
    } catch {
      localStorage.removeItem('pos_token')
      set({ user: null, ready: true })
    }
  },
  can: (perm) => {
    const u = get().user
    return !!u && u.permissions.includes(perm)
  },
}))
