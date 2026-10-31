/**
 * 刷新按钮原语（防重复）。
 *
 * 统一「刷新 / 重新加载」按钮：在途（pending）时图标旋转并禁用，杜绝快速重复点击引发的重复请求。
 * 图标态需调用方传 aria-label；带文案态用 label。
 *
 * 【为什么要透传 variant / size / iconSize】收敛前主控台有十余处手写刷新按钮，
 * 形态并不统一：有的 `ghost`、有的 `outline`；图标有的 `size-3.5`、有的 `size-4`；
 * 有的带文案、有的纯图标。初版只暴露 `pending` + `label`，结果每一处替换都要
 * 用 `className` 强行覆盖，反而比手写更绕——那样的"统一"没人会自愿使用。
 *
 * 现在把这三个维度显式化：`variant` 决定按钮样式、`size` 决定按钮盒尺寸、
 * `iconSize` 决定图标尺寸。三者的默认值取站内最常见的一组（outline / sm / 4），
 * 特殊场景显式传入即可，不必再靠 className 打补丁。
 */
import * as React from 'react'
import { RotateCw } from 'lucide-react'
import { Button } from './button'
import type { VariantProps } from 'class-variance-authority'
import { cn } from '../lib/utils'

type ButtonVariants = VariantProps<typeof Button>

interface RefreshButtonProps
  extends Omit<React.ComponentProps<'button'>, 'children' | 'size' | 'color'> {
  /** 在途标记：为真时图标旋转并禁用按钮。 */
  pending?: boolean
  /** 文案；省略则为图标按钮（调用方须传 aria-label）。 */
  label?: React.ReactNode
  /** 按钮样式，默认 `outline`（站内最常见）。 */
  variant?: ButtonVariants['variant']
  /** 按钮盒尺寸；省略时按有无 `label` 自动取 `sm` / `icon-sm`。 */
  size?: ButtonVariants['size']
  /**
   * 图标尺寸类（如 `size-3.5` / `size-4`）。默认 `size-4`。
   *
   * 单独一个维度是因为图标与按钮盒并不总是同比例：紧凑工具条里常见
   * 「按钮 `icon-sm` + 图标 `size-3.5`」的组合，用 `size` 推断不出来。
   */
  iconSize?: string
}

/** 刷新按钮：pending 时旋转 + 禁用；有 label 显文案，否则图标态。 */
export function RefreshButton({
  pending,
  label,
  variant = 'outline',
  size,
  iconSize = 'size-4',
  className,
  disabled,
  ...props
}: RefreshButtonProps) {
  return (
    <Button
      type="button"
      variant={variant}
      size={size ?? (label ? 'sm' : 'icon-sm')}
      disabled={disabled || pending}
      // 在途时给屏幕阅读器一个明确提示：按钮被禁用但用户需要知道「正在刷新」而非「不可用」。
      aria-busy={pending || undefined}
      className={className}
      {...props}
    >
      <RotateCw className={cn(iconSize, pending && 'animate-spin')} aria-hidden />
      {label}
    </Button>
  )
}
