import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 侧栏分组容器（原语层）：标题行（可折叠）+ 内容区 + 计数。
 *
 * 只做**展示与开合动画**，不关心内容是什么——标题与计数都是 `ReactNode`，
 * 传文字、图标、徽章都行；组件本身不认识「工作区」「分组」这类业务概念。
 *
 * 【开合动画为什么用 grid-rows 而不是 `hidden`】`display: none` 不可过渡，
 * 折叠瞬间内容整块消失、下方元素立刻重排（跳一下）。改用
 * `grid-template-rows: 1fr → 0fr` 收高度 + 同步淡出，高度是连续让出去的，
 * 与侧栏宽度过渡同一节拍。内层必须再包一层 `overflow-hidden`：
 * grid 折叠靠子项裁切，否则内容会溢出到下面的分组上。
 */
export interface SidebarGroupProps {
  /** 分组标题。 */
  title: React.ReactNode
  /** 标题右侧计数（数字 / 徽章 / 任意节点）。 */
  count?: React.ReactNode
  /** 标题是否可点开合。false 时只渲染静态标题行。 */
  collapsible?: boolean
  /** 非受控初始展开态（受控时请用 `open` + `onOpenChange`）。 */
  defaultOpen?: boolean
  /** 受控展开态。 */
  open?: boolean
  /** 展开态变化回调（受控与非受控都会调用）。 */
  onOpenChange?: (open: boolean) => void
  /** 标题行左侧图标（可选）。 */
  icon?: React.ReactNode
  className?: string
  /** 内容区 class（滚动、内边距等留给调用方）。 */
  contentClassName?: string
  children?: React.ReactNode
}

export function SidebarGroup({
  title,
  count,
  collapsible = false,
  defaultOpen = true,
  open,
  onOpenChange,
  icon,
  className,
  contentClassName,
  children,
}: SidebarGroupProps) {
  // 受控 / 非受控共用同一套取值：受控时以 `open` 为准，非受控时用内部 state。
  const [uncontrolledOpen, setUncontrolledOpen] = React.useState(defaultOpen)
  const expanded = open ?? uncontrolledOpen

  const toggle = () => {
    const next = !expanded
    if (open === undefined) setUncontrolledOpen(next)
    onOpenChange?.(next)
  }

  const heading = (
    <>
      {icon && <span className="flex size-[15px] shrink-0 items-center justify-center">{icon}</span>}
      <span className="min-w-0 flex-1 truncate">{title}</span>
      {count !== undefined && (
        <span
          data-slot="sidebar-group-count"
          className="ml-auto shrink-0 rounded-[var(--radius-tiny)] bg-muted px-[5px] text-[10px] leading-[16px] text-muted-foreground"
        >
          {count}
        </span>
      )}
    </>
  )

  return (
    <div data-slot="sidebar-group" data-open={expanded || undefined} className={cn('min-w-0', className)}>
      {collapsible ? (
        <button
          type="button"
          data-slot="sidebar-group-trigger"
          // 开合状态必须由 `aria-expanded` 表达：标题行收起后内容仍在 DOM（grid 折叠），
          // 只靠视觉无法让读屏知道这块是展开的还是收起的。
          aria-expanded={expanded}
          onClick={toggle}
          className={cn(
            'flex w-full items-center gap-[7px] rounded-md px-[8px] py-[5px] text-left text-[11px]',
            'text-muted-foreground transition-colors duration-[var(--motion-duration-fast)] ease-ios',
            'hover:bg-muted/60 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/40',
          )}
        >
          {heading}
        </button>
      ) : (
        <div
          data-slot="sidebar-group-title"
          className="flex items-center gap-[7px] px-[8px] py-[5px] text-[11px] text-muted-foreground"
        >
          {heading}
        </div>
      )}

      <div
        // 同 `SideNavResource`：视觉收起后内容仍在 DOM，需 `inert` 让它不可交互、
        // 也不被读屏读到。不可折叠的分组始终展开，不加 inert。
        inert={collapsible && !expanded ? true : undefined}
        className={cn(
          'grid transition-[grid-template-rows,opacity] duration-[var(--motion-duration-slow)] ease-ios',
          collapsible && !expanded ? 'grid-rows-[0fr] opacity-0' : 'grid-rows-[1fr] opacity-100',
        )}
      >
        <div className={cn('overflow-hidden', contentClassName)}>{children}</div>
      </div>
    </div>
  )
}
