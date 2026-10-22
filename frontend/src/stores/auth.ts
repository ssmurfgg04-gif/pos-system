import { create } from 'zustand'
import { api, User, clearToken, setToken, token } from '../lib/api'
import { cartResetForUser } from './cart'

interface AuthState {
  user: User | null
  ready: boolean
  login: (username: string, password: string, shopId?: string) => Promise<void>
  pinLogin: (userId: number, pin: string, shopId?: string) => Promise<void>
  logout: () => void
  refresh: () => Promise<void>
  can: (perm: string) => boolean
}

export const useAuth = create<AuthState>((set, get) => ({
  user: null,
  ready: false,
  login: async (username, password, shopId) => {
    const res = await api.post<{ token: string; user: User }>('/api/v1/auth/login', {
      username,
      password,
      ...(shopId ? { shopId } : {}),
    })
    setToken(res.token)
    cartResetForUser(res.user.id) // a different staff member never inherits a basket
    set({ user: res.user, ready: true })
  },
  pinLogin: async (userId, pin, shopId) => {
    const res = await api.post<{ token: string; user: User }>('/api/v1/auth/pin', { userId, pin, ...(shopId ? { shopId } : {}) })
    setToken(res.token)
    cartResetForUser(res.user.id)
    set({ user: res.user, ready: true })
  },
  logout: () => {
    clearToken()
    cartResetForUser(null) // shift handoff starts a clean basket
    set({ user: null, ready: true })
  },
  refresh: async () => {
    if (!token()) {
      set({ user: null, ready: true })
      return
    }
    try {
      const user = await api.get<User>('/api/v1/me')
      set({ user, ready: true })
    } catch {
      clearToken()
      set({ user: null, ready: true })
    }
  },
  can: (perm) => {
    const u = get().user
    return !!u && u.permissions.includes(perm)
  },
}))
