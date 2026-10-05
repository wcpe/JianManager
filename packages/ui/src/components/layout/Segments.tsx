import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 分段控件（FR-496 阶段 6 收尾）：同一维度的少数几种互斥视图之间的切换。
 *
 * 与 `PlatformTabs` 的分工：`PlatformTabs` 切的是**同一工作区内的一组页面**（内容层级，
 * 原型 `.platform-tabs`，下划线样式）；本组件切的是**当前页内同一份数据的少数几种呈现**
 * （原型 `.segments`，凹槽底 + 白底选中块）。原型里这是两套样式，不可互相替代。
 *
 * 与 `@jianmanager/ui/components/tabs`（radix）的分工：那套要求把内容交给 `TabsContent`
 * 托管；本组件只渲染**切换条本身**，内容留在调用方（如列表数据区在页面别处），
 * 因此沿用等价的 `role="tablist"` / `role="tab"` / `aria-selected` 可访问语义。
 *
 * 为什么此时才补：迁移期间 LogsPage 与 NodesPage 各自手写了同一形态，实现与取值均已漂移
 * （一处是 Button + variant 切换、另一处是自绘 button + 条件类）。本组件把原型 `.segments`
 * 的取值固定为一处真相。
 */
export function Segments({ className, children, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      role="tablist"
      data-slot="segments"
      className={cn(
        'inline-flex shrink-0 items-center gap-[2px] rounded-lg border bg-muted p-[3px]',
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}

/**
 * 分段项。`active` 时抬为卡片底色并加浅阴影（原型 `.segments button.active`）。
 *
 * 用 `role="tab"` 而非 `aria-current`：前者是本形态既有的可访问语义（两处手写实现即如此），
 * 迁移只换实现不换语义。布局层另两个页签原语（`PlatformTab` / `ObjectPageHeader.tools`）
 * 走 `aria-current`，那是「导航到别处」，与本组件「就地切换呈现」不是一回事。
 */
export function Segment({
  active = false,
  className,
  children,
  ...props
}: React.ComponentProps<'button'> & { active?: boolean }) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      data-slot="segment"
      data-active={active || undefined}
      className={cn(
        'rounded-md px-[10px] py-1 text-[11px] whitespace-nowrap text-muted-foreground',
        'transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:text-foreground',
        active && 'bg-card font-medium text-foreground shadow-sm',
        className,
      )}
      {...props}
    >
      {children}
    </button>
  )
}
