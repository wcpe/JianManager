import { describe, it, expect, vi, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { focusManager, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

import { usePlatformObservabilityOverview } from './metrics'
import api from '@/api/client'

vi.mock('@/api/client', () => ({ default: { get: vi.fn() } }))

const mockedGet = vi.mocked(api.get)

function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>
}

describe('usePlatformObservabilityOverview 轮询（FR-402）', () => {
  afterEach(() => {
    focusManager.setFocused(true)
    vi.useRealTimers()
    mockedGet.mockReset()
  })

  it('管理员可见时每 10 秒刷新，隐藏页暂停', async () => {
    vi.useFakeTimers()
    mockedGet.mockResolvedValue({ data: { health: {}, resources: {}, bots: {}, alerts: [], tasks: [], exceptions: [] } })
    renderHook(() => usePlatformObservabilityOverview(true), { wrapper: makeWrapper() })

    await act(async () => { await vi.advanceTimersByTimeAsync(0) })
    expect(mockedGet).toHaveBeenCalledTimes(1)
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
    expect(mockedGet).toHaveBeenCalledTimes(2)

    focusManager.setFocused(false)
    await act(async () => { await vi.advanceTimersByTimeAsync(30_000) })
    expect(mockedGet).toHaveBeenCalledTimes(2)
  })

  it('普通成员禁用查询，不请求管理员端点', async () => {
    renderHook(() => usePlatformObservabilityOverview(false), { wrapper: makeWrapper() })
    await Promise.resolve()
    expect(mockedGet).not.toHaveBeenCalled()
  })
})
