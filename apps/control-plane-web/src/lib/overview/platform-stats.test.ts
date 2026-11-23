import { describe, it, expect } from 'vitest'
import { tallyBy, summarizeProbeReachability } from './platform-stats'

describe('tallyBy', () => {
  it('空列表返回空分布', () => {
    expect(tallyBy([], (x: { k: string }) => x.k)).toEqual([])
  })

  it('按键分桶计数 + 占比，按 count 降序', () => {
    const list = [{ r: 'backend' }, { r: 'backend' }, { r: 'proxy' }, { r: 'universal' }]
    const d = tallyBy(list, (x) => x.r)
    expect(d).toEqual([
      { key: 'backend', count: 2, pct: 0.5 },
      { key: 'proxy', count: 1, pct: 0.25 },
      { key: 'universal', count: 1, pct: 0.25 },
    ])
    // 各桶占比之和为 1
    expect(d.reduce((s, b) => s + b.pct, 0)).toBeCloseTo(1)
  })

  it('空/未定义键归入 emptyLabel，不丢弃（各桶之和=总数）', () => {
    const list = [{ os: 'linux' }, { os: '' }, { os: undefined as unknown as string }, { os: null as unknown as string }]
    const d = tallyBy(list, (x) => x.os)
    const empty = d.find((b) => b.key === '—')
    expect(empty?.count).toBe(3)
    expect(d.reduce((s, b) => s + b.count, 0)).toBe(4)
  })

  it('计数相同按 key 字典序稳定排列', () => {
    const d = tallyBy([{ k: 'b' }, { k: 'a' }], (x) => x.k)
    expect(d.map((x) => x.key)).toEqual(['a', 'b'])
  })
})

describe('summarizeProbeReachability', () => {
  it('无后端时比例为 0', () => {
    expect(summarizeProbeReachability([])).toEqual({ available: 0, total: 0, pct: 0 })
  })

  it('按 available 计可达比例', () => {
    const r = summarizeProbeReachability([{ available: true }, { available: true }, { available: false }, { available: false }])
    expect(r).toEqual({ available: 2, total: 4, pct: 0.5 })
  })
})
