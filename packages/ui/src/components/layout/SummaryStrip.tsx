import * as React from 'react'

import { cn } from '../../lib/utils'

/**
 * 摘要统计条（FR-496 阶段 3）：页面顶部的等宽数字概览。
 *
 * 原型为 5 列等宽网格，窄屏降为 3 列（分两行）。做成 `<div>` 而非 `<table>`，
 * 因为它是阅读性概览而非可排序数据；每个格子可点击（传入 `onClick` 即变为按钮语义）。
 */
export function SummaryStrip({ className, children, ...props }: React.ComponentProps<'div'>) {
  return (
    <div
      data-slot="summary-strip"
      className={cn(
        'grid shrink-0 grid-cols-5 overflow-hidden rounded-lg border bg-card',
        'max-md:grid-cols-3',
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}

/**
 * 摘要格：一个标签 + 一个数值。
 * `tone` 只影响数值颜色（状态语义），不改变底色，保持整条视觉统一。
 */
export function SummaryItem({
  label,
  value,
  tone = 'default',
  className,
  ...props
}: React.ComponentProps<'button'> & {
  label: React.ReactNode
  value: React.ReactNode
  tone?: 'default' | 'success' | 'warning' | 'danger'
}) {
  const toneClass = {
    default: 'text-foreground',
    success: 'text-status-success',
    warning: 'text-status-warning',
    danger: 'text-status-danger',
  }[tone]

  return (
    <button
      type="button"
      data-slot="summary-item"
      data-tone={tone}
      className={cn(
        'flex min-w-0 flex-col items-start gap-1 border-r border-border px-4 py-3 text-left last:border-r-0',
        'transition-colors duration-[var(--motion-duration-normal)] ease-ios hover:bg-muted/50',
        className,
      )}
      {...props}
    >
      <span className="truncate text-[11px] text-muted-foreground">{label}</span>
      <span className={cn('font-mono text-base font-medium tabular-nums', toneClass)}>
        {/* 数字统一按 zh-CN 千分位展示，与 PageHeader 的计数口径保持一致；
            需要精确原样输出时传字符串。 */}
        {typeof value === 'number' ? value.toLocaleString('zh-CN') : value}
      </span>
    </button>
  )
}
