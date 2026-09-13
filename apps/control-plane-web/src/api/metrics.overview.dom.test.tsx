import { describe, expect, it } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createElement, type ReactNode } from 'react'
import { loginMockUser } from '@/test/auth'
import { useMetricOverview } from './metrics'

function createWrapper(client: QueryClient) {
  return function Wrapper({ children }: { children: ReactNode }) {
    return createElement(QueryClientProvider, { client }, children)
  }
}

describe('useMetricOverview（FIX-观测刷新）', () => {
  it('可见页面每十秒刷新，隐藏页面不在后台轮询', async () => {
    loginMockUser()
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const { result } = renderHook(() => useMetricOverview('24h'), { wrapper: createWrapper(client) })

    await waitFor(() => expect(result.current.isSuccess).toBe(true))

    const query = client.getQueryCache().find({ queryKey: ['metricOverview', '24h', 'auto'] })
    expect(query?.options.refetchInterval).toBe(10_000)
    expect(query?.options.refetchIntervalInBackground).toBe(false)
  })
})
