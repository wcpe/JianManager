import { startTransition, useCallback, useLayoutEffect, useMemo, useRef, useState, type RefObject } from 'react'

export interface VirtualWindowInput {
  total: number
  itemSize: number
  viewportSize: number
  scrollOffset: number
  overscan?: number
}

export interface VirtualWindow {
  start: number
  end: number
  before: number
  after: number
}

export function virtualWindow({
  total,
  itemSize,
  viewportSize,
  scrollOffset,
  overscan = 4,
}: VirtualWindowInput): VirtualWindow {
  if (total <= 0 || itemSize <= 0) return { start: 0, end: 0, before: 0, after: 0 }

  const viewport = Math.max(0, viewportSize)
  const offset = Math.max(0, scrollOffset)
  const first = Math.floor(offset / itemSize)
  const visible = Math.ceil(viewport / itemSize)
  const start = Math.max(0, first - overscan)
  const end = Math.min(total, first + visible + overscan)

  return {
    start,
    end,
    before: start * itemSize,
    after: Math.max(0, (total - end) * itemSize),
  }
}

/** 变高虚拟窗口的输入：`offsets` 是长度 total+1 的前缀和（offsets[0]=0）。 */
export interface VirtualWindowVariedInput {
  total: number
  /** 前缀和数组：offsets[i] = 第 i 行的起始偏移，offsets[total] = 总高。 */
  offsets: readonly number[]
  viewportSize: number
  scrollOffset: number
  overscan?: number
}

/**
 * 变高行（如控制台自动换行的长行）的虚拟窗口。
 *
 * 与等高版语义一致：start/end 是**行下标**闭开区间，before/after 是两侧占位高度。
 * 定位用二分——包含 scrollOffset 的行是「offsets[i] ≤ offset < offsets[i+1]」的 i。
 */
export function virtualWindowVaried({
  total,
  offsets,
  viewportSize,
  scrollOffset,
  overscan = 4,
}: VirtualWindowVariedInput): VirtualWindow {
  if (total <= 0 || offsets.length < total + 1) return { start: 0, end: 0, before: 0, after: 0 }

  const viewport = Math.max(0, viewportSize)
  const offset = Math.max(0, scrollOffset)

  // 二分找最后一个 offsets[i] <= offset 的行（它盖住了滚动点）。
  let lo = 0
  let hi = total - 1
  let first = 0
  while (lo <= hi) {
    const mid = (lo + hi) >> 1
    const midOffset = offsets[mid]
    // mid ∈ [0, total-1]，而入口已校验 offsets.length ≥ total+1，故 midOffset 恒存在；
    // 判空仅为收窄类型（命中即越界，二分已无意义，直接收束）。
    if (midOffset === undefined) break
    if (midOffset <= offset) {
      first = mid
      lo = mid + 1
    } else {
      hi = mid - 1
    }
  }

  // 第一个起始偏移越过视口底部的行（压线的行也要算可见）。
  const bottom = offset + viewport
  let end = total
  {
    let lo2 = first
    let hi2 = total - 1
    while (lo2 <= hi2) {
      const mid = (lo2 + hi2) >> 1
      const midOffset = offsets[mid]
      // 同上一处二分：mid ∈ [first, total-1] ⊂ [0, total-1]，恒在前缀和长度内，判空只为收窄类型。
      if (midOffset === undefined) break
      if (midOffset < bottom) {
        lo2 = mid + 1
      } else {
        end = mid
        hi2 = mid - 1
      }
    }
  }

  const start = Math.max(0, first - overscan)
  const endWithOverscan = Math.min(total, end + overscan)

  // before/after 取的是前缀和：start ∈ [0, total-1]、endWithOverscan ∈ [0, total]，
  // 均落在长度 ≥ total+1 的 offsets 内，故三处取值不会越界；判空只为收窄类型（命中即数据自相矛盾）。
  const before = offsets[start]
  const totalOffset = offsets[total]
  const afterOffset = offsets[endWithOverscan]
  if (before === undefined || totalOffset === undefined || afterOffset === undefined) {
    return { start: 0, end: 0, before: 0, after: 0 }
  }

  return {
    start,
    end: endWithOverscan,
    before,
    after: Math.max(0, totalOffset - afterOffset),
  }
}

