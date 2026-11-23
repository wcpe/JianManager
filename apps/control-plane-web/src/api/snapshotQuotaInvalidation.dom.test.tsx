import { describe, expect, it, vi, beforeEach } from 'vitest'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { renderHook, act, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'

import api from '@/api/client'
import { INSTANCE_QUERY_GC_TIME_MS } from '@/api/instances'
import { useCreateSnapshot, useDeleteSnapshot, useRollbackSnapshot } from './snapshots'
import { useBinaryRollback, useBinaryUpgrade } from './binaryVersion'

vi.mock('@/api/client', () => ({ default: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }))
vi.mock('sonner', () => ({ toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() } }))

const mockedPost = vi.mocked(api.post)
const mockedDelete = vi.mocked(api.delete)

/**
 * W-05：快照 / 二进制版本的**异步任务提交**必须失效三组缓存。
 *
 * 两条缺陷现场：
 * ① 回滚原先失效 `['instance', id]`（单数）—— 但实例详情的真实 key 是 `['instances', id]`
 *    （instances.ts 的 instanceQueryOptions）。全仓无 query 使用单数 key，属**死失效**：
 *    回滚会停服并把实例置为 STOPPED，实例状态显示却不刷新。
 * ② 五个 mutation 全都不失效 `['tasks']`，而任务列表轮询是**条件式**的
 *    （无在途任务即不轮询）——「当前没有别的在途任务」时，刚提交的快照/版本任务
 *    不会出现在任务中心，运维看不到进度与失败原因。
 *
 * 本测试逐 mutation 断言失效的 key 集合，防止有人把其中一条删掉。
 */
function makeClient() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  // 记录所有被失效的 key（含前缀匹配的语义由 TanStack 自身保证，这里只看调用参数）。
  const invalidated: unknown[] = []
  const orig = client.invalidateQueries.bind(client)
  vi.spyOn(client, 'invalidateQueries').mockImplementation((filters, options) => {
    invalidated.push(filters?.queryKey)
    return orig(filters, options)
  })
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
  return { client, invalidated, wrapper }
}

/** 断言失效集合里同时含给定的每个 key（顺序无关）。 */
function expectInvalidated(invalidated: unknown[], expected: unknown[][]) {
  for (const key of expected) {
    expect(
      invalidated.some((k) => JSON.stringify(k) === JSON.stringify(key)),
      `应失效 ${JSON.stringify(key)}，实际失效集合：${JSON.stringify(invalidated)}`,
    ).toBe(true)
  }
}

const INSTANCE_ID = 7

describe('快照 mutation 的缓存失效集合（W-05）', () => {
  beforeEach(() => {
    mockedPost.mockReset()
    mockedDelete.mockReset()
  })

  it('创建快照：失效快照列表 + 实例详情 + 任务中心', async () => {
    mockedPost.mockResolvedValue({ data: { id: 1 } })
    const { invalidated, wrapper } = makeClient()
    const { result } = renderHook(() => useCreateSnapshot(INSTANCE_ID), { wrapper })

    await act(async () => {
      await result.current.mutateAsync('快照 A')
    })
    expectInvalidated(invalidated, [
      ['instance-snapshots', INSTANCE_ID],
      ['instances', INSTANCE_ID],
      ['tasks'],
    ])
  })

  it('回滚：失效快照列表 + 实例详情（复数 key，不得再是死失效的单数）+ 任务中心', async () => {
    mockedPost.mockResolvedValue({ data: { taskId: 't1', binaryMismatch: false } })
    const { invalidated, wrapper } = makeClient()
    const { result } = renderHook(() => useRollbackSnapshot(INSTANCE_ID), { wrapper })

    await act(async () => {
      await result.current.mutateAsync(42)
    })
    expectInvalidated(invalidated, [
      ['instance-snapshots', INSTANCE_ID],
      ['instances', INSTANCE_ID],
      ['tasks'],
    ])
    // 单数 `['instance', id]` 是死失效：全仓无 query 使用该 key。
    expect(invalidated.some((k) => JSON.stringify(k) === JSON.stringify(['instance', INSTANCE_ID]))).toBe(false)
  })

  it('删除快照：失效快照列表（删除不是异步任务，不强制失效 tasks）', async () => {
    mockedDelete.mockResolvedValue({ data: { deleted: true } })
    const { invalidated, wrapper } = makeClient()
    const { result } = renderHook(() => useDeleteSnapshot(INSTANCE_ID), { wrapper })

    await act(async () => {
      await result.current.mutateAsync(42)
    })
    expectInvalidated(invalidated, [['instance-snapshots', INSTANCE_ID]])
  })
})

describe('二进制版本 mutation 的缓存失效集合（W-05）', () => {
  beforeEach(() => {
    mockedPost.mockReset()
  })

  it('升级：失效版本视图 + 实例详情 + 任务中心', async () => {
    mockedPost.mockResolvedValue({ data: { taskId: 't2' } })
    const { invalidated, wrapper } = makeClient()
    const { result } = renderHook(() => useBinaryUpgrade(INSTANCE_ID), { wrapper })

    await act(async () => {
      await result.current.mutateAsync(12)
    })
    expectInvalidated(invalidated, [
      ['instance-binary-version', INSTANCE_ID],
      ['instances', INSTANCE_ID],
      ['tasks'],
    ])
  })

  it('回滚：失效版本视图 + 实例详情 + 任务中心', async () => {
    mockedPost.mockResolvedValue({ data: { taskId: 't3' } })
    const { invalidated, wrapper } = makeClient()
    const { result } = renderHook(() => useBinaryRollback(INSTANCE_ID), { wrapper })

    await act(async () => {
      await result.current.mutateAsync(undefined)
    })
    expectInvalidated(invalidated, [
      ['instance-binary-version', INSTANCE_ID],
      ['instances', INSTANCE_ID],
      ['tasks'],
    ])
  })
})

/**
 * 失效的 key 必须与**真实存在的 query** 对齐：这里用真 QueryClient 观察
 * 「失效后该 query 是否被标记为 stale」，避免再次出现「失效了没人用的 key」这类无声缺陷。
 */
describe('失效目标必须是真实存在的 query（防死失效）', () => {
  it('实例详情 query 在回滚后被标记 stale', async () => {
    mockedPost.mockResolvedValue({ data: { taskId: 't4', binaryMismatch: false } })
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
    const wrapper = ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    )
    // 预置与 instances.ts 完全一致的 key 的缓存项（真 key：复数）。
    client.setQueryData(['instances', INSTANCE_ID], { id: INSTANCE_ID, status: 'RUNNING' })
    client.setQueryData(['instance-snapshots', INSTANCE_ID], [])
    client.setQueryData(['tasks', undefined], [])

    const { result } = renderHook(() => useRollbackSnapshot(INSTANCE_ID), { wrapper })
    await act(async () => {
      await result.current.mutateAsync(42)
    })

    await waitFor(() =>
      expect(client.getQueryState(['instances', INSTANCE_ID])?.isInvalidated).toBe(true),
    )
    expect(client.getQueryState(['instance-snapshots', INSTANCE_ID])?.isInvalidated).toBe(true)
    expect(client.getQueryState(['tasks', undefined])?.isInvalidated).toBe(true)
    // 实例详情 gcTime 常量未被本改动影响（防误改）。
    expect(INSTANCE_QUERY_GC_TIME_MS).toBeGreaterThan(0)
  })
})
