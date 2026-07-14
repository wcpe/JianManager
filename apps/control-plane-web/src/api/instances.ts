import { keepPreviousData, queryOptions, useInfiniteQuery, useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import api from '@/api/client'
import { removeServer } from '@/components/console/server-selection'

/**
 * 实例域查询缓存保留时长（FR-297）：控制台来回切换（页签/跨服）时命中缓存先呈现旧数据、
 * 后台刷新，避免每次回切都白屏等待。默认 gcTime 5 分钟不够覆盖运营者巡检节奏，提至 15 分钟。
 */
export const INSTANCE_QUERY_GC_TIME_MS = 15 * 60_000

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
  /** 当前状态原因，主要用于 CRASHED：异步委托失败的具体错误（如「实例未绑定 JDK…」），供前端显示。 */
  statusReason?: string
  startCommand: string
  /** 绑定的 JDK id（0/缺省=未绑定，用系统 Java）。 */
  jdkId?: number
  workDir: string
  /** 就地导入标记（FR-302）：工作目录为托管区外原目录，删除实例不删原目录。 */
  workDirInPlace?: boolean
  /** docker 模式的容器镜像引用（FR-078，ADR-019）；非 docker 模式为空。 */
  image?: string
  /** docker 模式 CPU 核数上限（FR-079）；0=不限制，仅 docker 模式生效。 */
  cpuLimit?: number
  /** docker 模式内存上限（MiB，FR-079）；0=不限制，仅 docker 模式生效。 */
  memLimitMb?: number
  /** docker 模式磁盘上限（MiB，FR-079）；0=不限制，v1 仅持久化/展示不注入。 */
  diskLimitMb?: number
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

/**
 * 实例是否处于「搭建中」（FR-331）：一键搭建 provision 任务未终态期间，后端把实例
 * statusReason 固定标注为「搭建中：…」且状态保持 STOPPED（FR-319 二轮③）。
 * 前端据此硬性禁用启动入口（与后端启动闸同一信号源）；任务终态后 reason 被清空/改写，自然解禁。
 */
export function isProvisioningInstance(inst: Pick<InstanceInfo, 'status' | 'statusReason'>): boolean {
  return inst.status === 'STOPPED' && !!inst.statusReason?.startsWith('搭建中')
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

export interface InstanceSearchParams extends InstanceListParams {
  /** 名称子串搜索（FR-247）。 */
  q?: string
  sort?: 'name' | 'status' | 'createdAt' | 'nodeId'
  order?: 'asc' | 'desc'
  page?: number
  pageSize?: number
}

export interface InstanceSearchResult {
  items: InstanceInfo[]
  total: number
  page: number
  pageSize: number
}

export interface InstanceNodeCount {
  nodeId: number
  count: number
}

export interface InstanceAggregate {
  total: number
  byStatus: Record<string, number>
  byNode: InstanceNodeCount[]
  byRole: Record<string, number>
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

/** 分页搜索实例（FR-235）：用于 1000+ 实例页面，避免首屏拉取全集。 */
export function useInstanceSearch(params: InstanceSearchParams = {}, enabled = true) {
  return useQuery({
    queryKey: ['instances', 'search', params],
    enabled,
    queryFn: async () => {
      const { data } = await api.get<InstanceSearchResult>('/instances/search', { params })
      return data
    },
    refetchInterval: (query) => {
      const instances = query.state.data?.items
      if (instances?.some(i => i.status === 'STARTING' || i.status === 'STOPPING')) return 2000
      return false
    },
  })
}

/** 分页搜索实例（FR-247，面向 1000+ 实例）。 */
export const useSearchInstances = useInstanceSearch

/** 无限分页实例搜索（FR-235）：滚动到未加载区域时按页补齐。 */
export function useInfiniteInstanceSearch(params: Omit<InstanceSearchParams, 'page'>, initialPage = 1) {
  return useInfiniteQuery({
    queryKey: ['instances', 'search', 'infinite', params, initialPage],
    initialPageParam: initialPage,
    queryFn: async ({ pageParam }) => {
      const { data } = await api.get<InstanceSearchResult>('/instances/search', {
        params: { ...params, page: pageParam },
      })
      return data
    },
    getNextPageParam: (lastPage) => {
      const loaded = lastPage.page * lastPage.pageSize
      return loaded < lastPage.total ? lastPage.page + 1 : undefined
    },
    refetchInterval: (query) => {
      const instances = query.state.data?.pages.flatMap((p) => p.items)
      if (instances?.some(i => i.status === 'STARTING' || i.status === 'STOPPING')) return 2000
      return false
    },
  })
}

/** 获取实例维度聚合计数（FR-247）。 */
export function useInstanceAggregate(params?: InstanceListParams & { q?: string }, enabled = true) {
  return useQuery({
    queryKey: ['instances', 'aggregate', params],
    queryFn: async () => {
      const { data } = await api.get<InstanceAggregate>('/instances/aggregate', { params })
      return data
    },
    enabled,
  })
}

/**
 * 实例详情查询选项（FR-297）：useInstance 与悬停预取（`lib/instance-prefetch.ts`）共用
 * 同一 queryKey/queryFn/gcTime，保证预取结果能被后续 useInstance 直接命中。
 */
export function instanceQueryOptions(id: number) {
  return queryOptions({
    queryKey: ['instances', id],
    queryFn: async () => {
      const { data } = await api.get<InstanceInfo>(`/instances/${id}`)
      return data
    },
    gcTime: INSTANCE_QUERY_GC_TIME_MS,
  })
}

/** 获取实例详情（过渡状态时自动轮询；FR-297 回切先呈现缓存后台刷新）。 */
export function useInstance(id: number) {
  return useQuery({
    ...instanceQueryOptions(id),
    enabled: !!id,
    placeholderData: keepPreviousData,
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
  /** docker 资源限额（FR-079）：传值（含 0=清除限制）覆盖，不传则不变。变更对下次启动生效。 */
  cpuLimit?: number
  memLimitMb?: number
  diskLimitMb?: number
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
    onSuccess: (_data, id) => {
      toast.success('实例已删除')
      qc.invalidateQueries({ queryKey: ['instances'] })
      // 从侧栏「最近打开/收藏」剔除，避免残留死链（BUG 修复）。
      removeServer(id)
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
