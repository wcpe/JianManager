import { describe, expect, it } from 'vitest'
import { virtualWindow, virtualWindowVaried } from './virtual-list'

describe('virtualWindow', () => {
  it('calculates visible rows with overscan and spacer heights', () => {
    const win = virtualWindow({
      total: 1200,
      itemSize: 36,
      viewportSize: 360,
      scrollOffset: 360,
      overscan: 2,
    })

    expect(win.start).toBe(8)
    expect(win.end).toBe(22)
    expect(win.before).toBe(288)
    expect(win.after).toBe((1200 - 22) * 36)
  })

  it('clamps negative scroll and empty lists', () => {
    expect(virtualWindow({ total: 0, itemSize: 36, viewportSize: 360, scrollOffset: 0 })).toEqual({
      start: 0,
      end: 0,
      before: 0,
      after: 0,
    })

    expect(virtualWindow({ total: 5, itemSize: 36, viewportSize: 360, scrollOffset: -100 }).start).toBe(0)
  })
})

describe('virtualWindowVaried 变高虚拟窗口', () => {
  // 行高 [10, 30, 10] → offsets [0, 10, 40, 50]
  const offsets = [0, 10, 40, 50]

  it('定位到包含滚动偏移的行，压线行算可见', () => {
    // offset 35 落在行 1（10..40）内；视口底 55 越过行 2 结束（50）→ 行 2 可见
    const win = virtualWindowVaried({ total: 3, offsets, viewportSize: 20, scrollOffset: 35, overscan: 0 })
    expect(win.start).toBe(1)
    expect(win.end).toBe(3)
    expect(win.before).toBe(10)
    expect(win.after).toBe(0)
  })

  it('overscan 向两侧扩并钳制边界', () => {
    const win = virtualWindowVaried({ total: 3, offsets, viewportSize: 20, scrollOffset: 0, overscan: 2 })
    expect(win.start).toBe(0)
    expect(win.end).toBe(3)
    expect(win.before).toBe(0)
    expect(win.after).toBe(0)
  })

  it('大偏移时 after 精确反映尾部剩余高度', () => {
    const win = virtualWindowVaried({ total: 3, offsets, viewportSize: 5, scrollOffset: 0, overscan: 0 })
    // 视口 0..5 只见行 0（0..10 压线）→ end=1，after = 50-10 = 40
    expect(win.end).toBe(1)
    expect(win.after).toBe(40)
  })

  it('offsets 不足或空列表时安全归零', () => {
    expect(virtualWindowVaried({ total: 3, offsets: [0, 10], viewportSize: 20, scrollOffset: 0 })).toEqual({
      start: 0,
      end: 0,
      before: 0,
      after: 0,
    })
    expect(virtualWindowVaried({ total: 0, offsets: [0], viewportSize: 20, scrollOffset: 0 })).toEqual({
      start: 0,
      end: 0,
      before: 0,
      after: 0,
    })
  })
})
