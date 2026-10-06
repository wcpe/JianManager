import { ThemeSwitcher as ThemeSwitcherView } from '@jianmanager/ui'
import { useThemeStore } from '@/stores/theme'

/**
 * 主题切换器的应用接线层（ADR-097）。
 *
 * 视图本体已迁入组件库并受控；本层只把主题 store 的当前值与 setter 注入进去，
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function ThemeSwitcher({ compact = false }: { compact?: boolean }) {
  const colorTheme = useThemeStore((s) => s.colorTheme)
  const setColorTheme = useThemeStore((s) => s.setColorTheme)
  const theme = useThemeStore((s) => s.theme)
  const setTheme = useThemeStore((s) => s.setTheme)

  return (
    <ThemeSwitcherView
      compact={compact}
      colorTheme={colorTheme}
      theme={theme}
      onColorThemeChange={setColorTheme}
      onThemeChange={setTheme}
    />
  )
}
