import { describe, it, expect, beforeEach } from 'vitest'
import { act, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { useConsoleStore } from '@/stores/console'
import { ALL_PERMISSION_NODE_IDS, DEFAULT_ROLE_NODES } from '@/lib/roles'
import ConsoleSidebar from './ConsoleSidebar'

/** 平台管理员登录（JWT + 权限 store 全开）。 */
function loginAsAdmin() {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
  loginMockUser(`mock.${payload}.sig`)
  usePermissionsStore.setState({
    nodes: new Set(ALL_PERMISSION_NODE_IDS),
    roleKey: 'platform_admin',
    isPlatformAdmin: true,
    loaded: true,
  })
}

function loginAsGroupAdmin() {
  const payload = btoa(JSON.stringify({ userId: 2, username: 'op', role: 1, exp: Math.floor(Date.now() / 1000) + 900 }))
  loginMockUser(`mock.${payload}.sig`)
  useAuthStore.setState({ role: 1 })
  usePermissionsStore.setState({
    nodes: new Set(DEFAULT_ROLE_NODES[1]),
    roleKey: 'group_admin',
    isPlatformAdmin: false,
    loaded: true,
  })
}

/**
 * ConsoleSidebar 六域 IA（FR-431）。
 */
describe('ConsoleSidebar 六域控制台 IA（FR-431）', () => {
  beforeEach(() => {
    loginAsAdmin()
    useConsoleStore.setState({ sidebarCollapsed: false, collapsedGroups: {} })
  })

  it('出现六域顶层 + 平台首页', () => {
    const { container } = renderWithProviders(<ConsoleSidebar />)
    expect(container.querySelector('aside')).toHaveAttribute('data-slot', 'console-sidebar')
    expect(screen.getByRole('link', { name: '平台首页' })).toHaveAttribute('href', '/')
    expect(screen.getByRole('button', { name: '服务器' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '群组网络' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '工作台' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '观测' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '客户端分发' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '平台设置' })).toBeInTheDocument()
  })

  it('观测域含通知中心，无告警、无 templates', () => {
    renderWithProviders(<ConsoleSidebar />)
    const obsGroup = screen.getByRole('button', { name: '观测' }).parentElement as HTMLElement
    expect(screen.getByRole('link', { name: '监控总览' })).toHaveAttribute('href', '/monitor')
    expect(within(obsGroup).getByRole('link', { name: '通知中心' })).toHaveAttribute('href', '/notifications')
    expect(within(obsGroup).queryByRole('link', { name: '告警' })).toBeNull()
    expect(within(obsGroup).queryByRole('link', { name: '模板' })).toBeNull()
  })

  it('工作台域含超级工作台与导播台', () => {
    renderWithProviders(<ConsoleSidebar />)
    const wb = screen.getByRole('button', { name: '工作台' }).parentElement as HTMLElement
    expect(within(wb).getByRole('link', { name: '超级工作台' })).toHaveAttribute('href', '/super')
    expect(within(wb).getByRole('link', { name: '导播台' })).toHaveAttribute('href', '/director')
  })

  it('客户端分发域独立，templates 在平台设置', () => {
    renderWithProviders(<ConsoleSidebar />)
    const client = screen.getByRole('button', { name: '客户端分发' }).parentElement as HTMLElement
    expect(within(client).getByRole('link', { name: '客户端分发' })).toHaveAttribute('href', '/client-channels')
    expect(within(client).getByRole('link', { name: '客户端分发运维' })).toHaveAttribute('href', '/client-dist-ops')

    const settings = screen.getByRole('button', { name: '平台设置' }).parentElement as HTMLElement
    expect(within(settings).getByRole('link', { name: '模板' })).toHaveAttribute('href', '/templates')
    expect(within(settings).getByRole('link', { name: '权限配置' })).toHaveAttribute('href', '/permissions')
  })

  it('平台设置含系统维护与 Agent（管理员）', () => {
    renderWithProviders(<ConsoleSidebar />)
    const settings = screen.getByRole('button', { name: '平台设置' }).parentElement as HTMLElement
    expect(within(settings).getByRole('link', { name: '数据库' })).toHaveAttribute('href', '/database')
    expect(within(settings).getByRole('link', { name: '系统更新' })).toHaveAttribute('href', '/system-update')
    expect(within(settings).getByRole('link', { name: 'Agent Token' })).toHaveAttribute('href', '/agent-tokens')
  })

  it('group_admin 按权限种子隐藏系统/用户/客户端分发', () => {
    loginAsGroupAdmin()
    renderWithProviders(<ConsoleSidebar />)
    expect(screen.queryByRole('button', { name: '客户端分发' })).toBeNull()
    expect(screen.queryByRole('link', { name: '数据库' })).toBeNull()
    expect(screen.queryByRole('link', { name: '权限配置' })).toBeNull()
    expect(screen.queryByRole('link', { name: '用户' })).toBeNull()
    expect(screen.queryByRole('link', { name: 'Agent Token' })).toBeNull()
    // group_admin 仍有服务器域与组内可读的备份/任务入口
    expect(screen.getByRole('button', { name: '服务器' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '备份' })).toHaveAttribute('href', '/backups')
  })

  it('侧栏宽度动画由专用 drawer CSS 管理', () => {
    const { container } = renderWithProviders(<ConsoleSidebar />)
    const aside = container.querySelector('aside') as HTMLElement
    const drawer = container.querySelector('[data-slot="sidebar-drawer"]') as HTMLElement
    expect(aside).toHaveClass('jm-console-sidebar')
    expect(drawer).toHaveClass('jm-sidebar-drawer')
    expect(aside).toHaveClass('shrink-0')
  })

  it('侧栏折叠/展开暴露抽屉动画状态', () => {
    const { container } = renderWithProviders(<ConsoleSidebar />)
    const aside = container.querySelector('aside') as HTMLElement
    expect(aside).toHaveAttribute('data-state', 'expanded')
    act(() => useConsoleStore.getState().toggleSidebar())
    expect(aside).toHaveAttribute('data-state', 'collapsed')
  })

  it('分组抽屉收缩时保留可动画内容容器', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ConsoleSidebar />)
    const serversHeader = screen.getByRole('button', { name: '服务器' })
    const serversGroup = serversHeader.parentElement as HTMLElement
    const content = serversGroup.querySelector('[data-slot="sidebar-nav-group-content"]') as HTMLElement
    expect(content).toHaveAttribute('data-state', 'open')
    await user.click(serversHeader)
    expect(content).toHaveAttribute('data-state', 'closed')
    expect(content).toHaveClass('jm-sidebar-group-content')
  })
})

