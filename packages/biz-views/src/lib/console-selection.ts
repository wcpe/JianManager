import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import type { LogLine } from './console-log-line'

/**
 * 锚点选区内核（FR-418，spec §3.1）。
 *
 * 存在的理由是 ADR-086 代价 2：输出区改成虚拟列表后**只有可视行有 DOM 节点**，
 * 原生跨行选区一旦拖出视口，屏幕外那些行既没有节点、也进不了 `window.getSelection()`
 * ——松手复制到的是「缺了中间一大段」的文本。所以跨行选区不再依赖 DOM：
 * 只记 `seq` 区间，文本落定时**从行缓冲按 seq 取 `raw`**。
 *
 * 本模块**故意不含任何 DOM/组件知识**（除了两个用 ref/state 的通用 hook）：
 * FR-421 的键盘复制模式要复用同一套区间语义——`begin`/`extendTo`/`selectAll`/`setRange`
 * 对应键盘的「起点 / 移动 / 全选 / 选块」，`selectionText` 则是 `yank`。
 * 鼠标与键盘只是两种「怎么算出 seq」的方式，区间与取文本的规则必须只有一份。
 */

/**
 * seq 锚点区间（闭区间，端点可逆序）。
 *
 * 保留 anchor/head 的方向而不直接存 min/max：`Shift+Click` 与键盘扩选要求
 * 「锚点不动、只挪终点」，方向丢了就无法反向缩回。
 */
export interface SeqRange {
  /** 起点（按下处 / 键盘标记处）。 */
  anchor: number
  /** 当前终点。 */
  head: number
}

/** 规范化为升序闭区间 `[start, end]`。 */
export function seqRangeBounds(range: SeqRange): { start: number; end: number } {
  return range.anchor <= range.head
    ? { start: range.anchor, end: range.head }
    : { start: range.head, end: range.anchor }
}

/** seq 是否落在区间内。 */
export function seqRangeContains(range: SeqRange | null | undefined, seq: number): boolean {
  if (!range) return false
  const { start, end } = seqRangeBounds(range)
  return seq >= start && seq <= end
}

/**
 * 行是否属于选区。
 *
 * 除了自身落在区间内，**堆栈帧还跟随其异常头行**（`stackOf`）：折叠状态下用户只能拖到头行，
 * 若不跟随，复制一个 NPE 块就只拿到「java.lang.NullPointerException」一行、帧全丢——
 * 与 spec §3.2「整块作为一个单位」冲突。这条规则与级别过滤的接缝
 * （{@link import('./console-stack-block').filterWithStackBlocks}）保持一致。
 */
export function isLineSelected(range: SeqRange | null | undefined, line: LogLine): boolean {
  if (!range) return false
  if (seqRangeContains(range, line.seq)) return true
  return line.stackOf !== undefined && seqRangeContains(range, line.stackOf)
}

/**
 * 区间内的行。
 *
 * 入参必须是**行缓冲的全量快照**而不是渲染用的可视行——这正是「不从 DOM 取」的落点。
 */
export function linesInRange(lines: readonly LogLine[], range: SeqRange | null | undefined): LogLine[] {
  if (!range) return []
  return lines.filter((line) => isLineSelected(range, line))
}

/** 选区文本：一律取 `raw` 拼接（spec §1.1——用户要原始日志，不是我们拆列重排过的）。 */
export function selectionText(lines: readonly LogLine[], range: SeqRange | null | undefined): string {
  return linesInRange(lines, range)
    .map((line) => line.raw)
    .join('\n')
}

/** 全选缓冲（`Ctrl+A`）。空缓冲返回 null——不造一个空区间让工具条白浮出来。 */
export function fullSeqRange(lines: readonly LogLine[]): SeqRange | null {
  if (lines.length === 0) return null
  return { anchor: lines[0].seq, head: lines[lines.length - 1].seq }
}

/** 扩选：锚点不动，只挪终点。无区间时以 `seq` 自成一点（首次 `Shift+Click` 的退化情形）。 */
export function extendSeqRange(range: SeqRange | null | undefined, seq: number): SeqRange {
  return { anchor: range?.anchor ?? seq, head: seq }
}

/** 触发边缘自动滚动的边距（px，spec §3.1）。 */
export const AUTO_SCROLL_EDGE = 32

/** 单次 tick 的最大滚动步长（px）。越贴边越快，用于「拖到屏幕外继续扩选」。 */
export const AUTO_SCROLL_MAX_STEP = 24

/** 自动滚动的 tick 间隔（ms，≈60fps）。 */
export const AUTO_SCROLL_TICK_MS = 16

/**
 * 按指针到视口上/下边缘的距离算滚动步长（px/tick，负=上滚）。
 *
 * 纯函数：边缘判定与加速曲线要能单测，不必真起一个容器。指针在中间区域返回 0，
 * 调用方据此**停掉**定时器（而不是空转 tick），这样「松手/离开容器即停」只有一个出口。
 */
