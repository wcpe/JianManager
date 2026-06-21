import * as React from 'react'

import { cn } from '@/lib/utils'

/** 分区面板（FR-061）：统一边框/圆角 + 可选标题栏（标题 + 头部操作）+ 内容区。 */
export function Panel({
  title,
  actions,
  children,
  className,
  bodyClassName,
  ...props
}: Omit<React.ComponentProps<'div'>, 'title'> & {
  title?: React.ReactNode
  /** 标题栏右侧操作区（按钮/选择器等）。 */
  actions?: React.ReactNode
  /** 内容区额外类名（覆盖默认 padding）。 */
  bodyClassName?: string
}) {
  return (
    <div
      data-slot="panel"
      className={cn('flex min-h-0 flex-col rounded-lg border bg-card text-card-foreground', className)}
      {...props}
    >
      {(title || actions) && (
        <div className="flex shrink-0 items-center justify-between gap-2 border-b px-3 py-2">
          {title && <div className="text-xs font-semibold tracking-wide text-foreground">{title}</div>}
          {actions && <div className="flex items-center gap-1">{actions}</div>}
        </div>
      )}
      <div className={cn('min-h-0 flex-1 p-3', bodyClassName)}>{children}</div>
    </div>
  )
}
