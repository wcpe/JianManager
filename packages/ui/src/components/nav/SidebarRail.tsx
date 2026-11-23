import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 折叠态图标钢轨（原语层）：宽度受限时只排布图标的窄条。
 *
 * 【为什么单独成一个原语】「只剩图标时怎么排」与「展开时怎么排」不是同一套规则：
 * 图标需要自己居中、行与行之间要更紧、且不能出现文字占位。宿主若各自手写这段，
 * 就会出现「有的栏用了 justify-center（瞬跳）、有的栏用对称 padding（可过渡）」的漂移。
 * 这里只固定**容器**的这几条：竖向堆叠、图标行居中、可滚动。
 *
 * 行本身仍用 `SideNavRow`（折叠态它自己会处理对称内边距与文字收起），
 * 本组件不重造行样式。
 */
export interface SidebarRailProps extends React.ComponentProps<'div'> {
  className?: string
}

export function SidebarRail({ className, children, ...props }: SidebarRailProps) {
  return (
    <div
      data-slot="sidebar-rail"
      className={cn(
        // `min-h-0` + `overflow-y-auto`：图标数量不确定，多到装不下时由这一条自己滚，
        // 不把底部的常驻段顶出视口。
        'flex min-h-0 flex-col items-center overflow-y-auto overflow-x-hidden scrollbar-none',
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}
