import { describe, it, expect } from 'vitest'
import { tasksRefetchInterval, ACTIVE_TASKS_REFETCH_MS, type TaskState } from './tasks'

/**
 * FR-329：任务轮询启停规则（纯函数）。
 * 有任一非终态任务（pending/running）→ 2s 短轮询；全部终态 / 空 / 未加载 → 停（false）。
 */
describe('tasksRefetchInterval（FR-329）', () => {
  const of = (...states: TaskState[]) => states.map((state) => ({ state }))

  it('未加载（undefined）→ 停轮询', () => {
    expect(tasksRefetchInterval(undefined)).toBe(false)
  })

  it('空列表 → 停轮询', () => {
    expect(tasksRefetchInterval([])).toBe(false)
  })

  it('全部终态（succeeded/failed/canceled）→ 停轮询', () => {
    expect(tasksRefetchInterval(of('succeeded', 'failed', 'canceled'))).toBe(false)
  })

  it('存在 running → 2s 轮询', () => {
    expect(tasksRefetchInterval(of('succeeded', 'running'))).toBe(ACTIVE_TASKS_REFETCH_MS)
  })

  it('存在 pending（尚未开跑也算活跃）→ 2s 轮询', () => {
    expect(tasksRefetchInterval(of('pending'))).toBe(ACTIVE_TASKS_REFETCH_MS)
  })
})
