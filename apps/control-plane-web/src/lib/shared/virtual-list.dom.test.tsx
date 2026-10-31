import { describe, expect, it } from 'vitest'
import { fireEvent, render, waitFor } from '@testing-library/react'
import { useVirtualRows } from './virtual-list'

/**
 * 滚动路径的行为契约（useVirtualRows.onScroll）。
 *
 * 背景：原先每个 scroll 事件都 `setMetrics({...})`——每次都是新对象引用，React 无法 bailout，
 * 于是一帧内派发 N 次 scroll 就要重算窗口 + 重渲染整列表 N 次；scrollTop 没变时也一样白渲染。
 * 现在改为 rAF 合并 + 同值短路。本文件锁死这两条：
 * ① 一帧内的多次滚动只产生一次渲染，且窗口直接落到**最后一个**位置（中间值不渲染）；
 * ② scrollTop 未变化时一次渲染都不多发。
 */

const ITEM_SIZE = 20
const VIEWPORT = 100

/** 把容器打桩成可滚动（jsdom 无布局，高度不参与计算，只给可控的 scrollTop）。 */
function stubScrollable(el: HTMLElement, initialTop = 0) {
  let top = initialTop
  Object.defineProperty(el, 'scrollTop', {
    configurable: true,
    get: () => top,
    set: (value: number) => {
      top = value
    },
  })
}

/** 每次渲染都把当前窗口起点记下来——数组内容即「渲染序列」。 */
function Harness({ starts }: { starts: number[] }) {
  const { containerRef, onScroll, range } = useVirtualRows({
    total: 1000,
    itemSize: ITEM_SIZE,
    overscan: 0,
    fallbackViewportSize: VIEWPORT,
  })
  starts.push(range.start)
  return <div ref={containerRef} onScroll={onScroll} data-testid="viewport" />
}

/** 等一帧 rAF + 一次 React 微任务刷新，让滚动合并的结果落定。 */
const settle = () => new Promise((resolve) => setTimeout(resolve, 50))

describe('useVirtualRows 滚动路径', () => {
  it('同一帧内的多次滚动合并为一次渲染，且直接落到最后一个位置', async () => {
    const starts: number[] = []
    const { getByTestId } = render(<Harness starts={starts} />)
    const el = getByTestId('viewport')
    stubScrollable(el)
    // 先热一次：挂载期 measure 的 transition 会占一条车道，第一次真正渲染时可能一并结清
    // （同一状态下多渲染一次），那是挂载期的账，不属于滚动路径。
    el.scrollTop = 5 * ITEM_SIZE
    fireEvent.scroll(el)
    await waitFor(() => expect(starts.at(-1)).toBe(5))
    await settle()

    // 一帧内连滚三次（触控板惯性 / 程序化赋值叠加用户滚动都会这样）。
    const before = starts.length
    el.scrollTop = 10 * ITEM_SIZE
    fireEvent.scroll(el)
    el.scrollTop = 20 * ITEM_SIZE
    fireEvent.scroll(el)
    el.scrollTop = 30 * ITEM_SIZE
    fireEvent.scroll(el)

    await waitFor(() => expect(starts.at(-1)).toBe(30))
    await settle()

    // 合并后只渲染一次，且是最终位置：10/20 这两个中间位置从未进入渲染序列。
    expect(starts.slice(before)).toEqual([30])

    // 再来一次，确认稳定态下「一帧 N 次滚动 = 一次渲染」不是偶然。
    const afterFirstBurst = starts.length
    el.scrollTop = 40 * ITEM_SIZE
    fireEvent.scroll(el)
    el.scrollTop = 50 * ITEM_SIZE
    fireEvent.scroll(el)
    await waitFor(() => expect(starts.at(-1)).toBe(50))
    await settle()

    expect(starts.slice(afterFirstBurst)).toEqual([50])
  })

  it('scrollTop 未变化时不产生多余渲染（同值短路）', async () => {
    const starts: number[] = []
    const { getByTestId } = render(<Harness starts={starts} />)
    const el = getByTestId('viewport')
    stubScrollable(el)
    await settle()

    // 先真实滚一次，让 metrics.scrollOffset 与 DOM 一致。
    el.scrollTop = 5 * ITEM_SIZE
    fireEvent.scroll(el)
    await waitFor(() => expect(starts.at(-1)).toBe(5))
    await settle()
    const rendersAfterMove = starts.length

    // 浏览器在「到头/到底、内容没变、程序化赋值落回原位」等场景会派发 scroll 而 scrollTop 不变。
    fireEvent.scroll(el)
    fireEvent.scroll(el)
    await settle()

    expect(starts.length).toBe(rendersAfterMove)
  })
})
