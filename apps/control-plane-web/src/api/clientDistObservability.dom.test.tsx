import { describe, it, expect } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createElement, type ReactNode } from 'react'
import { loginMockUser } from '@/test/auth'
import { useClientDistObservability } from './clientDistObservability'

/**
 * useClientDistObservability 强断言（FR-219 消费 FR-217）。
 * 走 MSW 假后端的 /client-dist/observability handler（domains/client.ts），断言时序/分布/汇总解析；
 * enabled=false（channelId 为 null）时不触发请求。setup.ts 已 onUnhandledRequest:'error'。
 */
function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return createElement(QueryClientProvider, { client: qc }, children)
}

describe('useClientDistObservability（FR-217 消费）', () => {
  it('按 channelId + range 取观测视图（时序/分布/汇总）', async () => {
    loginMockUser()
    const { result } = renderHook(() => useClientDistObservability('skyblock-s1', '30d'), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))

    const d = result.current.data!
    expect(d.summary.activeMachines).toBe(512)
    // 30d 超明细保留窗 → 人次近似（activeMachinesExact=false）。
    expect(d.summary.activeMachinesExact).toBe(false)
    expect(d.summary.failStaticRate).toBeCloseTo(0.0278)
    expect(d.versionDist[0]).toMatchObject({ version: 7, count: 900 })
    expect(d.platformDist[0]).toMatchObject({ os: 'windows', count: 1200 })
    expect(d.lagDist[0]).toMatchObject({ lag: 0, count: 320 })
  })

  it('短窗 7d 落保留窗内 → activeMachinesExact=true', async () => {
    loginMockUser()
    const { result } = renderHook(() => useClientDistObservability('skyblock-s1', '7d'), { wrapper })
    await waitFor(() => expect(result.current.isSuccess).toBe(true))
    expect(result.current.data?.summary.activeMachinesExact).toBe(true)
  })

  it('channelId 为 null 时不发请求（enabled=false）', async () => {
    loginMockUser()
    const { result } = renderHook(() => useClientDistObservability(null, '7d'), { wrapper })
    // 给若干微任务后仍为 pending 且无数据，证明 query 未触发。
    await Promise.resolve()
    expect(result.current.fetchStatus).toBe('idle')
    expect(result.current.data).toBeUndefined()
  })
})
