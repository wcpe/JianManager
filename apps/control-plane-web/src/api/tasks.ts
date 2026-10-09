import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 全局任务中心 API（FR-183，见 ADR-040）。
 * 长任务（如 JDK 安装）发起即返回 taskId；进度/日志/历史经轮询 `/tasks` 查看。
 * 后端按归属收敛：非平台管理员只见自己发起的任务，平台管理员见全部。
 */

/** 任务状态。pending/running 为进行中，succeeded/failed/canceled 为终态（canceled=被强制停止，FR-227）。 */
// 类型与纯逻辑（终态判定、kind 文案键、轮询启停）已回迁应用侧（原 ADR-097 迁包已撤销）；
// 此处转出，调用点零改动。本模块只保留取数 hook 与请求参数类型。
import { tasksRefetchInterval } from '@/lib/task-status'
import { TaskState, Task, TaskLog, TaskPage } from '@/lib/task-status'

export { isTerminalTask, tasksRefetchInterval, TASK_KIND_LABEL_KEYS, ACTIVE_TASKS_REFETCH_MS } from '@/lib/task-status'
export { TaskState, Task, TaskLog, TaskPage } from '@/lib/task-status'

/** 任务列表筛选（FR-227）+ 分页窗口（FR-337）。空字段不传。 */
export interface TaskListParams {
  /** 每窗行数，服务端缺省 100、钳制 [1,500]（FR-337）。 */
  limit?: number
  /** 偏移，缺省 0（FR-337；Web 端增长窗口恒 0，供 API 消费方翻页）。 */
  offset?: number
  kind?: string
  state?: TaskState | ''
  nodeId?: number
  keyword?: string
  /** RFC3339 创建时间下界（FR-227 时间筛选）。 */
  since?: string
}

/**
 * 任务列表（FR-183 + FR-227 筛选 + FR-329 自动刷新 + FR-337 分页信封）。
 * 返回 `{items,total,limit,offset}` 信封；「加载更多」= 扩大 limit 的增长窗口（offset 恒 0），
 * 轮询天然重取整个已加载窗口，进度/新任务/total 同步刷新。
 * 存在非终态任务时短轮询（2s）刷新进度；全部终态时停止轮询，避免空转。
 * staleTime 置 0（覆盖全局 30s）：任务数据秒级演进，若命中「30s 内看过一眼」的新鲜缓存，
 * 挂载时不重取且缓存里无活跃任务→轮询也不启动，页面会卡死在旧快照（真机「要手动刷新才动」）。
 */
export function useTasks(params: TaskListParams = {}) {
  const query: Record<string, string | number> = { limit: params.limit ?? 100 }
  if (params.offset) query.offset = params.offset
  if (params.kind) query.kind = params.kind
  if (params.state) query.state = params.state
  if (params.nodeId) query.nodeId = params.nodeId
  if (params.keyword) query.keyword = params.keyword
  if (params.since) query.since = params.since
  return useQuery({
    queryKey: ['tasks', query],
    queryFn: async () => {
      const { data } = await api.get<TaskPage>('/tasks', { params: query })
      return data
    },
    staleTime: 0,
    // 「加载更多」扩窗/改筛选时保留旧结果避免列表闪烁（同 useAuditLogs 既有范式）。
    placeholderData: (prev) => prev,
    refetchInterval: (q) => tasksRefetchInterval(q.state.data?.items),
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
