import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

export interface InstanceInfo {
  id: number
  uuid: string
  nodeId: number
  name: string
  type: string
  processType: string
  status: string
  startCommand: string
  workDir: string
  autoStart: boolean
  autoRestart: boolean
  createdAt: string
}

/** 获取实例列表（有过过渡状态实例时自动轮询）。 */
export function useInstances(params?: { nodeId?: number; status?: string; groupId?: number }) {
  return useQuery({
    queryKey: ['instances', params],
    queryFn: async () => {
      const { data } = await api.get<InstanceInfo[]>('/instances', { params })
      return data
    },
    refetchInterval: (query) => {
      const instances = query.state.data
      if (instances?.some(i => i.status === 'STARTING' || i.status === 'STOPPING')) return 2000
      return false
    },
  })
}

/** 获取实例详情（过渡状态时自动轮询）。 */
export function useInstance(id: number) {
  return useQuery({
    queryKey: ['instances', id],
    queryFn: async () => {
      const { data } = await api.get<InstanceInfo>(`/instances/${id}`)
      return data
    },
    enabled: !!id,
    refetchInterval: (query) => {
      const status = query.state.data?.status
      if (status === 'STARTING' || status === 'STOPPING') return 2000
      return false
    },
  })
}

/** 启动实例。 */
export function useStartInstance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post(`/instances/${id}/start`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instances'] }),
  })
}

/** 停止实例。 */
export function useStopInstance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post(`/instances/${id}/stop`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instances'] }),
  })
}

/** 重启实例。 */
export function useRestartInstance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post(`/instances/${id}/restart`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instances'] }),
  })
}

/** 强制终止实例。 */
export function useKillInstance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post(`/instances/${id}/kill`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instances'] }),
  })
}

/** 删除实例。 */
export function useDeleteInstance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/instances/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instances'] }),
  })
}
