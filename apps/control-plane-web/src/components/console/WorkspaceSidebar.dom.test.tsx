import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { useConsoleStore } from '@/stores/console'
import { ALL_PERMISSION_NODE_IDS, DEFAULT_ROLE_NODES } from '@/lib/roles'
import WorkspaceSidebar from './WorkspaceSidebar'

/**
 * 数据源整模块 mock（FR-496 阶段 6）：侧栏的资源树 / 分类计数 / 群组列表都来自这些 hook，
 * 用固定小数据集断言结构，不依赖假后端的规模与排序（那些由各页自己的 dom 用例守）。
 */
vi.mock('@/api/nodes', () => ({
  useNodes: () => ({
    data: [
      { id: 1, name: 'node-main', status: 1 },
      { id: 2, name: 'node-east', status: 0 },
    ],
  }),
}))
vi.mock('@/api/instances', () => ({
  useInstanceAggregate: () => ({
    data: { total: 42, byStatus: {}, byRole: {}, byNode: [{ nodeId: 1, count: 30 }, { nodeId: 2, count: 12 }] },
  }),
  useInstanceSearch: () => ({
    data: {
      items: [
        { id: 7, nodeId: 1, name: 'lobby-01', status: 'RUNNING' },
        { id: 8, nodeId: 1, name: 'survival-01', status: 'CRASHED' },
        { id: 9, nodeId: 2, name: 'mini-01', status: 'STOPPED' },
      ],
      total: 3,
      page: 1,
      pageSize: 100,
    },
  }),
  useSearchInstances: () => ({ data: { items: [], total: 0, page: 1, pageSize: 200 } }),
}))
vi.mock('@/api/networks', () => ({
  useNetworks: () => ({
    data: [
      {
        id: 5,
        uuid: 'n-5',
        name: '生存群组',
        description: '',
        memberCount: 12,
        memberStatus: { running: 11, stopped: 0, crashed: 1, starting: 0, stopping: 0 },
        createdAt: '',
        updatedAt: '',
      },
    ],
  }),
}))

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

/** 用户组管理员登录：权限按种子收敛（无 node.read / user.read / system.*）。 */
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
 * 新工作区侧栏（FR-496 阶段 6）。
 *
 * 只锁**结构与行为契约**（四段槽位、权限裁剪、激活判定、跳转目标、折叠态），不测视觉：
 * 这些契约是阶段 7 把其余页面迁到新外壳、以及后续删 `nav-config.ts` 六域定义时的安全网。
 *
 * FR-496 阶段 6 补丁：工作区切换从本模块的 `ConsoleWorkspaceBar` 并回顶栏，
 * 那组用例随之迁到 `ConsoleHeader.workspaces.dom.test.tsx`（本文件只管侧栏自身）。
 * 折叠动画的契约也一并换真源：宽度过渡现在只作用于 `.jm-console-sidebar` 自身，
 * 由 `console-shell-motion.test.ts` 守（jsdom 没有样式计算，本文件只断言折叠态结构）。
 */
