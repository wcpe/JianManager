import * as React from 'react'
import { cva, type VariantProps } from 'class-variance-authority'

import { cn } from '../lib/utils'

/**
 * 加载指示器（FR-496 阶段 6）。
 *
 * 此前加载反馈散落在各处：刷新按钮转自己的图标、提交按钮只把文字换成"保存中…"、
 * 面板用骨架屏。同一个"正在等后端"的状态有四种表达，用户每次都要重新认。
 * 这里给出统一的转圈原语，供 Button `isLoading` 等场景复用。
 *
 * 用 SVG 而非 CSS border 技巧：SVG 的描边线宽/端点可控，在 12px 这种小尺寸下
 * 比 border 转圈更清晰，也不会因圆角裁切出现断头。
 */

const spinnerVariants = cva(
  // 缓动与时长不走 motion token：旋转是持续动画，没有"开始/结束"的节奏概念，
  // 用固定线速度（1s 一圈）才能让不同尺寸的指示器观感一致。
  'inline-block shrink-0 animate-spin',
  {
    variants: {
      size: {
        xs: 'size-3',
        sm: 'size-3.5',
        default: 'size-4',
        lg: 'size-5',
      },
    },
    defaultVariants: {
      size: 'default',
    },
  },
)

function Spinner({
  className,
  size = 'default',
  label,
  ...props
}: Omit<React.ComponentProps<'svg'>, 'children'> &
  VariantProps<typeof spinnerVariants> & {
    /**
     * 可访问名。不给即视为纯装饰（对辅助技术隐藏）——
     * 加载状态通常已由容器的 `aria-busy`/文案表达，指示器再报一次只是噪音。
     */
    label?: string
  }) {
  const isDecorative = label === undefined

  return (
    <svg
      data-slot="spinner"
      data-size={size}
      viewBox="0 0 24 24"
      fill="none"
      role={isDecorative ? undefined : 'img'}
      aria-label={label}
      aria-hidden={isDecorative ? true : undefined}
      className={cn(spinnerVariants({ size }), className)}
      {...props}
    >
      {/* 轨道 + 头部两段圆弧：单段圆弧在暗色底上容易看不清"转的是个圆" */}
      <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth="3" className="opacity-25" />
      <path
        d="M21 12a9 9 0 0 0-9-9"
        stroke="currentColor"
        strokeWidth="3"
        strokeLinecap="round"
        className="opacity-90"
      />
    </svg>
  )
}

export { Spinner, spinnerVariants }
