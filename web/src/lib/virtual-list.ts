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

interface VirtualRowsOptions {
  total: number
  itemSize: number
  overscan?: number
  fallbackViewportSize?: number
  fallbackCrossSize?: number
}

export function useVirtualRows({
  total,
  itemSize,
  overscan = 6,
  fallbackViewportSize = 640,
  fallbackCrossSize = 1024,
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

  const range = useMemo(
    () =>
      virtualWindow({
        total,
        itemSize,
        viewportSize: metrics.viewportSize,
        scrollOffset: metrics.scrollOffset,
        overscan,
      }),
    [itemSize, metrics.scrollOffset, metrics.viewportSize, overscan, total],
  )

  return {
    containerRef,
    crossSize: metrics.crossSize,
    onScroll,
    range,
    totalSize: Math.max(0, total * itemSize),
  }
}
