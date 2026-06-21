import type { CreateScheduleBody, UpdateScheduleBody, ScheduleInfo } from '@/api/schedules'

/** 创建/编辑定时任务对话框的表单状态。 */
export interface ScheduleFormState {
  instanceId: string
  name: string
  cronExpr: string
  action: string
  command: string
  enabled: boolean
}

/** 可选的定时任务动作（与后端 action 枚举对齐）。 */
export const SCHEDULE_ACTIONS = ['start', 'stop', 'restart', 'command', 'backup'] as const

/** 新建对话框的初始空表单。 */
export const EMPTY_SCHEDULE_FORM: ScheduleFormState = {
  instanceId: '',
  name: '',
  cronExpr: '',
  action: 'restart',
  command: '',
  enabled: true,
}

/** 编辑时用已有任务回填表单（实例/名称只读，command 不回填——后端不返回 payload）。 */
export function formFromSchedule(s: ScheduleInfo): ScheduleFormState {
  return {
    instanceId: String(s.instanceId),
    name: s.name,
    cronExpr: s.cronExpr,
    action: s.action,
    command: '',
    enabled: s.enabled,
  }
}

/**
 * 由创建表单派生 POST /schedules 请求体。
 * 仅 action=command 时携带 payload；cronExpr 去除首尾空白。
 */
export function toCreateBody(form: ScheduleFormState): CreateScheduleBody {
  return {
    instanceId: Number(form.instanceId),
    name: form.name,
    cronExpr: form.cronExpr.trim(),
    action: form.action,
    payload: form.action === 'command' ? form.command : undefined,
  }
}

/**
 * 由编辑表单派生 PUT /schedules/:id 请求体。
 * 后端仅接收 cronExpr/action/enabled 三个字段（实例/名称/payload 不可改）。
 */
export function toUpdateBody(form: ScheduleFormState): UpdateScheduleBody {
  return {
    cronExpr: form.cronExpr.trim(),
    action: form.action,
    enabled: form.enabled,
  }
}
