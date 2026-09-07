import { describe, expect, it } from 'vitest'

import { ConsoleLineBuffer } from './console-line-buffer'
import type { LogLine } from './console-log-line'
import {
  AUTO_SCROLL_EDGE,
  AUTO_SCROLL_MAX_STEP,
  edgeScrollStep,
  extendSeqRange,
  fullSeqRange,
  isLineSelected,
  linesInRange,
  selectionText,
  seqRangeBounds,
  seqRangeContains,
} from './console-selection'

/**
 * 锚点选区内核（FR-418，spec §3.1）。
 *
 * 这些用例全部**不碰 DOM**——这正是本 FR 的要点：虚拟列表只渲染可视行，选区文本必须
 * 从行缓冲按 seq 取，取材逻辑因此可以（也必须）在没有任何渲染的环境里被验证。
 */

/** 造 n 条真实解析过的行（走行缓冲，避免手捏与生产不一致的假对象）。 */
function makeLines(n: number, prefix = 'line'): readonly LogLine[] {
  const buffer = new ConsoleLineBuffer()
  for (let i = 0; i < n; i++) buffer.appendChunk(`${prefix}-${i}\n`, 'stdout')
  return buffer.snapshot()
}

describe('seq 区间语义', () => {
  it('反向拖选（自下向上）规范化为升序闭区间，锚点方向仍保留', () => {
    expect(seqRangeBounds({ anchor: 90, head: 12 })).toEqual({ start: 12, end: 90 })
    expect(seqRangeBounds({ anchor: 12, head: 90 })).toEqual({ start: 12, end: 90 })
  })

  it('区间是闭区间：两端都算选中', () => {
    const range = { anchor: 5, head: 9 }
    expect(seqRangeContains(range, 5)).toBe(true)
    expect(seqRangeContains(range, 9)).toBe(true)
    expect(seqRangeContains(range, 4)).toBe(false)
    expect(seqRangeContains(range, 10)).toBe(false)
    expect(seqRangeContains(null, 5)).toBe(false)
  })

  it('扩选只挪终点、锚点不动（Shift+Click 与键盘扩选共用）', () => {
    const first = extendSeqRange({ anchor: 30, head: 40 }, 12)
    expect(first).toEqual({ anchor: 30, head: 12 })
    // 反向再扩回去，锚点仍是 30。
    expect(extendSeqRange(first, 77)).toEqual({ anchor: 30, head: 77 })
  })

  it('无区间时扩选退化为单行（首次 Shift+Click 没有锚点可用）', () => {
    expect(extendSeqRange(null, 8)).toEqual({ anchor: 8, head: 8 })
  })

  it('全选缓冲取首尾 seq；空缓冲不造空区间', () => {
    const lines = makeLines(1000)
    expect(fullSeqRange(lines)).toEqual({ anchor: 0, head: 999 })
    expect(fullSeqRange([])).toBeNull()
  })
})

