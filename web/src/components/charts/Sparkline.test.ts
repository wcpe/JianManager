import { describe, it, expect } from 'vitest'
import { sparkPath } from './Sparkline'

describe('sparkPath（FR-221 迷你趋势线）', () => {
  it('全空/空数组返回空 d', () => {
    expect(sparkPath([])).toBe('')
    expect(sparkPath([{ value: null }, { value: null }])).toBe('')
  })

  it('单点画在中点（x=50）', () => {
    const d = sparkPath([{ value: 5 }])
    expect(d.startsWith('M50.0')).toBe(true)
  })

  it('多点首段 M、后续 L，值越大越靠上（y 越小）', () => {
    // 升序值：第一个点 y 应大于最后一个点 y（顶部 y 小）。
    const d = sparkPath([{ value: 0 }, { value: 10 }])
    const seg = d.split(/(?=[ML])/).map((s) => s.trim()).filter(Boolean)
    expect(seg[0][0]).toBe('M')
    expect(seg[1][0]).toBe('L')
    const y0 = Number(seg[0].split(' ')[1])
    const y1 = Number(seg[1].split(' ')[1])
    expect(y0).toBeGreaterThan(y1)
  })

  it('缺测处断开成多段（出现两个 M）', () => {
    const d = sparkPath([{ value: 1 }, { value: null }, { value: 3 }])
    const ms = (d.match(/M/g) ?? []).length
    expect(ms).toBe(2)
  })
})
