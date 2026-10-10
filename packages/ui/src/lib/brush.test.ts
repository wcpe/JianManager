import { describe, it, expect } from 'vitest'
import { brushSelectionToWindow, isFullWindow, filterRowsByWindow } from './brush'

// 四个时刻各自具名：断言里按名字引用，既不必数下标，也避免「取到空槽位」在类型上失守。
const T0 = '2026-06-26T00:00:00Z'
const T1 = '2026-06-26T01:00:00Z'
const T2 = '2026-06-26T02:00:00Z'
const T3 = '2026-06-26T03:00:00Z'

/** 升序时间戳数组（与曲线数据同序），供被测函数按下标对换算时间窗。 */
const TS = [T0, T1, T2, T3]

describe('brushSelectionToWindow', () => {
  it('映射下标对为时间窗', () => {
    expect(brushSelectionToWindow(TS, 1, 2)).toEqual({ from: T1, to: T2 })
  })
  it('start>end 自动交换（反向拖手柄）', () => {
    expect(brushSelectionToWindow(TS, 3, 0)).toEqual({ from: T0, to: T3 })
  })
  it('越界下标夹到边界', () => {
    expect(brushSelectionToWindow(TS, -5, 99)).toEqual({ from: T0, to: T3 })
  })
  it('undefined 下标按默认兜底（start→0 / end→末尾）', () => {
    expect(brushSelectionToWindow(TS, undefined, undefined)).toEqual({ from: T0, to: T3 })
    expect(brushSelectionToWindow(TS, 2, undefined)).toEqual({ from: T2, to: T3 })
  })
  it('空数组返回 null', () => {
    expect(brushSelectionToWindow([], 0, 1)).toBeNull()
  })
  it('单点数组退化为该点闭区间', () => {
    expect(brushSelectionToWindow([T0], 0, 0)).toEqual({ from: T0, to: T0 })
  })
})

describe('isFullWindow', () => {
  it('覆盖首末视为全段', () => {
    expect(isFullWindow(TS, { from: T0, to: T3 })).toBe(true)
  })
  it('部分窗非全段', () => {
    expect(isFullWindow(TS, { from: T1, to: T2 })).toBe(false)
  })
  it('null 窗 / 空数据视为全段', () => {
    expect(isFullWindow(TS, null)).toBe(true)
    expect(isFullWindow([], { from: 'x', to: 'y' })).toBe(true)
  })
})

describe('filterRowsByWindow', () => {
  const rows = TS.map((ts, i) => ({ ts, v: i }))
  it('闭区间含端点过滤', () => {
    expect(filterRowsByWindow(rows, { from: T1, to: T2 }).map((r) => r.v)).toEqual([1, 2])
  })
  it('null 窗原样返回', () => {
    expect(filterRowsByWindow(rows, null)).toHaveLength(4)
  })
})
