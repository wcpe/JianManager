import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen } from '@testing-library/react'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import ConsoleHeader from './ConsoleHeader'

vi.mock('@/api/instances', () => ({
  useInstance: () => ({ data: { id: 1, name: 'survival-1', status: 'RUNNING' } }),
  useInstanceAggregate: () => ({ data: { total: 1, byStatus: { CRASHED: 0 }, byNode: [], byRole: {} } }),
}))
vi.mock('@/api/nodes', () => ({
  useNodes: () => ({ data: [{ id: 1, name: 'alpha', status: 1 }] }),
}))
vi.mock('@/api/metrics', () => ({
  useMetricOverview: () => ({ data: { totals: { onlineNodeCount: 1, runningInstances: 2 } } }),
}))
vi.mock('@/api/tasks', () => ({
  useTasks: () => ({ data: { items: [], total: 0, limit: 100, offset: 0 } }),
}))
vi.mock('@/api/notification-feed', () => ({
  useFeedUnreadCount: () => ({ data: 0 }),
  useNotificationFeed: () => ({ data: { items: [] } }),
}))

/** 方案 C（ADR-071）：顶栏承载品牌区 + 面包屑 + 操作区；节点作用域下拉已下线。 */
describe('ConsoleHeader 布局', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('顶栏承载品牌区与面包屑，且已下线节点作用域下拉', () => {
    const { container } = renderWithProviders(<ConsoleHeader />, { route: '/instances/1' })

    expect(container.querySelector('header')).toHaveAttribute('data-slot', 'console-header')
    expect(container.querySelector('header')).toHaveClass('jm-console-header')
    // 方案 C：品牌 Logo 移入顶栏品牌区。
    expect(screen.getByText('JianManager')).toBeInTheDocument()
    // FR-268 节点作用域下拉已下线。
    expect(screen.queryByRole('button', { name: '节点作用域' })).not.toBeInTheDocument()
    // 主题 / 配色切换仍只在侧栏底部，不在顶栏。
    expect(screen.queryByRole('button', { name: '切换主题' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Jian 绿' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '青绿' })).not.toBeInTheDocument()
  })
})
