import * as React from 'react'
import { ChevronRightIcon } from 'lucide-react'

import { cn } from '../../lib/utils'

/**
 * 页头（FR-496 阶段 3）：内容页的统一开头——可选面包屑 + 标题 + 可选计数 + 描述 + 右侧操作区。
 *
 * 布局规范要求任何内容页的第一个子元素都是它，从而消除「页名重复出现在面包屑、
 * 页面大标题、外层卡壳」的冗余（原型第四节：去掉重复的页名、指标和外层卡壳）。
 *
 * 计数用 `<span>` 贴在标题右侧（原型 `.page-title-count`，12px muted），
 * 而不是另起一行——标题行永远只有一行高，避免页头膨胀。
 *
 * 面包屑为**可选**：普通内容页不传。原型第四节明确「普通首页和列表不放无信息增量的
 * 单级面包屑」；真正需要它的是**多步流程页**——原型里 `wizardPage`（创建实例）与
 * `publishPage`（发布客户端版本）都用「`实例 > 创建实例` + 取消按钮」这一形态。
 *
 * 渲染与 `ObjectPageHeader` 的面包屑同款（分隔符、当前项语义、焦点环一致），但那边是
 * 对象详情专用（另带图标/状态/元信息/指标条/工具导航），流程页不需要那一套，故在此另开。
 */

/** 面包屑的一级。`to` 存在即渲染为链接，否则为纯文本（通常是当前页自身）。 */
export interface PageBreadcrumb {
  label: React.ReactNode
  to?: string
}

export function PageHeader({
  title,
  count,
  description,
  actions,
  breadcrumbs,
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
  /** 面包屑层级；通常只由多步流程页传。 */
  breadcrumbs?: PageBreadcrumb[]
}) {
  const crumbs = breadcrumbs ?? []
  // 单级面包屑（如「实例」）不提供层级信息，直接不渲染这一行——与 ObjectPageHeader 同判据。
  const showBreadcrumbs = crumbs.length > 1

  return (
    <div data-slot="page-header" className={cn('flex shrink-0 flex-col', className)} {...props}>
      {showBreadcrumbs && (
        <nav
          data-slot="page-breadcrumb"
          aria-label="面包屑"
          className="mb-[5px] flex items-center gap-[5px] text-[10px] text-muted-foreground"
        >
          {crumbs.map((crumb, index) => (
            // 面包屑是静态有序列表，位置即身份；用 index 作 key 不会引起复用错位。
            <React.Fragment key={index}>
              {index > 0 && <ChevronRightIcon className="size-3 shrink-0 opacity-60" aria-hidden />}
              {crumb.to ? (
                <a
                  href={crumb.to}
                  className="truncate rounded-xs transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:text-primary focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
                >
                  {crumb.label}
                </a>
              ) : (
                <span
                  data-slot="page-breadcrumb-current"
                  aria-current={index === crumbs.length - 1 ? 'page' : undefined}
                  className="truncate"
                >
                  {crumb.label}
                </span>
              )}
            </React.Fragment>
          ))}
        </nav>
      )}
      {/* 标题行单独一层：不传面包屑时这一层的样式与旧版外层完全一致，行为不变。 */}
      <div className="flex items-center justify-between gap-[15px]">
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
    </div>
  )
}
