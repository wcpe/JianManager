import * as React from 'react'

import { cn } from '@/lib/utils'
import { toneChipClass, type Tone } from '@/lib/tone'

/** 分区面板（FR-061/FR-163）：大圆角 + 柔和阴影统一卡片原语，可选标题栏（图标/标题 + 头部操作）+ 内容区。 */
export function Panel({
  title,
  actions,
  children,
  className,
  bodyClassName,
  icon,
  tone = 'primary',
  hoverable,
  ...props
}: Omit<React.ComponentProps<'div'>, 'title'> & {
  title?: React.ReactNode
  /** 标题栏右侧操作区（按钮/选择器等）。 */
  actions?: React.ReactNode
  /** 内容区额外类名（覆盖默认 padding）。 */
  bodyClassName?: string
  /** 标题左侧语义图标块（传入图标元素时显示）。 */
  icon?: React.ReactNode
  /** 图标块色调，默认主色。 */
  tone?: Tone
  /** 启用 hover 反馈（主色晕染阴影 shadow-lift，iOS 缓动；FR-176 去位移、只换阴影不抬位）。 */
  hoverable?: boolean
}) {
  return (
    <div
      data-slot="panel"
      className={cn(
        'flex min-h-0 flex-col rounded-xl border bg-card text-card-foreground shadow-soft',
        hoverable &&
          'transition-[box-shadow] duration-300 ease-ios hover:shadow-lift',
        className,
      )}
      {...props}
    >
      {(title || actions || icon) && (
        <div className="flex shrink-0 items-center justify-between gap-2 border-b px-3 py-2">
          <div className="flex min-w-0 items-center gap-2">
            {icon && (
              <span
                className={cn('flex size-6 shrink-0 items-center justify-center rounded-md', toneChipClass(tone))}
              >
                {icon}
              </span>
            )}
            {title && <div className="truncate text-xs font-semibold tracking-wide text-foreground">{title}</div>}
          </div>
          {actions && <div className="flex shrink-0 items-center gap-1">{actions}</div>}
        </div>
      )}
      <div className={cn('min-h-0 flex-1 p-3', bodyClassName)}>{children}</div>
    </div>
  )
}
