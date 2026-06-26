import { useTranslation } from 'react-i18next'
import { Check, Monitor, Moon, Sun, type LucideIcon } from 'lucide-react'

import { useThemeStore } from '@/stores/theme'
import { cn } from '@/lib/utils'
import type { ColorTheme, ThemeMode } from '@/lib/theme'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

/** 主题色圆点配置：色值固定（用于色样预览，非品牌变量），命中态描边走当前主色变量。 */
const COLOR_DOTS: Array<{ value: ColorTheme; swatch: string; labelKey: string }> = [
  { value: 'indigo', swatch: '#6366F1', labelKey: 'colorTheme.indigo' },
  { value: 'teal', swatch: '#14B8A6', labelKey: 'colorTheme.teal' },
]

/** 明暗三态选项：图标 + 文字，dropdown 直选（非盲循环，承 FR-132）。 */
const MODE_OPTIONS: Array<{ value: ThemeMode; icon: LucideIcon; labelKey: string }> = [
  { value: 'light', icon: Sun, labelKey: 'theme.light' },
  { value: 'dark', icon: Moon, labelKey: 'theme.dark' },
  { value: 'system', icon: Monitor, labelKey: 'theme.system' },
]

/**
 * 全局主题切换器（FR-164）：侧栏底部一处切，全站 CSS 变量实时跟变。
 * 左 = 主题色圆点（靛蓝/青绿直选，复用 preview.html `.dotc` 观感：命中态主色描边）；
 * 右 = 明暗（图标 + dropdown 三态直选）。主题色与明暗正交、各自 localStorage 持久。
 * 折叠态（compact）下隐藏文字标签、仅留圆点与图标，适配仅图标轨。
 */
export default function ThemeSwitcher({ compact = false }: { compact?: boolean }) {
  const { t } = useTranslation()
  const colorTheme = useThemeStore((s) => s.colorTheme)
  const setColorTheme = useThemeStore((s) => s.setColorTheme)
  const theme = useThemeStore((s) => s.theme)
  const setTheme = useThemeStore((s) => s.setTheme)
  const ModeIcon = theme === 'light' ? Sun : theme === 'dark' ? Moon : Monitor

  return (
    <div className={cn('flex items-center gap-2', compact && 'flex-col gap-1.5')}>
      {!compact && (
        <span className="shrink-0 text-[11px] text-muted-foreground/70">{t('colorTheme.label')}</span>
      )}
      <div className="flex items-center gap-1.5">
        {COLOR_DOTS.map(({ value, swatch, labelKey }) => {
          const active = colorTheme === value
          return (
            <button
              key={value}
              type="button"
              onClick={() => setColorTheme(value)}
              aria-label={t(labelKey)}
              aria-pressed={active}
              title={t(labelKey)}
              className={cn(
                'size-4 rounded-md border-2 border-card transition-all ease-ios hover:scale-110',
                active && 'ring-2 ring-primary ring-offset-0',
              )}
              style={{ backgroundColor: swatch }}
            />
          )
        })}
      </div>

      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button
            type="button"
            aria-label={t('theme.toggle')}
            title={t(`theme.${theme}`)}
            className={cn(
              'grid size-7 shrink-0 place-items-center rounded-md text-foreground/70 transition-colors hover:bg-accent/60 hover:text-foreground',
              !compact && 'ml-auto',
            )}
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
