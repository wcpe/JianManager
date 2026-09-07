import { useCallback, useLayoutEffect, useMemo, useRef, useState } from 'react'

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
    if (offsets[mid] <= offset) {
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
      if (offsets[mid] < bottom) {
        lo2 = mid + 1
      } else {
        end = mid
        hi2 = mid - 1
      }
    }
  }

  const start = Math.max(0, first - overscan)
  const endWithOverscan = Math.min(total, end + overscan)

  return {
    start,
    end: endWithOverscan,
    before: offsets[start],
    after: Math.max(0, offsets[total] - offsets[endWithOverscan]),
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
    setMetrics({
      viewportSize: el.clientHeight || fallbackViewportSize,
      crossSize: el.clientWidth || fallbackCrossSize,
      scrollOffset: el.scrollTop,
    })
  }, [fallbackCrossSize, fallbackViewportSize])

  useLayoutEffect(() => {
    measure()
    const el = containerRef.current
    if (!el) return

    const resizeObserver = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(measure)
    resizeObserver?.observe(el)
    window.addEventListener('resize', measure)
    return () => {
      resizeObserver?.disconnect()
      window.removeEventListener('resize', measure)
    }
  }, [measure])

  const onScroll = useCallback(() => {
    const el = containerRef.current
    if (!el) return
    setMetrics((prev) => ({ ...prev, scrollOffset: el.scrollTop }))
  }, [])

  // 变高模式：sizes → 前缀和（每行新高度时 O(n) 重建，控制台量级下可忽略）。
  const offsets = useMemo(() => {
    if (!sizes || sizes.length < total) return null
    const prefix = new Array<number>(total + 1)
    prefix[0] = 0
    for (let i = 0; i < total; i++) prefix[i + 1] = prefix[i] + (sizes[i] > 0 ? sizes[i] : itemSize)
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
