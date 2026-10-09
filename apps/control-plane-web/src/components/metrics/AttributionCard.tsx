import { useState } from 'react'
import { usePerformanceAttribution } from '@/api/metrics'
import { AttributionCard as AttributionCardView } from '@/components/views/instances/AttributionCard'
import type { MetricRange } from '@jianmanager/ui'

// 结果视图是纯展示，原样再导出（其他页面直接用它渲染已有结果）。
export { AttributionResultView } from '@/components/views/instances/AttributionCard'

/**
 * 性能归因卡的应用接线层（ADR-097 a 范式）。
 *
 * 卡片本体是受控视图（见 components/views）；本层持有「是否已分析过」并接按需取数。
 * 保留同路径的导出与同一套 props，调用点无需改动。
 */
export function AttributionCard({ instanceUuid, range }: { instanceUuid: string; range: MetricRange }) {
  const [requested, setRequested] = useState(false)
  const { data, isFetching, isError, refetch } = usePerformanceAttribution({
    targetId: instanceUuid,
    range,
    enabled: requested,
  })

  return (
    <AttributionCardView
      result={data}
      fetching={isFetching}
      error={isError}
      // 已取过数时走 `refetch()`：`setRequested(true)` 在 `requested` 已为 true 时是同值更新，
      // React 不会重渲染、`enabled`/`queryKey` 均不变，TanStack Query 也不重新取数——
      // 结果是「重新分析」点了毫无反应（自审 M3）。
      onAnalyze={() => (requested ? void refetch() : setRequested(true))}
    />
  )
}
