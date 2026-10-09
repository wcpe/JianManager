import { useState } from 'react'
import { useNavigate } from 'react-router'
import { useInstanceRanking } from '@/api/metrics'
import { InstanceRankingPanel as InstanceRankingPanelView, type RankingWindow } from '@/components/views/instances/InstanceRankingPanel'
import type { RankingMetric } from '@jianmanager/ui/lib/ranking'

// 纯逻辑格式化函数与窗口常量原样再导出（测试与其他页面直接引用）。
export { fmtRankingValue, RANKING_WINDOWS } from '@/components/views/instances/InstanceRankingPanel'

/**
 * 全局排行面板的应用接线层（ADR-097 a 范式）。
 *
 * 面板本体已迁入组件库并受控；本层持有指标/窗口/节点筛选三态（取数依赖它们）并接下钻路由。
 * 保留同路径的导出与同一套 props，调用点无需改动。
 */
export function InstanceRankingPanel({ nodes }: { nodes?: { uuid: string; name: string }[] }) {
  const navigate = useNavigate()
  const [metric, setMetric] = useState<RankingMetric>('inst_tps')
  const [window, setWindow] = useState<RankingWindow>('5m')
  const [nodeId, setNodeId] = useState<string>('all')
  const { data, isError, isLoading } = useInstanceRanking({
    metric,
    window,
    limit: 50,
    nodeId: nodeId === 'all' ? undefined : nodeId,
  })

  return (
    <InstanceRankingPanelView
      nodes={nodes}
      metric={metric}
      onMetricChange={setMetric}
      window={window}
      onWindowChange={setWindow}
      nodeId={nodeId}
      onNodeIdChange={setNodeId}
      data={data}
      loading={isLoading}
      error={isError}
      onOpenInstance={(id) => navigate(`/instances/${id}`)}
    />
  )
}