export function edgeScrollStep(
  clientY: number,
  bounds: { top: number; bottom: number },
  options: { edge?: number; maxStep?: number } = {},
): number {
  const edge = options.edge ?? AUTO_SCROLL_EDGE
  const maxStep = options.maxStep ?? AUTO_SCROLL_MAX_STEP
  if (edge <= 0) return 0

  const topDistance = clientY - bounds.top
  if (topDistance < edge) {
    // 拖到容器上方（负距离）时按满速上滚，不外推出更大的值。
    const ratio = Math.min(1, Math.max(0, (edge - topDistance) / edge))
    return -Math.max(1, Math.round(maxStep * ratio))
  }
  const bottomDistance = bounds.bottom - clientY
  if (bottomDistance < edge) {
    const ratio = Math.min(1, Math.max(0, (edge - bottomDistance) / edge))
    return Math.max(1, Math.round(maxStep * ratio))
  }
  return 0
}

/** 选区内核对外的操作面（鼠标与 FR-421 键盘复制模式共用同一套）。 */
export interface SeqSelectionApi {
  range: SeqRange | null
  /** 落下锚点（鼠标 mousedown / 键盘打标记）。 */
  begin: (seq: number) => void
  /** 挪终点（鼠标 mousemove / 键盘移动）。 */
  extendTo: (seq: number) => void
  /** 全选缓冲（`Ctrl+A`）。 */
  selectAll: (lines: readonly LogLine[]) => void
  /** 直接设定整段（双击堆栈块 / 键盘选块）。 */
  setRange: (range: SeqRange | null) => void
  clear: () => void
  /** 行是否被选中（含跟随异常头的堆栈帧）。 */
  isSelected: (line: LogLine) => boolean
}

/** 选区状态 hook。语义全部委托给上面的纯函数，hook 只持有 state。 */
export function useSeqSelection(): SeqSelectionApi {
  const [range, setRange] = useState<SeqRange | null>(null)

  const begin = useCallback((seq: number) => setRange({ anchor: seq, head: seq }), [])
  const extendTo = useCallback((seq: number) => setRange((prev) => extendSeqRange(prev, seq)), [])
  const selectAll = useCallback((lines: readonly LogLine[]) => setRange(fullSeqRange(lines)), [])
  const clear = useCallback(() => setRange(null), [])
  const isSelected = useCallback((line: LogLine) => isLineSelected(range, line), [range])

  return useMemo(
    () => ({ range, begin, extendTo, selectAll, setRange, clear, isSelected }),
    [begin, clear, extendTo, isSelected, range, selectAll],
  )
}

/** {@link useEdgeAutoScroll} 的操作面。 */
export interface EdgeAutoScrollApi {
  /** 设定步长；0 即停（不空转 tick）。 */
  setStep: (step: number) => void
  /** 立即停止（松手 / 拖拽结束 / 卸载）。 */
  stop: () => void
  /** 是否正在滚动（测试与调试用）。 */
  isRunning: () => boolean
}

/**
 * 边缘自动滚动定时器。
 *
 * 用 `setInterval` 而非 `requestAnimationFrame`：后者在页签隐藏时不触发，
 * 会把「拖拽中切走页签」变成一个永不结束的拖拽；且 rAF 在 jsdom 下不可断言，
 * 而「自动滚动必须能停」是 FR-418 的验收项，得测得到。
 *
 * 停止有三个出口且都收敛到 `stop()`：步长归零、显式调用、组件卸载——
 * 绝不留下一个还在滚已消失容器的定时器。
 */
export function useEdgeAutoScroll(onStep: (step: number) => void): EdgeAutoScrollApi {
  const stepRef = useRef(0)
  const timerRef = useRef<number | null>(null)
  // 回调闭包里带着 rows / 选区等每帧都在变的量，必须取最新的那一份，
  // 否则 tick 会拿 mousedown 那一刻的旧 rows 去算终点。
  const onStepRef = useRef(onStep)
  useEffect(() => {
    onStepRef.current = onStep
  }, [onStep])

  const stop = useCallback(() => {
    stepRef.current = 0
    if (timerRef.current !== null) {
      clearInterval(timerRef.current)
      timerRef.current = null
    }
  }, [])

  const setStep = useCallback(
    (step: number) => {
      stepRef.current = step
      if (step === 0) {
        stop()
        return
      }
      if (timerRef.current !== null) return
      timerRef.current = window.setInterval(() => {
        if (stepRef.current === 0) {
          stop()
          return
        }
        onStepRef.current(stepRef.current)
      }, AUTO_SCROLL_TICK_MS)
    },
    [stop],
  )

  useEffect(() => stop, [stop])

  const isRunning = useCallback(() => timerRef.current !== null, [])

  return useMemo(() => ({ setStep, stop, isRunning }), [isRunning, setStep, stop])
}