describe('区间取文本（不依赖 DOM，spec §3.1 核心）', () => {
  it('跨 500+ 行区间逐行取 raw，行数与内容都完整无缺', () => {
    const lines = makeLines(1000)
    // 视口最多也就渲染几十行，这里的 700 行区间里绝大多数从未有过 DOM 节点。
    const text = selectionText(lines, { anchor: 200, head: 899 })

    const parts = text.split('\n')
    expect(parts).toHaveLength(700)
    expect(parts[0]).toBe('line-200')
    expect(parts.at(-1)).toBe('line-899')
    // 中间不缺行：逐条核对比只抽查两头更能钉死「缺行」这个失败模式。
    parts.forEach((part, index) => expect(part).toBe(`line-${200 + index}`))
  })

  it('反向拖选（从下往上）取到的文本仍按时间正序', () => {
    const lines = makeLines(50)
    expect(selectionText(lines, { anchor: 20, head: 15 })).toBe(
      ['line-15', 'line-16', 'line-17', 'line-18', 'line-19', 'line-20'].join('\n'),
    )
  })

  it('取的是 raw 而非拆列后的 body：原始前缀一字不改', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('[09:12:13] [Server thread/WARN]: [MyPlugin] Can not keep up\n', 'stdout')
    const lines = buffer.snapshot()

    expect(selectionText(lines, { anchor: 0, head: 0 })).toBe(
      '[09:12:13] [Server thread/WARN]: [MyPlugin] Can not keep up',
    )
  })

  it('空区间取空串，不抛错', () => {
    expect(selectionText(makeLines(3), null)).toBe('')
    expect(linesInRange(makeLines(3), null)).toEqual([])
  })

  it('堆栈帧跟随其异常头：只选中头行也能拿到整块（含 Caused by 链）', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('before\n', 'stdout')
    buffer.appendChunk(
      [
        'java.lang.NullPointerException: boom',
        '\tat a.B.c(B.java:1)',
        '\tat d.E.f(E.java:2)',
        'Caused by: java.lang.IllegalStateException: root',
        '\tat g.H.i(H.java:3)',
        '\t... 3 more',
        '',
      ].join('\n'),
      'stdout',
    )
    buffer.appendChunk('after\n', 'stdout')
    const lines = buffer.snapshot()

    // 异常头 seq=1；区间只圈住它，帧（2..6）在区间之外。
    const text = selectionText(lines, { anchor: 1, head: 1 })
    expect(text.split('\n')).toEqual([
      'java.lang.NullPointerException: boom',
      '\tat a.B.c(B.java:1)',
      '\tat d.E.f(E.java:2)',
      'Caused by: java.lang.IllegalStateException: root',
      '\tat g.H.i(H.java:3)',
      '\t... 3 more',
    ])
    // 相邻的普通行不被顺带捎走。
    expect(text).not.toContain('before')
    expect(text).not.toContain('after')
  })

  it('isLineSelected 对帧行按头行判定（渲染高亮与取文本用同一条规则）', () => {
    const buffer = new ConsoleLineBuffer()
    buffer.appendChunk('java.lang.NullPointerException: boom\n\tat a.B.c(B.java:1)\n', 'stdout')
    const [head, frame] = buffer.snapshot()

    expect(isLineSelected({ anchor: 0, head: 0 }, head)).toBe(true)
    expect(isLineSelected({ anchor: 0, head: 0 }, frame)).toBe(true)
    // 头不在区间内时，帧也不该被选中。
    expect(isLineSelected({ anchor: 5, head: 9 }, frame)).toBe(false)
  })
})

describe('边缘自动滚动步长', () => {
  const bounds = { top: 100, bottom: 700 }

  it('指针在中间区域返回 0（据此停掉定时器，而不是空转 tick）', () => {
    expect(edgeScrollStep(400, bounds)).toBe(0)
    expect(edgeScrollStep(100 + AUTO_SCROLL_EDGE, bounds)).toBe(0)
    expect(edgeScrollStep(700 - AUTO_SCROLL_EDGE, bounds)).toBe(0)
  })

  it('靠上边缘为负（上滚）、靠下边缘为正（下滚）', () => {
    expect(edgeScrollStep(110, bounds)).toBeLessThan(0)
    expect(edgeScrollStep(690, bounds)).toBeGreaterThan(0)
  })

  it('越贴边越快，且不超过上限', () => {
    const near = edgeScrollStep(699, bounds)
    const far = edgeScrollStep(675, bounds)
    expect(near).toBeGreaterThan(far)
    expect(near).toBeLessThanOrEqual(AUTO_SCROLL_MAX_STEP)
  })

  it('拖到容器之外仍按满速滚，不外推出更离谱的步长', () => {
    expect(edgeScrollStep(-5000, bounds)).toBe(-AUTO_SCROLL_MAX_STEP)
    expect(edgeScrollStep(5000, bounds)).toBe(AUTO_SCROLL_MAX_STEP)
  })

  it('哪怕只差 1px 也至少滚 1px：否则贴边时会「卡住不动」', () => {
    expect(Math.abs(edgeScrollStep(700 - AUTO_SCROLL_EDGE + 1, bounds))).toBeGreaterThanOrEqual(1)
  })
})
