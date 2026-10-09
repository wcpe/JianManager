import { useCallback, useState } from 'react'
import { TopologyGraph as TopologyGraphView } from '@/components/views/console/TopologyGraph'
import { useTopology } from '@/api/topology'
import { useNodes } from '@/api/nodes'
import { useInstanceGroups } from '@/api/instanceGroups'

/** 拓扑图对外 props（保持迁移前形状：调用点只传 className）。 */
export interface TopologyGraphProps {
  className?: string
}

/**
 * 群组服拓扑图接线层（ADR-097）：视图已归包，取数留在应用侧。
 *
 * - `useTopology()`：单条聚合查询消 per-proxy N+1（FR-335）；
 * - `useNodes()`：节点负载标签数据源（FR-453，复用已缓存的节点列表，无 per-instance N+1）；
 * - `useInstanceGroups()`：groupTree 维度专用数据源（FR-452），仅选中该维度时才拉取——
 *   由视图通过 `onGroupTreeNeeded` 通知，本层据此开关查询。
 */
export default function TopologyGraph({ className }: TopologyGraphProps) {
  const { data, isLoading } = useTopology()
  const { data: nodes } = useNodes()
  const [groupTreeNeeded, setGroupTreeNeeded] = useState(false)
  const { data: groupNodes } = useInstanceGroups({ enabled: groupTreeNeeded })

  // 视图挂载/维度变化时通知一次；useCallback 保持引用稳定，避免视图 effect 反复触发。
  const onGroupTreeNeeded = useCallback((needed: boolean) => setGroupTreeNeeded(needed), [])

  return (
    <TopologyGraphView
      className={className}
      data={data}
      isLoading={isLoading}
      nodes={nodes}
      groupNodes={groupNodes}
      onGroupTreeNeeded={onGroupTreeNeeded}
    />
  )
}
