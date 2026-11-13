import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 侧栏分区标题（原语层）：弱化小字 + 可选上方分隔线。
 *
 * 与 `SidebarGroup` 的区别：分组是**可开合的容器**，分区标题只是一行标注——
 * 它不折叠、不包内容、不参与状态。宿主要在一段内容上方加一句「常用」「最近」时用它，
 * 不需要为此引入一个带开合状态的容器。
 *
 * 分隔线用 `border-t` 而不是另外插一个 `<hr>`：`<hr>` 在 flex 列里会带自己的外边距，
 * 且它是流内容而非语义边界；`border-t` 不占布局高度，也不会在容器 padding 里叠加出空隙。
 */
export interface SidebarSectionProps extends Omit<React.ComponentProps<'div'>, 'title'> {
  /** 分区标题。 */
  title: React.ReactNode
  /** 是否在上方画一条分隔线（默认不画）。 */
  divider?: boolean
  className?: string
}

export function SidebarSection({ title, divider = false, className, children, ...props }: SidebarSectionProps) {
  return (
    <div
      data-slot="sidebar-section"
      className={cn(
        divider && 'border-t border-border/60',
        'min-w-0',
        className,
      )}
      {...props}
    >
      <div
        data-slot="sidebar-section-title"
        className="px-[8px] pb-[3px] pt-[8px] text-[10px] uppercase tracking-[.4px] text-muted-foreground/70"
      >
        {title}
      </div>
      {children}
    </div>
  )
}
