import { useTranslation } from 'react-i18next'
import { Check, Monitor, Moon, Palette, Sun, type LucideIcon } from 'lucide-react'

import { useThemeStore } from '@/stores/theme'
import { cn } from '@jianmanager/ui'
import { COLOR_THEMES, type ColorTheme, type ThemeMode } from '@/lib/theme'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@jianmanager/ui/components/dropdown-menu'

/** 主题色色样：取各主题亮色 --primary 的 hex，与 index.css 覆盖组保持一致（固定色值用于菜单内预览，非品牌变量）。 */
const COLOR_SWATCHES: Record<ColorTheme, string> = {
  indigo: '#158053',
  teal: '#14B8A6',
  ocean: '#2563eb',
  violet: '#7c3aed',
  sunset: '#ea8a3a',
}

/** 明暗三态选项：图标 + 文字，dropdown 直选（非盲循环，承 FR-132）。 */
const MODE_OPTIONS: Array<{ value: ThemeMode; icon: LucideIcon; labelKey: string }> = [
  { value: 'light', icon: Sun, labelKey: 'theme.light' },
  { value: 'dark', icon: Moon, labelKey: 'theme.dark' },
  { value: 'system', icon: Monitor, labelKey: 'theme.system' },
]

/**
 * 全局主题切换器（FR-164）：侧栏底部一处切，全站 CSS 变量实时跟变。
 * 两枚图标按钮、各配 dropdown：调色板 = 主题色 5 选（点开才展开，圆点不再常驻侧栏）；
 * 明暗 = 三态直选。主题色与明暗正交、各自 localStorage 持久。
 * 折叠态（compact）布局不变——本来就是纯图标，只是与语言切换器一起纵向排列。
 */
export default function ThemeSwitcher({ compact = false }: { compact?: boolean }) {
  const { t } = useTranslation()
  const colorTheme = useThemeStore((s) => s.colorTheme)
  const setColorTheme = useThemeStore((s) => s.setColorTheme)
  const theme = useThemeStore((s) => s.theme)
  const setTheme = useThemeStore((s) => s.setTheme)
  const ModeIcon = theme === 'light' ? Sun : theme === 'dark' ? Moon : Monitor

  return (
    <div className={cn('flex items-center gap-1', compact && 'flex-col gap-1')}>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label={t('colorTheme.label')}
            title={t(`colorTheme.${colorTheme}`)}
            className="relative grid size-7 shrink-0 place-items-center rounded-md text-foreground/70 transition-colors hover:bg-accent/60 hover:text-foreground"
          >
            <Palette className="size-4" />
            {/* 角标色点：不点开也能看出当前主题色 */}
            <span
              aria-hidden
              className="absolute right-0.5 bottom-0.5 size-1.5 rounded-full border border-background"
              style={{ backgroundColor: COLOR_SWATCHES[colorTheme] }}
            />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent side="top" align={compact ? 'center' : 'start'} className="w-40">
          {COLOR_THEMES.map((value) => {
            const active = colorTheme === value
            return (
              <DropdownMenuItem key={value} onClick={() => setColorTheme(value)}>
                <span
                  aria-hidden
                  className={cn(
                    'size-3.5 shrink-0 rounded-md border-2 border-card',
                    active && 'ring-2 ring-primary',
                  )}
                  style={{ backgroundColor: COLOR_SWATCHES[value] }}
                />
                <span className="flex-1">{t(`colorTheme.${value}`)}</span>
                {active && <Check className="size-3.5" />}
              </DropdownMenuItem>
            )
          })}
        </DropdownMenuContent>
      </DropdownMenu>

      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label={t('theme.toggle')}
            title={t(`theme.${theme}`)}
            className="grid size-7 shrink-0 place-items-center rounded-md text-foreground/70 transition-colors hover:bg-accent/60 hover:text-foreground"
          >
            <ModeIcon className="size-4" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent side="top" align={compact ? 'center' : 'end'} className="w-40">
          {MODE_OPTIONS.map(({ value, icon: Icon, labelKey }) => (
            <DropdownMenuItem key={value} onClick={() => setTheme(value)}>
              <Icon className="size-4" />
              <span className="flex-1">{t(labelKey)}</span>
              {theme === value && <Check className="size-3.5" />}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  )
}
