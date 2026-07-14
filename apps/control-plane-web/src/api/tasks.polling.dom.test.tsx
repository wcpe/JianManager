import { describe, it, expect, vi, afterEach } from 'vitest'
import { renderHook, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

import { useTasks, ACTIVE_TASKS_REFETCH_MS, type Task, type TaskPage } from './tasks'
import api from '@/api/client'

// 直接替换 axios 客户端（不走 MSW）：假计时器下驱动轮询节拍，逐拍断言请求次数。
vi.mock('@/api/client', () => ({ default: { get: vi.fn() } }))

const mockedGet = vi.mocked(api.get)

/** 造一条指定状态的任务（其余字段轮询判定不关心）。 */
function task(state: Task['state']): Task {
  return {
    id: 1,
    taskId: 't-1',
    nodeId: 1,
    kind: 'provision',
    state,
    progress: state === 'succeeded' ? 100 : 40,
    title: '一键搭建 lobby',
    detail: '下载核心',
    error: '',
    result: '',
    cancelRequested: false,
    createdBy: 1,
    createdAt: '2026-07-14T00:00:00Z',
    updatedAt: '2026-07-14T00:00:00Z',
  }
}

/** 造 GET /tasks 的分页信封响应（FR-337）。 */
function page(...items: Task[]): TaskPage {
  return { items, total: items.length, limit: 100, offset: 0 }
}

function makeWrapper() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  )
}

/**
 * FR-329：useTasks 轮询启停行为（假计时器）。响应为 FR-337 分页信封，轮询判活取 `data.items`。
 * 存在非终态任务 → 每 2s 自动重取；任务到终态 → 下一拍后停止，不再空转。
 */
describe('useTasks 自动轮询（FR-329）', () => {
  afterEach(() => {
    vi.useRealTimers()
    mockedGet.mockReset()
  })

  it('活跃任务每 2s 重取，任务终态后停止轮询', async () => {
    vi.useFakeTimers()
    mockedGet.mockResolvedValue({ data: page(task('running')) })

    renderHook(() => useTasks(), { wrapper: makeWrapper() })

    // 首拍：挂载即取。
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(mockedGet).toHaveBeenCalledTimes(1)

    // 2s 一拍：running 在列 → 自动重取。
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ACTIVE_TASKS_REFETCH_MS)
    })
    expect(mockedGet).toHaveBeenCalledTimes(2)

    // 下一拍返回终态：这一拍仍会发（上拍数据判活），拿到终态后停。
    mockedGet.mockResolvedValue({ data: page(task('succeeded')) })
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ACTIVE_TASKS_REFETCH_MS)
    })
    expect(mockedGet).toHaveBeenCalledTimes(3)

    // 终态后长时间推进：不再有任何重取（空闲停轮询）。
    await act(async () => {
      await vi.advanceTimersByTimeAsync(ACTIVE_TASKS_REFETCH_MS * 10)
    })
    expect(mockedGet).toHaveBeenCalledTimes(3)
  })

  it('全部终态时从一开始就不启动轮询', async () => {
    vi.useFakeTimers()
    mockedGet.mockResolvedValue({ data: page(task('succeeded')) })

    renderHook(() => useTasks(), { wrapper: makeWrapper() })

    await act(async () => {
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(mockedGet).toHaveBeenCalledTimes(1)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(ACTIVE_TASKS_REFETCH_MS * 10)
    })
    expect(mockedGet).toHaveBeenCalledTimes(1)
  })
})
