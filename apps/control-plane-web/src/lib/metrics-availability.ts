/**
 * MC 直探可用性位与数据来源（FR-446 / FR-447）。
 *
 * 后端 `MetricsData` 已从「`-1` / `--` 伪值」改为**显式可用性位**（`playersAvailable` /
 * `motdAvailable` / …）与 `sourceMask`（本拍命中来源位）。前端据此渲染「不可用」，
 * 不再把缺测当成 `0`（否则未就绪实例会显示「0 人在线」）。
 *
 * 采集优先级链 `探针 → SLP → Query → 不可用`（FR-447）：同指标多源以探针为准，
 * 探针缺该指标时按 SLP → Query 回落，三档皆无即「不可用」。本模块为纯函数，便于穷举三态。
 */

/** 来源位（与后端 `metrics.SourceMask` 对齐）。 */
export const METRIC_SOURCE_PROBE = 1
export const METRIC_SOURCE_SLP = 2
export const METRIC_SOURCE_QUERY = 4

export type MetricSourceKey = 'probe' | 'slp' | 'query'

/** 参与可用性判断的最小指标形状（与 `InstanceMetricsData` 子集对齐，便于测试传入骨架）。 */
export interface AvailabilityBits {
  sourceMask?: number
  probeAvailable?: boolean
  slpAvailable?: boolean
  queryAvailable?: boolean
}

/**
 * 解析本拍命中的来源，按 FR-447 优先级 `探针 → SLP → Query` 排序去重。
 *
 * `sourceMask` 为 0 / 缺省时按可用性位回退（兼容旧 CP 或 devmock 未发 `sourceMask` 的场景）。
 */
export function activeMetricSources(m: AvailabilityBits): MetricSourceKey[] {
  let mask = m.sourceMask ?? 0
  if (mask === 0) {
    if (m.probeAvailable) mask |= METRIC_SOURCE_PROBE
    if (m.slpAvailable) mask |= METRIC_SOURCE_SLP
    if (m.queryAvailable) mask |= METRIC_SOURCE_QUERY
  }
  const out: MetricSourceKey[] = []
  if (mask & METRIC_SOURCE_PROBE) out.push('probe')
  if (mask & METRIC_SOURCE_SLP) out.push('slp')
  if (mask & METRIC_SOURCE_QUERY) out.push('query')
  return out
}

/** 本拍是否命中任一来源；三源皆非即整体「不可用」（无任何可展示的数据源）。 */
export function hasAnyMetricSource(m: AvailabilityBits): boolean {
  return activeMetricSources(m).length > 0
}

/** 来源位 → i18n 键（供前端标注数据来源，如「探针」「SLP 直探」「Query 直探」）。 */
export function metricSourceLabelKey(source: MetricSourceKey): string {
  switch (source) {
    case 'probe':
      return 'metrics.source.probe'
    case 'slp':
      return 'metrics.source.slp'
    case 'query':
      return 'metrics.source.query'
  }
}

/**
 * 单指标可用性 → 展示值：可用显示值，不可用显示「不可用」占位（由调用方传入文案）。
 *
 * 用于统一「缺测即不可用」的渲染语义，避免 `0` / `-1` / `--` 被误读为真实数值。
 */
export function displayIfAvailable<T>(available: boolean | undefined, value: T, unavailableLabel: string): T | string {
  return available ? value : unavailableLabel
}
