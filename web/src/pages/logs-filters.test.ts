import { describe, it, expect } from 'vitest'
import {
  logLevelStatus,
  timeRangeToParams,
  buildExportParams,
  computeVirtualWindow,
  TIME_RANGE_PRESETS,
  type LogExportScope,
} from './logs-filters'
import type { LogQueryParams } from '@/api/logs'

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

describe('buildExportParams', () => {
  const base: LogQueryParams = {
    level: 'error',
    keyword: 'boom',
    page: 3,
    pageSize: 50,
    from: '2026-06-26T11:00:00.000Z',
    to: '2026-06-26T12:00:00.000Z',
  }

  it('currentPage keeps the exact page/pageSize so only the visible page exports', () => {
    const out = buildExportParams(base, 'currentPage')
    expect(out.page).toBe(3)
    expect(out.pageSize).toBe(50)
    expect(out.level).toBe('error')
  })

  it('allMatched drops pagination so the whole filtered set exports', () => {
    const out = buildExportParams(base, 'allMatched')
    expect(out.page).toBeUndefined()
    expect(out.pageSize).toBeUndefined()
    // 但保留筛选条件（含时间）。
    expect(out.level).toBe('error')
    expect(out.from).toBe('2026-06-26T11:00:00.000Z')
    expect(out.to).toBe('2026-06-26T12:00:00.000Z')
  })

  it('range drops pagination but keeps the from/to window', () => {
    const out = buildExportParams(base, 'range')
    expect(out.page).toBeUndefined()
    expect(out.pageSize).toBeUndefined()
    expect(out.from).toBe('2026-06-26T11:00:00.000Z')
    expect(out.to).toBe('2026-06-26T12:00:00.000Z')
  })

  it('does not mutate the input params', () => {
    const snapshot = JSON.parse(JSON.stringify(base))
    buildExportParams(base, 'allMatched')
    expect(base).toEqual(snapshot)
  })

  it('treats an unknown scope like allMatched (safe default)', () => {
    const out = buildExportParams(base, 'bogus' as LogExportScope)
    expect(out.page).toBeUndefined()
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
