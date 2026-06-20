import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

export interface NodeInfo {
  id: number
  uuid: string
  name: string
  host: string
  grpcPort: number
  wsPort: number
  status: number // 0=offline, 1=online, 2=starting
  /** 维护模式（cordon）：true 时禁止新实例调度到该节点（FR-048）。 */
  maintenance: boolean
  os: string
  arch: string
  cpuCores: number
  memoryMb: number
  diskTotalMb: number
  cpuUsage: number
  memoryUsage: number
  diskUsage: number
  networkBytesSent: number
  networkBytesRecv: number
  lastHeartbeat: string | null
  createdAt: string
}

/** 节点排空结果（FR-048）。 */
export interface DrainResult {
  stoppedCount: number
  stopped: number[]
  failed: number[]
  errors?: string[]
}

/** 获取节点列表，支持自动轮询刷新。 */
export function useNodes(options?: { refetchInterval?: number }) {
  return useQuery({
    queryKey: ['nodes'],
    queryFn: async () => {
      const { data } = await api.get<NodeInfo[]>('/nodes')
      return data
    },
    refetchInterval: options?.refetchInterval,
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

/** 置/解节点维护模式（cordon，FR-048）。 */
export function useSetNodeMaintenance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, enabled }: { id: number; enabled: boolean }) =>
      api.post<NodeInfo>(`/nodes/${id}/maintenance`, { enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['nodes'] }),
  })
}

/** 排空节点：停止其上运行实例（FR-048）。 */
export function useDrainNode() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post<DrainResult>(`/nodes/${id}/drain`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['nodes'] })
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
  })
}

/** 主动下线节点：解除注册并保留记录（FR-048）。 */
export function useDeleteNode() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/nodes/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['nodes'] }),
  })
}
