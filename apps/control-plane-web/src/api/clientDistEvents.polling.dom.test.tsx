import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

import { CLIENT_DIST_REALTIME_REFETCH_MS, useClientDistErrorSummary, useClientDistRealtime } from './clientDistEvents'
import api from '@/api/client'

vi.mock('@/api/client', () => ({ default: { get: vi.fn() } }))

const mockedGet = vi.mocked(api.get)

function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

const realtimeBody = {
  summary1h: { manifestPulls: 0, artifactPulls: 0, errorRequests: 0, activeMachines: 0 },
  requestRate24h: [],
  topIps1h: [],
  recentErrors: [],
}
const errorSummaryBody = { topErrors: [], samples: [] }

/**
 * 页面 B 实时监控/错误摘要的轮询（R35）。
 *
 * 这是替代「把 30_000 常量传进 resolveInterval 再与自身比较」那种空转测试的重写版：
 * 真起 hook + fake timers，按 **HTTP 调用次数** 断言「确实排了定时器」与「disabled 时不请求」。
 * 删掉生产代码里的 refetchInterval 行，第一条会立刻变红（review 的验收方式）。
 */
describe('useClientDistRealtime / useClientDistErrorSummary 轮询', () => {
  // 焦点状态必须在 **beforeEach** 恢复，不能放 afterEach：
  // focusManager.setFocused(true) 会立即给「上一轮仍挂着的查询」排一次重取，
  // 那次异步调用会串进下一条用例（实测：enabled=false 用例里莫名出现 1 次请求）。
  beforeEach(() => {
    focusManager.setFocused(true)
  })

  afterEach(() => {
    vi.useRealTimers()
    mockedGet.mockReset()
  })

  it('realtime 每 30 秒自动刷新一次', async () => {
    vi.useFakeTimers()
    mockedGet.mockResolvedValue({ data: realtimeBody })
    renderHook(() => useClientDistRealtime({ channelId: 'c1' }), { wrapper: makeWrapper() })

    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(mockedGet).toHaveBeenCalledTimes(1)

    await act(async () => { await vi.advanceTimersByTimeAsync(CLIENT_DIST_REALTIME_REFETCH_MS) })
    expect(mockedGet).toHaveBeenCalledTimes(2)

    // 再一个周期 → 第三次；证明是周期性而不是仅一次。
    await act(async () => { await vi.advanceTimersByTimeAsync(CLIENT_DIST_REALTIME_REFETCH_MS) })
    expect(mockedGet).toHaveBeenCalledTimes(3)
  })

  it('error-summary 同样按周期刷新', async () => {
    vi.useFakeTimers()
    mockedGet.mockResolvedValue({ data: errorSummaryBody })
    renderHook(() => useClientDistErrorSummary({ range: '7d' }), { wrapper: makeWrapper() })

    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(mockedGet).toHaveBeenCalledTimes(1)

    await act(async () => { await vi.advanceTimersByTimeAsync(CLIENT_DIST_REALTIME_REFETCH_MS) })
    expect(mockedGet).toHaveBeenCalledTimes(2)
  })

  it('标签页在后台时不轮询（refetchIntervalInBackground=false）', async () => {
    vi.useFakeTimers()
    mockedGet.mockResolvedValue({ data: realtimeBody })
    renderHook(() => useClientDistRealtime({ channelId: 'c1' }), { wrapper: makeWrapper() })

    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(mockedGet).toHaveBeenCalledTimes(1)

    focusManager.setFocused(false)
    await act(async () => { await vi.advanceTimersByTimeAsync(CLIENT_DIST_REALTIME_REFETCH_MS * 3) })
    expect(mockedGet).toHaveBeenCalledTimes(1)
  })

  it('enabled=false 时完全不请求，也不排定时器', async () => {
    vi.useFakeTimers()
    mockedGet.mockResolvedValue({ data: realtimeBody })
    renderHook(() => useClientDistRealtime({ channelId: 'c1', enabled: false }), { wrapper: makeWrapper() })

    await act(async () => { await vi.advanceTimersByTimeAsync(CLIENT_DIST_REALTIME_REFETCH_MS * 3) })
    expect(mockedGet).not.toHaveBeenCalled()
  })
})
