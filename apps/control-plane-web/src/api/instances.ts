import { keepPreviousData, queryOptions, useInfiniteQuery, useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import api from '@/api/client'
import { removeServer } from '@/components/console/server-selection'
import { apiErrorMessage } from '@/lib/api-error'
import type { InstanceBatchAction, InstanceBatchResult } from '@jianmanager/ui/lib/instance-batch'

/**
 * 实例域查询缓存保留时长（FR-297）：控制台来回切换（页签/跨服）时命中缓存先呈现旧数据、
 * 后台刷新，避免每次回切都白屏等待。默认 gcTime 5 分钟不够覆盖运营者巡检节奏，提至 15 分钟。
 */
export const INSTANCE_QUERY_GC_TIME_MS = 15 * 60_000

// 实例实体类型已迁至 `@jianmanager/ui`（ADR-097）；此处转出，调用点零改动。
import type { InstanceInfo } from '@jianmanager/ui'
export type { InstanceInfo } from '@jianmanager/ui'

/**
 * 实例是否处于「长操作在途」（FR-331，FR-323 扩展导入/克隆）：一键搭建 / 导入搬迁 / 克隆拷贝
 * 任务未终态期间，后端把实例 statusReason 固定标注为「搭建中：…/导入中：…/克隆中：…」
 * 且状态保持 STOPPED（FR-319 二轮③）。前端据此硬性禁用启动入口（与后端启动闸同一信号源）；
 * 任务终态后 reason 被清空/改写，自然解禁。
 */
export function isProvisioningInstance(inst: Pick<InstanceInfo, 'status' | 'statusReason'>): boolean {
  return inst.status === 'STOPPED' && /^(搭建中|导入中|克隆中)/.test(inst.statusReason ?? '')
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
  /**
   * 按实例 uuid 精确查。用于把 URL 里的实例 uuid（下钻深链 `?instance=<uuid>`）解析成数字 id，
   * 无需为此拉全量实例列表。
   */
  uuid?: string
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
  /**
   * 按进程类型（direct / daemon / docker / rcon）的计数，含全部枚举键零补。
   * 有了它，统计页不必为一张「进程类型分布」图而拉全量实例列表。
   */
  byProcessType: Record<string, number>
}

/**
 * 实例列表的兜底轮询间隔（FR-496 阶段 6 补丁）。
 *
 * 【为什么删掉了原先的「过渡态 2 秒轮询」】原实现是
 * `some(STARTING || STOPPING) ? 2000 : false`，设计意图是「只在实例启停过程中加速刷新」。
 * 它有两个致命问题：
 *   1. **与 SSE 重复**。`api/events.ts` 的 `useInstanceEvents` 已经在推送 `state_change`
 *      并 `invalidateQueries(['instances'])`，其注释写明定位就是「替代轮询方案」。
 *      两套机制同时存在，等于每次状态变化都要走一遍推送 + 一遍轮询。
 *   2. **条件可能永久为真**。真实环境里实例卡在 STARTING（启动超时、端口占用、OOM）
 *      是常见故障，而非瞬时态；一旦如此，这些查询就退化为**永不停止的 2 秒轮询**。
 *      实测（devmock 把 2/5 状态随机设为过渡态且不收敛）：静止无操作时 6 秒内仍产生
 *      37 个请求、132 次 DOM 变更，每 2 秒触发约 440ms 的 React 渲染，页面持续发烫。
 *
 * 现在只留一个很长的兜底：SSE 是长连接，经代理/网关时仍可能被静默切断，
 * 兜底保证「推送万一失效，状态最终仍会收敛」。30 秒一次的固定开销可忽略，
 * 与原先动辄 2 秒一轮完全不是一个量级。
 */
const INSTANCE_FALLBACK_POLL_MS = 30_000

/**
 * 获取实例列表（状态变化由 SSE 推送，兜底见 INSTANCE_FALLBACK_POLL_MS）。
 *
 * `enabled` 用于抑制「只在特定视图下才需要」的调用：例如监控页仅在下钻到实例层时才需要
 * 把 URL 里的实例 uuid 解析成 id，平台/节点层不该为这件事付千级全量列表的代价。
 */
export function useInstances(params?: InstanceListParams, enabled = true) {
  return useQuery({
    queryKey: ['instances', params],
    enabled,
    queryFn: async () => {
      const { data } = await api.get<InstanceInfo[]>('/instances', { params })
      return data
    },
    refetchInterval: INSTANCE_FALLBACK_POLL_MS,
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
    refetchInterval: INSTANCE_FALLBACK_POLL_MS,
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
    refetchInterval: INSTANCE_FALLBACK_POLL_MS,
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

/**
 * 获取实例详情（FR-297 回切先呈现缓存后台刷新）。
 * 状态刷新同样走 SSE：`useInstanceEvents` 的 `invalidateQueries(['instances'])`
 * 前缀匹配会命中本 query 的 `['instances', id]`，故这里也只留兜底间隔。
 */
export function useInstance(id: number) {
  return useQuery({
    ...instanceQueryOptions(id),
    enabled: !!id,
    placeholderData: keepPreviousData,
    refetchInterval: INSTANCE_FALLBACK_POLL_MS,
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

/**
 * 接管运行态漂移（FR-471）：把实例工作目录下未被平台纳管的活进程接管进平台生命周期。
 * 后端语义为「先停止该外在进程，再以受管方式重新拉起」——成功后实例真正进入平台管理，
 * 面板状态与磁盘进程一一对应（此前可能显示 STOPPED 而磁盘在跑）。
 * 失败信息由后端定向给出（如进程无法停止），原样透传给后端 message。
 */
export async function adoptInstanceRuntime(id: number): Promise<{ message?: string }> {
  const { data } = await api.post<{ message?: string }>(`/instances/${id}/adopt-runtime`)
  return data
}

/**
 * 接管运行态漂移的写操作 hook：成功后 toast 并失效实例列表/详情缓存，
 * 失败把后端定向 message（如「节点未连接」）原样透出。
 */
export function useAdoptInstanceRuntime() {
  const qc = useQueryClient()
  const { t } = useTranslation()
  return useMutation({
    mutationFn: (id: number) => adoptInstanceRuntime(id),
    onSuccess: (_data, id) => {
      toast.success(t('serverConsole.runtimeDriftAdoptDone'))
      // 接管会改状态与漂移字段：列表与详情缓存一并失效（详情 key 为 ['instances', id]）。
      void qc.invalidateQueries({ queryKey: ['instances'] })
      void qc.invalidateQueries({ queryKey: ['instances', id] })
    },
    onError: (err: unknown) => {
      toast.error(apiErrorMessage(err, t('serverConsole.runtimeDriftAdoptFailed')))
    },
  })
}

/** 重建损毁实例（FR-342）：复用已存搭建参数重跑搭建，无需重填；起后台任务，进度见任务中心。 */
export function useRebuildInstance() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.post(`/instances/${id}/rebuild`),
    onSuccess: () => {
      toast.success('实例重建中…')
      qc.invalidateQueries({ queryKey: ['instances'] })
      qc.invalidateQueries({ queryKey: ['tasks'] })
    },
    onError: (err: Error & { response?: { data?: { message?: string } } }) => {
      toast.error(err.response?.data?.message || '重建失败')
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
  /** 自定义启动环境变量（FR-344）：传对象（含空对象=清空）覆盖，不传则不变。变更对下次启动生效。 */
  envVars?: Record<string, string>
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

/** 实例环境（FR-344 环境变量页签）：configured=自定义启动 env（可编辑源）、runtime=运行时进程实际环境。 */
export interface InstanceEnvData {
  configured: Record<string, string>
  runtime: Record<string, string> | null
  runtimeAvailable: boolean
  note: string
}

/** 查询实例环境（FR-344）：configured + 运行时实际环境；15s 轮询（运行时段随进程变化）。 */
export function useInstanceEnv(instanceId: number, enabled = true) {
  return useQuery({
    queryKey: ['instance-env', instanceId],
    queryFn: () => api.get<InstanceEnvData>(`/instances/${instanceId}/env`).then((r) => r.data),
    enabled: enabled && instanceId > 0,
    refetchInterval: 15000,
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

/**
 * 实例批量操作动作与结果计数（FR-058）：契约归包，受控视图与 API 层共用（ADR-097）。
 * 本地绑定来自文件顶部的 import，此处仅对外再导出，避免两处各写一份。
 */
export type { InstanceBatchAction, InstanceBatchResult } from '@jianmanager/ui/lib/instance-batch'

/** 批量操作筛选条件（与列表筛选维度一致）。 */
export interface InstanceBatchFilter {
  nodeId?: number
  status?: string
  role?: string
  /** 显式实例集合（可选）：按选中实例走 filter 语义，使滚动编排的灰度抽样（ratio）生效（FR-457）。 */
  instanceIds?: number[]
}

/** 批量操作请求，目标由 ids 或 filter 二选一指定。 */
export interface InstanceBatchRequest {
  action: InstanceBatchAction
  ids?: number[]
  filter?: InstanceBatchFilter
  /** action=command 时下发的命令。 */
  command?: string
}

// 批量操作结果计数 `InstanceBatchResult` 的定义已随受控视图归包（见上方 re-export）。

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
