import { useMetricOverview, useMetricSeries, type MetricResolution } from '@/api/metrics'
import type { MetricRange } from '@/components/charts/RangePicker'
import type { RawSeries } from '@/lib/monitor-metrics'

/** 监控 target：平台聚合 / 单节点 / 单实例（与 MonitorSource 对齐）。 */
export type SeriesTarget =
  | { kind: 'platform' }
  | { kind: 'node'; uuid: string }
  | { kind: 'instance'; uuid: string }

/**
 * 按 target + 区间 + 粒度取原始序列（FR-221：供关键指标概览/多指标对比共享同一查询）。
 * 平台走 /metrics/overview（聚合 trends），节点/实例走 /metrics/series。两查询无条件调用、
 * 用 enabled 互斥（满足 rules-of-hooks；TanStack 对 disabled 查询不发请求）。
 */
export function useTargetSeries(
  target: SeriesTarget,
  range: MetricRange,
  resolution: MetricResolution,
): { series: RawSeries[]; isLoading: boolean } {
  const isPlatform = target.kind === 'platform'
  const targetId = isPlatform ? '' : target.uuid
  const scope = target.kind === 'instance' ? 'instance' : 'node'

  const overview = useMetricOverview(range, resolution)
  const seriesQ = useMetricSeries({ scope, targetId, range, resolution, enabled: !isPlatform && !!targetId })

  if (isPlatform) {
    const series: RawSeries[] = (overview.data?.trends ?? []).map((tr) => ({
      metricKey: tr.metricKey,
      points: tr.points.map((p) => ({ ts: p.ts, value: p.avg })),
    }))
    return { series, isLoading: overview.isLoading }
  }
  const series: RawSeries[] = (seriesQ.data?.series ?? []).map((s) => ({
    metricKey: s.metricKey,
    world: s.world,
    points: s.points.map((p) => ({ ts: p.ts, value: p.avg })),
  }))
  return { series, isLoading: seriesQ.isLoading }
}
