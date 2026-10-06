/**
 * 历史曲线序列的契约（FR-060 / FR-221）。
 *
 * 归包理由：监控段与图表卡都要读它，应用侧 API 层改从包 import，避免两处各写一份。
 */

/** 序列上的一个采样点。 */
export interface SeriesPoint {
  ts: string
  avg: number | null
  min: number | null
  max: number | null
}

/** 一条历史序列。scope=instance 含分世界时 world 非空。 */
export interface MetricSeries {
  metricKey: string
  unit: string
  world: string
  points: SeriesPoint[]
}
