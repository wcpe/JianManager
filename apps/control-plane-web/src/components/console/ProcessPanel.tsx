import { useInstanceMetrics } from '@/api/metrics'
import { ProcessPanel as ProcessPanelView } from '@/components/views/instances/ProcessPanel'

/**
 * 通用进程指标面板的应用接线层（ADR-097 a 范式）。
 *
 * 面板本体是受控视图（见 components/views，不取数）；本层取指标后注入。
 * 保留同名同路径的具名导出与同一套 props，三个调用点无需改动。
 */
export function ProcessPanel({ instanceId }: { instanceId: number }) {
  const { data: metrics } = useInstanceMetrics(instanceId, true)
  return <ProcessPanelView metrics={metrics} />
}
