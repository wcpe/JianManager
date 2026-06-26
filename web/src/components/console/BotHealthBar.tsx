import { useTranslation } from 'react-i18next'
import { healthBreakdown, type HealthKind } from './bot-health'
import { cn } from '@/lib/utils'

/** 健康段类型 → 配色（绿/琥珀/红/灰），用 FR-163 状态 token。 */
const SEG_COLOR: Record<HealthKind, string> = {
  connected: 'bg-status-success',
  connecting: 'bg-status-warning',
  error: 'bg-status-danger',
  stopped: 'bg-muted-foreground/40',
}

/**
 * Bot 健康条多段着色（FR-147，兑现 FR-040「细分 connecting/error」）。
 * 传 byStatus（各状态计数）则多段（connected/connecting/error/stopped）；
 * 不传 byStatus 时退化为「在线 vs 其余」两段（分组摘要只有 online/total 的场景）。
 */
export function BotHealthBar({
  total,
  online,
  byStatus,
  className,
}: {
  total: number
  online: number
  /** 各状态计数（来自全局 summary.byStatus）；提供则多段着色。 */
  byStatus?: Record<string, number>
  className?: string
}) {
  const { t } = useTranslation()
  // 有精确 byStatus 走多段；否则用 online/(total-online) 凑两段（connected + stopped 兜底）。
  const segments = byStatus
    ? healthBreakdown(total, byStatus)
    : healthBreakdown(total, { connected: online })

  return (
    <div
      className={cn('flex h-2.5 w-full overflow-hidden rounded-full bg-muted', className)}
      title={t('bots.healthTooltip', { online, total })}
    >
      {segments.map((seg) => (
        <div
          key={seg.kind}
          className={SEG_COLOR[seg.kind]}
          style={{ width: `${seg.ratio * 100}%` }}
        />
      ))}
    </div>
  )
}
