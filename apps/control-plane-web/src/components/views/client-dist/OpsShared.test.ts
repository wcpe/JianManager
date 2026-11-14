import { describe, expect, it } from 'vitest'
import { logWindowParams, rangeForSpan, toApiRange, toStatsDays } from './OpsShared'

/**
 * 页面 B 时间窗换算（R30 回归）。
 *
 * 选择器（ObsWindow）产出的是分发观测端点自己的枚举 `24h/7d/30d/90d/180d`，
 * 而 `MetricRange` 只到 `1y`。以前调用方把 `180d` 强转 `MetricRange` 再进换算，
 * switch 落到 default 静默降成 `7d`——用户选「近 180 天」实际只查了 7 天。
 */
describe('toApiRange（预设档 → 观测端点 range）', () => {
  it.each([
    ['1h', '24h'],
    ['6h', '24h'],
    ['24h', '24h'],
    ['7d', '7d'],
    ['30d', '30d'],
    ['90d', '90d'],
    ['1y', '180d'],
    // 关键：页面选择器直接给的就是 180d，必须原样保留而不是回退 7d。
    ['180d', '180d'],
  ])('%s → %s', (input, expected) => {
    expect(toApiRange(input)).toBe(expected)
  })

  it('未知值保守回退 7d', () => {
    expect(toApiRange('')).toBe('7d')
    expect(toApiRange('bogus')).toBe('7d')
  })
})

describe('toStatsDays（预设档 → 日看板天数）', () => {
  it.each([
    ['1h', 1],
    ['6h', 1],
    ['24h', 1],
    ['7d', 7],
    ['30d', 30],
    ['90d', 90],
    ['1y', 180],
    ['180d', 180],
  ])('%s → %i 天', (input, expected) => {
    expect(toStatsDays(input)).toBe(expected)
  })
})

describe('rangeForSpan（自定义起止 → 最近预设档）', () => {
  const base = Date.parse('2026-01-01T00:00:00Z')
  const plus = (hours: number) => new Date(base + hours * 3_600_000).toISOString()

  it('跨度落到最近的档位', () => {
    expect(rangeForSpan(plus(0), plus(12))).toBe('24h')
    expect(rangeForSpan(plus(0), plus(72))).toBe('7d')
    expect(rangeForSpan(plus(0), plus(24 * 20))).toBe('30d')
    expect(rangeForSpan(plus(0), plus(24 * 60))).toBe('90d')
    expect(rangeForSpan(plus(0), plus(24 * 120))).toBe('1y')
  })

  it('缺失/非法/倒置的区间返回 null（调用方回退到当前档）', () => {
    expect(rangeForSpan(undefined, plus(1))).toBeNull()
    expect(rangeForSpan(plus(1), undefined)).toBeNull()
    expect(rangeForSpan('bogus', plus(1))).toBeNull()
    expect(rangeForSpan(plus(10), plus(1))).toBeNull()
  })
})

describe('logWindowParams（列表与导出共用同一时间窗，R31）', () => {
  it('预设档 → range 枚举（180d 不降级）', () => {
    expect(logWindowParams({ range: '7d' })).toEqual({ range: '7d' })
    expect(logWindowParams({ range: '180d' })).toEqual({ range: '180d' })
  })

  it('自定义区间原样透传 from/to，不近似成预设档', () => {
    const from = '2026-09-01T00:00:00.000Z'
    const to = '2026-09-03T12:34:56.000Z'
    expect(logWindowParams({ from, to })).toEqual({ from, to })
  })
})
