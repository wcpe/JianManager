import { create } from 'zustand'
import {
  COLOR_THEME_KEY,
  MODE_KEY,
  applyColorTheme,
  applyMode,
  getSystemMode,
  resolveColorTheme,
  resolveMode,
  resolveSystemMode,
  type ColorTheme,
  type ResolvedMode,
  type ThemeMode,
} from '@/lib/theme'

/**
 * 全局主题 store（FR-026 明暗 + FR-164 主题色）。
 *
 * 明暗（mode）与主题色（colorTheme）正交、各自 localStorage 持久；纯逻辑下沉 `lib/theme.ts`。
 * DOM 套用由 `lib/theme.initThemeFromStorage` 在 app 入口（早于 React 挂载）先跑以消除首屏闪烁、
 * 并让登录/初始化页也套主题；本 store 的 `loadFromStorage` 仅做状态回填 + 注册系统主题监听，
 * 不再是「唯一初始化点」（修 FR-163 自查发现的「明暗初始化仅在 console shell 跑」）。
 */
interface ThemeState {
  /** 明暗偏好三态 */
  theme: ThemeMode
  /** 解析后的实际明暗（system 落到具体值） */
  resolvedTheme: ResolvedMode
  /** 主题色：靛蓝（默认）/ 青绿 */
  colorTheme: ColorTheme
  setTheme: (theme: ThemeMode) => void
  setColorTheme: (color: ColorTheme) => void
  /** 回填初始状态并注册系统主题监听（DOM 套用已由入口完成，此处幂等） */
  loadFromStorage: () => void
}

let systemListenerBound = false

export const useThemeStore = create<ThemeState>((set) => ({
  theme: resolveMode(typeof localStorage !== 'undefined' ? localStorage.getItem(MODE_KEY) : null),
  resolvedTheme: getSystemMode(),
  colorTheme: resolveColorTheme(typeof localStorage !== 'undefined' ? localStorage.getItem(COLOR_THEME_KEY) : null),

  setTheme: (theme) => {
    const resolved = resolveSystemMode(theme)
    localStorage.setItem(MODE_KEY, theme)
    applyMode(resolved)
    set({ theme, resolvedTheme: resolved })
  },

  setColorTheme: (color) => {
    localStorage.setItem(COLOR_THEME_KEY, color)
    applyColorTheme(color)
    set({ colorTheme: color })
  },

  loadFromStorage: () => {
    const mode = resolveMode(localStorage.getItem(MODE_KEY))
    const color = resolveColorTheme(localStorage.getItem(COLOR_THEME_KEY))
    const resolved = resolveSystemMode(mode)
    // 套用幂等（入口已套），确保 store 状态与 DOM 一致
    applyMode(resolved)
    applyColorTheme(color)
    set({ theme: mode, resolvedTheme: resolved, colorTheme: color })

    // 系统主题变化时，仅当偏好为 system 才跟随；监听只注册一次。
    if (!systemListenerBound && typeof window !== 'undefined' && window.matchMedia) {
      systemListenerBound = true
      window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
        const current = resolveMode(localStorage.getItem(MODE_KEY))
        if (current === 'system') {
          const next = getSystemMode()
          applyMode(next)
          set({ resolvedTheme: next })
        }
      })
    }
  },
}))
