import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createHoverPrefetcher, INSTANCE_PREFETCH_DELAY_MS } from './instance-prefetch'

/**
 * FR-297 悬停预取防抖纯逻辑：稳定悬停 150ms 才触发一次预取，
 * 快速掠过（提前 leave / 换行）不得触发，避免鼠标扫过列表放请求风暴。
 */
describe('createHoverPrefetcher', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('稳定悬停超过防抖时长后触发一次预取', () => {
    const prefetch = vi.fn()
    const prefetcher = createHoverPrefetcher(prefetch)

    prefetcher.enter(7)
    vi.advanceTimersByTime(INSTANCE_PREFETCH_DELAY_MS - 1)
    expect(prefetch).not.toHaveBeenCalled()

    vi.advanceTimersByTime(1)
    expect(prefetch).toHaveBeenCalledTimes(1)
    expect(prefetch).toHaveBeenCalledWith(7)

    // 触发后不重复：计时器已消费。
    vi.advanceTimersByTime(1000)
    expect(prefetch).toHaveBeenCalledTimes(1)
  })

  it('防抖期内离开则取消，不触发预取', () => {
    const prefetch = vi.fn()
    const prefetcher = createHoverPrefetcher(prefetch)

    prefetcher.enter(7)
    vi.advanceTimersByTime(INSTANCE_PREFETCH_DELAY_MS - 10)
    prefetcher.leave()
    vi.advanceTimersByTime(1000)

    expect(prefetch).not.toHaveBeenCalled()
  })

  it('快速掠过多行只对最后停留的行触发', () => {
    const prefetch = vi.fn()
    const prefetcher = createHoverPrefetcher(prefetch)

    prefetcher.enter(1)
    vi.advanceTimersByTime(50)
    prefetcher.enter(2)
    vi.advanceTimersByTime(50)
    prefetcher.enter(3)
    vi.advanceTimersByTime(INSTANCE_PREFETCH_DELAY_MS)

    expect(prefetch).toHaveBeenCalledTimes(1)
    expect(prefetch).toHaveBeenCalledWith(3)
  })

  it('cancel 等价 leave：卸载清理后不触发', () => {
    const prefetch = vi.fn()
    const prefetcher = createHoverPrefetcher(prefetch)

    prefetcher.enter(9)
    prefetcher.cancel()
    vi.advanceTimersByTime(1000)

    expect(prefetch).not.toHaveBeenCalled()
  })

  it('自定义防抖时长生效', () => {
    const prefetch = vi.fn()
    const prefetcher = createHoverPrefetcher(prefetch, 300)

    prefetcher.enter(5)
    vi.advanceTimersByTime(299)
    expect(prefetch).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(prefetch).toHaveBeenCalledWith(5)
  })
})
