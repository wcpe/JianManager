import { startTransition, useCallback, useLayoutEffect, useMemo, useRef, useState } from 'react'

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
}

export function useVirtualRows({
  total,
  itemSize,
  overscan = 6,
  fallbackViewportSize = 640,
  fallbackCrossSize = 1024,
  sizes,
}: VirtualRowsOptions) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [metrics, setMetrics] = useState({
    viewportSize: fallbackViewportSize,
    crossSize: fallbackCrossSize,
    scrollOffset: 0,
  })

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
  }, [fallbackCrossSize, fallbackViewportSize])

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
      resizeObserver?.disconnect()
      window.removeEventListener('resize', debouncedMeasure)
    }
  }, [measure])

  const onScroll = useCallback(() => {
    const el = containerRef.current
    if (!el) return
    setMetrics((prev) => ({ ...prev, scrollOffset: el.scrollTop }))
  }, [])

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
