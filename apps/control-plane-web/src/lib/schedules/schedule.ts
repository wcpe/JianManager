/**
 * 定时任务的核心契约（FR-153）。
 *
 * 归包理由：实例控制台的「备份 · 定时」分区与调度页都要读它，应用侧 API 层改从包 import，
 * 避免两处各写一份。
 */

/** 一条定时任务。 */
export interface ScheduleInfo {
  id: number
  uuid: string
  instanceId: number
  /**
   * 实例展示名（后端已回填）。有了它，调度列表不必为了显示名字而拉全量实例列表。
   * 实例被删时缺省，前端回退显示 #id。
   */
  instanceName?: string
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
