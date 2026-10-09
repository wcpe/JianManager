import { describe, it, expect } from 'vitest'
import { formatRelativeTime } from './relative-time'

const NOW = Date.parse('2026-06-28T12:00:00Z')

describe('formatRelativeTime', () => {
  it('空/非法入参返回空串', () => {
    expect(formatRelativeTime(null)).toBe('')
    expect(formatRelativeTime(undefined)).toBe('')
    expect(formatRelativeTime('')).toBe('')
    expect(formatRelativeTime('not-a-date', { now: NOW })).toBe('')
  })

  it('小于一分钟为「刚刚」', () => {
    expect(formatRelativeTime('2026-06-28T11:59:30Z', { now: NOW })).toBe('刚刚')
    expect(formatRelativeTime('2026-06-28T12:00:00Z', { now: NOW })).toBe('刚刚')
  })

  it('未来时间（时钟偏差）按「刚刚」', () => {
    expect(formatRelativeTime('2026-06-28T12:05:00Z', { now: NOW })).toBe('刚刚')
  })

  it('分钟级', () => {
    expect(formatRelativeTime('2026-06-28T11:55:00Z', { now: NOW })).toBe('5 分钟前')
    expect(formatRelativeTime('2026-06-28T11:01:00Z', { now: NOW })).toBe('59 分钟前')
  })

  it('小时级', () => {
    expect(formatRelativeTime('2026-06-28T09:00:00Z', { now: NOW })).toBe('3 小时前')
  })

  it('天级', () => {
    expect(formatRelativeTime('2026-06-26T12:00:00Z', { now: NOW })).toBe('2 天前')
  })

  it('超过七天回落具体日期', () => {
    const out = formatRelativeTime('2026-06-10T12:00:00Z', { now: NOW })
    expect(out).not.toBe('')
    expect(out).not.toMatch(/前$/)
    expect(out).not.toBe('刚刚')
  })

  it('接受毫秒时间戳', () => {
    expect(formatRelativeTime(NOW - 2 * 60 * 1000, { now: NOW })).toBe('2 分钟前')
  })
})
