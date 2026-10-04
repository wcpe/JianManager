import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 页头（FR-496 阶段 3）：内容页的统一开头——标题 + 可选计数 + 描述 + 右侧操作区。
 *
 * 布局规范要求任何内容页的第一个子元素都是它，从而消除「页名重复出现在面包屑、
 * 页面大标题、外层卡壳」的冗余（原型第四节：去掉重复的页名、指标和外层卡壳）。
 *
 * 计数用 `<span>` 贴在标题右侧（原型 `.page-title-count`，12px muted），
 * 而不是另起一行——标题行永远只有一行高，避免页头膨胀。
 */
export function PageHeader({
  title,
  count,
  description,
  actions,
  className,
  ...props
}: Omit<React.ComponentProps<'div'>, 'title'> & {
  title: React.ReactNode
  /** 标题右侧计数；数字自动按 zh-CN 千分位。 */
  count?: number | string
  /** 标题下方一行说明（11px muted）。 */
  description?: React.ReactNode
  /** 右侧操作区（按钮组等）。 */
  actions?: React.ReactNode
}) {
  return (
    <div
      data-slot="page-header"
      className={cn('flex shrink-0 items-center justify-between gap-[15px]', className)}
      {...props}
    >
      <div className="min-w-0">
        <h1 className="truncate text-xl font-bold text-foreground">
          {title}
          {count !== undefined && (
            <span className="ml-2 align-middle text-xs font-normal text-muted-foreground">
              {typeof count === 'number' ? count.toLocaleString('zh-CN') : count}
            </span>
          )}
        </h1>
        {description && <p className="mt-[3px] text-[11px] text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
    </div>
  )
}
