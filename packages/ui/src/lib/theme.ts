/**
 * 主题纯逻辑（FR-164 全局双主题 + 明暗）。
 *
 * 主题色（colorTheme: indigo|teal|ocean|violet|sunset）与明暗（mode: light|dark|system）**正交**，
 * 各自 localStorage 持久。主题色从 2 种扩到 5 种后，每套主题在 index.css 拥有「品牌 + 结构」完整
 * token 组（背景/卡片/边框随主题色相调温），CSS 侧的叠加规则不变：indigo 承 :root/.dark 根变量，
 * 其余主题用 [data-theme]（亮）与 [data-theme].dark（暗）覆盖，特异性保证与明暗正交。
 * 纯函数（解析/循环/属性决策）可单测；DOM 套用与系统媒体查询封装为薄 helper，供 store 与 app 入口共享，
 * 保证「明暗初始化提到入口、登录页也套」与首屏无闪一处实现、不重复。
 */

/** 主题色：indigo（Jian 绿）为默认（兼容旧存储值，无 data-theme，承根变量）；其余 4 色见 index.css 覆盖组。 */
export type ColorTheme = 'indigo' | 'teal' | 'ocean' | 'violet' | 'sunset'

/** 主题色全集：顺序即 ThemeSwitcher 圆点的 UI 展示顺序。 */
export const COLOR_THEMES: readonly ColorTheme[] = ['indigo', 'teal', 'ocean', 'violet', 'sunset']

/** 明暗偏好三态。 */
export type ThemeMode = 'light' | 'dark' | 'system'

/** 解析后的明暗（system 落到具体 light/dark）。 */
export type ResolvedMode = 'light' | 'dark'

/** 明暗持久键（沿用 FR-026/FR-132 既有 'theme'，避免迁移既有用户偏好）。 */
export const MODE_KEY = 'theme'

/** 主题色持久键。 */
export const COLOR_THEME_KEY = 'colorTheme'

/** 将任意持久值/输入归一为合法主题色，未知回退 indigo（默认品牌主题；旧用户 teal 等继续有效）。 */
export function resolveColorTheme(value: string | null | undefined): ColorTheme {
  return value !== null && value !== undefined && (COLOR_THEMES as readonly string[]).includes(value)
    ? (value as ColorTheme)
    : 'indigo'
}

/** 将任意持久值/输入归一为合法明暗三态，未知回退 system。 */
export function resolveMode(value: string | null | undefined): ThemeMode {
  return value === 'light' || value === 'dark' || value === 'system' ? value : 'system'
}

/**
 * 主题色 → `<html data-theme>` 属性值决策：
 * indigo=null（移除属性，回落 :root 根品牌变量）；其余 → 自身字符串（命中对应 [data-theme] 覆盖组）。
 */
export function colorThemeAttr(theme: ColorTheme): string | null {
  return theme === 'indigo' ? null : theme
}

/** 主题色按 COLOR_THEMES 顺序双向循环（圆点直选之外的快捷切换备用）。 */
export function cycleColorTheme(theme: ColorTheme): ColorTheme {
  const index = COLOR_THEMES.indexOf(theme)
  return COLOR_THEMES[(index + 1) % COLOR_THEMES.length]
}

/** 明暗三态循环 light → dark → system → light。 */
export function nextMode(mode: ThemeMode): ThemeMode {
  if (mode === 'light') return 'dark'
  if (mode === 'dark') return 'system'
  return 'light'
}

/** 读取系统明暗偏好（非 DOM/SSR 环境回退 light）。 */
export function getSystemMode(): ResolvedMode {
  if (typeof window === 'undefined' || !window.matchMedia) return 'light'
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

/** 将明暗三态解析为具体 light/dark。 */
export function resolveSystemMode(mode: ThemeMode): ResolvedMode {
  return mode === 'system' ? getSystemMode() : mode
}

/** 把解析后的明暗套到 `<html>`（class light/dark 二选一）。非 DOM 环境跳过。 */
export function applyMode(resolved: ResolvedMode): void {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  root.classList.remove('light', 'dark')
  root.classList.add(resolved)
}

/** 把主题色套到 `<html data-theme>`（indigo 移除属性）。非 DOM 环境跳过。 */
export function applyColorTheme(theme: ColorTheme): void {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  const attr = colorThemeAttr(theme)
  if (attr === null) root.removeAttribute('data-theme')
  else root.setAttribute('data-theme', attr)
}

/**
 * 从 localStorage 读取主题色 + 明暗并立即套到 `<html>`。
 * 由 app 入口（早于 React 挂载）调用以消除首屏闪烁，并保证登录/初始化页也套主题。
 */
export function initThemeFromStorage(): void {
  if (typeof window === 'undefined') return
  const color = resolveColorTheme(localStorage.getItem(COLOR_THEME_KEY))
  const mode = resolveMode(localStorage.getItem(MODE_KEY))
  applyColorTheme(color)
  applyMode(resolveSystemMode(mode))
}
