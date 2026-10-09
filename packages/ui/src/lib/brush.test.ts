import { describe, it, expect } from 'vitest'
import { brushSelectionToWindow, isFullWindow, filterRowsByWindow } from './brush'

const TS = ['2026-06-26T00:00:00Z', '2026-06-26T01:00:00Z', '2026-06-26T02:00:00Z', '2026-06-26T03:00:00Z']

describe('brushSelectionToWindow', () => {
  it('映射下标对为时间窗', () => {
    expect(brushSelectionToWindow(TS, 1, 2)).toEqual({ from: TS[1], to: TS[2] })
  })
  it('start>end 自动交换（反向拖手柄）', () => {
    expect(brushSelectionToWindow(TS, 3, 0)).toEqual({ from: TS[0], to: TS[3] })
  })
  it('越界下标夹到边界', () => {
    expect(brushSelectionToWindow(TS, -5, 99)).toEqual({ from: TS[0], to: TS[3] })
  })
  it('undefined 下标按默认兜底（start→0 / end→末尾）', () => {
    expect(brushSelectionToWindow(TS, undefined, undefined)).toEqual({ from: TS[0], to: TS[3] })
    expect(brushSelectionToWindow(TS, 2, undefined)).toEqual({ from: TS[2], to: TS[3] })
  })
  it('空数组返回 null', () => {
    expect(brushSelectionToWindow([], 0, 1)).toBeNull()
  })
  it('单点数组退化为该点闭区间', () => {
    expect(brushSelectionToWindow([TS[0]], 0, 0)).toEqual({ from: TS[0], to: TS[0] })
  })
})

describe('isFullWindow', () => {
  it('覆盖首末视为全段', () => {
    expect(isFullWindow(TS, { from: TS[0], to: TS[3] })).toBe(true)
  })
  it('部分窗非全段', () => {
    expect(isFullWindow(TS, { from: TS[1], to: TS[2] })).toBe(false)
  })
  it('null 窗 / 空数据视为全段', () => {
    expect(isFullWindow(TS, null)).toBe(true)
    expect(isFullWindow([], { from: 'x', to: 'y' })).toBe(true)
  })
})

describe('filterRowsByWindow', () => {
  const rows = TS.map((ts, i) => ({ ts, v: i }))
  it('闭区间含端点过滤', () => {
    expect(filterRowsByWindow(rows, { from: TS[1], to: TS[2] }).map((r) => r.v)).toEqual([1, 2])
  })
  it('null 窗原样返回', () => {
    expect(filterRowsByWindow(rows, null)).toHaveLength(4)
  })
})
