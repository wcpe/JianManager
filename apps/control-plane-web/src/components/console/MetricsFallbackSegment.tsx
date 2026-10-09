import { useInstanceMetrics } from '@/api/metrics'
import MetricsFallbackSegmentView from '@/components/views/instances/MetricsFallbackSegment'

/** 直探摘要及其数据类型随视图归包；这里再导出，保持既有引用路径可用。 */
export {
  DirectProbeSummary,
  type DirectProbeSummaryData,
} from '@/components/views/instances/MetricsFallbackSegment'

/**
 * 轻量监控视图的应用接线层（ADR-097 a 范式）。
 *
 * 视图本体已迁入组件库并受控；本层取进程指标后注入。
 * 直探摘要暂不传（FR-446/447 编排未落地）——视图会显式呈现「不可用」，而非落回探针伪值。
 * 保留同路径的默认导出与同一套 props，调用点无需改动。
 */
export default function MetricsFallbackSegment({ instanceId }: { instanceId: number }) {
  const { data: metrics } = useInstanceMetrics(instanceId, true)
  return <MetricsFallbackSegmentView metrics={metrics} />
}
