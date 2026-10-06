import { useInstance } from '@/api/instances'
import { useInstanceMetrics } from '@/api/metrics'
import { useInstanceCapabilities } from '@/lib/capabilities'
import { resolveMetricsTabStyle } from '@/lib/metrics-source'
import MetricsTabSegmentView from '@jianmanager/ui/components/views/instances/MetricsTabSegment'
import MetricsSegment from './MetricsSegment'
import MetricsFallbackSegment from './MetricsFallbackSegment'

/**
 * `metrics` Tab 分流器的应用接线层（ADR-097）。
 *
 * 分流判定留在应用侧：它要读能力画像与探针可用性（`resolveMetricsTabStyle` 也是应用侧知识）。
 * 视图只按结果渲染，两个分支在此注入。保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function MetricsTabSegment({ instanceId }: { instanceId: number }) {
  const { data: instance } = useInstance(instanceId)
  const profile = useInstanceCapabilities(instance)
  const { data: metrics } = useInstanceMetrics(instanceId, true)
  const style = resolveMetricsTabStyle(profile, {
    // FR-447 接入点：采集降级编排落地后，此处补 `sourceAvailable` 更细可用性位。
    probeAvailable: metrics?.probeAvailable,
  })

  return (
    <MetricsTabSegmentView
      style={style}
      probeSlot={<MetricsSegment instanceUuid={instance?.uuid ?? ''} instanceId={instanceId} />}
      fallbackSlot={<MetricsFallbackSegment instanceId={instanceId} />}
    />
  )
}
