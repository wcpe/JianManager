import { describe, it, expect } from 'vitest'
import { healthBreakdown, type HealthBreakdownSegment } from './bot-health'

describe('healthBreakdown', () => {
  it('total<=0 返回空数组（渲染空轨道）', () => {
    expect(healthBreakdown(0, {})).toEqual([])
    expect(healthBreakdown(-1, { connected: 5 })).toEqual([])
  })

  it('按 byStatus 多段拆分：connected/connecting/error/stopped 各自成段，占比正确', () => {
    const segs: HealthBreakdownSegment[] = healthBreakdown(10, {
      connected: 5,
      connecting: 2,
      error: 1,
      stopped: 2,
    })
    const byKind = Object.fromEntries(segs.map((s) => [s.kind, s]))
    expect(byKind.connected.count).toBe(5)
    expect(byKind.connected.ratio).toBeCloseTo(0.5, 5)
    expect(byKind.connecting.count).toBe(2)
    expect(byKind.error.count).toBe(1)
    expect(byKind.stopped.count).toBe(2)
    // 段顺序固定：connected → connecting → error → stopped
    expect(segs.map((s) => s.kind)).toEqual(['connected', 'connecting', 'error', 'stopped'])
  })

  it('计数缺失的状态不产生空段', () => {
    const segs = healthBreakdown(5, { connected: 5 })
    expect(segs).toHaveLength(1)
    expect(segs[0].kind).toBe('connected')
    expect(segs[0].ratio).toBeCloseTo(1, 5)
  })

  it('pending 归入 connecting（连接生命周期前段），error+pending 合并计数', () => {
    const segs = healthBreakdown(4, { connecting: 1, pending: 1, error: 2 })
    const byKind = Object.fromEntries(segs.map((s) => [s.kind, s]))
    expect(byKind.connecting.count).toBe(2) // connecting + pending
    expect(byKind.error.count).toBe(2)
  })

  it('已知状态计数之和不足 total 时，余量归入 stopped（兜底未知/已停止）', () => {
    // total=10，已知 connected=3，其余 7 视作已停止/未知
    const segs = healthBreakdown(10, { connected: 3 })
    const byKind = Object.fromEntries(segs.map((s) => [s.kind, s]))
    expect(byKind.connected.count).toBe(3)
    expect(byKind.stopped.count).toBe(7)
  })

  it('计数之和超过 total（数据竞态）时按 total 截断，不溢出 100%', () => {
    const segs = healthBreakdown(2, { connected: 5 })
    const total = segs.reduce((s, x) => s + x.count, 0)
    expect(total).toBeLessThanOrEqual(2)
    const ratioSum = segs.reduce((s, x) => s + x.ratio, 0)
    expect(ratioSum).toBeLessThanOrEqual(1.000001)
  })
})
