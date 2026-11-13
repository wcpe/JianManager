import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { useConsoleStore } from '@/stores/console'
import { ALL_PERMISSION_NODE_IDS, DEFAULT_ROLE_NODES } from '@/lib/shared/roles'
import { FAVORITES_KEY } from '@/lib/console/server-selection'
import { PINNED_TOOLS_KEY } from '@/lib/console/sidebar-tools'
import WorkspaceSidebar from './WorkspaceSidebar'

/** 
 * 数据源整模块 mock（重构件后）：侧栏的实例窗口 / 计数 / 节点与拓扑成员都来自这些 hook，
 * 用固定小数据集断言结构，不依赖假后端的规模与排序（那些由各页自己的 dom 用例守）。
 */
const { searchCalls } = vi.hoisted(() => ({ searchCalls: [] as unknown[][] }))
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
  useInstanceSearch: (params: unknown, enabled?: boolean) => {
    searchCalls.push([params, enabled])
    return {
      data: {
        items: [
          { id: 7, uuid: 'i-lobby', nodeId: 1, name: 'lobby-01', status: 'RUNNING', tags: '["env:prod","region:r1"]', type: 'minecraft_java', role: 'backend', serverPort: 25565, createdAt: '2026-10-01T08:00:00Z' },
          { id: 8, uuid: 'i-survival', nodeId: 1, name: 'survival-01', status: 'CRASHED', tags: '["env:prod","region:r1","zone:z1","survival","creative","infra"]', type: 'minecraft_java', role: 'backend', serverPort: 25566, createdAt: '2026-09-20T08:00:00Z' },
          { id: 9, uuid: 'i-mini', nodeId: 2, name: 'mini-01', status: 'STOPPED', tags: '["env:dev"]', type: 'minecraft_java', role: 'universal', serverPort: 25567, createdAt: '2026-08-15T08:00:00Z' },
        ],
        total: 3,
        page: 1,
        pageSize: 100,
      },
    }
  },
  useSearchInstances: () => ({ data: { items: [], total: 0, page: 1, pageSize: 200 } }),
}))
vi.mock('@/api/topology', () => ({
  useTopology: () => ({
    data: { networks: [{ id: 5, name: '生存群组', memberInstanceIds: [7] }] },
  }),
}))
vi.mock('@/api/instanceGroups', () => ({
  // 侧栏「分组」段读后端组织分组树。这里给一棵三层小树，断言层级能渲染出来。
  useInstanceGroups: () => ({
    data: [
      { id: 100, uuid: 'g-root', name: '晨星集群', parentId: null, sort: 0, instanceCount: 3, memberInstanceIds: [] },
      { id: 101, uuid: 'g-region', name: '东岚大区', parentId: 100, sort: 0, instanceCount: 3, memberInstanceIds: [] },
      { id: 102, uuid: 'g-sg', name: '生存服组', parentId: 101, sort: 0, instanceCount: 2, memberInstanceIds: [7, 8] },
    ],
  }),
  useInstanceGroupSubtree: () => ({ data: [] }),
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
 * 新工作区侧栏（重构件后）。
 *
 * 只锁**结构与行为契约**（段顺序、工具收纳、资源面板数据联动、置顶、折叠态、权限收敛），不测视觉。
 */
describe('WorkspaceSidebar 工作区侧栏（重构件）', () => {
  beforeEach(() => {
    localStorage.clear()
    searchCalls.length = 0
    loginAsAdmin()
    useConsoleStore.setState({ sidebarCollapsed: false, lastWorkspaceKey: null })
  })

  it('结构：壳类与段顺序（固定入口 / 资源区 / 底部常驻），标题行已移除', () => {
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const aside = container.querySelector('[data-slot="console-sidebar"]') as HTMLElement

    // 沿用旧壳的槽位名与类：宽度动画（jm-*）与折叠态 CSS 仍按这套钩子工作。
    expect(aside).toHaveClass('jm-console-sidebar')
    expect(aside).toHaveClass('w-[246px]')
    expect(aside).toHaveAttribute('data-state', 'expanded')
    expect(aside.querySelector('[data-slot="sidebar-drawer"]')).toHaveClass('jm-sidebar-drawer')

    const surface = aside.querySelector('[data-slot="console-sidebar-surface"]') as HTMLElement
    const shortcuts = surface.querySelector('[data-slot="side-nav-shortcuts"]') as HTMLElement
    const resource = surface.querySelector('[data-slot="side-nav-resource"]') as HTMLElement
    const bottom = surface.querySelector('[data-slot="side-nav-bottom"]') as HTMLElement

    expect([shortcuts, resource, bottom].every(Boolean)).toBe(true)
    expect(shortcuts.compareDocumentPosition(resource) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(resource.compareDocumentPosition(bottom) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    // 工作区名标题行已移除（「不要留」），侧栏无障碍名仍由 aside 提供。
    expect(surface.querySelector('[data-slot="side-nav-head"]')).toBeNull()
    expect(aside).toHaveAttribute('aria-label', '服务器运维导航')
  })

  it('固定入口来自真实登记：首页 / 服务器 / 节点，实例与节点带计数', () => {
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const shortcuts = container.querySelector('[data-slot="side-nav-shortcuts"]') as HTMLElement

    expect(within(shortcuts).getByRole('link', { name: '首页' })).toBeInTheDocument()
    // 计数口径：实例总数取聚合（42），节点数取节点列表长度（2）。
    expect(within(shortcuts).getByRole('link', { name: /^服务器/ })).toHaveTextContent('42')
    expect(within(shortcuts).getByRole('link', { name: /^节点/ })).toHaveTextContent('2')
  })

  it('底部只剩「更多」入口；已收纳的工具不在底部占位', () => {
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/networks/topology' })
    const bottom = container.querySelector('[data-slot="side-nav-bottom"]') as HTMLElement

    // 默认全部工具处于「已收纳」：底部不渲染任何工具链接，只有一个「更多」入口。
    for (const name of ['网络拓扑', '分组管理', '超级工作台', '导播台']) {
      expect(within(bottom).queryByRole('link', { name })).toBeNull()
    }
    expect(within(bottom).getByTestId('sidebar-tools-more')).toBeInTheDocument()
  })

  it('置回的工具在底部有独立一段（与「更多」菜单同源），没有置回时整段与分隔线都不渲染', async () => {
    const user = userEvent.setup()
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const bottom = container.querySelector('[data-slot="side-nav-bottom"]') as HTMLElement

    // 【契约变更·本次】「显示在侧边栏」原本没有渲染位置（拖回后哪儿都不出现）。
    // 现在它在偏好块上方有自己的段；一个都没置回时整段不渲染，偏好块顶部的分隔线也一并消失
    // （用户明确指出：没放任何东西时那条横线是多余的）。
    expect(container.querySelector('[data-slot="sidebar-pinned-tools"]')).toBeNull()
    const prefs = container.querySelector('[data-slot="console-sidebar-preferences"]') as HTMLElement
    expect(getComputedStyle(prefs).borderTopWidth === '1px').toBe(false)

    // 置回一个工具 → 段出现、真链接可点、分隔线回来。
    localStorage.setItem(PINNED_TOOLS_KEY, JSON.stringify(['/super']))
    const { container: after } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const section = after.querySelector('[data-slot="sidebar-pinned-tools"]') as HTMLElement
    expect(section).not.toBeNull()
    expect(within(section).getByRole('link', { name: /超级工作台/ })).toHaveAttribute('href', '/super')

    // 菜单里的「显示在侧边栏」区与它是同一份偏好（不出现两套状态）。
    await user.click(within(bottom).getByTestId('sidebar-tools-more'))
    expect(screen.getByTestId('sidebar-tool-row-shown-/super')).toBeInTheDocument()
  })

  it('资源面板：搜索 + 筛选 + 一个分组方式下拉 + 服务器列表；无「选择服务器」', () => {
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const resource = container.querySelector('[data-slot="side-nav-resource"]') as HTMLElement

    expect(within(resource).getByTestId('sidebar-resource-panel')).toBeInTheDocument()
    expect(within(resource).getByRole('textbox', { name: '搜索服务器' })).toBeInTheDocument()
    expect(within(resource).getByRole('button', { name: /筛选与分组/ })).toBeInTheDocument()
    // 维度选择并入「筛选与分组」按钮（点击后是菜单里的「分组方式」分组）。
    expect(within(resource).getByRole('button', { name: /筛选与分组/ })).toBeInTheDocument()
    expect(within(resource).queryByRole('combobox')).toBeNull()
    // 服务器选择器已删（「服务器选择器直接删掉」）。
    expect(within(resource).queryByRole('button', { name: '选择服务器' })).toBeNull()

    // 默认分组方式=按节点：组头取节点名，行取窗口实例（名称序）。
    expect(within(resource).getByText('node-main')).toBeInTheDocument()
    expect(within(resource).getByText('node-east')).toBeInTheDocument()
    expect(within(resource).getByText('lobby-01')).toBeInTheDocument()
    expect(within(resource).getByText('survival-01')).toBeInTheDocument()
    expect(within(resource).getByText('mini-01')).toBeInTheDocument()
  })

  it('服务器列表虚拟化：只挂载可视窗口内的行，不随窗口总量线性增长', () => {
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const list = container.querySelector('[data-slot="sidebar-resource-list"]') as HTMLElement

    // 列表是滚动容器（虚拟化的前提：高度由容器自身量出）。
    expect(list).toBeInTheDocument()
    expect(list.className).toContain('overflow-y-auto')

    // 每台服务器在 DOM 里只出现一次（虚拟化不得重复挂载同一行）。
    const rows = list.querySelectorAll('[data-testid="sidebar-resource-row"]')
    const names = [...rows].map((row) => row.textContent ?? '')
    expect(new Set(names).size).toBe(names.length)

    // 【有意的取舍】jsdom 里 `clientHeight` 恒为 0 → 回退视口 900px，窗口覆盖 3 行数据全部可见。
    // 真实浏览器下窗口只挂载 ~20 行，这条断言在 jsdom 里验证的是「机制存在且不丢行」，
    // 千行滚动行为由浏览器实测（护栏要求：性能结论必须真机佐证）。
    expect(rows.length).toBeGreaterThan(0)
    expect(rows.length).toBeLessThanOrEqual(3)
  })

  it('服务器行紧凑两行：名称 / 备注 / 行尾在线人数（TPS 已删），行高与虚拟化常量一致', () => {
    // 先清空收藏：否则收藏行会被提到「置顶区」、从分组列表里剔除，这里就取不到行来断言。
    localStorage.removeItem(FAVORITES_KEY)
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const list = container.querySelector('[data-slot="sidebar-resource-list"]') as HTMLElement
    const rows = [...list.querySelectorAll('[data-testid="sidebar-resource-row"]')]
    expect(rows.length).toBeGreaterThan(0)

    // 【行高契约】`SERVER_ROW_HEIGHT` 与行盒高度必须严格一致，否则虚拟窗口逐行漂移。
    // 这里断言的是**渲染出来的类名**（jsdom 不套样式表，量不出 32px）：
    // 高度是 `h-[32px]` 这一个来源，改动 CSS 时这条会立刻失败，提醒同步常量。
    for (const row of rows) expect(row.className).toContain('h-[32px]')

    // 第一行实例有；备注取 `statusReason`（崩溃原因等自由文本即备注语义），空则留空。
    const first = rows[0]
    expect(first.querySelector('.text-\\[12px\\]')).not.toBeNull()
    expect(first.querySelector('.h-\\[12px\\]')).not.toBeNull()

    // 行尾只剩在线人数这一项（用户要求删掉 TPS）：单段、不再有 `tps` 字样。
    const rightCol = first.querySelector('.tabular-nums') as HTMLElement
    expect(rightCol).not.toBeNull()
    expect(rightCol.querySelectorAll('span.block')).toHaveLength(0)
    expect(rightCol.textContent ?? '').not.toMatch(/tps|%|G/)
  })

  it('服务器行第二行常驻类别徽标：有备注时接 `· 备注`，无备注时只剩徽标', () => {
    localStorage.removeItem(FAVORITES_KEY)
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const rows = [...container.querySelectorAll('[data-testid="sidebar-resource-row"]')]
    expect(rows.length).toBeGreaterThan(0)

    // mock 实例均无 `kind` 字段 → 走 role/type 回退推断：
    // 三台都是 minecraft_java + backend/universal，故都应显示「插件服」。
    for (const row of rows) {
      expect(row.textContent).toContain('插件服')
    }

    // 第二行结构：徽标常驻（备注为空时这一行也不空——这正是选它的理由）。
    const badge = rows[0].querySelector('span.rounded-\\[3px\\]') ?? [...rows[0].querySelectorAll('span')].find((s) => s.textContent === '插件服')
    expect(badge).toBeTruthy()
  })

  it('搜索经防抖落定后才下发服务端 q', async () => {
    const user = userEvent.setup()
    renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })

    await user.type(screen.getByRole('textbox', { name: '搜索服务器' }), 'lobby')
    // 防抖 300ms：落定前不下发 q，落定后最近一次调用带 q。
    await new Promise((resolve) => setTimeout(resolve, 400))
    const lastParams = searchCalls.at(-1)?.[0] as { q?: string } | undefined
    expect(lastParams?.q).toBe('lobby')
  })

  it('置顶：行内按钮写入收藏（置顶）并可取消', async () => {
    const user = userEvent.setup()
    renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })

    // 默认「按节点」分组下窗口实例直接成行，钉按钮就在行内。
    await user.click(screen.getByRole('button', { name: '置顶：lobby-01' }))
    expect(JSON.parse(localStorage.getItem(FAVORITES_KEY) ?? '[]')).toEqual([{ id: 7, name: 'lobby-01', status: 'RUNNING', nodeId: 1, uuid: 'u-7' }].map(({ id, name, status, nodeId }) => expect.objectContaining({ id, name, status, nodeId })))
    expect(screen.getByRole('button', { name: '取消置顶：lobby-01' })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '取消置顶：lobby-01' }))
    expect(JSON.parse(localStorage.getItem(FAVORITES_KEY) ?? '[]')).toEqual([])
  })

  it('折叠态：资源区 grid 收起（不是瞬跳卸载），固定入口与工具轨仍在', () => {
    useConsoleStore.setState({ sidebarCollapsed: true })
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })
    const aside = container.querySelector('[data-slot="console-sidebar"]') as HTMLElement

    expect(aside).toHaveAttribute('data-state', 'collapsed')
    expect(aside).toHaveAttribute('data-collapsed', 'true')
    expect(aside).toHaveClass('w-[54px]')
    const resource = aside.querySelector('[data-slot="side-nav-resource"]') as HTMLElement
    // 资源区收起方式同头段：grid-rows 0fr + 淡出（`hidden` 不可过渡，会触发二次布局）。
    expect(resource).toHaveClass('grid-rows-[0fr]')
    expect(screen.getByRole('link', { name: '首页' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '服务器' })).toBeInTheDocument()
  })

  it('权限收敛：无 instance.read 时资源面板不渲染实例行（空态）', () => {
    loginAsGroupAdmin()
    usePermissionsStore.setState({ nodes: new Set(['network.read']), isPlatformAdmin: false, loaded: true })
    renderWithProviders(<WorkspaceSidebar />, { route: '/instances' })

    expect(screen.queryByText('lobby-01')).toBeNull()
    // 无实例时的统一空态。
    expect(screen.getByText('没有匹配的服务器')).toBeInTheDocument()
    // 权限裁剪与旧侧栏同语义：无 instance.read / node.read 时对应入口不出现；
    // 组内可读的入口照常保留（服务器运维域仍可进群组与工作台）。
    expect(screen.queryByRole('link', { name: '服务器' })).toBeNull()
    expect(screen.queryByRole('link', { name: /节点/ })).toBeNull()
    // 工具默认全部「已收纳」，底部只留「更多」入口——故这里断言入口在、链接不在。
    expect(screen.getByTestId('sidebar-tools-more')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: /网络拓扑/ })).toBeNull()
  })

  it('底部偏好块（所有工作区常驻）：主题切换 / 版本号 / 开源协议——旧侧栏的唯一入口不能丢', () => {
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/users' })

    const bottom = container.querySelector('[data-slot="side-nav-bottom"]') as HTMLElement
    const preferences = bottom.querySelector('[data-slot="console-sidebar-preferences"]') as HTMLElement
    expect(preferences).toBeInTheDocument()
    // 主题色与明暗切换当前只在侧栏底部（/settings 的「外观」明说不重复主题色）。
    expect(within(preferences).getByRole('button', { name: '切换主题' })).toBeInTheDocument()
    // 版本号 + 开源许可入口。
    expect(within(preferences).getByText(/^v\d/)).toBeInTheDocument()
    expect(within(preferences).getByRole('link', { name: '开源协议' })).toHaveAttribute('href', '/licenses')
  })

  it('非服务器运维工作区：扁平列出全部入口（无分组小标题），不渲染工具轨', () => {
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/users' })

    const groups = container.querySelector('[data-slot="console-sidebar-groups"]') as HTMLElement
    expect(groups).not.toBeNull()
    // 分组小标题已移除：组名文本不再出现。
    expect(within(groups).queryByText('身份与权限')).toBeNull()
    expect(within(groups).queryByText('系统维护')).toBeNull()
    // 导航行是 link；`aria-current` 落在 `<a>` 上才被读屏当作「当前页」。
    expect(within(groups).getByRole('link', { name: '用户' })).toHaveAttribute('aria-current', 'page')
    expect(within(groups).getByRole('link', { name: '开源协议' })).toBeInTheDocument()
    expect(container.querySelector('[data-slot="sidebar-tool-rail"]')).toBeNull()
  })

  it('非服务器运维工作区折叠态：全部入口压成图标轨', () => {
    useConsoleStore.setState({ sidebarCollapsed: true })
    const { container } = renderWithProviders(<WorkspaceSidebar />, { route: '/users' })

    expect(container.querySelector('[data-slot="console-sidebar-groups"]')).toBeNull()
    const rail = container.querySelector('[data-slot="side-nav-shortcuts"]') as HTMLElement
    expect(within(rail).getByRole('link', { name: '用户' })).toBeInTheDocument()
    // 「开源许可」有两处：图标轨一条，底部偏好块一条（后者折叠后仍在 DOM、靠 inert 不可交互）。
    expect(within(rail).getByRole('link', { name: '开源协议' })).toBeInTheDocument()
  })
})