describe('WorkspaceSidebar 工作区侧栏（FR-496 阶段 6）', () => {
  beforeEach(() => {
    loginAsAdmin()
    useConsoleStore.setState({ sidebarCollapsed: false, lastWorkspaceKey: null })
  })

  it('四段依次为：工作区首段 / 固定入口 / 资源区 / 底部常驻', () => {
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const aside = container.querySelector('[data-slot="console-sidebar"]') as HTMLElement

    // 沿用旧壳的槽位名与类：宽度动画（jm-*）与折叠态 CSS 仍按这套钩子工作。
    expect(aside).toHaveClass('jm-console-sidebar')
    // 基准宽度照原型（展开 246px / 折叠 54px），由 ui 包 `SideNav` 给出；
    // 外壳另把 CSS 变量对齐到同值（见 `DashboardPage` 的 SIDEBAR_WIDTH_VARS）。
    expect(aside).toHaveClass('w-[246px]')
    expect(aside).toHaveAttribute('data-state', 'expanded')
    expect(aside.querySelector('[data-slot="sidebar-drawer"]')).toHaveClass('jm-sidebar-drawer')

    const surface = aside.querySelector('[data-slot="console-sidebar-surface"]') as HTMLElement
    const head = surface.querySelector('[data-slot="side-nav-head"]') as HTMLElement
    const shortcuts = surface.querySelector('[data-slot="side-nav-shortcuts"]') as HTMLElement
    const resource = surface.querySelector('[data-slot="side-nav-resource"]') as HTMLElement
    const bottom = surface.querySelector('[data-slot="side-nav-bottom"]') as HTMLElement

    // 顺序即原型 `.sidebar` 的 flex 列顺序，四段缺一不可。
    expect([head, shortcuts, resource, bottom].every(Boolean)).toBe(true)
    expect(head.compareDocumentPosition(shortcuts) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(shortcuts.compareDocumentPosition(resource) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(resource.compareDocumentPosition(bottom) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

    // 首段：工作区名 + 「资源视图」pill（pill 只给服务器运维）。
    expect(within(head).getByText('服务器运维')).toBeInTheDocument()
    expect(within(head).getByText('资源视图')).toBeInTheDocument()
    expect(aside).toHaveAttribute('aria-label', '服务器运维导航')
  })

  it('固定入口来自真实登记：平台首页 / 全部服务器 / 节点，实例与节点带计数', () => {
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const shortcuts = container.querySelector('[data-slot="side-nav-shortcuts"]') as HTMLElement

    // 导航行现在包在 `<Link>` 里，所以角色是 link 而非 button ——这正是本次要修的语义：
    // 真 `<a href>` 才有中键 / 新标签页 / 复制链接地址。
    expect(within(shortcuts).getByRole('link', { name: /平台首页/ })).toBeInTheDocument()
    // 计数口径：实例总数取聚合（42），节点数取节点列表长度（2）。
    expect(within(shortcuts).getByRole('link', { name: /全部服务器/ })).toHaveTextContent('42')
    expect(within(shortcuts).getByRole('link', { name: /节点/ })).toHaveTextContent('2')
  })

  it('底部常驻是「群组与工作台」分组的四个真实目的地，并保留旧侧栏的激活态语义', () => {
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/networks/topology' })
    const bottom = container.querySelector('[data-slot="side-nav-bottom"]') as HTMLElement

    // `aria-current` 落在 `<a>` 上（行组件是 `<div>`，语义要有可聚焦元素承载才生效）。
    expect(within(bottom).getByRole('link', { name: /网络拓扑/ })).toHaveAttribute('aria-current', 'page')
    expect(within(bottom).getByRole('link', { name: /分组管理/ })).toBeInTheDocument()
    expect(within(bottom).getByRole('link', { name: /超级工作台/ })).toBeInTheDocument()
    expect(within(bottom).getByRole('link', { name: /导播台/ })).toBeInTheDocument()
  })

  it('资源区：三段切换 + 常驻「选择服务器」，按节点段渲染节点/实例两级树', () => {
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const resource = container.querySelector('[data-slot="side-nav-resource"]') as HTMLElement
    const segments = resource.querySelector('[data-slot="resource-nav-segments"]') as HTMLElement

    expect(within(segments).getByRole('button', { name: '按节点' })).toHaveAttribute('aria-pressed', 'true')
    expect(within(segments).getByRole('button', { name: '按群组' })).toBeInTheDocument()
    expect(within(segments).getByRole('button', { name: '收藏' })).toBeInTheDocument()
    // 跨全量检索入口：旧侧栏的 ServerSelector 原样保留（FR-240/293）。
    expect(within(resource).getByRole('button', { name: '选择服务器' })).toBeInTheDocument()

    const tree = resource.querySelector('[data-slot="resource-tree"]') as HTMLElement
    expect(within(tree).getByRole('button', { name: '展开 node-main' })).toBeInTheDocument()
    expect(within(tree).getByText('node-main')).toBeInTheDocument()
    expect(within(tree).getByText('node-east')).toBeInTheDocument()
  })

  it('资源树：展开节点列实例，点节点进节点页、点实例进实例控制台、其余折叠成列表页', async () => {
    const user = userEvent.setup()
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const tree = container.querySelector('[data-slot="resource-tree"]') as HTMLElement

    await user.click(within(tree).getByRole('button', { name: '展开 node-main' }))
    expect(within(tree).getByText('lobby-01')).toBeInTheDocument()
    // 30 − 已列 2 = 28：计数用聚合总数，链接要如实反映服务器上还有多少。
    expect(within(tree).getByRole('button', { name: '查看其余 28 个实例' })).toBeInTheDocument()

    await user.click(within(tree).getByText('lobby-01'))
    expect(window.location.pathname).toBe('/instances/7')

    await user.click(within(tree).getByRole('button', { name: 'node-east' }))
    expect(window.location.pathname).toBe('/nodes')
    expect(window.location.search).toBe('?node=2')

    await user.click(within(tree).getByRole('button', { name: '查看其余 28 个实例' }))
    expect(window.location.pathname).toBe('/instances')
    expect(window.location.search).toBe('?nodeId=1')
  })

  it('资源区：按群组段列群组成员数与健康态，点击进按群组过滤的实例列表', async () => {
    const user = userEvent.setup()
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const resource = container.querySelector('[data-slot="side-nav-resource"]') as HTMLElement

    await user.click(within(resource).getByRole('button', { name: '按群组' }))
    const groups = resource.querySelector('[data-slot="resource-nav-groups"]') as HTMLElement
    const row = within(groups).getByRole('button', { name: /生存群组/ })
    expect(row).toHaveTextContent('12')

    await user.click(row)
    expect(window.location.pathname).toBe('/instances')
    expect(window.location.search).toBe('?networkId=5')
  })

  it('资源区：收藏段复用常驻服务器列（收藏 / 最近打开），无数据时给空态', async () => {
    const user = userEvent.setup()
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })

    await user.click(within(container).getByRole('button', { name: '收藏' }))
    const favorites = container.querySelector('[data-slot="resource-nav-favorites"]') as HTMLElement
    expect(within(favorites).getByTestId('sidebar-server-list')).toBeInTheDocument()
    expect(within(favorites).getByText(/暂无常用服务器/)).toBeInTheDocument()
  })

  it('折叠态：54px 图标轨，资源区与首段隐去，固定入口仍在', () => {
    useConsoleStore.setState({ sidebarCollapsed: true })
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const aside = container.querySelector('[data-slot="console-sidebar"]') as HTMLElement

    expect(aside).toHaveAttribute('data-state', 'collapsed')
    expect(aside).toHaveAttribute('data-collapsed', 'true')
    expect(aside).toHaveClass('w-[54px]')
    // 折叠态靠 ui 包的行/段样式收敛（SideNav 通过 context 下发），本文件不自己改宽度。
    const head = aside.querySelector('[data-slot="side-nav-head"]') as HTMLElement
    const resource = aside.querySelector('[data-slot="side-nav-resource"]') as HTMLElement
    // 头段收起方式：grid-rows 0fr + 淡出（不是 `hidden` 瞬跳），与侧栏宽度过渡同节拍。
    expect(head).toHaveClass('grid-rows-[0fr]')
    // 资源区收起方式同头段：grid-rows 0fr + 淡出（`hidden` 不可过渡，会触发二次布局）。
    expect(resource).toHaveClass('grid-rows-[0fr]')
    // 图标轨行仍可点、仍有名字（aria-label 兜住被隐藏的文字节点）。
    // 角色是 link：导航行现在包在 `<Link>` 里，真 `<a href>` 才有中键 / 新标签页语义。
    expect(screen.getByRole('link', { name: '平台首页' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '全部服务器' })).toBeInTheDocument()
  })

  it('权限裁剪与旧侧栏同语义：无 node.read 就不出现节点入口与「按节点」段', () => {
    loginAsGroupAdmin()
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })

    expect(screen.queryByText('节点')).toBeNull()
    expect(container.querySelector('[data-slot="resource-tree"]')).toBeNull()
    // 段可见性收敛：按节点（需 node.read）消失，按群组与收藏仍在，缺省段随之落到按群组。
    const segments = container.querySelector('[data-slot="resource-nav-segments"]') as HTMLElement
    expect(within(segments).queryByRole('button', { name: '按节点' })).toBeNull()
    expect(within(segments).getByRole('button', { name: '按群组' })).toHaveAttribute('aria-pressed', 'true')
    // 组内可读的入口照常保留（服务器运维域仍可进实例、群组与工作台）。
    expect(screen.getByRole('link', { name: /全部服务器/ })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /网络拓扑/ })).toBeInTheDocument()
  })

  it('底部偏好块（所有工作区常驻）：主题切换 / 版本号 / 开源许可——旧侧栏的唯一入口不能丢', () => {
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/users' })

    const bottom = container.querySelector('[data-slot="side-nav-bottom"]') as HTMLElement
    const preferences = bottom.querySelector('[data-slot="console-sidebar-preferences"]') as HTMLElement
    expect(preferences).toBeInTheDocument()
    // FR-164：主题色与明暗切换当前只在侧栏底部（/settings 的「外观」明说不重复主题色）。
    expect(within(preferences).getByRole('button', { name: '切换主题' })).toBeInTheDocument()
    // FR-132：版本号 + 开源许可入口。
    expect(within(preferences).getByText(/^v\d/)).toBeInTheDocument()
    expect(within(preferences).getByRole('link', { name: '开源许可' })).toHaveAttribute('href', '/licenses')
  })

  it('非服务器运维工作区：按分组列出该工作区全部入口（平台管理分组）', () => {
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/users' })

    const head = container.querySelector('[data-slot="side-nav-head"]') as HTMLElement
    expect(within(head).getByText('平台管理')).toBeInTheDocument()
    // 非运维工作区没有「资源视图」pill（原型同款）。
    expect(within(head).queryByText('资源视图')).toBeNull()

    const groups = container.querySelector('[data-slot="console-sidebar-groups"]') as HTMLElement
    expect(within(groups).getByText('身份与权限')).toBeInTheDocument()
    expect(within(groups).getByText('系统维护')).toBeInTheDocument()
    // 导航行是 link；`aria-current` 落在 `<a>` 上才被读屏当作「当前页」。
    expect(within(groups).getByRole('link', { name: '用户' })).toHaveAttribute('aria-current', 'page')
    expect(within(groups).getByRole('link', { name: '开源许可' })).toBeInTheDocument()
  })

  it('非服务器运维工作区折叠态：全部入口压成图标轨（原型 .section-nav 折叠形态）', () => {
    useConsoleStore.setState({ sidebarCollapsed: true })
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/users' })

    // 折叠态没有分组标题与列表（那两段都靠 246px 宽度才成立），改用图标轨。
    expect(container.querySelector('[data-slot="console-sidebar-groups"]')).toBeNull()
    const rail = container.querySelector('[data-slot="side-nav-shortcuts"]') as HTMLElement
    expect(within(rail).getByRole('link', { name: '用户' })).toBeInTheDocument()
    // 「开源许可」有两处：图标轨一条，底部偏好块一条。后者折叠后**仍在 DOM**
    // （改用 grid 收起高度而非卸载，避免二次布局跳动），靠 `inert` 保证不可交互、
    // 不被读屏读到——但 testing-library 的 getByRole 不识别 `inert`，故这里用 within 限定。
    expect(within(rail).getByRole('link', { name: '开源许可' })).toBeInTheDocument()
  })
})
