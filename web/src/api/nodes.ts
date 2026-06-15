import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

export interface NodeInfo {
  id: number
  uuid: string
  name: string
  host: string
  grpcPort: number
  wsPort: number
  status: number // 0=offline, 1=online, 2=starting
  os: string
  arch: string
  cpuCores: number
  memoryMb: number
  diskTotalMb: number
  cpuUsage: number
  memoryUsage: number
  diskUsage: number
  lastHeartbeat: string | null
  createdAt: string
}

/** 获取节点列表。 */
export function useNodes() {
  return useQuery({
    queryKey: ['nodes'],
    queryFn: async () => {
      const { data } = await api.get<NodeInfo[]>('/nodes')
      return data
    },
  })
}

/** 获取节点详情。 */
export function useNode(id: number) {
  return useQuery({
    queryKey: ['nodes', id],
    queryFn: async () => {
      const { data } = await api.get<NodeInfo>(`/nodes/${id}`)
      return data
    },
    enabled: !!id,
  })
}
