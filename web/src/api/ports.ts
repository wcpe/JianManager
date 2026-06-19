import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/** 单个实例的端口占用（对应后端 service.PortUsage，FR-032）。 */
export interface PortUsage {
  instanceId: number
  name: string
  role: string
  serverPort: number
  rconPort: number
  queryPort: number
}

/** 节点端口占用与分配范围（对应后端 service.NodePortsResult）。 */
export interface NodePorts {
  nodeId: number
  ranges: { serverPortBase: number; rconPortBase: number; rangeSize: number }
  occupied: PortUsage[]
}

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
