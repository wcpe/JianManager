import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen } from '@testing-library/react'
import type { ComponentProps } from 'react'

vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))

import { toast } from 'sonner'
import { ConsoleLineBuffer } from '@/lib/console-line-buffer'
import type { LogLine } from '@/lib/console-log-line'
import { renderWithProviders } from '@/test/render'
import ConsoleOutputView from './ConsoleOutputView'

/**
 * 锚点选区与堆栈块折叠（FR-418，spec §3）。
 *
 * 本文件的核心命题是 ADR-086 代价 2：**屏幕外的行没有 DOM 节点**，所以跨行选区的文本
 * 必须来自行缓冲。因此用例刻意构造「选区两端在 DOM 里都不存在」的局面再断言复制结果完整
 * ——若实现偷懒读了 `window.getSelection()`，这些用例必然拿到残缺文本而失败。
 *
 * jsdom 不做布局：`getBoundingClientRect` / `clientHeight` / `scrollHeight` 全为 0，
 * 故显式打桩成可控值，让「屏幕坐标 → 行 seq」的几何换算可预测（行高 22px：14 × 1.6）。
 */

const ROW_HEIGHT = 22
const VIEWPORT = 640

/** 把容器打桩成「有布局、可滚动」：视口 640px 从 y=0 起，内容 total 行。 */
function stubGeometry(el: HTMLElement, totalRows: number) {
  let top = 0
  Object.defineProperty(el, 'scrollHeight', { configurable: true, get: () => totalRows * ROW_HEIGHT })
  Object.defineProperty(el, 'clientHeight', { configurable: true, get: () => VIEWPORT })
  Object.defineProperty(el, 'scrollTop', {
    configurable: true,
    get: () => top,
    set: (value: number) => {
      top = value
    },
  })
  el.getBoundingClientRect = () =>
    ({ top: 0, bottom: VIEWPORT, left: 0, right: 1200, width: 1200, height: VIEWPORT, x: 0, y: 0 }) as DOMRect
}

/** 造 n 条真实解析过的行（走行缓冲，避免手捏与生产不一致的假对象）。 */
function makeLines(n: number): readonly LogLine[] {
  const buffer = new ConsoleLineBuffer()
  for (let i = 0; i < n; i++) buffer.appendChunk(`line-${i}\n`, 'stdout')
  return buffer.snapshot()
}

/** clientY → 该点落在第几行（与组件内的几何换算同一套公式）。 */
const yOfRow = (index: number) => index * ROW_HEIGHT + 2

const output = () => screen.getByTestId('console-output')
const renderedSeqs = () =>
  Array.from(document.querySelectorAll('[data-console-line-seq]')).map((el) =>
    Number(el.getAttribute('data-console-line-seq')),
  )
const copiedText = () => vi.mocked(navigator.clipboard.writeText).mock.calls.at(-1)?.[0] ?? ''

/** 渲染 n 行并完成几何打桩。 */
function renderConsole(lines: readonly LogLine[], props: Partial<ComponentProps<typeof ConsoleOutputView>> = {}) {
  renderWithProviders(<ConsoleOutputView lines={lines} droppedCount={0} fontSize={14} {...props} />)
  stubGeometry(output(), lines.length)
}

beforeEach(() => {
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: { writeText: vi.fn(async () => undefined) },
  })
  vi.mocked(toast.success).mockClear()
  vi.mocked(toast.error).mockClear()
})

afterEach(() => {
  vi.useRealTimers()
})

