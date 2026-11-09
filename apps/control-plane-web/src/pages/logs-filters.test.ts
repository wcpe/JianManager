import { describe, it, expect } from 'vitest'
import {
  logLevelStatus,
  timeRangeToParams,
  computeVirtualWindow,
  TIME_RANGE_PRESETS,
  LOG_VIEWS,
} from '@/lib/logs/logs-filters'

describe('logLevelStatus', () => {
  it('maps error to danger and warn to warning (unified with alerts semantics)', () => {
    expect(logLevelStatus('error')).toBe('danger')
    expect(logLevelStatus('warn')).toBe('warning')
  })

  it('maps info to info and debug to neutral', () => {
    expect(logLevelStatus('info')).toBe('info')
    expect(logLevelStatus('debug')).toBe('neutral')
  })

  it('falls back to neutral for unknown levels', () => {
    expect(logLevelStatus('trace')).toBe('neutral')
    expect(logLevelStatus('')).toBe('neutral')
  })
})

describe('LOG_VIEWS', () => {
  it('defines the primary views plus the administrator-only full search view', () => {
    // legacy 已于 2026-10-29 随 CP logs 表下线并从取值域移除：留着它会让 ?view=legacy
    // 深链落进一个不存在的数据源（不报错也不给数据，静默失效）。
    expect(LOG_VIEWS).toEqual(['platform', 'node_instance', 'all'])
  })
})

describe('timeRangeToParams', () => {
  // 固定锚点，避免依赖真实时钟。
  const now = new Date('2026-06-26T12:00:00.000Z')

  it('returns no time bounds for the "all" preset', () => {
    expect(timeRangeToParams('all', now)).toEqual({})
  })

  it('computes a from offset for relative presets, with to = now', () => {
    const p1h = timeRangeToParams('1h', now)
    expect(p1h.to).toBe(now.toISOString())
    expect(p1h.from).toBe(new Date('2026-06-26T11:00:00.000Z').toISOString())

    const p24h = timeRangeToParams('24h', now)
    expect(p24h.from).toBe(new Date('2026-06-25T12:00:00.000Z').toISOString())

    const p7d = timeRangeToParams('7d', now)
    expect(p7d.from).toBe(new Date('2026-06-19T12:00:00.000Z').toISOString())

    const p15m = timeRangeToParams('15m', now)
    expect(p15m.from).toBe(new Date('2026-06-26T11:45:00.000Z').toISOString())
  })

  it('exposes every preset in TIME_RANGE_PRESETS including all', () => {
    expect(TIME_RANGE_PRESETS).toContain('all')
    expect(TIME_RANGE_PRESETS).toContain('15m')
    expect(TIME_RANGE_PRESETS).toContain('7d')
  })
})

describe('computeVirtualWindow', () => {
  it('returns the full set when total fits and no scroll', () => {
    const w = computeVirtualWindow({
      scrollTop: 0,
      viewportHeight: 400,
      rowHeight: 40,
      total: 5,
      overscan: 4,
    })
    expect(w.startIndex).toBe(0)
    expect(w.endIndex).toBe(5)
    expect(w.padTop).toBe(0)
    expect(w.padBottom).toBe(0)
  })

  it('windows a long list and applies overscan on both sides', () => {
    // 1000 行 × 40px = 40000px；视口 400px 显示 ~10 行。
    const w = computeVirtualWindow({
      scrollTop: 4000, // 第 100 行起
      viewportHeight: 400,
      rowHeight: 40,
      total: 1000,
      overscan: 5,
    })
    // 起点 100 - overscan 5 = 95。
    expect(w.startIndex).toBe(95)
    // 终点：100 + ceil(400/40)=10 行 + overscan 5 = 115。
    expect(w.endIndex).toBe(115)
    expect(w.padTop).toBe(95 * 40)
    expect(w.padBottom).toBe((1000 - 115) * 40)
  })

  it('clamps the start at 0 and the end at total', () => {
    const top = computeVirtualWindow({
      scrollTop: 40, // 第 1 行
      viewportHeight: 400,
      rowHeight: 40,
      total: 1000,
      overscan: 10,
    })
    expect(top.startIndex).toBe(0)
    expect(top.padTop).toBe(0)

    const bottom = computeVirtualWindow({
      scrollTop: 999 * 40,
      viewportHeight: 400,
      rowHeight: 40,
      total: 1000,
      overscan: 10,
    })
    expect(bottom.endIndex).toBe(1000)
    expect(bottom.padBottom).toBe(0)
  })

  it('returns an empty window for an empty list', () => {
    const w = computeVirtualWindow({
      scrollTop: 0,
      viewportHeight: 400,
      rowHeight: 40,
      total: 0,
      overscan: 4,
    })
    expect(w.startIndex).toBe(0)
    expect(w.endIndex).toBe(0)
    expect(w.padTop).toBe(0)
    expect(w.padBottom).toBe(0)
  })

  it('guards against a zero/negative rowHeight (avoids div-by-zero)', () => {
    const w = computeVirtualWindow({
      scrollTop: 100,
      viewportHeight: 400,
      rowHeight: 0,
      total: 100,
      overscan: 4,
    })
    expect(w.startIndex).toBe(0)
    expect(w.endIndex).toBe(100)
  })
})
