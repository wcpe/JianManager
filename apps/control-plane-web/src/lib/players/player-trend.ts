import type { SeriesPoint } from '@/lib/metrics/metric-series'

/**
 * 玩家在线趋势的契约（FR-469）。
 *
 * 归包理由：趋势卡要按它渲染曲线与时段分布，应用侧 API 层改从包 import。
 */
export interface PlayerTrendResult {
  resolution: string
  timezone: string
  trend: SeriesPoint[]
  hourlyDist: number[]
  peakValue: number
  peakAt: string | null
  dailyAvg: number
}
