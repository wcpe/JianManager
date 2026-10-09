import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'
// 契约类型随受控视图归包（ADR-097 a 范式）：API 层与包内组件共用同一份定义，
// 避免包内组件为取类型而反向依赖应用的 @/api。
import type { NodePorts } from '@/lib/node-ports'

export type { NodePorts, PortUsage } from '@/lib/node-ports'

/** 查看某节点的端口占用情况（系统分配端口的可视化）。 */
export function useNodePorts(nodeId: number) {
  return useQuery({
    queryKey: ['node-ports', nodeId],
    queryFn: async () => {
      const { data } = await api.get<NodePorts>(`/nodes/${nodeId}/ports`)
      return data
    },
    enabled: !!nodeId,
  })
}
