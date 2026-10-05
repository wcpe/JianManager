import * as React from 'react'
import { ChevronRightIcon } from 'lucide-react'

import { cn } from '../../lib/utils'
import type { StatusLevel } from '../../lib/threshold'
import { StatusBadge } from '../status-badge'

/**
 * 对象页头（FR-496 阶段 4）：实例详情与节点详情共用的同一个对象身份区。
 *
 * 为什么只有一个组件：设计规则明确要求「节点和实例详情使用同一对象头」——对象头只出现一处，
 * 自上而下固定为「紧凑面包屑 → 对象名/状态/元信息/针对本对象的操作 → 精简指标条 → 工具导航」
 * （`.tmp/设计/布局设计与交付说明.md` 第二节）。实例与节点在结构上的差异只有内容（图标、
 * 指标名、工具名），因此差异全部由 props 表达；一旦拆成两个组件，两边就会各自漂移出
 * 「哪个多一条边、哪个少一个指标」的分歧，正是本轮要消除的问题。
 *
 * 与 PageHeader 的分工：PageHeader 是**列表/首页**的页头（标题 + 计数 + 说明），
 * 对象页头是**详情页**的身份区。两者不互相替代，详情页的第一个子元素是对象页头。
 *
 * 与原型的三处刻意对齐：
 * ① 面包屑只在层级 ≥2 时渲染。设计规则写明「普通首页和列表不放无信息增量的单级面包屑；
 *    对象详情保留稳定层级」，所以单级面包屑直接省略，而不是渲染成一行冗余文案。
 * ② 指标条是**带分隔线的一行**而非指标网格：对象头里的数字是「一眼扫过」的上下文，
 *    不是可点击的概览卡片（原型 `.object-stats` 与 `.metric-grid` 是两套样式）。
 * ③ 工具导航是对象级的横向页签，与工作区级的 PlatformTabs 是两回事，故不复用后者。
 */

/** 面包屑的一级。`to` 存在即渲染为链接，否则为纯文本（通常是当前对象自身）。 */
export interface ObjectBreadcrumb {
  label: React.ReactNode
  to?: string
}

/** 状态与指标的语义色调。`default` 表示「无状态含义」，走中性前景色。 */
export type ObjectTone = 'default' | 'success' | 'warning' | 'danger'

/** 指标条上的一格：标签 + 数值。 */
export interface ObjectMetric {
  label: React.ReactNode
  /** 数值；需要「20 / 100」这类复合写法时传 ReactNode。 */
  value: React.ReactNode
  /** 仅影响数值颜色（如 TPS 低于阈值转警告色），不改变底色。 */
  tone?: ObjectTone
}

/** 对象级工具入口（横向页签的一项）。 */
export interface ObjectTool {
  /** 稳定唯一 key，用于标识工具身份。 */
  key: string
  label: React.ReactNode
  /** 当前工具；以 2px 主色下划线标记。 */
  active?: boolean
  onSelect?: () => void
}

/**
 * 色调 → 状态徽章等级。
 *
 * 复用 StatusBadge 而不是自己写一份色表：状态色是设计系统的公共词汇，
 * 对象头只是它的一个使用场景，色值口径必须与原「实例/节点状态列」保持同一处真相。
 */
const TONE_LEVEL: Record<ObjectTone, StatusLevel> = {
  default: 'neutral',
  success: 'success',
  warning: 'warning',
  danger: 'danger',
}

/** 色调 → 前景文字色。 */
const TONE_TEXT: Record<ObjectTone, string> = {
  default: 'text-foreground',
  success: 'text-status-success',
  warning: 'text-status-warning',
  danger: 'text-status-danger',
}

