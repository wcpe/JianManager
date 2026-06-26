import { cn } from '@/lib/utils'
import type { StatusLevel } from '@/lib/threshold'

/** 状态等级 → 徽章底/字配色（半透明底 + 状态色字，高密度面板用）。 */
const LEVEL_CLASS: Record<StatusLevel, string> = {
  success: 'bg-status-success/15 text-status-success',
  warning: 'bg-status-warning/15 text-status-warning',
  danger: 'bg-status-danger/15 text-status-danger',
  info: 'bg-status-info/15 text-status-info',
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
      {dot && <span className={cn('size-1.5 shrink-0 rounded-full', DOT_CLASS[level], pulse && 'animate-pulse')} />}
      {label}
    </span>
  )
}
