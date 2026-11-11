import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { appendLiveEvent, useLiveLogStream, type LiveFrame, type LiveSubscriber } from './use-live-log-stream'

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())
const event = (i: number) => ({ event_id: String(i), log_source_id: 'inst:2/file', event_time_utc: String(i), message: `row-${i}` })

function transport() {
  const connections: { signal: AbortSignal; onFrame: (frame: LiveFrame) => void; fail: (error: unknown) => void }[] = []
  const subscribe: LiveSubscriber = vi.fn((_params, options) => new Promise<void>((_resolve, reject) => {
    connections.push({ ...options, fail: reject })
    options.signal.addEventListener('abort', () => reject(new DOMException('Cancelled', 'AbortError')), { once: true })
  }))
  return { connections, subscribe }
}

describe('bounded instance live buffer', () => {
  it('bounds the display page and deduplicates replay without mixing streams', () => {
    let rows: Record<string, unknown>[] = []
    for (let i = 0; i < 130; i++) rows = appendLiveEvent(rows, event(i))
    expect(rows).toHaveLength(100)
    expect(appendLiveEvent(rows, event(129))).toBe(rows)
    expect(appendLiveEvent(rows, { ...event(129), log_source_id: 'inst:3/file' })[0].log_source_id).toBe('inst:3/file')
  })
  it('batches a burst and cancels old query callbacks', async () => {
    const { connections, subscribe } = transport()
    const { result, rerender, unmount } = renderHook(({ instanceId }) => useLiveLogStream({ instanceId }, true, false, subscribe), { initialProps: { instanceId: 2 } })
    await act(async () => {
      connections[0].onFrame({ type: 'ready' })
      for (let i = 0; i < 120; i++) connections[0].onFrame({ type: 'event', event: event(i) })
    })
    expect(result.current.items).toHaveLength(0)
    await act(() => vi.advanceTimersByTimeAsync(100))
    expect(result.current.items).toHaveLength(100)
    rerender({ instanceId: 3 })
    expect(connections[0].signal.aborted).toBe(true)
    expect(result.current.items).toHaveLength(0)
    await act(async () => connections[0].onFrame({ type: 'event', event: event(121) }))
    expect(result.current.items).toHaveLength(0)
    unmount()
    expect(connections[1].signal.aborted).toBe(true)
  })
  it('pauses without changing the visible page, then resumes the same identity', async () => {
    const { connections, subscribe } = transport()
    const { result, rerender } = renderHook(({ paused }) => useLiveLogStream({ instanceId: 2 }, true, paused, subscribe), { initialProps: { paused: false } })
    await act(async () => { connections[0].onFrame({ type: 'ready' }); connections[0].onFrame({ type: 'event', event: event(1) }); await vi.advanceTimersByTimeAsync(100) })
    const page = result.current.items
    rerender({ paused: true })
    expect(result.current.status).toBe('paused')
    expect(result.current.items).toBe(page)
    expect(connections[0].signal.aborted).toBe(true)
    rerender({ paused: false })
    expect(result.current.items).toBe(page)
  })
  it('exposes unsupported as a visible polling fallback and stops on revoked access', async () => {
    const { connections, subscribe } = transport()
    const { result, rerender } = renderHook(({ instanceId }) => useLiveLogStream({ instanceId }, true, false, subscribe), { initialProps: { instanceId: 2 } })
    await act(async () => connections[0].fail({ code: 'UNSUPPORTED', retryable: false }))
    expect(result.current.status).toBe('fallback')
    rerender({ instanceId: 3 })
    await act(async () => connections[1].fail({ code: 'FORBIDDEN', retryable: false }))
    expect(result.current.status).toBe('error')
    expect(result.current.items).toHaveLength(0)
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(subscribe).toHaveBeenCalledTimes(2)
  })
  it('reconnects with bounded backoff and releases retry timers when stopped', async () => {
    const { connections, subscribe } = transport()
    const { result, unmount } = renderHook(() => useLiveLogStream({ instanceId: 2 }, true, false, subscribe))
    await act(async () => connections[0].fail({ code: 'STREAM_CLOSED', retryable: true }))
    expect(result.current.status).toBe('reconnecting')
    await act(() => vi.advanceTimersByTimeAsync(499))
    expect(subscribe).toHaveBeenCalledTimes(1)
    await act(() => vi.advanceTimersByTimeAsync(1))
    expect(subscribe).toHaveBeenCalledTimes(2)
    unmount()
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(subscribe).toHaveBeenCalledTimes(2)
  })
})
