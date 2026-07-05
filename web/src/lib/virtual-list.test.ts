import { describe, expect, it } from 'vitest'
import { virtualWindow } from './virtual-list'

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