interface VirtualRowsOptions {
  total: number
  itemSize: number
  overscan?: number
  fallbackViewportSize?: number
  fallbackCrossSize?: number
  /**
   * 变高模式：每行高度（px）。提供时 itemSize 只作为回退，
   * 窗口/总高/占位全部按前缀和计算（控制台自动换行用）。
   */
  sizes?: readonly number[]
  /**
   * 可选的**外部容器 ref**。
   *
   * 默认由 hook 自建并返回；当调用方还需要对这个滚动容器做别的事
   * （如「贴底跟随」读取 scrollHeight、或自定义 onScroll 逻辑）时，
   * 让调用方持有 ref 更直接——否则调用方只能改写 hook 返回的 ref，
   * 那既违背单向数据流，也会触发 `react-hooks/immutability` 规则。
   */
  containerRef?: RefObject<HTMLDivElement | null>
}

export function useVirtualRows({
  total,
  itemSize,
  overscan = 6,
  fallbackViewportSize = 640,
  fallbackCrossSize = 1024,
  sizes,
  containerRef: externalRef,
}: VirtualRowsOptions) {
  const internalRef = useRef<HTMLDivElement>(null)
  /**
   * 容器 ref：外部提供则用之，否则用内部自建的。
   *
   * 用 `useMemo` 而不是直接 `externalRef ?? internalRef`：后者是一个**每次渲染都会
   * 重新求值的表达式**，React Compiler 无法追踪它在闭包里是否稳定，会报
   * "Existing memoization could not be preserved" 并跳过该组件的自动记忆化。
   * 两个来源本身都是稳定 ref（`useRef` 的返回对象、或调用方传的 ref），
   * 所以只需把这个选择动作 memo 一次。
   */
  const containerRef = useMemo(() => externalRef ?? internalRef, [externalRef, internalRef])
  const scrollRafRef = useRef<number | null>(null)
  const [metrics, setMetrics] = useState({
    viewportSize: fallbackViewportSize,
    crossSize: fallbackCrossSize,
    scrollOffset: 0,
  })

  /*
    containerRef 是稳定 ref（`externalRef ?? internalRef`，两者都是 ref 对象），
    但 React Compiler 无法穿透 `??` 证明这点，于是跳过本组件的自动记忆化并报
    "Existing memoization could not be preserved"。下方依赖数组与 memo 均已手写完整，
    故接受该提示（是优化被跳过的告知，不是正确性问题）。
  */
  // eslint-disable-next-line react-hooks/preserve-manual-memoization -- 见上：稳定 ref，依赖已手写
  const measure = useCallback(() => {
    const el = containerRef.current
    if (!el) return
    const next = {
      viewportSize: el.clientHeight || fallbackViewportSize,
      crossSize: el.clientWidth || fallbackCrossSize,
      scrollOffset: el.scrollTop,
    }
    // 【必须做同值短路】侧栏开合会让内容区宽度**逐帧**变化，ResizeObserver 因此在整个 320ms
    // 过渡里每帧回调。原先这里无条件 `setMetrics({...})`——每次都是新对象引用，React 无法
    // bailout，于是「展开一次」要额外重渲染约 19 次（320ms × 60fps）。
    // trace 实测：click 事件本身只 62.5ms，但之后 1 秒内仍有 4 次 186~349ms 的大渲染
    // （合计 1070ms），正好覆盖展开动画全程——这就是「侧栏展开很卡」的直接来源。
    // 返回 prev 时 React 直接跳过本次渲染；只有尺寸真的变了才更新。
    //
    // 另外用 `startTransition` 降级：宽度变化后的重算是"非紧急"的，降级后可被中断，
    // 浏览器优先绘制侧栏动画帧。实测（React Profiler 埋点）内容区因侧栏开合累计阻塞
    // 538.8ms、最大单项 nested-update 229ms，都发生在防抖到期之后——把它们降级，
    // 用户操作就不会被这些重算挡住。
    startTransition(() => {
      setMetrics((prev) =>
        prev.viewportSize === next.viewportSize &&
        prev.crossSize === next.crossSize &&
        prev.scrollOffset === next.scrollOffset
          ? prev
          : next,
      )
    })
  }, [containerRef, fallbackCrossSize, fallbackViewportSize])

  useLayoutEffect(() => {
    measure()
    const el = containerRef.current
    if (!el) return

    // 【为什么宽度回调必须防抖】侧栏开合会让容器宽度**逐帧**变化，ResizeObserver 因此每帧
    // 回调。而每次回调都会 setState → 触发一次 React reconcile；本页组件树约 920 个元素，
    // 一次 reconcile 约 200ms，于是「渲染阻塞 → 动画帧被跳过 → 宽度再变 → 又 setState」
    // 在整个 320ms 过渡里循环。实测：侧栏宽度 2 秒内只渲染出 5 帧（38/45/62/252/645ms），
    // 帧间隔最大 393ms——这就是肉眼看到的「展开卡住」。
    // 同值短路（见 measure）挡不住它：宽度每帧确实不同，值确实在变。
    // 防抖取 400ms（> 侧栏动画 320ms）使过渡期间完全不重算，代价是列表在过渡中用旧宽度
    // 排版约一帧，容器本身有 overflow 兜底，肉眼不可见。
    let timer: number | undefined
    const debouncedMeasure = () => {
      window.clearTimeout(timer)
      timer = window.setTimeout(measure, 400)
    }

    const resizeObserver = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(debouncedMeasure)
    resizeObserver?.observe(el)
    window.addEventListener('resize', debouncedMeasure)
    return () => {
      window.clearTimeout(timer)
      if (scrollRafRef.current !== null) {
        window.cancelAnimationFrame(scrollRafRef.current)
        scrollRafRef.current = null
      }
      resizeObserver?.disconnect()
      window.removeEventListener('resize', debouncedMeasure)
    }
  }, [containerRef, measure])

  /**
   * 把当前滚动位置同步进 metrics（同值短路，与 measure 同一标准）。
   *
   * 【为什么必须做同值短路】`setMetrics({...prev, scrollOffset})` 每次都产生新对象，React 无法
   * bailout → 即使 scrollTop 没变也要重渲染整个列表（本文件多处列表页组件树都在千级元素量级）。
   * 返回 prev 时 React 直接跳过本次渲染，与 measure 里的处理一致。
   */
  // eslint-disable-next-line react-hooks/preserve-manual-memoization -- 同上：稳定 ref + 依赖已手写
  const flushScroll = useCallback(() => {
    scrollRafRef.current = null
    const el = containerRef.current
    if (!el) return
    const next = el.scrollTop
    setMetrics((prev) => (prev.scrollOffset === next ? prev : { ...prev, scrollOffset: next }))
  }, [containerRef])

  /**
   * 滚动事件处理器。
   *
   * 【为什么必须按帧合并】原实现每个 scroll 事件都 `setMetrics` 一次。浏览器在一次滚动里
   * 可能连续派发多个 scroll 事件（触控板惯性、滚轮连击、以及程序化 `scrollTop` 赋值叠加
   * 用户滚动），每个事件都是一次「重算窗口 + 重渲染整列表」。滚动事件按 HTML 规范发生在
   * 「update the rendering」的 scroll steps 里，而 rAF 回调就在同一帧的紧接着一步——
   * 因此在 scroll 里挂 rAF 既能把同一帧内的多次事件合并成**一次** setState，又不会推迟到下一帧
   * （不会出现视口晚一帧才跟上滚动的「拖影」）。
   */
  const onScroll = useCallback(() => {
    if (scrollRafRef.current !== null) return
    scrollRafRef.current = window.requestAnimationFrame(flushScroll)
  }, [flushScroll])

  // 变高模式：sizes → 前缀和（每行新高度时 O(n) 重建，控制台量级下可忽略）。
  // 用累加器而不是回读 prefix[i]：与「prefix[i+1] = prefix[i] + h」逐项等价（prefix[0]=0）。
  const offsets = useMemo(() => {
    if (!sizes || sizes.length < total) return null
    const prefix = new Array<number>(total + 1)
    let acc = 0
    prefix[0] = 0
    for (let i = 0; i < total; i++) {
      const size = sizes[i]
      // 非正高度（含越界取不到）回退 itemSize，与 `sizes[i] > 0 ? sizes[i] : itemSize` 同判据。
      acc += size !== undefined && size > 0 ? size : itemSize
      prefix[i + 1] = acc
    }
    return prefix
  }, [itemSize, sizes, total])

  const range = useMemo(
    () =>
      offsets
        ? virtualWindowVaried({
            total,
            offsets,
            viewportSize: metrics.viewportSize,
            scrollOffset: metrics.scrollOffset,
            overscan,
          })
        : virtualWindow({
            total,
            itemSize,
            viewportSize: metrics.viewportSize,
            scrollOffset: metrics.scrollOffset,
            overscan,
          }),
    [itemSize, metrics.scrollOffset, metrics.viewportSize, offsets, overscan, total],
  )

  const totalSize = useMemo(() => {
    if (offsets) return offsets[total] ?? 0
    return Math.max(0, total * itemSize)
  }, [itemSize, offsets, total])

  return {
    containerRef,
    crossSize: metrics.crossSize,
    onScroll,
    range,
    offsets,
    totalSize,
  }
}
