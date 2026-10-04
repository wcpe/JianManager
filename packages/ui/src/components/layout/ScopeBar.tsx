import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 作用域条（FR-496 阶段 3）：明示「当前这一页在什么范围内取数」，并提供范围切换。
 *
 * 原型把「范围」做成独立的条而不是塞进页头操作区，理由是：范围不是过滤条件，
 * 它改变的是查询对象本身（如「全部节点」→「node-east-02」），必须比普通筛选更显眼。
 * 右侧 `note` 用于放口径说明（如统计时区、数据新鲜度）。
 */
export function ScopeBar({
  scope,
  note,
  children,
  className,
  ...props
}: React.ComponentProps<'div'> & {
  /** 当前范围标签；传入时以主色调 chip 形式前置展示。 */
  scope?: React.ReactNode
  /** 右侧补充说明（自动推到最右）。 */
  note?: React.ReactNode
}) {
  return (
    <div
      data-slot="scope-bar"
      className={cn(
        'flex shrink-0 flex-wrap items-center gap-[7px] rounded-lg border bg-card px-[13px] py-[9px]',
        className,
      )}
      {...props}
    >
      {scope && (
        <span className="inline-flex items-center gap-1.5 rounded-md bg-accent px-2 py-[3px] text-[11px] font-medium text-accent-foreground">
          {scope}
        </span>
      )}
      {children}
      {note && <small className="ml-auto text-[11px] text-muted-foreground">{note}</small>}
    </div>
  )
}
