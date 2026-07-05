import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import DashboardPage from './DashboardPage'

vi.mock('@/api/events', () => ({
  useInstanceEvents: () => undefined,
}))
vi.mock('@/api/instances', () => ({
  useInstances: () => ({ data: [{ id: 2, uuid: 'i-lobby', name: 'lobby-proxy', status: 'STOPPED' }] }),
  useInstance: () => ({ data: { id: 2, uuid: 'i-lobby', name: 'lobby-proxy', status: 'STOPPED', nodeId: 1, serverPort: 25577 } }),
  useStartInstance: () => ({ mutate: () => undefined }),
  useStopInstance: () => ({ mutate: () => undefined }),
  useRestartInstance: () => ({ mutate: () => undefined }),
  useKillInstance: () => ({ mutate: () => undefined }),
}))
vi.mock('@/api/nodes', () => ({
  useNodes: () => ({ data: [{ id: 1, name: 'alpha', status: 1, diskUsage: 12 }] }),
}))
vi.mock('@/api/metrics', () => ({
  useMetricOverview: () => ({ data: { totals: { onlineNodeCount: 1, runningInstances: 2 } } }),
  useInstanceMetrics: () => ({ data: { tps: 19.8, msptMillis: 28, memoryMb: 2048, heapMaxMb: 4096, cpuPercent: 36, onlinePlayers: 12, probeAvailable: false } }),
}))
vi.mock('@/api/serverState', () => ({
  useServerState: () => ({ data: { connected: false, state: { server: { onlinePlayers: 12, maxPlayers: 200 } } } }),
}))
vi.mock('@/api/logs', () => ({
  useLogs: () => ({ data: { items: [] } }),
}))
vi.mock('@/api/tasks', () => ({
  useTasks: () => ({ data: [] }),
}))
vi.mock('@/api/notification-feed', () => ({
  useFeedUnreadCount: () => ({ data: 0 }),
  useNotificationFeed: () => ({ data: { items: [] } }),
}))

/** 控制台 Shell 宽屏布局回归：外壳和工作区必须显式吃满可用宽度。 */
describe('DashboardPage 宽屏布局', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('Shell、右列和工作区容器显式满宽，避免宽屏右侧留白', () => {
    const { container } = renderWithProviders(<DashboardPage />, { route: '/instances/2' })

    const shell = container.firstElementChild as HTMLElement
    const mainColumn = shell.querySelector('aside')?.nextElementSibling as HTMLElement
    const main = shell.querySelector('main') as HTMLElement

    expect(shell).toHaveClass('w-screen')
    expect(shell).toHaveClass('overflow-hidden')
    expect(shell).toHaveAttribute('data-slot', 'console-shell')
    expect(mainColumn).toHaveClass('w-full')
    expect(main).toHaveClass('w-full')
    expect(main).toHaveAttribute('data-slot', 'console-main')
  })

  it('桌面侧栏切换期间锁定布局宽度，动画结束后再落位', async () => {
    const user = userEvent.setup()
    const { container } = renderWithProviders(<DashboardPage />, { route: '/instances/2' })

    const shell = container.querySelector('[data-slot="console-shell"]') as HTMLElement
    const content = container.querySelector('[data-slot="console-content"]') as HTMLElement

    expect(shell).toHaveAttribute('data-sidebar-layout', 'expanded')
    expect(shell).toHaveAttribute('data-sidebar-motion', 'idle')
    expect(content).toHaveClass('jm-console-content')

    const collapseButtons = screen.getAllByRole('button', { name: '收起侧栏' })
    await user.click(collapseButtons[1]!)

    expect(shell).toHaveAttribute('data-sidebar-target', 'collapsed')
    expect(shell).toHaveAttribute('data-sidebar-layout', 'expanded')
    expect(shell).toHaveAttribute('data-sidebar-motion', 'collapsing')

    await waitFor(() => {
      expect(shell).toHaveAttribute('data-sidebar-layout', 'collapsed')
      expect(shell).toHaveAttribute('data-sidebar-motion', 'idle')
    })
  })

  it('移动端提供可见导航入口，并能展开导航面板', async () => {
    const user = userEvent.setup()
    renderWithProviders(<DashboardPage />, { route: '/instances' })

    const mobileNav = screen.getByLabelText('移动导航')
    expect(mobileNav).toHaveAttribute('data-slot', 'mobile-console-nav')

    await user.click(within(mobileNav).getByRole('button', { name: '服务器' }))
    expect(mobileNav.querySelector('[data-slot="mobile-nav-panel"]')).toHaveAttribute('data-state', 'open')
    expect(within(mobileNav).getByRole('link', { name: '全部服务器' })).toBeInTheDocument()
    expect(within(mobileNav).getByRole('link', { name: '节点' })).toBeInTheDocument()
  })

  it('移动端导航面板收起时保留关闭动画状态', async () => {
    const user = userEvent.setup()
    renderWithProviders(<DashboardPage />, { route: '/instances' })

    const mobileNav = screen.getByLabelText('移动导航')
    await user.click(within(mobileNav).getByRole('button', { name: '服务器' }))

    const panel = mobileNav.querySelector('[data-slot="mobile-nav-panel"]') as HTMLElement
    expect(panel).toHaveAttribute('data-state', 'open')

    await user.click(within(mobileNav).getByRole('button', { name: '收起移动导航' }))

    expect(panel).toHaveAttribute('data-state', 'closed')
  })

  it('移动端群组网络面板只点亮当前子页面', async () => {
    const user = userEvent.setup()
    renderWithProviders(<DashboardPage />, { route: '/networks/topology' })

    const mobileNav = screen.getByLabelText('移动导航')
    await user.click(within(mobileNav).getByRole('button', { name: '群组网络' }))

    expect(within(mobileNav).getByRole('link', { name: '网络拓扑' })).toHaveAttribute('aria-current', 'page')
    expect(within(mobileNav).getByRole('link', { name: '分组管理' })).not.toHaveAttribute('aria-current')
  })
})
