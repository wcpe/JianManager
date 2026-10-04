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
  useInstanceSearch: () => ({ data: { items: [], total: 0, page: 1, pageSize: 30 } }),
  useInstanceAggregate: () => ({ data: { total: 1, byStatus: { STOPPED: 1 }, byNode: [{ nodeId: 1, count: 1 }], byRole: {} } }),
  useInstance: () => ({ data: { id: 2, uuid: 'i-lobby', name: 'lobby-proxy', status: 'STOPPED', nodeId: 1, serverPort: 25577 } }),
  useSearchInstances: () => ({ data: { items: [], total: 0, page: 1, pageSize: 200 }, isLoading: false, isFetching: false }),
  useInstanceAggregate: () => ({ data: { total: 0, byStatus: { CRASHED: 0 }, byNode: [], byRole: {} } }),
  useStartInstance: () => ({ mutate: () => undefined }),
  useStopInstance: () => ({ mutate: () => undefined }),
  useRestartInstance: () => ({ mutate: () => undefined }),
  useKillInstance: () => ({ mutate: () => undefined }),
  // FR-342：实例控制台始终初始化重建 mutation，布局测试的整模块 mock 必须同步提供。
  useRebuildInstance: () => ({ mutate: () => undefined }),
  // FR-331：路由挂 InstanceConsolePage 时会调用，工厂 mock 需带上（恒 false=非搭建中）。
  isProvisioningInstance: () => false,
  // FR-313 崩溃诊断卡经 crashSnapshots.ts 引用该常量（真模块），工厂 mock 缺它会在
  // 懒加载片段落地时抛「No export is defined」偶发炸布局用例。
  INSTANCE_QUERY_GC_TIME_MS: 15 * 60_000,
}))
vi.mock('@/api/nodes', () => ({
  useNodes: () => ({ data: [{ id: 1, name: 'alpha', status: 1, diskUsage: 12 }] }),
}))
vi.mock('@/api/metrics', () => ({
  useMetricOverview: () => ({ data: { totals: { onlineNodeCount: 1, runningInstances: 2 } } }),
  useInstanceMetrics: () => ({ data: { tps: 19.8, msptMillis: 28, memoryMb: 2048, heapMaxMb: 4096, cpuPercent: 36, onlinePlayers: 12, probeAvailable: false } }),
  // FR-343：实例概览会请求 TPS 时序，布局测试返回空序列即可。
  useMetricSeries: () => ({ data: { series: [] } }),
}))
vi.mock('@/api/serverState', () => ({
  useServerState: () => ({ data: { connected: false, state: { server: { onlinePlayers: 12, maxPlayers: 200 } } } }),
}))
vi.mock('@/api/logs', () => ({
  useLogs: () => ({ data: { items: [] } }),
}))
vi.mock('@/api/tasks', () => ({
  useTasks: () => ({ data: { items: [], total: 0, limit: 100, offset: 0 } }),
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

  it('折叠只改侧栏自身状态：外壳不再有「锁定宽度 / 动画落位」的第二段状态机', async () => {
    const user = userEvent.setup()
    const { container } = renderWithProviders(<DashboardPage />, { route: '/instances/2' })

    // 先等待懒加载的实例控制台落地，避免侧栏断言抢在路由组件初始化前完成。
    await waitFor(() => expect(container.querySelector('[data-page="instance-console"]')).toBeInTheDocument(), { timeout: 5_000 })

    const shell = container.querySelector('[data-slot="console-shell"]') as HTMLElement
    const sidebar = container.querySelector('[data-slot="console-sidebar"]') as HTMLElement
    const content = container.querySelector('[data-slot="console-content"]') as HTMLElement

    // 外壳**不得**承载侧栏折叠态：一旦挂上 data-sidebar-target，根组件就必须订阅 sidebarCollapsed，
    // 于是点一次 logo 要 reconcile 整棵树（实测 532ms）。折叠态只由侧栏自身的 data-state 表达，
    // 顶栏品牌列的宽度由 index.css 的 :has() 从这里派生。此断言即该约束的守护。
    expect(shell).not.toHaveAttribute('data-sidebar-target')
    expect(sidebar).toHaveAttribute('data-state', 'expanded')
    expect(content).toHaveClass('jm-console-content')
    // FR-496 阶段 6 补丁：`data-sidebar-layout` / `data-sidebar-motion`（320ms 落位状态机）整段退场，
    // 宽度过渡改由 `.jm-console-sidebar` 自己承担（见 console-shell-motion.test.ts）。
    expect(shell).not.toHaveAttribute('data-sidebar-layout')
    expect(shell).not.toHaveAttribute('data-sidebar-motion')

    const collapseButtons = screen.getAllByRole('button', { name: '收起侧栏' })
    await user.click(collapseButtons[1]!)

    // 目标态与侧栏状态同帧切换：没有中间态，也就没有「两段式位移」。
    expect(shell).not.toHaveAttribute('data-sidebar-target')
    expect(sidebar).toHaveAttribute('data-state', 'collapsed')
    // 内容区只是同一 flex 行的兄弟节点，外壳不再给它加任何过渡/平移钩子。
    expect(shell).not.toHaveAttribute('data-sidebar-motion')
  })

  it('顶栏是唯一一条：工作区切换在顶栏内，不再另起一行', async () => {
    const { container } = renderWithProviders(<DashboardPage />, { route: '/instances' })

    const header = container.querySelector('[data-slot="console-header"]') as HTMLElement
    expect(header).toHaveClass('h-[53px]')
    expect(container.querySelector('[data-slot="console-workspace-bar"]')).toBeNull()
    expect(within(header).getByRole('navigation', { name: '工作区' })).toBeInTheDocument()
    // 顶栏不输出页名：页名只由内容页的大标题承担。
    expect(container.querySelector('[aria-label="breadcrumb"]')).toBeNull()
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