export function ObjectPageHeader({
  breadcrumbs = [],
  title,
  icon,
  status,
  meta = [],
  actions,
  metrics = [],
  note,
  tools = [],
  toolsLabel = '对象工具',
  onNavigate,
  className,
  live,
  ...props
}: Omit<React.ComponentProps<'header'>, 'className'> & {
  /** 层级路径；**少于两级时整块省略**（单级面包屑没有信息增量）。 */
  breadcrumbs?: ObjectBreadcrumb[]
  /** 对象名（实例名 / 节点名）。 */
  title: React.ReactNode
  /** 对象图标（lucide 组件），可选。 */
  icon?: React.ReactNode
  /** 状态徽章；`tone` 缺省为 `default`。 */
  status?: { tone?: ObjectTone; label: string }
  /** 元信息：所属节点、端口、核心版本、UUID 等。 */
  meta?: Array<{ label: React.ReactNode; value: React.ReactNode }>
  /** 操作区（只放影响当前对象的操作）。 */
  actions?: React.ReactNode
  /** 精简指标条。 */
  metrics?: ObjectMetric[]
  /** 指标条最右的时效说明（如「探针 · 2 秒前更新」），窄屏隐藏。 */
  note?: React.ReactNode
  /** 工具导航项。 */
  tools?: ObjectTool[]
  /** 工具导航的可访问名（「实例工具」/「节点工具」）。 */
  toolsLabel?: string
  /**
   * 链接导航回调。传入后，带 `to` 的面包屑会拦截默认跳转并回调，
   * 让路由层（react-router 等）接管；不传则退化为原生 `<a href>`。
   * 组件库不依赖具体路由实现，这是两者之间的唯一接缝。
   */
  onNavigate?: (to: string) => void
  /**
   * 实时区标记：`true` 时整块页头挂 `aria-live="polite"`。
   *
   * 对象详情页的状态/指标可能由推送驱动（如压测会话的 SSE 流），值变化时需要播报。
   * 挂在 `<header>` 而非指标条上：`aria-live` 只播报**发生变化**的内容，标题与面包屑
   * 是静态的不会触发，因此两者等价；而挂在页头上不会漏掉身份区里的状态徽章。
   */
  live?: boolean
  className?: string
}) {
  // 单级面包屑（如列表页的「实例」）不提供层级信息，直接不渲染这一行
  const showBreadcrumbs = breadcrumbs.length > 1
  // 指标条在「有指标」或「只有时效说明」时都要出现：观测不可用时指标为空，
  // 但那条「上次采样于何时」的说明恰恰是此时最重要的信息，不能被吞掉。
  const showMetrics = metrics.length > 0 || note != null

  function renderCrumb(crumb: ObjectBreadcrumb, index: number) {
    const isLast = index === breadcrumbs.length - 1
    const { to } = crumb

    if (!to) {
      return (
        <span
          data-slot="object-breadcrumb-current"
          aria-current={isLast ? 'page' : undefined}
          className="truncate"
        >
          {crumb.label}
        </span>
      )
    }

    return (
      <a
        href={to}
        onClick={
          onNavigate
            ? (event) => {
                event.preventDefault()
                onNavigate(to)
              }
            : undefined
        }
        className="truncate rounded-xs transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:text-primary focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
      >
        {crumb.label}
      </a>
    )
  }

  return (
    <header
      data-slot="object-page-header"
      aria-live={live ? 'polite' : undefined}
      className={cn(
        'flex shrink-0 flex-col border-b bg-card px-[22px] pt-4 max-md:px-3 max-md:pt-[13px]',
        className,
      )}
      {...props}
    >
      {showBreadcrumbs && (
        <nav
          data-slot="object-breadcrumb"
          aria-label="面包屑"
          className="mb-[5px] flex items-center gap-[5px] text-[10px] text-muted-foreground"
        >
          {breadcrumbs.map((crumb, index) => (
            // 面包屑是静态有序列表，位置即身份；用 index 作 key 不会引起复用错位
            <React.Fragment key={index}>
              {index > 0 && (
                <ChevronRightIcon className="size-3 shrink-0 opacity-60" aria-hidden />
              )}
              {renderCrumb(crumb, index)}
            </React.Fragment>
          ))}
        </nav>
      )}

      <div
        data-slot="object-identity"
        className="flex flex-wrap items-center justify-between gap-x-[15px] gap-y-2"
      >
        <div className="flex min-w-0 items-center gap-3">
          {icon && (
            <span
              data-slot="object-icon"
              aria-hidden
              className="flex size-10 shrink-0 items-center justify-center rounded-[9px] border border-primary/15 bg-primary/10 text-primary [&_svg]:size-[22px] max-md:size-[33px]"
            >
              {icon}
            </span>
          )}
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
              <h1 className="truncate text-xl font-bold tracking-tight text-foreground">{title}</h1>
              {status && (
                <span data-slot="object-status" data-tone={status.tone ?? 'default'}>
                  <StatusBadge level={TONE_LEVEL[status.tone ?? 'default']} label={status.label} />
                </span>
              )}
            </div>
            {meta.length > 0 && (
              <div
                data-slot="object-meta"
                className="mt-[3px] flex flex-wrap items-center gap-x-[7px] gap-y-1 text-[10px] text-muted-foreground"
              >
                {meta.map((entry, index) => (
                  <React.Fragment key={index}>
                    {index > 0 && (
                      <span aria-hidden className="opacity-60">
                        ·
                      </span>
                    )}
                    <span data-slot="object-meta-item" className="inline-flex items-center gap-1.5">
                      <span>{entry.label}</span>
                      <span className="font-mono text-foreground">{entry.value}</span>
                    </span>
                  </React.Fragment>
                ))}
              </div>
            )}
          </div>
        </div>

        {actions && (
          <div
            data-slot="object-actions"
            className="flex shrink-0 flex-wrap items-center gap-[7px] max-md:ml-auto"
          >
            {actions}
          </div>
        )}
      </div>

      {showMetrics && (
        <div
          data-slot="object-metrics"
          className="mt-[15px] flex flex-wrap items-center gap-y-2 border-t py-[11px] text-[11px]"
        >
          {/* 分隔线用 divide-x 而非每格自带 border-right：这样最后一格右侧不会留下一道悬空的竖线 */}
          <div className="flex flex-wrap items-center divide-x divide-border">
            {metrics.map((metric, index) => (
              <div
                key={index}
                data-slot="object-metric"
                data-tone={metric.tone ?? 'default'}
                className="flex items-baseline gap-2 px-[18px] first:pl-0 max-md:px-2"
              >
                <span className="text-[10px] text-muted-foreground">{metric.label}</span>
                <span
                  className={cn(
                    'font-mono font-medium tabular-nums',
                    TONE_TEXT[metric.tone ?? 'default'],
                  )}
                >
                  {metric.value}
                </span>
              </div>
            ))}
          </div>
          {note != null && (
            <>
              <span className="min-w-4 flex-1" />
              <span className="shrink-0 text-[10px] text-muted-foreground max-md:hidden">
                {note}
              </span>
            </>
          )}
        </div>
      )}

      {tools.length > 0 && (
        <nav
          data-slot="object-tools"
          aria-label={toolsLabel}
          className="flex shrink-0 items-center gap-5 overflow-x-auto scrollbar-none"
        >
          {tools.map((tool) => (
            <button
              key={tool.key}
              type="button"
              data-slot="object-tool"
              data-active={tool.active || undefined}
              aria-current={tool.active ? 'page' : undefined}
              onClick={tool.onSelect}
              className={cn(
                'flex min-h-[41px] shrink-0 items-center gap-1.5 whitespace-nowrap border-b-2 border-transparent px-px py-[9px] text-xs text-muted-foreground',
                'transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:text-foreground',
                'max-md:min-h-[37px]',
                tool.active && 'border-primary font-medium text-foreground',
              )}
            >
              {tool.label}
            </button>
          ))}
        </nav>
      )}
    </header>
  )
}