describe('锚点选区：跨行区间（spec §3.1）', () => {
  it('跨行拖选建立 seq 区间并浮出工具条', () => {
    renderConsole(makeLines(1000))

    fireEvent.mouseDown(output(), { clientY: yOfRow(2), button: 0 })
    fireEvent.mouseMove(window, { clientY: yOfRow(9) })

    expect(output()).toHaveAttribute('data-console-selection', '2-9')
    expect(screen.getByTestId('console-selection-toolbar')).toHaveTextContent('已选 8 行')
    fireEvent.mouseUp(window)
    // 松手后区间不消失——工具条要留给用户去点复制。
    expect(screen.getByTestId('console-selection-toolbar')).toBeInTheDocument()
  })

  it('复制的文本来自行缓冲：区间跨到屏幕外仍完整无缺行', async () => {
    vi.useFakeTimers()
    renderConsole(makeLines(1000))
    const el = output()

    // 从第 2 行按下、跨到第 9 行进入锚点模式，再把指针压到底边触发自动滚动。
    fireEvent.mouseDown(el, { clientY: yOfRow(2), button: 0 })
    fireEvent.mouseMove(window, { clientY: yOfRow(9) })
    fireEvent.mouseMove(window, { clientY: VIEWPORT - 10 })
    // 滚 500 tick：选区终点随滚动持续外扩（指针一直不动）。
    act(() => vi.advanceTimersByTime(500 * 16))
    fireEvent.mouseUp(window)

    const [start, end] = el.getAttribute('data-console-selection')!.split('-').map(Number)
    expect(start).toBe(2)
    // 自动滚动确实把终点带出了初始视口（初始只渲染到 ~40 行）。
    expect(end).toBeGreaterThan(300)

    // 关键前提：区间起点此刻**不在 DOM 里**。若实现读 DOM/原生选区，下面必然缺行。
    const onScreen = renderedSeqs()
    expect(onScreen).not.toContain(2)
    expect(onScreen).not.toContain(100)

    fireEvent.click(screen.getByRole('button', { name: '复制' }))
    await vi.waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalled())

    const parts = copiedText().split('\n')
    expect(parts).toHaveLength(end - start + 1)
    // 逐行核对，而不是只抽查两头——「缺行」正是这条 FR 要消灭的失败模式。
    parts.forEach((part, index) => expect(part).toBe(`line-${start + index}`))
    await vi.waitFor(() => expect(toast.success).toHaveBeenCalledWith('已复制'))
  })

  it('自动滚动在松手后可靠停止，不留失控定时器', () => {
    vi.useFakeTimers()
    renderConsole(makeLines(1000))
    const el = output()

    fireEvent.mouseDown(el, { clientY: yOfRow(2), button: 0 })
    fireEvent.mouseMove(window, { clientY: yOfRow(9) })
    fireEvent.mouseMove(window, { clientY: VIEWPORT - 10 })
    act(() => vi.advanceTimersByTime(160))
    const scrolledTo = el.scrollTop
    expect(scrolledTo).toBeGreaterThan(0)

    fireEvent.mouseUp(window)
    act(() => vi.advanceTimersByTime(5000))

    expect(el.scrollTop).toBe(scrolledTo)
  })

  it('指针回到中间区域即停止自动滚动（不必松手）', () => {
    vi.useFakeTimers()
    renderConsole(makeLines(1000))
    const el = output()

    fireEvent.mouseDown(el, { clientY: yOfRow(2), button: 0 })
    fireEvent.mouseMove(window, { clientY: yOfRow(9) })
    fireEvent.mouseMove(window, { clientY: VIEWPORT - 10 })
    act(() => vi.advanceTimersByTime(160))
    fireEvent.mouseMove(window, { clientY: VIEWPORT / 2 })
    const scrolledTo = el.scrollTop

    act(() => vi.advanceTimersByTime(5000))
    expect(el.scrollTop).toBe(scrolledTo)
    fireEvent.mouseUp(window)
  })

  it('同一行内按下并移动不进入锚点模式（留给原生字符级选区）', () => {
    renderConsole(makeLines(1000))

    fireEvent.mouseDown(output(), { clientY: yOfRow(2), button: 0 })
    // 在同一行内横向/微幅移动：仍是第 2 行。
    fireEvent.mouseMove(window, { clientY: yOfRow(2) + 6 })
    fireEvent.mouseUp(window)

    expect(output()).not.toHaveAttribute('data-console-selection')
    expect(screen.queryByTestId('console-selection-toolbar')).not.toBeInTheDocument()
    // 也不给容器挂 select-none：那会把行内选取一起废掉。
    expect(output().className).not.toContain('select-none')
  })

  it('Shift+Click 从上次落点扩选', () => {
    renderConsole(makeLines(1000))

    fireEvent.mouseDown(output(), { clientY: yOfRow(2), button: 0 })
    fireEvent.mouseUp(window)
    fireEvent.mouseDown(output(), { clientY: yOfRow(9), button: 0, shiftKey: true })

    expect(output()).toHaveAttribute('data-console-selection', '2-9')
    expect(screen.getByTestId('console-selection-toolbar')).toHaveTextContent('已选 8 行')
  })

  it('Shift+Click 在已有区间上只挪终点、锚点不动', () => {
    renderConsole(makeLines(1000))

    fireEvent.mouseDown(output(), { clientY: yOfRow(10), button: 0 })
    fireEvent.mouseMove(window, { clientY: yOfRow(20) })
    fireEvent.mouseUp(window)
    // 反向 Shift+Click 到锚点之上：区间应变成 [4, 10]。
    fireEvent.mouseDown(output(), { clientY: yOfRow(4), button: 0, shiftKey: true })

    expect(output()).toHaveAttribute('data-console-selection', '4-10')
  })

  it('Ctrl+A 全选整个缓冲（不是 DOM 里那几十行）', async () => {
    const lines = makeLines(1000)
    renderConsole(lines)

    expect(renderedSeqs().length).toBeLessThan(60)
    fireEvent.keyDown(output(), { key: 'a', ctrlKey: true })

    expect(output()).toHaveAttribute('data-console-selection', '0-999')
    expect(screen.getByTestId('console-selection-toolbar')).toHaveTextContent('已选 1000 行')

    fireEvent.click(screen.getByRole('button', { name: '复制' }))
    await vi.waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalled())
    const parts = copiedText().split('\n')
    expect(parts).toHaveLength(1000)
    expect(parts[0]).toBe('line-0')
    expect(parts.at(-1)).toBe('line-999')
  })

  it('Escape 取消选区；工具条的「取消选择」同效', () => {
    renderConsole(makeLines(100))

    fireEvent.keyDown(output(), { key: 'a', ctrlKey: true })
    fireEvent.keyDown(output(), { key: 'Escape' })
    expect(screen.queryByTestId('console-selection-toolbar')).not.toBeInTheDocument()

    fireEvent.keyDown(output(), { key: 'a', ctrlKey: true })
    fireEvent.click(screen.getByRole('button', { name: '取消选择' }))
    expect(screen.queryByTestId('console-selection-toolbar')).not.toBeInTheDocument()
  })

  it('复制失败（连 execCommand 兜底也失败）给出失败回执，不静默', async () => {
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined })
    Object.defineProperty(document, 'execCommand', { configurable: true, value: vi.fn(() => false) })
    renderConsole(makeLines(20))

    fireEvent.keyDown(output(), { key: 'a', ctrlKey: true })
    fireEvent.click(screen.getByRole('button', { name: '复制' }))

    await vi.waitFor(() => expect(toast.error).toHaveBeenCalledWith('复制失败'))
    expect(toast.success).not.toHaveBeenCalled()
  })

  it('「只看这段」把区间当临时过滤器，且可退出', () => {
    renderConsole(makeLines(1000))

    fireEvent.mouseDown(output(), { clientY: yOfRow(10), button: 0 })
    fireEvent.mouseMove(window, { clientY: yOfRow(14) })
    fireEvent.mouseUp(window)
    fireEvent.click(screen.getByRole('button', { name: '只看这段' }))

    expect(screen.getByTestId('console-range-filter')).toHaveTextContent('只看选中区间（5 行）')
    expect(renderedSeqs()).toEqual([10, 11, 12, 13, 14])

    fireEvent.click(screen.getByRole('button', { name: '退出区间' }))
    expect(screen.queryByTestId('console-range-filter')).not.toBeInTheDocument()
    expect(renderedSeqs()).toContain(0)
  })
})

