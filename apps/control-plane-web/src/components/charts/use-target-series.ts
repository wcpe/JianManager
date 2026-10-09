import { useMetricOverview, useMetricSeries } from '@/api/metrics'
import type { MetricResolution } from '@/api/metrics'
import type { MetricRange } from '@jianmanager/ui'
import type { MonitorSource } from '@jianmanager/ui/charts/MonitorSkeleton'
import type { RawSeries } from '@jianmanager/ui/lib/monitor-metrics'

/**
 * 监控 target：平台聚合 / 单节点 / 单实例。
 *
 * 与包内的 `MonitorSource` 同构（后者供 `MonitorSkeleton` 的 source 用），故直接复用其类型
 * 而不是各写一份——两者若漂移，下钻选出的 target 会喂不进主图网格。
 */
export type SeriesTarget = MonitorSource

/**
 * 按 target + 区间 + 粒度取原始序列（FR-221：供关键指标概览/多指标对比共享同一查询）。
 * 平台走 /metrics/overview（聚合 trends），节点/实例走 /metrics/series。两查询无条件调用、
 * 用 enabled 互斥（满足 rules-of-hooks；TanStack 对 disabled 查询不发请求）。
 *
 * 本 hook 依赖应用侧 API 层（鉴权与取数都在这里），故留在应用侧不入包；
 * 它产出的 `RawSeries` 已是包内类型，可直接喂给包内视图。
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
