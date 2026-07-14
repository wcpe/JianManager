/**
 * 骨架屏占位原语。
 *
 * 统一各页加载态：以 animate-pulse 灰块占位真实内容轮廓，替代裸「加载中…」文字。
 * 走主题 CSS 变量（bg-muted），暗/亮色自适应。
 */
import * as React from 'react'
import { cn } from '../lib/utils'

/** 单个骨架块。通过 className 控制尺寸/圆角，组合出卡片/行/文本占位。 */
function Skeleton({ className, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="skeleton"
      className={cn('animate-pulse rounded-md bg-muted', className)}
      {...props}
    />
  )
}

export { Skeleton }
