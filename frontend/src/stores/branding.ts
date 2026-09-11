import { create } from 'zustand'
import { api, Branding } from '../lib/api'
import { configureMoney } from '../lib/money'

const fallback: Branding = {
  app_name: 'Point of Sale',
  store_name: '',
  brand_color: '#10B981',
  currency_symbol: 'KES',
  currency_code: 'KES',
  tax_percent: 16,
  tax_included: true,
  payment_mode: 'auto',
  till_number: '',
  paybill_number: '',
  mpesa_env: 'mock',
}

interface BrandingState {
  branding: Branding
  loaded: boolean
  load: () => Promise<void>
}

export const useBranding = create<BrandingState>((set) => ({
  branding: fallback,
  loaded: false,
  load: async () => {
    try {
      const b = await api.get<Branding>('/api/v1/branding')
      configureMoney(b.currency_symbol)
      // White-label hook: override the brand color at runtime.
      document.documentElement.style.setProperty('--color-brand', b.brand_color || '#10B981')
      if (b.app_name) document.title = b.app_name
      set({ branding: b, loaded: true })
    } catch {
      configureMoney(fallback.currency_symbol)
      set({ branding: fallback, loaded: true })
    }
  },
}))
