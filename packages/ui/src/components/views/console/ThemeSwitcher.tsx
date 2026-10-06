import { useTranslation } from 'react-i18next'
import { Check, Monitor, Moon, Palette, Sun, type LucideIcon } from 'lucide-react'

import { cn } from '@jianmanager/ui'
import { COLOR_THEMES, type ColorTheme, type ThemeMode } from '@jianmanager/ui/lib/theme'
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

export interface ThemeSwitcherProps {
  /** 折叠态：只留主题色按钮，明暗按钮收宽淡出。 */
  compact?: boolean
  /** 当前主题色。 */
  colorTheme: ColorTheme
  /** 当前明暗三态。 */
  theme: ThemeMode
  /** 选择主题色（应用侧落 store + localStorage）。 */
  onColorThemeChange: (theme: ColorTheme) => void
  /** 选择明暗三态。 */
  onThemeChange: (mode: ThemeMode) => void
}

/**
 * 全局主题切换器（FR-164）：侧栏底部一处切，全站 CSS 变量实时跟变。
 * 两枚图标按钮、各配 dropdown：调色板 = 主题色 5 选（点开才展开，圆点不再常驻侧栏）；
 * 明暗 = 三态直选。主题色与明暗正交、各自 localStorage 持久。
 * 折叠态（compact）布局不变——本来就是纯图标，只是与语言切换器一起纵向排列。
 *
 * 取值/落值由 props 注入（ADR-097）：本视图不触达 store。
 */
export function ThemeSwitcher({
  compact = false,
  colorTheme,
  theme,
  onColorThemeChange,
  onThemeChange,
}: ThemeSwitcherProps) {
  const { t } = useTranslation()
  const ModeIcon = theme === 'light' ? Sun : theme === 'dark' ? Moon : Monitor

  return (
    // 【为什么不再 `flex-col`】`flex-direction` **不参与过渡**（瞬跳）：折叠瞬间两个按钮
    // 从横排跳成纵排，紧接着侧栏宽度过渡又把它们挤一次——布局连算两遍，
    // 这正是底部那处高度回折的来源。
    // 折叠态改为「只留主题色按钮，明暗按钮收宽淡出」：横排方向不变，
    // 用 `grid-cols 0fr→1fr` 收宽度（可过渡），整个过程连续无跳变。
    // 代价是折叠态少一个明暗入口——明暗仍可在 `/settings` 的「外观」分组改（FR-164）。
    // `gap` 一并归零并纳入过渡，否则留着的 4px 会把剩下的按钮推偏 2px。
    <div
      className={cn(
        'flex items-center transition-[gap] duration-[var(--motion-duration-slow)] ease-ios',
        compact ? 'gap-0' : 'gap-1',
      )}
    >
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
              <DropdownMenuItem key={value} onClick={() => onColorThemeChange(value)}>
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

      {/* 明暗按钮：折叠态整块收起（宽度归零 + 淡出），而非换行。
          `inert` 与视觉收起同步——元素仍在 DOM 里，不加它 Tab 与读屏仍会走到它。 */}
      <div
        inert={compact ? true : undefined}
        className={cn(
          'grid transition-[grid-template-columns,opacity] duration-[var(--motion-duration-slow)] ease-ios',
          compact ? 'grid-cols-[0fr] opacity-0' : 'grid-cols-[1fr] opacity-100',
        )}
      >
        <div className="overflow-hidden">
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
                <DropdownMenuItem key={value} onClick={() => onThemeChange(value)}>
                  <Icon className="size-4" />
                  <span className="flex-1">{t(labelKey)}</span>
                  {theme === value && <Check className="size-3.5" />}
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </div>
    </div>
  )
}
