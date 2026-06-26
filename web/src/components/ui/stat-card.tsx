import * as React from 'react'

import { cn } from '@/lib/utils'
import { MiniBar } from '@/components/ui/mini-bar'
import { statusTextClass, type StatusLevel } from '@/lib/threshold'
import { toneChipClass, type Tone } from '@/lib/tone'
import { deltaTone } from '@/lib/stat-card'

/**
 * KPI 统计卡（FR-163）：图标块 + 标签 + 大数值 + 「按指标混搭」右侧视觉。
 * 占比型传 `bar`、计数型传 `delta`/`sub`、走势型传 `trend`——映射规则见 `lib/stat-card.pickStatVisual`。
 */
export interface StatCardProps {
  /** 指标名（小标签）。 */
  label: React.ReactNode
  /** 主数值（大字号 tabular）。 */
  value: React.ReactNode
  /** 副信息，如 `/38`、`在线`。 */
  sub?: React.ReactNode
  /** 左上语义图标。 */
  icon?: React.ReactNode
  /** 图标块色调，默认主色。 */
  tone?: Tone
  /** 占比条（占比型指标）：value/max → 阈值变色 MiniBar。 */
  bar?: { value: number; max?: number; level?: StatusLevel }
  /** 增量（计数型走势）：±数字 + 方向语义着色。 */
  delta?: number
  /** 增量方向语义，缺省「升为好」（绿）；越低越好的指标传 `down`。 */
  deltaGoodWhen?: 'up' | 'down'
  /** 走势型自定义视觉（如趋势线）。 */
  trend?: React.ReactNode
  className?: string
}

export function StatCard({
  label,
  value,
  sub,
  icon,
  tone = 'primary',
  bar,
  delta,
  deltaGoodWhen,
  trend,
  className,
}: StatCardProps) {
  const dt = delta !== undefined ? deltaTone(delta, deltaGoodWhen) : null
  return (
    <div
      data-slot="stat-card"
      className={cn(
        'flex flex-col rounded-xl border bg-card px-3 py-2.5 text-card-foreground shadow-soft',
        className,
      )}
    >
      <div className="flex items-center gap-2">
        {icon && (
          <span className={cn('flex size-6 shrink-0 items-center justify-center rounded-md', toneChipClass(tone))}>
            {icon}
          </span>
        )}
        <span className="text-[11px] font-medium text-muted-foreground">{label}</span>
      </div>
      <div className="mt-1 flex items-baseline gap-1.5">
        <span className="text-2xl font-semibold leading-none tabular-nums">{value}</span>
        {sub && <span className="text-xs text-muted-foreground">{sub}</span>}
        {dt && delta !== undefined && (
          <span className={cn('text-xs font-medium', statusTextClass(dt.level))}>
            {dt.arrow}
            {Math.abs(delta)}
          </span>
        )}
      </div>
      {bar && <MiniBar value={bar.value} max={bar.max} level={bar.level} className="mt-2" />}
      {trend && <div className="mt-2">{trend}</div>}
    </div>
  )
}
