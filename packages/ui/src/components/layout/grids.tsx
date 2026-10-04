import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 布局网格族（FR-496 阶段 3）：把原型的几套栅格固化为组件，消除各页面手写 grid 类名。
 *
 * 每个网格都带窄屏降级（原型在 900px 与 640px 两档断点收窄列数），
 * 因此页面不再需要自己写断点类——这是「内容页布局规范统一」的一部分。
 */

/** 主次两列（主内容 + 侧栏卡片）；窄屏降为单列。 */
export function TwoCol({ className, children, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="two-col"
      className={cn('grid grid-cols-[minmax(0,1.65fr)_minmax(270px,1fr)] gap-4 max-lg:grid-cols-1', className)}
      {...props}
    >
      {children}
    </div>
  )
}

/** 主次中三列；窄屏降为单列。 */
export function ThreeCol({ className, children, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="three-col"
      className={cn('grid grid-cols-3 gap-4 max-lg:grid-cols-1', className)}
      {...props}
    >
      {children}
    </div>
  )
}

/**
 * 指标网格：4 列等宽，格与格之间用 1px 底色缝（而非 border）分隔，
 * 因此网格整体只有外框一条边，视觉上更紧凑（原型 `.metric-grid`）。
 */
export function MetricGrid({ className, children, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="metric-grid"
      className={cn(
        'grid grid-cols-4 gap-px overflow-hidden rounded-lg border bg-border',
        'max-md:grid-cols-2',
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}

/** 单个指标格（配合 `MetricGrid` 使用，底色需为面板色才能显出缝）。 */
export function MetricCell({ className, children, ...props }: React.ComponentProps<'div'>) {
  return (
    <div data-slot="metric-cell" className={cn('bg-card p-4', className)} {...props}>
      {children}
    </div>
  )
}

/** 卡片网格：3 列（实例/节点卡片墙）；窄屏依次降为 2 列、1 列。 */
export function CardsGrid({ className, children, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="cards-grid"
      className={cn(
        'grid min-h-0 grid-cols-3 gap-3 overflow-auto',
        'max-xl:grid-cols-2 max-md:grid-cols-1',
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}

/**
 * 设置页布局：左侧分组导航（170px 固定）+ 右侧内容（自适应）。
 * 窄屏降为单列（分组导航转为横向滚动），因此左侧栏自身需支持横向溢出。
 */
export function SettingsLayout({ className, children, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="settings-layout"
      className={cn(
        'grid min-h-0 flex-1 grid-cols-[170px_minmax(0,1fr)] gap-4 max-lg:grid-cols-1',
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}
