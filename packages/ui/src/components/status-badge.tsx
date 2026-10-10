import { cn } from '../lib/utils'
import type { StatusLevel } from '../lib/threshold'

/**
 * 状态等级 → 徽章底/字配色（高密度面板用）。
 *
 * 【为什么不用 `bg-status-X/15 text-status-X`】纯状态色压在自身的 15% 淡底上，实测
 * **warning 2.72:1、success 2.78:1，低于 WCAG 非文本 3:1**（info 4.20 / danger 3.81 勉强过）。
 * 根因是把「实色底上的前景」直接拿来当「淡底上的墨色」用 —— 淡底提亮了背景，
 * 前景却没跟着加深。改用 weak/ink 三件套（手工取值，非 alpha 混合）后：
 * warning 6.54 / success 6.76 / info 6.12 / danger 5.91，并已纳入对比度门禁
 * （5 主题 × 明暗全绿）。暗色下由 tokens.css 的 color-mix 自动跟随。
 */
const LEVEL_CLASS: Record<StatusLevel, string> = {
  success: 'bg-success-weak text-success-ink',
  warning: 'bg-warning-weak text-warning-ink',
  danger: 'bg-danger-weak text-danger-ink',
  info: 'bg-info-weak text-info-ink',
  neutral: 'bg-muted text-muted-foreground',
}

const DOT_CLASS: Record<StatusLevel, string> = {
  success: 'bg-status-success',
  warning: 'bg-status-warning',
  danger: 'bg-status-danger',
  info: 'bg-status-info',
  neutral: 'bg-muted-foreground',
}

/** 状态徽章（FR-061）：状态等级 → 色点 + 文案，用于实例/节点状态列。 */
export function StatusBadge({
  level,
  label,
  dot = true,
  pulse = false,
  className,
}: {
  level: StatusLevel
  label: string
  /** 是否显示前导色点，默认显示。 */
  dot?: boolean
  /** 过渡态（启动中/停止中）色点脉冲动画，提示「进行中」（FR-138）。 */
  pulse?: boolean
  className?: string
}) {
  return (
    <span
      data-slot="status-badge"
      className={cn(
        'inline-flex w-fit items-center gap-1.5 rounded px-1.5 py-0.5 text-xs font-medium whitespace-nowrap',
        LEVEL_CLASS[level],
        className,
      )}
    >
      {dot && <span className={cn('size-1.5 shrink-0 rounded-full', DOT_CLASS[level], pulse && 'animate-pulse motion-reduce:animate-none')} />}
      {label}
    </span>
  )
}
