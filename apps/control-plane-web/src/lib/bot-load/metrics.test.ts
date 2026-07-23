import { describe, it, expect } from 'vitest'
import { appendMetricPoints, clampChartPoints, formatRatio, formatLatencyMs } from './metrics'
import type { BotLoadMetricPoint } from './types'

function pt(ts: string, connected: number): BotLoadMetricPoint {
  return {
    timestamp: ts,
    stageIndex: 0,
    counts: { connected, planned: 100 },
    command: {},
    barrier: {},
    executor: [],
    latency: {
      connectP50Ms: null,
      connectP95Ms: null,
      connectP99Ms: null,
      scheduleLagP50Ms: null,
      scheduleLagP95Ms: null,
      scheduleLagP99Ms: null,
      barrierReleaseLagP50Ms: null,
      barrierReleaseLagP95Ms: null,
      barrierReleaseLagP99Ms: null,
    },
    errors: {},
  }
}

describe('metrics helpers', () => {
  it('appendMetricPoints 裁剪窗口', () => {
    const base = Array.from({ length: 5 }, (_, i) => pt(`t${i}`, i))
    const next = appendMetricPoints(base, pt('t5', 5), 3)
    expect(next).toHaveLength(3)
    expect(next[0]!.timestamp).toBe('t3')
  })

  it('clampChartPoints 均匀采样', () => {
    const items = Array.from({ length: 10 }, (_, i) => i)
    expect(clampChartPoints(items, 4)).toEqual([0, 3, 6, 9])
  })

  it('formatRatio / formatLatencyMs', () => {
    expect(formatRatio(0.901)).toBe('90.1%')
    expect(formatLatencyMs(null)).toBe('—')
    expect(formatLatencyMs(250)).toBe('250 ms')
    expect(formatLatencyMs(2500)).toBe('2.50 s')
  })
})
