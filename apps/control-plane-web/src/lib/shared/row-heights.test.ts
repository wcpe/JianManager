import { describe, expect, it } from 'vitest'
import { mergeMeasuredRowHeights, ROW_HEIGHT_EPSILON_PX } from './row-heights'

/**
 * 实测行高回填的合并规则。
 *
 * 组件级用例覆盖不到它：jsdom 无布局，`offsetHeight` 恒 0，走不到「差异达到阈值」那一步。
 * 而这条规则正是「折叠行不再用常量估算、按实测收敛」的核心，故单测锁死。
 */
describe('mergeMeasuredRowHeights 实测行高合并', () => {
  it('首次量到的行直接入库', () => {
    const { sizes, changed } = mergeMeasuredRowHeights(new Map(), [{ id: 'a', px: 56.7 }])
    expect(changed).toBe(true)
    expect(sizes.get('a')).toBe(56.7)
  })

  it('差异小于阈值不动（避免子像素抖动引发「实测→重渲染→再实测」死循环）', () => {
    const base = new Map([['a', 56.7]])
    const { sizes, changed } = mergeMeasuredRowHeights(base, [
      { id: 'a', px: 56.7 + ROW_HEIGHT_EPSILON_PX / 2 },
    ])
    expect(changed).toBe(false)
    expect(sizes.get('a')).toBe(56.7)
  })

  it('差异达到阈值即覆盖', () => {
    const base = new Map([['a', 43]])
    const { sizes, changed } = mergeMeasuredRowHeights(base, [{ id: 'a', px: 56.7 }])
    expect(changed).toBe(true)
    expect(sizes.get('a')).toBe(56.7)
  })

  it('无布局环境（px ≤ 0）一律忽略，保留估算', () => {
    const base = new Map([['a', 43]])
    const { sizes, changed } = mergeMeasuredRowHeights(base, [
      { id: 'a', px: 0 },
      { id: 'b', px: 0 },
    ])
    expect(changed).toBe(false)
    expect(sizes.size).toBe(1)
  })

  it('skipId（展开行自有一套实测通道）不重复记账', () => {
    const { sizes, changed } = mergeMeasuredRowHeights(new Map(), [{ id: 'open', px: 260 }], 'open')
    expect(changed).toBe(false)
    expect(sizes.has('open')).toBe(false)
  })

  it('不修改入参表（React 状态不可变）', () => {
    const base = new Map([['a', 43]])
    mergeMeasuredRowHeights(base, [{ id: 'b', px: 60 }])
    expect(base.size).toBe(1)
  })

  it('空 id 忽略（防止把量不到标识的节点写成一行）', () => {
    const { sizes, changed } = mergeMeasuredRowHeights(new Map(), [{ id: '', px: 60 }])
    expect(changed).toBe(false)
    expect(sizes.size).toBe(0)
  })
})
