import * as React from 'react'
import { ChevronRightIcon, HelpCircleIcon } from 'lucide-react'

import { cn } from '../../lib/utils'
import { Tooltip, TooltipContent, TooltipTrigger } from '../tooltip'

/**
 * 页头（FR-502 阶段 3）：内容页的统一开头——可选面包屑 + 标题 + 可选计数 + 说明图标 + 右侧操作区。
 *
 * 布局规范要求任何内容页的第一个子元素都是它，从而消除「页名重复出现在面包屑、
 * 页面大标题、外层卡壳」的冗余（原型第四节：去掉重复的页名、指标和外层卡壳）。
 *
 * 计数用 `<span>` 贴在标题右侧（原型 `.page-title-count`，12px muted），
 * 而不是另起一行——标题行永远只有一行高，避免页头膨胀。
 *
 * 【说明文字改为标题旁的问号图标】原先 `description` 渲染为标题下方一行 11px 灰字。
 * 17 个页面各写一句解释后，页头变成「大标题 + 一行小字」两行结构，视觉重量翻倍，
 * 而那句话的阅读频率极低（进过一次就知道这页是做什么的）。改为标题右侧一个 14px 的
 * 问号图标 + Tooltip：信息仍可取，但不再占用整行高度。
 *
 * 图标用 `<button>` 包裹而非裸 `<svg>`：说明属于可交互信息，必须能键盘聚焦、
 * 能用 Esc 关闭提示。可访问名取说明文案本身（`aria-label`），让读屏直接念出内容，
 * 而不是「帮助按钮」这种无信息量的名字。
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
  /**
   * 页面说明。不再渲染为标题下方的整行小字，而是收进标题右侧的问号图标里，
   * 悬停/聚焦时以 Tooltip 呈现（见文件头注释）。
   *
   * 传字符串时会同时用作图标的可访问名，读屏直接念出说明内容。
   */
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
        <div className="flex min-w-0 items-center gap-1.5">
          <h1 className="truncate text-xl font-bold text-foreground">
            {title}
            {count !== undefined && (
              <span className="ml-2 align-middle text-xs font-normal text-muted-foreground">
                {typeof count === 'number' ? count.toLocaleString('zh-CN') : count}
              </span>
            )}
          </h1>
          {description && (
            /*
              说明收进问号图标：信息仍可取，但不再占用标题下方整行。

              用 `<button>` 而非裸图标，才能键盘聚焦、用 Esc 关掉提示；
              可访问名取说明文案本身（字符串描述时），读屏直接念内容而不是「帮助按钮」。
              `aria-hidden` 加在 svg 上，避免图标自身被重复朗读。
            */
            <Tooltip>
              <TooltipTrigger asChild>
                <button
                  type="button"
                  data-slot="page-header-help"
                  aria-label={typeof description === 'string' ? description : '页面说明'}
                  className={cn(
                    'inline-flex size-4 shrink-0 items-center justify-center rounded-full',
                    'text-muted-foreground/70 transition-colors hover:text-foreground',
                    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40',
                  )}
                >
                  <HelpCircleIcon className="size-3.5" aria-hidden />
                </button>
              </TooltipTrigger>
              {/*
                必须向下弹：页头贴在视口顶端，向上弹时 Radix 量到的可用高度只剩 ~60px，
                提示会被压到贴着页面最上沿（实测 y=12），既远离图标又顶满左栏。
              */}
              <TooltipContent side="bottom" align="start" sideOffset={6} className="max-w-xs">
                {description}
              </TooltipContent>
            </Tooltip>
          )}
        </div>
        {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
      </div>
    </div>
  )
}
