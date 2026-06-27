import { useQuery } from '@tanstack/react-query'
import api from '@/api/client'

/**
 * 全局任务中心 API（FR-183，见 ADR-040）。
 * 长任务（如 JDK 安装）发起即返回 taskId；进度/日志/历史经轮询 `/tasks` 查看。
 * 后端按归属收敛：非平台管理员只见自己发起的任务，平台管理员见全部。
 */

/** 任务状态。pending/running 为进行中，succeeded/failed 为终态。 */
export type TaskState = 'pending' | 'running' | 'succeeded' | 'failed'

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
const TERMINAL_STATES: ReadonlySet<TaskState> = new Set<TaskState>(['succeeded', 'failed'])

/** 任务是否处于终态。 */
export function isTerminalTask(t: Pick<Task, 'state'>): boolean {
  return TERMINAL_STATES.has(t.state)
}

/**
 * 任务列表（FR-183）。
 * 存在进行中任务时短轮询（3s）刷新进度；全部终态时停止轮询，避免空转。
 */
export function useTasks(limit = 100) {
  return useQuery({
    queryKey: ['tasks', limit],
    queryFn: async () => {
      const { data } = await api.get<Task[]>('/tasks', { params: { limit } })
      return data
    },
    refetchInterval: (query) => {
      const tasks = query.state.data
      const hasActive = Array.isArray(tasks) && tasks.some((t) => !isTerminalTask(t))
      return hasActive ? 3000 : false
    },
  })
}

/** 单个任务详情（含日志）。进行中时短轮询（2s）。 */
export function useTask(taskId: string | undefined) {
  return useQuery({
    queryKey: ['task', taskId],
    queryFn: async () => {
      const { data } = await api.get<{ task: Task; logs: TaskLog[] }>(`/tasks/${taskId}`)
      return data
    },
    enabled: !!taskId,
    refetchInterval: (query) => {
      const task = query.state.data?.task
      return task && !isTerminalTask(task) ? 2000 : false
    },
  })
}
