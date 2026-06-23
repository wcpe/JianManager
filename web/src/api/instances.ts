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
  /** docker 模式的容器镜像引用（FR-078，ADR-019）；非 docker 模式为空。 */
  image?: string
  /** 系统分配的游戏服监听端口（FR-032），Bot 默认据此连入所属实例。 */
  serverPort: number
  autoStart: boolean
  autoRestart: boolean
  /**
   * 标签集合（FR-047）：环境维度复用 `env:` 前缀（如 `env:prod`），其余为自由标签。
   * 后端以原始 JSON 字符串返回（空为 ""、有值为 `'["env:prod"]'`、清空为 "null"），与
   * envVars/launchSpec 一致；消费前一律经 `parseTags()` 规范化为数组，勿直接当数组用。
   */
  tags: string | string[] | null
  createdAt: string
}

/** 实例列表多维筛选参数（FR-047）：任意组合，留空表示该维度不过滤。 */
export interface InstanceListParams {
  nodeId?: number
  status?: string
  groupId?: number
  role?: string
  /** 群组（Network）ID。 */
  networkId?: number
  /** 环境维度（dev/test/prod），对应 `env:` 前缀标签。 */
  env?: string
  /** 单个自由标签精确匹配。 */
  tag?: string
}

/** 获取实例列表（有过过渡状态实例时自动轮询）。 */
export function useInstances(params?: InstanceListParams) {
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

/** 可更新的实例字段（FR-047 新增 tags：环境/标签维度）。 */
export interface UpdateInstanceBody {
  name?: string
  startCommand?: string
  autoStart?: boolean
  autoRestart?: boolean
  jdkId?: number
  /** 传数组（含空数组）覆盖标签；不传则不变。 */
  tags?: string[]
}

/** 更新实例配置（含标签）。 */
export function useUpdateInstance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: UpdateInstanceBody }) =>
      api.put<InstanceInfo>(`/instances/${id}`, body).then((r) => r.data),
    onSuccess: (_data, { id }) => {
      qc.invalidateQueries({ queryKey: ['instances'] })
      qc.invalidateQueries({ queryKey: ['instances', id] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '更新失败')
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

/** 实例批量操作动作（FR-058）。 */
export type InstanceBatchAction = 'command' | 'start' | 'stop' | 'restart' | 'kill'

/** 批量操作筛选条件（与列表筛选维度一致）。 */
export interface InstanceBatchFilter {
  nodeId?: number
  status?: string
  role?: string
}

/** 批量操作请求，目标由 ids 或 filter 二选一指定。 */
export interface InstanceBatchRequest {
  action: InstanceBatchAction
  ids?: number[]
  filter?: InstanceBatchFilter
  /** action=command 时下发的命令。 */
  command?: string
}

/** 批量操作结果计数。 */
export interface InstanceBatchResult {
  action: string
  requested: number
  succeeded: number
  failed: number
  skipped: number
  errors: { instanceId: number; error: string }[]
}

/** 批量执行 command/start/stop/restart/kill（FR-058）。 */
export function useInstanceBatch() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (payload: InstanceBatchRequest) => {
      const { data } = await api.post<InstanceBatchResult>('/instances/batch', payload)
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['instances'] }),
  })
}
