import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'

export interface InstanceInfo {
  id: number
  uuid: string
  nodeId: number
  name: string
  type: string
  /** 群组服角色（FR-032）：proxy / backend / universal。 */
  role: string
  processType: string
  status: string
  startCommand: string
  workDir: string
  /** 系统分配的游戏服监听端口（FR-032），Bot 默认据此连入所属实例。 */
  serverPort: number
  autoStart: boolean
  autoRestart: boolean
  createdAt: string
}

/** 获取实例列表（有过过渡状态实例时自动轮询）。 */
export function useInstances(params?: { nodeId?: number; status?: string; groupId?: number; role?: string }) {
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
    onSuccess: () => {
      toast.success('实例启动中…')
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '启动失败')
    },
  })
}

/** 停止实例。 */
export function useStopInstance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post(`/instances/${id}/stop`),
    onSuccess: () => {
      toast.success('实例已停止')
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '停止失败')
    },
  })
}

/** 重启实例。 */
export function useRestartInstance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post(`/instances/${id}/restart`),
    onSuccess: () => {
      toast.success('实例重启中…')
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '重启失败')
    },
  })
}

/** 强制终止实例。 */
export function useKillInstance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post(`/instances/${id}/kill`),
    onSuccess: () => {
      toast.success('实例已强制终止')
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '终止失败')
    },
  })
}

/** 删除实例。 */
export function useDeleteInstance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/instances/${id}`),
    onSuccess: () => {
      toast.success('实例已删除')
      qc.invalidateQueries({ queryKey: ['instances'] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '删除失败')
    },
  })
}