/**
 * 折叠态分类图标导航（FR-332）。
 */
describe('折叠态分类图标导航（FR-332）', () => {
  beforeEach(() => {
    loginAsAdmin()
    useConsoleStore.setState({ sidebarCollapsed: true, collapsedGroups: {} })
  })

  it('分类图标渲染为链接，指向分类下第一个可见页面', () => {
    renderWithProviders(<ConsoleSidebar />)
    expect(screen.getByRole('link', { name: '服务器' })).toHaveAttribute('href', '/instances')
    expect(screen.getByRole('link', { name: '群组网络' })).toHaveAttribute('href', '/networks/topology')
    expect(screen.getByRole('link', { name: '工作台' })).toHaveAttribute('href', '/super')
    expect(screen.getByRole('link', { name: '观测' })).toHaveAttribute('href', '/monitor')
    expect(screen.getByRole('link', { name: '客户端分发' })).toHaveAttribute('href', '/client-channels')
    expect(screen.getByRole('link', { name: '平台设置' })).toHaveAttribute('href', '/users')
    expect(screen.getByRole('link', { name: '平台首页' })).toHaveAttribute('href', '/')
  })

  it('分类下任意子路由命中时分类图标显示激活态', () => {
    renderWithProviders(<ConsoleSidebar />, { route: '/logs' })
    expect(screen.getByRole('link', { name: '观测' })).toHaveAttribute('data-active', 'true')
    expect(screen.getByRole('link', { name: '服务器' })).toHaveAttribute('data-active', 'false')
  })

  it('点击分类图标路由跳到第一子页并点亮激活态，侧栏保持折叠', async () => {
    const user = userEvent.setup()
    const { container } = renderWithProviders(<ConsoleSidebar />)
    await user.click(screen.getByRole('link', { name: '观测' }))
    expect(window.location.pathname).toBe('/monitor')
    expect(screen.getByRole('link', { name: '观测' })).toHaveAttribute('data-active', 'true')
    expect(container.querySelector('aside')).toHaveAttribute('data-state', 'collapsed')
  })
})
