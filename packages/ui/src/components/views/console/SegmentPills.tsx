import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@jianmanager/ui'

/** 分段项：键 + i18n 标签键。 */
export interface SegmentPillOption<T extends string> {
  key: T
  labelKey: string
}

export interface SegmentPillsProps<T extends string> {
  options: SegmentPillOption<T>[]
  value: T
  onChange: (key: T) => void
  /** 分组可访问名（`aria-label`）。 */
  ariaLabel?: string
  /**
   * 焦点回送：为真时把焦点置于当前段按钮。
   *
   * 用于**寄居场景**——分段控件渲染在它所切换的那个视图内部（如资源管理器工具栏），
   * 切换会随旧视图卸载而带走焦点。开启后每次重挂都把焦点送回当前段，键盘用户可连续
   * 按 Tab/Enter 换段而不掉到 body。
   */
  autoFocusActive?: boolean
  /** 额外类名（寄居场景可微调内边距）。 */
  className?: string
}

/**
 * 药丸形分段控件（`rounded-full` + `aria-pressed` 按钮组）。
 *
 * 用按钮组而非 ARIA tablist：本控件可能渲染在它所切换的那个视图内部，tablist 嵌在
 * tabpanel 里是无效结构（FR-422 的取舍）。两处调用（FR-413 文件配置分段、FR-422 资源卡片
 * 视图分段）原先各写一份，现收敛于此。
 */
export function SegmentPills<T extends string>({
  options,
  value,
  onChange,
  ariaLabel,
  autoFocusActive = false,
  className,
}: SegmentPillsProps<T>) {
  const { t } = useTranslation()
  const activeRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (autoFocusActive) activeRef.current?.focus()
  }, [autoFocusActive, value])

  return (
    <div
      role="group"
      aria-label={ariaLabel}
      className={cn('inline-flex shrink-0 rounded-full bg-muted p-0.5', className)}
    >
      {options.map(({ key, labelKey }) => (
        <button
          key={key}
          ref={key === value ? activeRef : undefined}
          type="button"
          onClick={() => onChange(key)}
          aria-pressed={value === key}
          className={cn(
            'rounded-full px-3 py-1 text-xs transition-colors',
            value === key
              ? 'bg-card font-semibold text-foreground shadow-soft'
              : 'text-muted-foreground hover:text-foreground',
          )}
        >
          {t(labelKey)}
        </button>
      ))}
    </div>
  )
}
