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
  useTasks: () => ({ data: [] }),
}))
vi.mock('@/api/notification-feed', () => ({
  useFeedUnreadCount: () => ({ data: 0 }),
  useNotificationFeed: () => ({ data: { items: [] } }),
}))

/** FR-268/FR-269：控制台顶栏布局回归。 */
describe('ConsoleHeader 布局', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('顶栏不重复渲染侧栏品牌，给面包屑和操作区留出空间', () => {
    const { container } = renderWithProviders(<ConsoleHeader />, { route: '/instances/1' })

    expect(container.querySelector('header')).toHaveAttribute('data-slot', 'console-header')
    expect(container.querySelector('header')).toHaveClass('jm-console-header')
    expect(screen.queryByText('JianManager')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '节点作用域' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '切换主题' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Jian 绿' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '青绿' })).not.toBeInTheDocument()
  })
})
