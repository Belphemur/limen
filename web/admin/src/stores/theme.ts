import { defineStore } from 'pinia'

export type ThemeMode = 'system' | 'light' | 'dark'

const STORAGE_KEY = 'limen.theme'

function readStored(): ThemeMode {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    if (raw === 'light' || raw === 'dark' || raw === 'system') {
      return raw
    }
  } catch {
    /* SSR / private mode — ignore */
  }
  return 'system'
}

function systemPrefersDark(): boolean {
  if (typeof window === 'undefined' || !window.matchMedia) return false
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

interface ThemeState {
  mode: ThemeMode
  initialized: boolean
}

export const useThemeStore = defineStore('theme', {
  state: (): ThemeState => ({
    mode: 'system',
    initialized: false,
  }),
  getters: {
    effective(state): 'light' | 'dark' {
      if (state.mode !== 'system') return state.mode
      return systemPrefersDark() ? 'dark' : 'light'
    },
  },
  actions: {
    init() {
      if (this.initialized) return
      this.mode = readStored()
      this.apply()
      if (typeof window !== 'undefined' && window.matchMedia) {
        const mql = window.matchMedia('(prefers-color-scheme: dark)')
        mql.addEventListener('change', () => {
          if (this.mode === 'system') this.apply()
        })
      }
      this.initialized = true
    },
    set(next: ThemeMode) {
      this.mode = next
      try {
        localStorage.setItem(STORAGE_KEY, next)
      } catch {
        /* ignore */
      }
      this.apply()
    },
    apply() {
      if (typeof document === 'undefined') return
      document.documentElement.classList.toggle('dark', this.effective === 'dark')
    },
  },
})
