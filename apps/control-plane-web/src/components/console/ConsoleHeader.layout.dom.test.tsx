import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, within } from '@testing-library/react'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import ConsoleHeader from './ConsoleHeader'

vi.mock('@/api/instances', () => ({
  useInstanceAggregate: () => ({ data: { total: 1, byStatus: { CRASHED: 0 }, byNode: [], byRole: {} } }),
  useSearchInstances: () => ({ data: { items: [], total: 0, page: 1, pageSize: 200 }, isLoading: false, isFetching: false }),
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

/**
 * 顶栏布局契约（方案 C / ADR-071；FR-496 阶段 6 补丁）。
 *
 * 补丁改变了顶栏的两件事，本文件锁住它们：
 * 1. **唯一一条 53px 通栏**——工作区切换并回顶栏（原型 `.topbar` 内含 `.space-nav`），
 *    不再有独立的 `console-workspace-bar` 行；
 * 2. **顶栏不输出页名/面包屑**——页名只由内容页 `PageHeader` 承担。
 */
describe('ConsoleHeader 布局', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('顶栏是唯一一条 53px 通栏：品牌区 + 工作区切换 + 操作区，且已下线节点作用域下拉', () => {
    const { container } = renderWithProviders(<ConsoleHeader />, { route: '/instances/1' })

    const header = container.querySelector('header') as HTMLElement
    expect(header).toHaveAttribute('data-slot', 'console-header')
    expect(header).toHaveClass('jm-console-header')
    // 原型顶栏实测 53px（`.topbar` 是 `#app` 的第一条网格行）。
    expect(header).toHaveClass('h-[53px]')
    // 方案 C：品牌 Logo 移入顶栏品牌区。
    expect(screen.getByText('JianManager')).toBeInTheDocument()
    // FR-496 阶段 6 补丁：工作区切换不再另起一行，顶栏内就有 `.space-nav`。
    expect(container.querySelector('[data-slot="console-workspace-bar"]')).toBeNull()
    const workspaces = within(header).getByRole('navigation', { name: '工作区' })
    expect(within(workspaces).getAllByRole('button').map((b) => b.textContent)).toEqual([
      '服务器运维',
      '观测与自动化',
      '运营与分发',
      '平台管理',
    ])
    // 操作区仍在顶栏内（搜索入口 + 账户菜单）。
    expect(within(header).getByRole('button', { name: /搜索/ })).toBeInTheDocument()
    expect(within(header).getByRole('button', { name: '账户' })).toBeInTheDocument()
    // FR-268 节点作用域下拉已下线。
    expect(screen.queryByRole('button', { name: '节点作用域' })).not.toBeInTheDocument()
    // 主题 / 配色切换仍只在侧栏底部，不在顶栏。
    expect(screen.queryByRole('button', { name: '切换主题' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Jian 绿' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '青绿' })).not.toBeInTheDocument()
  })

  it('顶栏不输出页名与面包屑：页名只由内容页承担（PageHeader）', () => {
    const { container } = renderWithProviders(<ConsoleHeader />, { route: '/instances/1' })

    const header = container.querySelector('header') as HTMLElement
    // FR-134 的顶栏面包屑整段移除：同页页名不再在顶栏与内容页各出现一次。
    expect(container.querySelector('[aria-label="breadcrumb"]')).toBeNull()
    expect(header.querySelector('[aria-label="breadcrumb"]')).toBeNull()
    // 顶栏里没有任何页面级大标题（品牌字标是无序列表之外的 h2，不属于页名）。
    expect(within(header).queryByRole('heading', { level: 1 })).toBeNull()
    // 面包屑的数据来源（实例名 / 频道名）也随之不再被顶栏消费。
    expect(header.textContent).not.toContain('survival-1')
    expect(header.textContent).not.toContain('空岛一区')
  })
})
