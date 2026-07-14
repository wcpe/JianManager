import { describe, it, expect } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createElement, type ReactNode } from 'react'
import { loginMockUser } from '@/test/auth'
import { useMetricSeriesBatch } from './metrics'

/**
 * useMetricSeriesBatch 契约测试（FR-334）。走 MSW 假后端 /metrics/series/batch handler
 * （domains/observ.ts），断言按 targetId 分组的同构序列 + metrics 过滤 + 空目标不发请求。
 * setup.ts 已 onUnhandledRequest:'error'（未 mock 请求即失败）。
 */
function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return createElement(QueryClientProvider, { client: qc }, children)
}

describe('useMetricSeriesBatch（FR-334）', () => {
  it('按 targetId 分组返回同构序列（含 metrics 过滤）', async () => {
    loginMockUser()
    const { result } = renderHook(
      () => useMetricSeriesBatch({ scope: 'instance', targetIds: ['uuid-a', 'uuid-b'], range: '24h', metrics: ['inst_tps'] }),
      { wrapper },
    )
    await waitFor(() => expect(result.current.isSuccess).toBe(true))

    const d = result.current.data!
    expect(Object.keys(d.series).sort()).toEqual(['uuid-a', 'uuid-b'])
    // metrics 过滤后各目标只留 inst_tps；序列结构与单目标同构（metricKey/unit/world/points）。
    expect(d.series['uuid-a']).toHaveLength(1)
    expect(d.series['uuid-a'][0]).toMatchObject({ metricKey: 'inst_tps', world: '' })
    expect(d.series['uuid-a'][0].points.length).toBeGreaterThan(0)
    expect(d.series['uuid-a'][0].points[0]).toHaveProperty('avg')
    expect(d.skipped).toEqual([])
  })

  it('targetIds 为空时不发请求（enabled=false）', async () => {
    loginMockUser()
    const { result } = renderHook(
      () => useMetricSeriesBatch({ scope: 'instance', targetIds: [], range: '24h' }),
      { wrapper },
    )
    await Promise.resolve()
    expect(result.current.fetchStatus).toBe('idle')
    expect(result.current.data).toBeUndefined()
  })
})