describe('wrap（变高虚拟化）下的选区定位（评审 P1-2 回归）', () => {
  it('行高不均时屏幕坐标按 offsets 前缀和二分换算 seq，不按固定行高整除', () => {
    // 第 3 行 200 个字符：jsdom 下 crossW 回落 1024、charWidth 14×0.62=8.68，
    // 正文预算 1008−76=932px → 估 2 行 → 高 44px；其后各行整体下移 22px。
    const buffer = new ConsoleLineBuffer()
    for (let i = 0; i < 60; i++) {
      buffer.appendChunk(i === 3 ? `${'x'.repeat(200)}\n` : `line-${i}\n`, 'stdout')
    }
    renderConsole(buffer.snapshot(), { wrap: true })

    // offsets：行 3 起点 66、行 4 起点 110。clientY=100 落在 offsets 的第 3 行；
    // 若按固定行高整除（100/22=4）会错成第 4 行——两种算法在此分叉。
    fireEvent.mouseDown(output(), { clientY: 100, button: 0 })
    // clientY=120 落在 offsets 的第 4 行（110..132）；固定整除会错成第 5 行。
    fireEvent.mouseMove(window, { clientY: 120 })
    fireEvent.mouseUp(window)

    expect(output()).toHaveAttribute('data-console-selection', '3-4')
    expect(screen.getByTestId('console-selection-toolbar')).toHaveTextContent('已选 2 行')
  })
})

