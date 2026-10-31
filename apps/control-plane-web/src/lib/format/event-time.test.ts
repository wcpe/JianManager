import { describe, expect, it } from 'vitest'
import { formatEventDateTime, formatEventTime } from '@/lib/format/event-time'

describe('formatEventTime', () => {
  /** 一个固定的时间点：2023-11-14T22:13:20Z。 */
  const SECONDS = 1_700_000_000
  const MILLIS = SECONDS * 1000

  it('秒与毫秒得到同一个结果（这才是容错的全部意义）', () => {
    expect(formatEventTime(SECONDS)).toBe(formatEventTime(MILLIS))
  })

  it('毫秒值不会被再乘 1000（否则显示成公元 5 万年）', () => {
    // 直接与「正确毫秒」的格式化结果比对：若误乘 1000，这里会不等。
    expect(formatEventTime(MILLIS)).toBe(new Date(MILLIS).toLocaleTimeString())
    // 再补一条年份上界，防止将来有人把阈值改小导致毫秒被当秒。
    expect(new Date(MILLIS).getFullYear()).toBeLessThan(2100)
  })

  it('非法输入回退占位而不是 Invalid Date', () => {
    expect(formatEventTime(Number.NaN)).toBe('--')
    expect(formatEventTime(0)).toBe('--')
    expect(formatEventTime(-1)).toBe('--')
    expect(formatEventTime(Number.POSITIVE_INFINITY)).toBe('--')
  })

  it('边界：刚过阈值按毫秒解释', () => {
    const justAbove = 10_000_000_001
    expect(formatEventTime(justAbove)).toBe(new Date(justAbove).toLocaleTimeString())
  })

  it('边界：阈值以下按秒解释', () => {
    const justBelow = 9_999_999_999
    expect(formatEventTime(justBelow)).toBe(new Date(justBelow * 1000).toLocaleTimeString())
  })
})

describe('formatEventDateTime', () => {
  it('秒与毫秒同样归一', () => {
    expect(formatEventDateTime(1_700_000_000)).toBe(formatEventDateTime(1_700_000_000_000))
  })

  it('非法输入回退占位', () => {
    expect(formatEventDateTime(Number.NaN)).toBe('--')
    expect(formatEventDateTime(0)).toBe('--')
  })
})
