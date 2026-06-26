import { describe, it, expect } from 'vitest'
import { validateCron, nextRuns, describeCron } from './cron'

describe('validateCron', () => {
  it('接受标准 5 字段表达式', () => {
    expect(validateCron('0 4 * * *').valid).toBe(true)
    expect(validateCron('*/5 * * * *').valid).toBe(true)
    expect(validateCron('0 0 1,15 * 1-5').valid).toBe(true)
    expect(validateCron('0 0 1-5/2 * *').valid).toBe(true)
  })

  it('接受含秒的 6 字段表达式', () => {
    expect(validateCron('0 0 4 * * *').valid).toBe(true)
  })

  it('容忍多余空白', () => {
    expect(validateCron('  0   4 * * *  ').valid).toBe(true)
  })

  it('空串判为不合法并提示必填', () => {
    const r = validateCron('   ')
    expect(r.valid).toBe(false)
    expect(r.messageKey).toBe('schedules.cronRequired')
  })

  it('字段数不为 5/6 判为不合法', () => {
    const r = validateCron('0 4 * *')
    expect(r.valid).toBe(false)
    expect(r.messageKey).toBe('schedules.cronFieldCount')
  })

  it('字段含非法字符判为不合法', () => {
    const r = validateCron('0 4 * * MON')
    expect(r.valid).toBe(false)
    expect(r.messageKey).toBe('schedules.cronInvalidChar')
  })

  // BUG-020：残缺/空 term 此前因「仅校验允许字符」漏过，现按 term 结构逐个拒绝。
  it('残缺或空 term 判为不合法（BUG-020）', () => {
    expect(validateCron(',,, 4 * * *').valid).toBe(false)
    expect(validateCron('5,,6 4 * * *').valid).toBe(false)
    expect(validateCron('*/ * * * *').valid).toBe(false)
    expect(validateCron('1- 4 * * *').valid).toBe(false)
    expect(validateCron('- 4 * * *').valid).toBe(false)
  })
})

describe('nextRuns', () => {
  it('每天 04:00 给出后续日期，时分为 04:00', () => {
    const runs = nextRuns('0 4 * * *', 3, new Date('2026-01-01T00:00:00'))
    expect(runs).toHaveLength(3)
    for (const r of runs) {
      expect(r.getHours()).toBe(4)
      expect(r.getMinutes()).toBe(0)
    }
    // 相邻两次相隔一天
    expect(runs[1].getDate() - runs[0].getDate()).toBe(1)
  })

  it('每 15 分钟给出递增分钟点', () => {
    const runs = nextRuns('*/15 * * * *', 4, new Date('2026-01-01T10:00:00'))
    expect(runs.map((r) => r.getMinutes())).toEqual([15, 30, 45, 0])
  })

  it('非法或含秒（6 字段）表达式返回空数组', () => {
    expect(nextRuns('bad expr', 3)).toEqual([])
    expect(nextRuns('0 0 4 * * *', 3)).toEqual([])
  })
})

describe('describeCron', () => {
  it('识别每 N 分钟 / 每小时第 M 分 / 每天 HH:MM', () => {
    expect(describeCron('*/5 * * * *')).toEqual({ key: 'schedules.descEveryNMin', params: { n: 5 } })
    expect(describeCron('30 * * * *')).toEqual({ key: 'schedules.descHourlyAt', params: { m: 30 } })
    expect(describeCron('0 4 * * *')).toEqual({ key: 'schedules.descDailyAt', params: { time: '04:00' } })
  })

  it('无法识别的复杂表达式或非法表达式返回 null', () => {
    expect(describeCron('0 4 * * 1')).toBeNull()
    expect(describeCron('bad')).toBeNull()
  })
})
