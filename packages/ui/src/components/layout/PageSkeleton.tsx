import * as React from 'react'

import { cn } from '../../lib/utils'
import { Skeleton } from '../skeleton'
import { PageShell } from './PageShell'

/**
 * 页面骨架（FR-496 阶段 6 补丁）：路由级 Suspense fallback 与页内数据区的统一占位。
 *
 * 【为什么放在布局层而不是应用侧】
 * 骨架的全部价值是「在真实内容到达之前，先给出与它将占据的空间一致的结构」，所以它必须复用
 * {@link PageShell} 的留白/滚动模型和 `PageHeader` 的行高标度——这两样都是布局层的东西。
 * 抄到应用侧会因为拿不到真源而逐步漂移（改 PageShell 留白时骨架不会跟着变），随后表现为
 * 「骨架 → 真实内容」的布局跳动。应用侧只负责「哪个路由用哪种壳态」，见
 * `apps/control-plane-web/src/components/console/Workspace.tsx`。
 *
 * 【与真实内容的对齐口径（避免跳变）】
 * - 标题占位 `h-7` = PageHeader 标题 `text-xl` 的行高（1.75rem = 28px）；
 * - 操作区占位 `h-8` = 工具栏按钮 `size="sm"` 的高度；
 * - 筛选条占位 `h-11` = ScopeBar / 工具条一行控件（含 padding）的高度；
 * - 数据行占位 `py-2 + h-4 + 1px` = 33px，与审计/任务页的行高基准一致（见 `ROW_BASE_PX`）。
 * 这些都是「与真源同量级」的估算：骨架只活到 chunk 到达为止（通常是几百毫秒），
 * 近似高度已足以让骨架期就位、真实内容原地替换而不出现整页位移。
 *
 * 【动效】
 * 占位块复用 `Skeleton` 原语（`animate-pulse`，Tailwind 的 `--animate-pulse` token）。
 * 本层不写任何毫秒值：需要时长的地方一律走 `--motion-duration-*`，
 * 而骨架是循环脉动、不属于那套「交互时长」标度，故沿用既有原语而不是新造一个定值。
 */
export type PageSkeletonVariant = 'default' | 'fixed' | 'tool' | 'minimal'

/** 数据区默认占位行数：1440×900 下列表页大致能看到的行数。 */
const DEFAULT_ROWS = 9

export function PageSkeleton({
  variant = 'default',
  rows = DEFAULT_ROWS,
  className,
  ...props
}: React.ComponentProps<'div'> & {
  /** 壳态，与 `PageShell` 的 `variant` 一一对应；`minimal` 用于外壳本身还没挂载的首屏。 */
  variant?: PageSkeletonVariant
  /** 数据区占位行数。 */
  rows?: number
}) {
  // 首屏（App 级 fallback）：此时控制台外壳（侧栏/顶栏）本身也还没到，套页面壳只会二次跳动，
  // 所以只给一个居中的「品牌位 + 输入位」占位，代价最小、也最贴近登录/初始化页的真实形状。
  if (variant === 'minimal') {
    return (
      <div
        data-slot="page-skeleton"
        data-variant="minimal"
        aria-busy="true"
        className={cn('flex min-h-dvh w-full items-center justify-center p-6', className)}
        {...props}
      >
        <div className="w-full max-w-sm space-y-3">
          <Skeleton className="h-8 w-40" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
      </div>
    )
  }

  // 工具壳（`/super`、`/director`）：页面自带左栏与画布、无外层留白，骨架也去掉留白与页头，
  // 只铺满数据区——否则「有留白的骨架 → 无留白的工具页」会整体位移一次。
  if (variant === 'tool') {
    return (
      <PageShell
        variant="tool"
        data-slot="page-skeleton"
        data-variant="tool"
        aria-busy="true"
        className={className}
        {...props}
      >
        <Skeleton className="h-11 w-full shrink-0 rounded-none" />
        <DataPanelSkeleton rows={rows} className="rounded-none border-x-0 border-b-0" />
      </PageShell>
    )
  }

  return (
    <PageShell
      variant={variant}
      data-slot="page-skeleton"
      data-variant={variant}
      aria-busy="true"
      className={className}
      {...props}
    >
      {/* 页头占位：与 `PageHeader` 同容器类（justify-between + gap-[15px]），
          标题/描述/操作三块各占真实行高，故页头高度在骨架期就与就绪后一致。 */}
      <div data-slot="page-skeleton-header" className="flex shrink-0 items-center justify-between gap-[15px]">
        <div className="min-w-0">
          <Skeleton className="h-7 w-44" />
          <Skeleton className="mt-[3px] h-3.5 w-64" />
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <Skeleton className="h-8 w-20" />
          <Skeleton className="h-8 w-20" />
        </div>
      </div>

      {/* 筛选/作用域条占位：与 ScopeBar 同高的一整条，避免筛选条到位时把数据区整体推移。 */}
      <Skeleton className="h-11 w-full shrink-0 rounded-lg" />

      <DataPanelSkeleton rows={rows} />
    </PageShell>
  )
}

/**
 * 数据区骨架：与「`Panel` + 吸附列头 + N 行」同构的占位。
 *
 * 抽成独立导出是因为它有两种用法，且两种都必须与真实容器同壳：
 * ① {@link PageSkeleton} 内部（整页还没到）；
 * ② 页内「页头/筛选条已渲染、只有数据未到」的区间（如审计、任务、实例列表页）——
 *    这些页面此时应把页头与筛选条照常渲染出来，只让数据区显示骨架。
 */
export function DataPanelSkeleton({
  rows = DEFAULT_ROWS,
  className,
  ...props
}: React.ComponentProps<'div'> & { rows?: number }) {
  return (
    <div
      data-slot="data-skeleton"
      aria-hidden="true"
      className={cn(
        // 与 `Panel` 同壳：圆角、描边、卡片底色、柔和阴影，尺寸随父级 flex 伸缩。
        'flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border bg-card/95 shadow-soft',
        className,
      )}
      {...props}
    >
      {/* 列头占位：与真实列表的 `sticky ... px-3 py-2` 列头同高。 */}
      <div className="flex shrink-0 items-center gap-3 border-b bg-muted/40 px-3 py-2">
        <Skeleton className="h-3 w-16" />
        <Skeleton className="h-3 w-28" />
        <Skeleton className="h-3 flex-1" />
        <Skeleton className="h-3 w-20" />
      </div>
      <ListSkeleton rows={rows} />
    </div>
  )
}

/**
 * 表体占位行：每行 `px-3 py-2` + `h-4` 文本块 + 1px 分隔线 = 33px，
 * 与审计/任务页虚拟列表的行高基准（`ROW_BASE_PX`）取同一个数量级，滚动高度也因而接近。
 */
export function ListSkeleton({
  rows = DEFAULT_ROWS,
  className,
  ...props
}: React.ComponentProps<'div'> & { rows?: number }) {
  return (
    <div data-slot="list-skeleton" aria-hidden="true" className={cn('min-h-0 flex-1', className)} {...props}>
      {Array.from({ length: rows }, (_, row) => (
        <div key={row} data-slot="skeleton-row" className="flex items-center gap-3 border-b border-border/60 px-3 py-2">
          <Skeleton className="h-4 w-4 shrink-0 rounded-full" />
          <Skeleton className="h-4 w-28 shrink-0" />
          <Skeleton className="h-4 w-20 shrink-0" />
          <Skeleton className="h-4 flex-1" />
          <Skeleton className="h-4 w-16 shrink-0" />
        </div>
      ))}
    </div>
  )
}
