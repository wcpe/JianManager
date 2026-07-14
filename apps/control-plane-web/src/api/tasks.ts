import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 全局任务中心 API（FR-183，见 ADR-040）。
 * 长任务（如 JDK 安装）发起即返回 taskId；进度/日志/历史经轮询 `/tasks` 查看。
 * 后端按归属收敛：非平台管理员只见自己发起的任务，平台管理员见全部。
 */

/** 任务状态。pending/running 为进行中，succeeded/failed/canceled 为终态（canceled=被强制停止，FR-227）。 */
export type TaskState = 'pending' | 'running' | 'succeeded' | 'failed' | 'canceled'

/** 一条长任务。 */
export interface Task {
  id: number
  taskId: string
  nodeId: number
  /** 任务种类，如 jdk_install。 */
  kind: string
  state: TaskState
  /** 0~100。 */
  progress: number
  title: string
  detail: string
  /** 失败原因（仅 failed）。 */
  error: string
  /** 成功结果 JSON（如安装出的 JDK 信息，仅 succeeded）。 */
  result: string
  /** 已请求强制停止但 Worker 尚未确认中断（在线 running 取消时为 true，显「取消中」，FR-227）。 */
  cancelRequested: boolean
  createdBy: number
  createdAt: string
  updatedAt: string
}

/** 任务的一行滚动日志。 */
export interface TaskLog {
  id: number
  taskId: string
  seq: number
  line: string
  ts: string
}

/** 终态集合，便于判断是否仍需轮询。 */
const TERMINAL_STATES: ReadonlySet<TaskState> = new Set<TaskState>(['succeeded', 'failed', 'canceled'])

/** 任务是否处于终态。 */
export function isTerminalTask(t: Pick<Task, 'state'>): boolean {
  return TERMINAL_STATES.has(t.state)
}

/** 活跃任务轮询间隔（FR-329）：存在非终态任务时 ~2s 自动刷新进度。 */
export const ACTIVE_TASKS_REFETCH_MS = 2000

/**
 * 任务轮询启停判定（FR-329）：任一任务非终态 → 2s 短轮询；全部终态 / 空 / 未加载 → 停。
 * 抽纯函数供任务列表与单任务详情共用，且轮询启停规则可独立单测。
 */
export function tasksRefetchInterval(tasks: readonly Pick<Task, 'state'>[] | undefined): number | false {
  const hasActive = Array.isArray(tasks) && tasks.some((t) => !isTerminalTask(t))
  return hasActive ? ACTIVE_TASKS_REFETCH_MS : false
}

/** 任务列表筛选（FR-227）。空字段不传。 */
export interface TaskListParams {
  limit?: number
  kind?: string
  state?: TaskState | ''
  nodeId?: number
  keyword?: string
  /** RFC3339 创建时间下界（FR-227 时间筛选）。 */
  since?: string
}

/**
 * 任务列表（FR-183 + FR-227 筛选 + FR-329 自动刷新）。
 * 存在非终态任务时短轮询（2s）刷新进度；全部终态时停止轮询，避免空转。
 * staleTime 置 0（覆盖全局 30s）：任务数据秒级演进，若命中「30s 内看过一眼」的新鲜缓存，
 * 挂载时不重取且缓存里无活跃任务→轮询也不启动，页面会卡死在旧快照（真机「要手动刷新才动」）。
 */
export function useTasks(params: TaskListParams = {}) {
  const query: Record<string, string | number> = { limit: params.limit ?? 100 }
  if (params.kind) query.kind = params.kind
  if (params.state) query.state = params.state
  if (params.nodeId) query.nodeId = params.nodeId
  if (params.keyword) query.keyword = params.keyword
  if (params.since) query.since = params.since
  return useQuery({
    queryKey: ['tasks', query],
    queryFn: async () => {
      const { data } = await api.get<Task[]>('/tasks', { params: query })
      return data
    },
    staleTime: 0,
    refetchInterval: (q) => tasksRefetchInterval(q.state.data),
  })
}

/** 强制停止任务（FR-227）。成功后失效任务列表（下次轮询拉到「取消中」/canceled）。 */
export function useCancelTask() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (taskId: string) => api.post(`/tasks/${taskId}/cancel`, {}),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['tasks'] }),
  })
}

/** 单个任务详情（含日志）。进行中时短轮询（2s，FR-329 与列表同款启停规则）；终态即停。 */
export function useTask(taskId: string | undefined) {
  return useQuery({
    queryKey: ['task', taskId],
    queryFn: async () => {
      const { data } = await api.get<{ task: Task; logs: TaskLog[] }>(`/tasks/${taskId}`)
      return data
    },
    enabled: !!taskId,
    staleTime: 0,
    refetchInterval: (query) => {
      const task = query.state.data?.task
      return task ? tasksRefetchInterval([task]) : false
    },
  })
}
