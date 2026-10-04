import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 页面壳（FR-496 阶段 3）：统一所有内容页的留白、纵向节奏与滚动模型。
 *
 * 依据《资源优先工作区》原型实测的三种壳态：
 *   default — 整页滚动（原型 `.page`）
 *   fixed   — 固定视口、内部区域自行滚动（原型 `.page.fixed`）；用于列表页与设置页，
 *             使工具栏/表头常驻、只有数据区滚动
 *   tool    — 全占满、无外层留白（原型 `.page.tool`）；用于终端/文件等自带滚动的工具页
 *
 * 规范第一条：任何内容页的第一个子元素都应为 `PageHeader`。
 */
export function PageShell({
  variant = 'default',
  className,
  children,
  ...props
}: React.ComponentProps<'div'> & {
  /** 滚动模型；默认整页滚动。 */
  variant?: 'default' | 'fixed' | 'tool'
}) {
  return (
    <div
      data-slot="page-shell"
      data-variant={variant}
      className={cn(
        'flex min-h-0 flex-1 flex-col gap-[17px] px-[25px] py-[22px]',
        variant === 'default' && 'overflow-auto',
        variant === 'fixed' && 'overflow-hidden',
        variant === 'tool' && 'gap-0 p-0',
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}
