import { describe, it, expect, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { LayoutDashboard, Network, Server, Shield, HardDrive } from 'lucide-react'
import { WorkspaceSidebar } from './WorkspaceSidebar'
import type { WorkspaceDef } from '@jianmanager/ui/lib/workspace-navigation'
import type { WorkspaceLinkArgs } from './WorkspaceSidebar'

/**
 * 工作区侧栏 · 受控视图测（ADR-097）。
 *
 * 补的是**展示契约**：四段结构、按工作区分派、权限裁剪、折叠态图标轨、激活态语义、
 * 注入面（链接 / 跳转 / 子组件 / 主题）被正确使用。
 * 取数联动（devmock 纵切）由应用侧 `WorkspaceSidebar.dom.test.tsx` 覆盖。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        ws: { ops: '服务器运维', platform: '平台管理' },
        g: { opsResources: '固定入口', workbench: '群组与工作台', platformAdmin: '平台设置' },
        nav: {
          home: '平台首页',
          instances: '全部服务器',
          nodes: '节点',
          topo: '网络拓扑',
          users: '用户管理',
          resources: '资源导航',
        },
        resourceCard: { viewGroup: '资源视图' },
        grouping: { dim_node: '按节点', dim_network: '按群组' },
        sidebarServers: { favorites: '收藏' },
        networks: { empty: '暂无群组' },
        licenses: { entry: '开源许可' },
        theme: { toggle: '明暗', light: '浅色', dark: '深色', system: '跟随系统' },
        colorTheme: { label: '主题色', indigo: '墨绿' },
      },
    },
  },
  interpolation: { escapeValue: false },
})

/** 服务器运维工作区（真实登记的最小等价物）。 */
const opsWorkspace: WorkspaceDef = {
  key: 'ops',
  labelKey: 'ws.ops',
  groups: [
    {
      key: 'opsResources',
      labelKey: 'g.opsResources',
      icon: LayoutDashboard,
      children: [
        { to: '/', labelKey: 'nav.home', icon: LayoutDashboard },
        { to: '/instances', labelKey: 'nav.instances', icon: Server },
        { to: '/nodes', labelKey: 'nav.nodes', icon: HardDrive },
      ],
    },
    {
      key: 'opsGroupsWorkbench',
      labelKey: 'g.workbench',
      icon: Network,
      children: [{ to: '/networks/topology', labelKey: 'nav.topo', icon: Network }],
    },
  ],
}

/** 非运维工作区（验证分组列表与折叠图标轨分支）。 */
const platformWorkspace: WorkspaceDef = {
  key: 'platform',
  labelKey: 'ws.platform',
  groups: [
    {
      key: 'platformAdmin',
      labelKey: 'g.platformAdmin',
      icon: Shield,
      children: [{ to: '/users', labelKey: 'nav.users', icon: Shield }],
    },
  ],
}

/** 测试自备的链接渲染：朴素 `<a>`（无需任何路由库）。 */
function renderLink({ to, className, ariaCurrent, ariaLabel, title, children }: WorkspaceLinkArgs) {
  return (
    <a href={to} className={className} aria-current={ariaCurrent} aria-label={ariaLabel} title={title}>
      {children}
    </a>
  )
}

const fullPermissions = { nodes: new Set<string>(), isPlatformAdmin: true, loaded: true }

function renderSidebar(props: Partial<ComponentProps<typeof WorkspaceSidebar>> = {}) {
  const onNavigate = vi.fn()
  const renderServerSelector = vi.fn(() => <div data-testid="selector" />)
  const renderSidebarServerList = vi.fn(() => <div data-testid="server-list" />)
  const merged = {
    collapsed: false,
    activeWorkspace: opsWorkspace,
    data: {
      instanceTotal: 42,
      nodes: [{ id: 1, name: 'node-a' } as never],
      byNode: [{ nodeId: 1, count: 3 }],
      instances: [{ id: 9, name: 'inst-a', nodeId: 1, status: 'RUNNING' } as never],
      networks: [{ id: 5, name: 'survival', memberCount: 2, memberStatus: { running: 2, stopped: 0, crashed: 0, starting: 0, stopping: 0 } }],
    },
    permissions: fullPermissions,
    pathname: '/instances',
    renderLink,
    onNavigate,
    renderServerSelector,
    renderSidebarServerList,
    appVersion: '1.2.3',
    themeSwitcher: {
      colorTheme: 'indigo' as const,
      theme: 'system' as const,
      onColorThemeChange: vi.fn(),
      onThemeChange: vi.fn(),
    },
    ...props,
  }
  const { container } = render(
    <I18nextProvider i18n={testI18n}>
      <WorkspaceSidebar {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return { container, merged, onNavigate }
}

describe('WorkspaceSidebar（FR-496 工作区侧栏受控视图）', () => {
  it('无当前工作区时不渲染四段内容，仅保留侧栏壳。', () => {
    const { container } = renderSidebar({ activeWorkspace: null })
    expect(container.querySelector('[data-slot="console-sidebar"]')).not.toBeNull()
    expect(container.querySelector('[data-slot="side-nav-head"]')).toBeNull()
  })

  it('四段顺序：工作区首段 / 固定入口 / 资源区 / 底部常驻。', () => {
    const { container } = renderSidebar()
    const surface = container.querySelector('[data-slot="console-sidebar-surface"]') as HTMLElement
    const head = surface.querySelector('[data-slot="side-nav-head"]') as HTMLElement
    const shortcuts = surface.querySelector('[data-slot="side-nav-shortcuts"]') as HTMLElement
    const resource = surface.querySelector('[data-slot="side-nav-resource"]') as HTMLElement
    const bottom = surface.querySelector('[data-slot="side-nav-bottom"]') as HTMLElement

    expect([head, shortcuts, resource, bottom].every(Boolean)).toBe(true)
    expect(head.compareDocumentPosition(shortcuts) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(shortcuts.compareDocumentPosition(resource) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(resource.compareDocumentPosition(bottom) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('首段渲染工作区名与「资源视图」pill，标题回落地页。', () => {
    const { container } = renderSidebar()
    const head = container.querySelector('[data-slot="side-nav-head"]') as HTMLElement
    expect(within(head).getByText('服务器运维')).toBeInTheDocument()
    expect(within(head).getByText('资源视图')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /服务器运维/ })).toHaveAttribute('href', '/')
  })

  it('固定入口带计数（实例总数 / 节点数）。', () => {
    const { container } = renderSidebar()
    const shortcuts = container.querySelector('[data-slot="side-nav-shortcuts"]') as HTMLElement
    expect(within(shortcuts).getByRole('link', { name: /全部服务器/ })).toHaveTextContent('42')
    // 节点数取注入的节点列表长度（本例 1 个）。
    expect(within(shortcuts).getByRole('link', { name: /节点/ })).toHaveTextContent('1')
  })

  it('激活态落在链接的 aria-current 上（与 pathname 命中同一套优先级）。', () => {
    const { container } = renderSidebar({ pathname: '/networks/topology' })
    const bottom = container.querySelector('[data-slot="side-nav-bottom"]') as HTMLElement
    expect(within(bottom).getByRole('link', { name: /网络拓扑/ })).toHaveAttribute('aria-current', 'page')
  })

  it('权限裁剪：无 node.read 时「按节点」段不出现。', () => {
    renderSidebar({ permissions: { nodes: new Set(['network.read']), isPlatformAdmin: false, loaded: true } })
    expect(screen.queryByRole('button', { name: '按节点' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '按群组' })).toBeInTheDocument()
  })

  it('权限未加载时不做裁剪（宁可短暂多给，也不闪空白）。', () => {
    renderSidebar({ permissions: { nodes: new Set(), isPlatformAdmin: false, loaded: false } })
    expect(screen.getByRole('button', { name: '按节点' })).toBeInTheDocument()
  })

  it('资源区渲染注入的选择器，切到收藏段渲染注入的服务器列。', async () => {
    const user = userEvent.setup()
    const { merged } = renderSidebar()
    expect(merged.renderServerSelector).toHaveBeenCalled()
    expect(screen.getByTestId('selector')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '收藏' }))
    expect(merged.renderSidebarServerList).toHaveBeenCalled()
    expect(screen.getByTestId('server-list')).toBeInTheDocument()
  })

  it('群组行点击走注入的跳转（不带路由库）。', async () => {
    const user = userEvent.setup()
    const { onNavigate } = renderSidebar()
    await user.click(screen.getByRole('button', { name: '按群组' }))
    await user.click(screen.getByRole('button', { name: /survival/ }))
    expect(onNavigate).toHaveBeenCalledWith('/instances?networkId=5')
  })

  it('非运维工作区展开态按分组列出入口。', () => {
    const { container } = renderSidebar({ activeWorkspace: platformWorkspace })
    expect(container.querySelector('[data-slot="console-sidebar-groups"]')).not.toBeNull()
    expect(screen.getByRole('link', { name: /用户管理/ })).toHaveAttribute('href', '/users')
  })

  it('非运维工作区折叠态压成图标轨，仍保留全部入口。', () => {
    const { container } = renderSidebar({ activeWorkspace: platformWorkspace, collapsed: true })
    expect(container.querySelector('[data-slot="console-sidebar-groups"]')).toBeNull()
    // 折叠态只剩图标：无障碍名由 aria-label 兜住。
    expect(screen.getByRole('link', { name: '用户管理' })).toBeInTheDocument()
  })

  it('底部偏好块：版本号、许可入口与注入的主题切换。', () => {
    renderSidebar()
    expect(screen.getByText('v1.2.3')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /开源许可/ })).toHaveAttribute('href', '/licenses')
    expect(screen.getByRole('button', { name: '主题色' })).toBeInTheDocument()
  })
})
