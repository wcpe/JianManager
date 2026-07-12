import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import api from '@/api/client'

/** 定时任务（与后端 model.Schedule 对齐）。 */
export interface ScheduleInfo {
  id: number
  uuid: string
  instanceId: number
  name: string
  cronExpr: string
  /** 动作：start / stop / restart / command / backup。 */
  action: string
  /** action=command 时的命令文本（后端 model.Schedule.Payload，FR-153）。 */
  payload: string
  enabled: boolean
  lastRun: string | null
  createdAt: string
}

/** 定时任务执行日志（与后端 model.ScheduleExecutionLog 对齐）。 */
export interface ScheduleLogInfo {
  id: number
  scheduleId: number
  action: string
  /** 执行结果：success / failed。 */
  status: 'success' | 'failed'
  /** 失败时的错误信息（成功时为空串）。 */
  error: string
  startedAt: string
  finishedAt: string
}

/** 分页的执行日志响应（后端 GET /schedules/:id/logs）。 */
export interface ScheduleLogPage {
  items: ScheduleLogInfo[]
  total: number
  page: number
  pageSize: number
}

/** 创建定时任务请求体（与后端 CreateScheduleRequest 对齐）。 */
export interface CreateScheduleBody {
  instanceId: number
  name: string
  cronExpr: string
  action: string
  /** action=command 时携带的命令文本（后端存入 payload）。 */
  payload?: string
}

/** 更新定时任务请求体（后端按 PUT /schedules/:id 仅接收这三个可选字段）。 */
export interface UpdateScheduleBody {
  cronExpr?: string
  enabled?: boolean
  action?: string
  /** action=command 时携带的命令文本，使编辑可改命令（FR-153）。 */
  payload?: string
}

/** 获取定时任务列表（可按实例过滤）。 */
export function useSchedules(instanceId?: number) {
  return useQuery({
    queryKey: ['schedules', instanceId],
    queryFn: async () => {
      const { data } = await api.get<ScheduleInfo[]>('/schedules', {
        params: instanceId ? { instanceId } : undefined,
      })
      return data
    },
  })
}

/** 创建定时任务（POST /schedules）。 */
export function useCreateSchedule() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (body: CreateScheduleBody) => {
      const { data } = await api.post<ScheduleInfo>('/schedules', body)
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['schedules'] }),
  })
}

/** 更新定时任务（PUT /schedules/:id），用于改 cron/action 或启停切换。 */
export function useUpdateSchedule() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, body }: { id: number; body: UpdateScheduleBody }) => {
      const { data } = await api.put<ScheduleInfo>(`/schedules/${id}`, body)
      return data
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: ['schedules'] }),
  })
}

/** 删除定时任务（DELETE /schedules/:id）。 */
export function useDeleteSchedule() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => api.delete(`/schedules/${id}`),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['schedules'] }),
  })
}

/**
 * 获取指定定时任务的执行日志（GET /schedules/:id/logs，分页）。
 * `enabled` 控制仅在展开/选中某行时才发起请求，避免无谓拉取。
 */
export function useScheduleLogs(scheduleId: number | null, page = 1, pageSize = 20) {
  return useQuery({
    queryKey: ['scheduleLogs', scheduleId, page, pageSize],
    queryFn: async () => {
      const { data } = await api.get<ScheduleLogPage>(`/schedules/${scheduleId}/logs`, {
        params: { page, pageSize },
      })
      return data
    },
    enabled: scheduleId !== null,
  })
}
