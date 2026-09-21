import { useInstance } from '@/api/instances'
import { useInstanceMetrics } from '@/api/metrics'
import { useInstanceCapabilities } from '@/lib/capabilities'
import { resolveMetricsTabStyle } from '@/lib/metrics-source'
import MetricsSegment from './MetricsSegment'
import MetricsFallbackSegment from './MetricsFallbackSegment'

/**
 * `metrics` Tab 分流器（FR-448 §2.1）：同一 Tab 内按探针有无呈现**两套样式**——
 * - 有探针 → ServerProbe 全量指标 {@link MetricsSegment}（TPS/MSPT/世界/区块）；
 * - 无探针 → 轻量进程/直探视图 {@link MetricsFallbackSegment}（`ProcessPanel` + 直探摘要）。
 *
 * 判定信号优先 `metrics.probeAvailable`（既有运行时信号）；`resolveMetricsTabStyle` 为
 * FR-447 的更细数据源可用性位预留 `sourceAvailable` 接入点，届时在此补传即可。
 */
export default function MetricsTabSegment({ instanceId }: { instanceId: number }) {
  const { data: instance } = useInstance(instanceId)
  const profile = useInstanceCapabilities(instance)
  const { data: metrics } = useInstanceMetrics(instanceId, true)
  const style = resolveMetricsTabStyle(profile, {
    // FR-447 接入点：采集降级编排落地后，此处补 `sourceAvailable` 更细可用性位。
    probeAvailable: metrics?.probeAvailable,
  })
  if (style === 'probe') {
    return <MetricsSegment instanceUuid={instance?.uuid ?? ''} instanceId={instanceId} />
  }
  return <MetricsFallbackSegment instanceId={instanceId} />
}
