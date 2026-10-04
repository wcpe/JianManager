import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 工具条（FR-496 阶段 3）：面板内的单行操作区——筛选、搜索、批量动作。
 *
 * 与 `ScopeBar` 的分工：ScopeBar 管「这一页在什么范围内取数」，
 * Toolbar 管「这一屏的数据怎么操作」。二者都是横向条，但语义不同，不可互相替代。
 * 放面板内部时贴 `panel-head` 之下，由 `border-b` 与内容区分隔。
 */
export function Toolbar({ className, children, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="toolbar"
      className={cn(
        'flex shrink-0 flex-wrap items-center gap-2 border-b border-border px-[14px] py-[11px]',
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}

/**
 * 工具条内的弹性占位：把它放在需要右对齐的元素之前，即可把后续内容推到最右。
 * 窄屏时隐藏（此时由 `flex-wrap` 自然换行，右对齐失去意义）。
 */
export function ToolbarSpacer({ className, ...props }: React.ComponentProps<'div'>) {
  return <div data-slot="toolbar-spacer" className={cn('flex-1 max-md:hidden', className)} {...props} />
}
