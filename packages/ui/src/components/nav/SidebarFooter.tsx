import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 侧栏底部操作区（原语层）：一行并排的图标按钮。
 *
 * 【为什么只管「一行」】底部区的内容在各宿主里差别很大（主题、语言、更多、版本号……），
 * 但它们的**排布**是同一件事：并排、等距、靠左、不换行。把这件排布固化下来，
 * 各宿主就不必各写一遍 `flex items-center gap-1.5`，也不会出现某个宿主忘了 `shrink-0`
 * 导致按钮在窄侧栏里被压扁。
 *
 * 本组件不接管折叠态：底部区要不要在折叠时隐藏某些按钮，是宿主的决定
 * （有些宿主希望折叠后只剩一个开关，有些希望全部图标常驻）。
 */
export interface SidebarFooterProps extends React.ComponentProps<'div'> {
  className?: string
}

export function SidebarFooter({ className, children, ...props }: SidebarFooterProps) {
  return (
    <div
      data-slot="sidebar-footer"
      className={cn('flex items-center gap-1.5', className)}
      {...props}
    >
      {children}
    </div>
  )
}
