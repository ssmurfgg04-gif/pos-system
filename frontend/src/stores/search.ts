import { create } from 'zustand'

// posSearch — global catalog search. The header search box and the POS
// workspace search box share this store, so typing in the header filters
// the sell screen directly (single source of truth).
interface PosSearchState {
  query: string
  setQuery: (q: string) => void
}

export const usePosSearch = create<PosSearchState>((set) => ({
  query: '',
  setQuery: (query) => set({ query }),
}))
