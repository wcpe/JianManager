import { create } from 'zustand'

type Theme = 'light' | 'dark' | 'system'

interface ThemeState {
  theme: Theme
  resolvedTheme: 'light' | 'dark'
  setTheme: (theme: Theme) => void
  loadFromStorage: () => void
}

function getSystemTheme(): 'light' | 'dark' {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function resolveTheme(theme: Theme): 'light' | 'dark' {
  if (theme === 'system') return getSystemTheme()
  return theme
}

function applyTheme(resolved: 'light' | 'dark') {
  const root = document.documentElement
  root.classList.remove('light', 'dark')
  root.classList.add(resolved)
}

export const useThemeStore = create<ThemeState>((set) => ({
  theme: 'system',
  resolvedTheme: getSystemTheme(),

  setTheme: (theme) => {
    const resolved = resolveTheme(theme)
    localStorage.setItem('theme', theme)
    applyTheme(resolved)
    set({ theme, resolvedTheme: resolved })
  },

  loadFromStorage: () => {
    const saved = (localStorage.getItem('theme') as Theme) || 'system'
    const resolved = resolveTheme(saved)
    applyTheme(resolved)
    set({ theme: saved, resolvedTheme: resolved })

    // 监听系统主题变化
    window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
      const current = localStorage.getItem('theme') as Theme || 'system'
      if (current === 'system') {
        const newResolved = getSystemTheme()
        applyTheme(newResolved)
        set({ resolvedTheme: newResolved })
      }
    })
  },
}))
