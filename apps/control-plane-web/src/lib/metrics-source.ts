import type { Capability, DataSource, InstanceCapabilityProfile } from './capabilities'

/**
 * metrics Tab 的数据来源样式（FR-448 §2.1）：
 * - `probe`：ServerProbe 全量指标（TPS/MSPT/世界/区块，`MetricsSegment`）；
 * - `lightweight`：轻量进程/直探视图（`ProcessPanel` + 直探摘要）。
 *
 * 同 Tab、不同内容（非隐藏 Tab）——探针不可用时不展示伪 `-1`/零值占位。
 */
export type MetricsTabStyle = 'probe' | 'lightweight'

/**
 * 运行时数据源可用性位（FR-447 接入点）。
 * - `probe`：Worker 抓取 `/metrics` 成功（即既有的 `metrics.probeAvailable`，与 TPS 同源）；
 * - `direct`：MC 直探 SLP/Query（FR-446），接入前恒不可用；
 * - `node`：节点侧进程采集（`ProcessPanel` 的来源）。
 * 未提供的位一律按「不可用」处理。
 */
export type DataSourceAvailability = Partial<Record<DataSource, boolean>>

/** metrics Tab 样式判定信号。 */
export interface MetricsTabSignals {
  /** 运行时探针是否可用（`metrics.probeAvailable`，既有信号）。 */
  probeAvailable?: boolean
  /**
   * FR-447 接入点：更细的数据源可用性位。提供时 `sourceAvailable.probe` **优先**于
   * `probeAvailable`，供后续按来源分别判定（如探针在但直探被关闭）；当前调用方不传。
   */
  sourceAvailable?: DataSourceAvailability
}

/** 画像是否声明某能力以某来源为数据来源。 */
export function capabilityUsesSource(
  profile: InstanceCapabilityProfile,
  cap: Capability,
  source: DataSource,
): boolean {
  return profile.sources?.[cap]?.includes(source) ?? false
}

/**
 * 判定 metrics Tab 走哪套样式（FR-448 §2.1）。
 *
 * 画像 `sources.metrics` 声明 `probe` 为来源、且运行时探针可用 → 探针全量视图；
 * 其余情况（探针不在 / 画像未声明探针）→ 轻量降级视图。
 *
 * 判定信号优先取 `sourceAvailable.probe`（FR-447 更细位），缺省回落 `probeAvailable`。
 */
export function resolveMetricsTabStyle(
  profile: InstanceCapabilityProfile,
  signals: MetricsTabSignals,
): MetricsTabStyle {
  const declaredProbe = capabilityUsesSource(profile, 'metrics', 'probe')
  const probeAvailable = signals.sourceAvailable?.probe ?? signals.probeAvailable ?? false
  return declaredProbe && probeAvailable ? 'probe' : 'lightweight'
}
