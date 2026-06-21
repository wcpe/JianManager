import { describe, it, expect } from 'vitest'
import {
  EMPTY_SCHEDULE_FORM,
  formFromSchedule,
  toCreateBody,
  toUpdateBody,
  type ScheduleFormState,
} from './schedule-form'
import type { ScheduleInfo } from '@/api/schedules'

const sample: ScheduleInfo = {
  id: 7,
  uuid: 'uuid-7',
  instanceId: 3,
  name: '每晚重启',
  cronExpr: '0 4 * * *',
  action: 'restart',
  enabled: true,
  lastRun: null,
  createdAt: '2026-06-22T00:00:00Z',
}

describe('formFromSchedule', () => {
  it('用已有任务回填表单，command 留空（后端不返回 payload）', () => {
    const form = formFromSchedule(sample)
    expect(form).toEqual({
      instanceId: '3',
      name: '每晚重启',
      cronExpr: '0 4 * * *',
      action: 'restart',
      command: '',
      enabled: true,
    })
  })
})

describe('toCreateBody', () => {
  it('非 command 动作不携带 payload，cron 去空白', () => {
    const form: ScheduleFormState = {
      ...EMPTY_SCHEDULE_FORM,
      instanceId: '5',
      name: '备份',
      cronExpr: '  0 3 * * *  ',
      action: 'backup',
    }
    const body = toCreateBody(form)
    expect(body).toEqual({
      instanceId: 5,
      name: '备份',
      cronExpr: '0 3 * * *',
      action: 'backup',
      payload: undefined,
    })
  })

  it('command 动作携带命令文本作为 payload', () => {
    const form: ScheduleFormState = {
      ...EMPTY_SCHEDULE_FORM,
      instanceId: '5',
      name: '公告',
      cronExpr: '*/30 * * * *',
      action: 'command',
      command: 'say hello',
    }
    const body = toCreateBody(form)
    expect(body.action).toBe('command')
    expect(body.payload).toBe('say hello')
  })
})

describe('toUpdateBody', () => {
  it('仅产出 cronExpr/action/enabled 三字段（后端不可改实例/名称）', () => {
    const form: ScheduleFormState = {
      instanceId: '3',
      name: '原名',
      cronExpr: ' 0 5 * * * ',
      action: 'stop',
      command: 'ignored',
      enabled: false,
    }
    const body = toUpdateBody(form)
    expect(body).toEqual({ cronExpr: '0 5 * * *', action: 'stop', enabled: false })
    expect(Object.keys(body).sort()).toEqual(['action', 'cronExpr', 'enabled'])
  })
})
