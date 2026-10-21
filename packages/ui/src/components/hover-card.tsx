import * as React from 'react'
import { HoverCard as HoverCardPrimitive } from 'radix-ui'

import { cn } from '../lib/utils'

/**
 * 悬停预览卡（Radix HoverCard 包装）。
 *
 * 与 Tooltip 的分工：Tooltip 承载纯文本提示，本组件承载**结构化预览**（多行信息 / 徽章），
 * 经 Portal 渲染、自带开合延迟与碰撞翻转，不受侧栏滚动容器的 `overflow` 裁切——
 * 这正是「鼠标靠近列表项展示缩略信息」需要的形态。
 */
function HoverCard(props: React.ComponentProps<typeof HoverCardPrimitive.Root>) {
  return <HoverCardPrimitive.Root data-slot="hover-card" {...props} />
}

function HoverCardTrigger(props: React.ComponentProps<typeof HoverCardPrimitive.Trigger>) {
  return <HoverCardPrimitive.Trigger data-slot="hover-card-trigger" {...props} />
}

function HoverCardContent({
  className,
  align = 'start',
  side = 'right',
  sideOffset = 8,
  ...props
}: React.ComponentProps<typeof HoverCardPrimitive.Content>) {
  return (
    <HoverCardPrimitive.Portal data-slot="hover-card-portal">
      <HoverCardPrimitive.Content
        data-slot="hover-card-content"
        align={align}
        side={side}
        sideOffset={sideOffset}
        className={cn(
          'z-50 w-64 rounded-md border border-border bg-card p-3 text-card-foreground shadow-soft outline-none',
          className,
        )}
        {...props}
      />
    </HoverCardPrimitive.Portal>
  )
}

export { HoverCard, HoverCardTrigger, HoverCardContent }
