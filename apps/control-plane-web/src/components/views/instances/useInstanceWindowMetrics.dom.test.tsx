/** 
 * @file useInstanceWindowMetrics 单测：**增量取数**语义（本轮性能修复的核心）。
 *
 * 修的是什么：滚动是平移窗口，上一次的目标多半还在下一次的窗口里。此前每次都把整窗重取，
 * 每次都是一份 1h raw 全量序列（devmock 实测 400~550KB / 次）——滚一下就是几百 KB 白流量。
 * 现在只对新进视口（或已过期）的目标发请求，结果并入累积缓存。
 *
 * 这组用例直接断言**下发的 targetIds**，而不是间接看渲染结果：请求目标集合正是性能契约本身。
 */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

import { resetWindowMetricsCache, useInstanceWindowMetrics } from './useInstanceWindowMetrics'
import api from '@/api/client'
import type { InstanceInfo } from '@/lib/instances/instance-types'

// 直接替换 axios 客户端：断言每次批量请求的 targetIds（不走 MSW，避免假后端规模干扰）。
vi.mock('@/api/client', () => ({ default: { post: vi.fn() } }))

const mockedPost = vi.mocked(api.post)

/** 造一台实例（hook 只读 uuid）。 */
function inst(id: number): InstanceInfo {
  return { id, uuid: `u-${id}`, name: `s-${id}` } as InstanceInfo
}

/** 后端批量响应的最小形状：每个目标一条 TPS 序列 + 一条玩家序列。 */
function batchResponse(uuids: string[]) {
  const series: Record<string, unknown[]> = {}
  for (const uuid of uuids) {
    series[uuid] = [
      { metricKey: 'inst_tps', points: [{ t: 1, avg: 19.9 }] },
      { metricKey: 'inst_players_online', points: [{ t: 1, avg: 7 }] },
      { metricKey: 'inst_players_max', points: [{ t: 1, avg: 50 }] },
    ]
  }
  return { data: { series, skipped: [] } }
}

function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
}

/** 取最近一次批量请求下发的目标集合。 */
function lastTargets(): string[] {
  const call = mockedPost.mock.calls.at(-1)
  const body = call?.[1] as { targetIds?: string[] } | undefined
  return body?.targetIds ?? []
}

describe('useInstanceWindowMetrics 增量取数', () => {
  beforeEach(() => {
    // 快照缓存是模块级的（跨组件卸载存活），用例之间必须清空，否则前一条的数据会漏进来。
    resetWindowMetricsCache()
    mockedPost.mockReset()
    mockedPost.mockImplementation(((_url: string, body: { targetIds: string[] }) =>
      Promise.resolve(batchResponse(body.targetIds))) as never)
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('首次只请求视口内的目标', async () => {
    const visible = [inst(1), inst(2), inst(3)]
    const { result } = renderHook(() => useInstanceWindowMetrics(visible), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.size).toBe(3))
    expect(lastTargets()).toEqual(['u-1', 'u-2', 'u-3'])
  })

  it('窗口平移时只请求**新增**目标，已缓存的不再重复下发', async () => {
    const { result, rerender } = renderHook(
      ({ visible }: { visible: InstanceInfo[] }) => useInstanceWindowMetrics(visible),
      { wrapper: makeWrapper(), initialProps: { visible: [inst(1), inst(2)] } },
    )
    await waitFor(() => expect(result.current.size).toBe(2))

    // 平移：1 留在窗口里，2 滚出，3 新进入 —— 只该请求 3。
    rerender({ visible: [inst(1), inst(3)] })
    await waitFor(() => expect(result.current.has('u-3')).toBe(true))

    expect(lastTargets()).toEqual(['u-3'])
    // 缓存里已有 1，仍对渲染可见（不必重取）。
    expect(result.current.has('u-1')).toBe(true)
    // 滑出视口的 2 不再出现在返回值里（不给悬停卡喂过期行）。
    expect(result.current.has('u-2')).toBe(false)
  })

  it('目标集合完全没变时不产生新请求（防抖后同一窗口幂等）', async () => {
    const visible = [inst(1), inst(2)]
    const { result, rerender } = renderHook(() => useInstanceWindowMetrics(visible), { wrapper: makeWrapper() })
    await waitFor(() => expect(result.current.size).toBe(2))
    const before = mockedPost.mock.calls.length

    // 新数组、同样内容（React 侧确实是新引用）：uuid 集合没变 → 不该再发请求。
    rerender()
    await new Promise((resolve) => setTimeout(resolve, 100))

    expect(mockedPost.mock.calls.length).toBe(before)
  })

  it('快照同时带当前人数与上限（：侧栏「当前/最大」同批取回，不二次请求）', async () => {
    const visible = [inst(1)]
    const { result } = renderHook(() => useInstanceWindowMetrics(visible), { wrapper: makeWrapper() })

    await waitFor(() => expect(result.current.size).toBe(1))
    const snapshot = result.current.get('u-1')
    expect(snapshot?.players).toBe(7)
    expect(snapshot?.playersMax).toBe(50)
    // 上限与当前人数必须由**同一次**请求带回——分两次取会让「16/50」里的两半来自不同时刻。
    expect(mockedPost.mock.calls).toHaveLength(1)
  })

  it('超过 30s 后视口内目标重新纳入请求（避免数值冻结在首次见到它的一刻）', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const visible = [inst(1), inst(2)]
    const { result } = renderHook(() => useInstanceWindowMetrics(visible), { wrapper: makeWrapper() })
    await waitFor(() => expect(result.current.size).toBe(2))
    const before = mockedPost.mock.calls.length

    // 走过一个轮询周期：过期节拍推进，两个目标都应重新请求。
    await vi.advanceTimersByTimeAsync(31_000)
    await waitFor(() => expect(mockedPost.mock.calls.length).toBeGreaterThan(before))
    expect(lastTargets().sort()).toEqual(['u-1', 'u-2'])
  })
})
