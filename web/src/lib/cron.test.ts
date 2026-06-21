import { describe, it, expect } from 'vitest'
import { validateCron } from './cron'

describe('validateCron', () => {
  it('接受标准 5 字段表达式', () => {
    expect(validateCron('0 4 * * *').valid).toBe(true)
    expect(validateCron('*/5 * * * *').valid).toBe(true)
    expect(validateCron('0 0 1,15 * 1-5').valid).toBe(true)
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
})
