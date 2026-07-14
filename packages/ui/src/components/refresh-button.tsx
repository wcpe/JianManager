/**
 * 刷新按钮原语（防重复）。
 *
 * 统一「刷新 / 重新加载」按钮：在途（pending）时图标旋转并禁用，杜绝快速重复点击引发的重复请求。
 * 图标态需调用方传 aria-label；带文案态用 label。
 */
import * as React from 'react'
import { RotateCw } from 'lucide-react'
import { Button } from './button'
import { cn } from '../lib/utils'

interface RefreshButtonProps extends Omit<React.ComponentProps<'button'>, 'children'> {
  /** 在途标记：为真时图标旋转并禁用按钮。 */
  pending?: boolean
  /** 文案；省略则为图标按钮（调用方须传 aria-label）。 */
  label?: string
}

/** 刷新按钮：pending 时旋转 + 禁用；有 label 显文案，否则图标态。 */
export function RefreshButton({ pending, label, className, disabled, ...props }: RefreshButtonProps) {
  return (
    <Button
      type="button"
      variant="outline"
      size={label ? 'sm' : 'icon-sm'}
      disabled={disabled || pending}
      className={className}
      {...props}
    >
      <RotateCw className={cn('size-4', pending && 'animate-spin')} aria-hidden />
      {label}
    </Button>
  )
}