/** 普通行 + 完整 NPE 块（头 seq=1、帧 seq=2..6）+ 普通行。 */
const NPE = [
  '[09:12:13] [Server thread/ERROR]: java.lang.NullPointerException: boom',
  '\tat a.B.c(B.java:1)',
  '\tat d.E.f(E.java:2)',
  'Caused by: java.lang.IllegalStateException: root',
  '\tat g.H.i(H.java:3)',
  '\t... 3 more',
]

function makeStackLines(): readonly LogLine[] {
  const buffer = new ConsoleLineBuffer()
  buffer.appendChunk('[09:12:12] [Server thread/INFO]: Done (3.1s)!\n', 'stdout')
  buffer.appendChunk(`${NPE.join('\n')}\n`, 'stdout')
  buffer.appendChunk('[09:12:14] [Server thread/INFO]: after\n', 'stdout')
  return buffer.snapshot()
}

describe('堆栈块折叠（spec §3.2）', () => {
  it('默认折叠：只见异常首行 + 「堆栈 N 行」，帧不在 DOM 里', () => {
    renderConsole(makeStackLines())

    expect(renderedSeqs()).toEqual([0, 1, 7])
    expect(screen.getByText('堆栈 5 行')).toBeInTheDocument()
    expect(screen.getByText('java.lang.NullPointerException: boom')).toBeInTheDocument()
  })

  it('点开展开全部帧与 Caused by 链，再点回折叠', () => {
    renderConsole(makeStackLines())

    fireEvent.click(screen.getByRole('button', { name: '展开堆栈' }))
    expect(renderedSeqs()).toEqual([0, 1, 2, 3, 4, 5, 6, 7])
    expect(screen.getByText('Caused by: java.lang.IllegalStateException: root')).toBeInTheDocument()
    expect(screen.queryByText('堆栈 5 行')).not.toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: '折叠堆栈' }))
    expect(renderedSeqs()).toEqual([0, 1, 7])
  })

  it('「复制整块」拿到异常头 + 全部帧 + Caused by（折叠态下帧根本不在 DOM 里）', async () => {
    renderConsole(makeStackLines())

    expect(renderedSeqs()).not.toContain(2)
    fireEvent.click(screen.getByRole('button', { name: '复制整块' }))

    await vi.waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalledWith(NPE.join('\n')))
    await vi.waitFor(() => expect(toast.success).toHaveBeenCalledWith('已复制'))
  })

  it('双击异常头选中整块（含折叠着的帧）', () => {
    renderConsole(makeStackLines())

    fireEvent.doubleClick(document.querySelector('[data-console-line-seq="1"]')!)

    expect(output()).toHaveAttribute('data-console-selection', '1-6')
    expect(screen.getByTestId('console-selection-toolbar')).toHaveTextContent('已选 6 行')
  })

  it('选区圈住折叠的异常头即视为选中整块：头行高亮，复制含全部帧', async () => {
    renderConsole(makeStackLines())

    // 从第 0 行拖到异常头（第 1 行），帧仍是折叠的。
    fireEvent.mouseDown(output(), { clientY: yOfRow(0), button: 0 })
    fireEvent.mouseMove(window, { clientY: yOfRow(1) })
    fireEvent.mouseUp(window)

    expect(document.querySelector('[data-console-line-seq="1"]')).toHaveAttribute(
      'data-console-line-selected',
      'true',
    )
    fireEvent.click(screen.getByRole('button', { name: '复制' }))
    await vi.waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalled())
    expect(copiedText()).toBe(['[09:12:12] [Server thread/INFO]: Done (3.1s)!', ...NPE].join('\n'))
  })

  it('头行被环形缓冲丢弃的孤儿帧不折叠（不藏进看不见的抽屉）', () => {
    const orphan = makeStackLines().slice(2)
    renderConsole(orphan)

    expect(renderedSeqs()).toEqual([2, 3, 4, 5, 6, 7])
    expect(screen.queryByText(/堆栈 \d+ 行/)).not.toBeInTheDocument()
  })
})
