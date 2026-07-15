import { describe, it, expect, beforeEach } from 'vitest'
import { act, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useAuthStore } from '@/stores/auth'
import { useConsoleStore } from '@/stores/console'
import ConsoleSidebar from './ConsoleSidebar'

/**
 * ConsoleSidebar 信息架构（FR-268）：平台首页 / 服务器 / 群组网络 / 观测 / 平台管理。
 * 单服能力收进服务器控制台，侧栏只保留跨服务器与平台级入口。
 * 验侧栏渲染出正确 IA、链接指向正确路由。
 */
describe('ConsoleSidebar 高密度控制台 IA（FR-268）', () => {
  beforeEach(() => {
    // 已登录态，避免鉴权刷新干扰 IA 断言。
    loginMockUser()
    // 展开态；新 IA 不再在侧栏内嵌实例树。
    useConsoleStore.setState({ sidebarCollapsed: false, collapsedGroups: {} })
    // 普通用户即可（平台管理员仅额外注入 数据库/系统更新，不影响本 IA 断言）。
    useAuthStore.setState({ role: 1 })
  })

  it('出现五个一级域，且不再有一级「监控」域名', () => {
    const { container } = renderWithProviders(<ConsoleSidebar />)
    expect(container.querySelector('aside')).toHaveAttribute('data-slot', 'console-sidebar')
    expect(container.querySelector('aside')).toHaveClass('jm-console-sidebar')
    expect(screen.getByRole('link', { name: '平台首页' })).toHaveAttribute('href', '/')
    expect(screen.getByRole('button', { name: '服务器' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '群组网络' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '观测' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '平台管理' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '监控' })).toBeNull()
  })

  it('观测域下含 监控总览/客户端分发监控/日志中心/统计分析 子项，链接正确', () => {
    renderWithProviders(<ConsoleSidebar />)
    expect(screen.getByRole('link', { name: '监控总览' })).toHaveAttribute('href', '/monitor')
    expect(screen.getByRole('link', { name: '客户端分发监控' })).toHaveAttribute('href', '/client-dist-monitor')
    expect(screen.getByRole('link', { name: '日志中心' })).toHaveAttribute('href', '/logs')
    expect(screen.getByRole('link', { name: '统计分析' })).toHaveAttribute('href', '/statistics')
  })

  it('FR-272 页面在桌面侧栏归入指定导航域', () => {
    renderWithProviders(<ConsoleSidebar />)
    const serversGroup = screen.getByRole('button', { name: '服务器' }).parentElement as HTMLElement
    const platformGroup = screen.getByRole('button', { name: '平台管理' }).parentElement as HTMLElement

    expect(within(serversGroup).getByRole('link', { name: '玩家' })).toHaveAttribute('href', '/players')
    expect(within(serversGroup).getByRole('link', { name: 'Bot' })).toHaveAttribute('href', '/bots')
    expect(within(platformGroup).getByRole('link', { name: '定时任务' })).toHaveAttribute('href', '/schedules')
    expect(within(platformGroup).getByRole('link', { name: '备份' })).toHaveAttribute('href', '/backups')
  })

  it('任务中心归「平台管理」，不在「观测」域', () => {
    renderWithProviders(<ConsoleSidebar />)
    const tasks = screen.getByRole('link', { name: '任务中心' })
    expect(tasks).toHaveAttribute('href', '/tasks')

    const obsHeader = screen.getByRole('button', { name: '观测' })
    const obsGroup = obsHeader.parentElement as HTMLElement
    expect(within(obsGroup).queryByRole('link', { name: '任务中心' })).toBeNull()

    const platformHeader = screen.getByRole('button', { name: '平台管理' })
    const platformGroup = platformHeader.parentElement as HTMLElement
    expect(within(platformGroup).getByRole('link', { name: '任务中心' })).toBeInTheDocument()
  })

  it('FR-112 平台级入口收进平台管理，URL 保持不变', () => {
    renderWithProviders(<ConsoleSidebar />)
    const platformHeader = screen.getByRole('button', { name: '平台管理' })
    const platformGroup = platformHeader.parentElement as HTMLElement

    expect(within(platformGroup).getByRole('link', { name: '运行时资产' })).toHaveAttribute('href', '/runtime-assets')
    expect(within(platformGroup).getByRole('link', { name: '客户端分发' })).toHaveAttribute('href', '/client-channels')
    expect(within(platformGroup).getByRole('link', { name: '存储' })).toHaveAttribute('href', '/storage')
    expect(within(platformGroup).getByRole('link', { name: '用户' })).toHaveAttribute('href', '/users')
    expect(within(platformGroup).getByRole('link', { name: '用户组' })).toHaveAttribute('href', '/groups')
    expect(within(platformGroup).getByRole('link', { name: '审计日志' })).toHaveAttribute('href', '/audit')
    expect(within(platformGroup).getByRole('link', { name: '设置' })).toHaveAttribute('href', '/settings')
  })

  it('FR-112 数据库和系统更新仅平台管理员可见', () => {
    renderWithProviders(<ConsoleSidebar />)
    expect(screen.queryByRole('link', { name: '数据库' })).toBeNull()
    expect(screen.queryByRole('link', { name: '系统更新' })).toBeNull()

    useAuthStore.setState({ role: 10 })
    renderWithProviders(<ConsoleSidebar />)
    const platformHeader = screen.getAllByRole('button', { name: '平台管理' }).at(-1) as HTMLElement
    const platformGroup = platformHeader.parentElement as HTMLElement

    expect(within(platformGroup).getByRole('link', { name: '数据库' })).toHaveAttribute('href', '/database')
    expect(within(platformGroup).getByRole('link', { name: '系统更新' })).toHaveAttribute('href', '/system-update')
  })

  it('观测域不再有「告警」项（FR-216 收口）', () => {
    renderWithProviders(<ConsoleSidebar />)
    const obsHeader = screen.getByRole('button', { name: '观测' })
    const obsGroup = obsHeader.parentElement as HTMLElement
    expect(within(obsGroup).queryByRole('link', { name: '告警' })).toBeNull()
  })

  it('通知中心入口落「平台管理 / 任务与通知」，链接 /notifications', () => {
    renderWithProviders(<ConsoleSidebar />)
    const notif = screen.getByRole('link', { name: '通知中心' })
    expect(notif).toHaveAttribute('href', '/notifications')

    const platformHeader = screen.getByRole('button', { name: '平台管理' })
    const platformGroup = platformHeader.parentElement as HTMLElement
    expect(within(platformGroup).getByRole('link', { name: '通知中心' })).toBeInTheDocument()

    const obsHeader = screen.getByRole('button', { name: '观测' })
    const obsGroup = obsHeader.parentElement as HTMLElement
    expect(within(obsGroup).queryByRole('link', { name: '通知中心' })).toBeNull()
  })

  it('群组网络两个子入口只高亮当前路由，避免 /networks 与 /networks/topology 双选中', () => {
    renderWithProviders(<ConsoleSidebar />, { route: '/networks/topology' })

    expect(screen.getByRole('link', { name: '网络拓扑' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('link', { name: '分组管理' })).not.toHaveAttribute('aria-current')
  })

  it('实例详情路由会点亮服务器分组和全部服务器入口', () => {
    renderWithProviders(<ConsoleSidebar />, { route: '/instances/2' })

    expect(screen.getByRole('button', { name: '服务器' })).toHaveAttribute('data-active', 'true')
    expect(screen.getByRole('link', { name: '全部服务器' })).toHaveAttribute('aria-current', 'page')
  })

  it('/networks 只高亮分组管理，不高亮网络拓扑', () => {
    renderWithProviders(<ConsoleSidebar />, { route: '/networks' })

    expect(screen.getByRole('link', { name: '分组管理' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('link', { name: '网络拓扑' })).not.toHaveAttribute('aria-current')
  })

  it('颜色调节和主题切换保留在侧栏底部原位置', () => {
    renderWithProviders(<ConsoleSidebar />)

    expect(screen.getByText('主题色')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Jian 绿' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '青绿' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '切换主题' })).toBeInTheDocument()
  })

  it('侧栏宽度动画由专用 drawer CSS 管理', () => {
    const { container } = renderWithProviders(<ConsoleSidebar />)
    const aside = container.querySelector('aside') as HTMLElement
    const drawer = container.querySelector('[data-slot="sidebar-drawer"]') as HTMLElement

    expect(aside).toHaveClass('jm-console-sidebar')
    expect(drawer).toHaveClass('jm-sidebar-drawer')
    expect(aside).toHaveClass('shrink-0')
    expect(aside.className).not.toContain('transition-[width]')
    expect(aside.className).not.toContain('duration-200')
  })

  it('侧栏折叠/展开暴露抽屉动画状态', () => {
    const { container } = renderWithProviders(<ConsoleSidebar />)
    const aside = container.querySelector('aside') as HTMLElement

    expect(aside).toHaveAttribute('data-state', 'expanded')
    expect(container.querySelector('[data-slot="sidebar-drawer"]')).toHaveClass('jm-sidebar-drawer')

    // 折叠开关随方案 C 移入顶栏品牌区；侧栏单测直接驱动 store 验证抽屉状态回填。
    act(() => useConsoleStore.getState().toggleSidebar())

    expect(aside).toHaveAttribute('data-state', 'collapsed')
    expect(container.querySelector('[data-slot="sidebar-drawer"]')).toHaveAttribute('data-state', 'collapsed')
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
 * 折叠态分类图标导航（FR-332）：折叠图标轨下点分类图标不再是「无反应」的展开按钮，
 * 而是导航到该分类下第一个可见页面（groups 已按角色裁剪）并点亮激活态；展开态分类头
 * 仍是纯分组开关（上方既有用例覆盖，行为不变）。
 */
describe('折叠态分类图标导航（FR-332）', () => {
  beforeEach(() => {
    loginMockUser()
    useConsoleStore.setState({ sidebarCollapsed: true, collapsedGroups: {} })
    useAuthStore.setState({ role: 1 })
  })

  it('分类图标渲染为链接，指向分类下第一个可见页面；leaf 链接不变', () => {
    renderWithProviders(<ConsoleSidebar />)

    expect(screen.getByRole('link', { name: '服务器' })).toHaveAttribute('href', '/instances')
    expect(screen.getByRole('link', { name: '群组网络' })).toHaveAttribute('href', '/networks/topology')
    expect(screen.getByRole('link', { name: '观测' })).toHaveAttribute('href', '/monitor')
    expect(screen.getByRole('link', { name: '平台管理' })).toHaveAttribute('href', '/templates')
    expect(screen.getByRole('link', { name: '平台首页' })).toHaveAttribute('href', '/')
  })

  it('分类下任意子路由命中时分类图标显示激活态', () => {
    renderWithProviders(<ConsoleSidebar />, { route: '/logs' })

    expect(screen.getByRole('link', { name: '观测' })).toHaveAttribute('data-active', 'true')
    expect(screen.getByRole('link', { name: '服务器' })).toHaveAttribute('data-active', 'false')
  })

  // 异步用例放本文件末尾：让前面同步用例的在途 /nodes 查询有机会落地（同 setup.ts 拆卸注释）。
  it('点击分类图标路由跳到第一子页并点亮激活态，侧栏保持折叠', async () => {
    const user = userEvent.setup()
    const { container } = renderWithProviders(<ConsoleSidebar />)

    await user.click(screen.getByRole('link', { name: '观测' }))

    expect(window.location.pathname).toBe('/monitor')
    expect(screen.getByRole('link', { name: '观测' })).toHaveAttribute('data-active', 'true')
    expect(container.querySelector('aside')).toHaveAttribute('data-state', 'collapsed')
  })
})
