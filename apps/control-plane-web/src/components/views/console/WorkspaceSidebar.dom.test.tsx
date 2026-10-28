import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { Clapperboard, HardDrive, LayoutDashboard, LayoutGrid, Network, Server, Shield } from 'lucide-react'
import { WorkspaceSidebar } from '@/components/views/console/WorkspaceSidebar'
import type { WorkspaceDef } from '@/lib/console/workspace-navigation'
import type { WorkspaceLinkArgs } from '@/components/views/console/WorkspaceSidebar'
import { PINNED_TOOLS_KEY } from '@/lib/console/sidebar-tools'

/**
 * 工作区侧栏 · 受控视图测（ADR-103）。
 *
 * 补的是**展示契约**（重构件后）：无标题行/无分组小标题、固定入口紧凑段、常用区（置回工具）、
 * 资源面板注入、底部工具图标轨 + 「更多」收纳菜单、折叠态图标轨、注入面被正确使用。
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
          networks: '分组管理',
          super: '超级工作台',
          director: '导播台',
          users: '用户管理',
          collapseSidebar: '收起侧栏',
          expandSidebar: '展开侧栏',
        },
        sidebarTools: {
          more: '更多',
          shown: '显示在侧边栏',
          stowed: '已收纳',
          pinnedSection: '常用',
          pin: '置回侧边栏',
          unpin: '收纳到底部',
          moveUp: '上移',
          moveDown: '下移',
        },
        licenses: { entry: '开源协议' },
        theme: { toggle: '明暗', light: '浅色', dark: '深色', system: '跟随系统' },
        colorTheme: { label: '主题色', indigo: '墨绿' },
      },
    },
  },
  interpolation: { escapeValue: false },
})

/** 服务器运维工作区（真实登记的最小等价物：三固定入口 + 四工具）。 */
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
      children: [
        { to: '/networks/topology', labelKey: 'nav.topo', icon: Network },
        { to: '/networks', labelKey: 'nav.networks', icon: Network },
        { to: '/super', labelKey: 'nav.super', icon: LayoutGrid },
        { to: '/director', labelKey: 'nav.director', icon: Clapperboard },
      ],
    },
  ],
}

/** 非运维工作区（验证扁平列表与折叠图标轨分支）。 */
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
    <a
      href={to}
      className={typeof className === 'function' ? className(false) : className}
      aria-current={ariaCurrent}
      aria-label={ariaLabel}
      title={title}
    >
      {children}
    </a>
  )
}

const fullPermissions = { nodes: new Set<string>(), isPlatformAdmin: true, loaded: true }

function renderSidebar(props: Partial<ComponentProps<typeof WorkspaceSidebar>> = {}) {
  const onNavigate = vi.fn()
  const renderResourcePanel = vi.fn(() => <div data-testid="resource-panel" />)
  const merged = {
    collapsed: false,
    activeWorkspace: opsWorkspace,
    data: {
      instanceTotal: 42,
      nodes: [{ id: 1, name: 'node-a' } as never],
    },
    permissions: fullPermissions,
    pathname: '/instances',
    renderLink,
    onNavigate,
    renderResourcePanel,
    appVersion: '1.2.3',
    themeSwitcher: {
      colorTheme: 'indigo' as const,
      theme: 'system' as const,
      onColorThemeChange: vi.fn(),
      onThemeChange: vi.fn(),
    },
    language: { current: 'zh', onChange: vi.fn() },
    onToggleSidebar: vi.fn(),
    ...props,
  }
  const { container } = render(
    <I18nextProvider i18n={testI18n}>
      <WorkspaceSidebar {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return { container, merged, onNavigate }
}

describe('WorkspaceSidebar（工作区侧栏受控视图·重构件）', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('无当前工作区时不渲染内容，仅保留侧栏壳。', () => {
    const { container } = renderSidebar({ activeWorkspace: null })
    expect(container.querySelector('[data-slot="console-sidebar"]')).not.toBeNull()
    expect(container.querySelector('[data-slot="side-nav-shortcuts"]')).toBeNull()
  })

  it('结构顺序：固定入口 / 资源面板 / 底部常驻；标题行与分组小标题已移除。', () => {
    const { container } = renderSidebar()
    const surface = container.querySelector('[data-slot="console-sidebar-surface"]') as HTMLElement
    const shortcuts = surface.querySelector('[data-slot="side-nav-shortcuts"]') as HTMLElement
    const resource = surface.querySelector('[data-slot="side-nav-resource"]') as HTMLElement
    const bottom = surface.querySelector('[data-slot="side-nav-bottom"]') as HTMLElement

    expect([shortcuts, resource, bottom].every(Boolean)).toBe(true)
    expect(shortcuts.compareDocumentPosition(resource) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(resource.compareDocumentPosition(bottom) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    // 工作区名标题行与分组小标题一律不出现（本次要求「不要留」）。
    expect(container.querySelector('[data-slot="side-nav-head"]')).toBeNull()
    expect(screen.queryByText('服务器运维')).not.toBeInTheDocument()
    expect(screen.queryByText('固定入口')).not.toBeInTheDocument()
    expect(screen.queryByText('群组与工作台')).not.toBeInTheDocument()
  })

  it('固定入口为紧凑三段（类含 30px 行高覆写），带实例/节点计数。', () => {
    const { container } = renderSidebar()
    const shortcuts = container.querySelector('[data-slot="side-nav-shortcuts"]') as HTMLElement
    expect(shortcuts.className).toContain('min-h-[30px]')
    expect(within(shortcuts).getByRole('link', { name: /全部服务器/ })).toHaveTextContent('42')
    expect(within(shortcuts).getByRole('link', { name: /节点/ })).toHaveTextContent('1')
  })

  it('资源区渲染注入的资源面板；不再渲染服务器选择器。', () => {
    const { merged } = renderSidebar()
    expect(merged.renderResourcePanel).toHaveBeenCalled()
    expect(screen.getByTestId('resource-panel')).toBeInTheDocument()
    expect(screen.queryByTestId('selector')).not.toBeInTheDocument()
  })

  it('底部只留「更多」：「显示在侧边栏」为可拖拽行，收纳区仍列出已收纳工具', async () => {
    const user = userEvent.setup()
    const { container } = renderSidebar()
    const bottom = container.querySelector('[data-slot="side-nav-bottom"]') as HTMLElement

    // 【契约变更】工具不再以图标形式常驻底部：已收纳的工具在底部不占位，
    // 底部只剩「更多」入口（主题/语言/开源协议在其下方）。
    for (const name of ['网络拓扑', '分组管理', '超级工作台', '导播台']) {
      expect(within(bottom).queryByRole('link', { name })).toBeNull()
    }
    expect(within(bottom).getByTestId('sidebar-tools-more')).toBeInTheDocument()

    await user.click(within(bottom).getByTestId('sidebar-tools-more'))
    expect(screen.getByText('显示在侧边栏')).toBeInTheDocument()
    expect(screen.getByText('已收纳')).toBeInTheDocument()
    // 默认全部收纳：四个工具都在「已收纳」区，且都是可拖拽行。
    for (const to of ['/networks/topology', '/networks', '/super', '/director']) {
      expect(screen.getByTestId(`sidebar-tool-row-stowed-${to}`)).toBeInTheDocument()
    }
  })

  it('「显示在侧边栏」的工具在底部有真实落脚点，且与「更多」菜单共用同一份偏好', async () => {
    const user = userEvent.setup()
    localStorage.setItem(PINNED_TOOLS_KEY, JSON.stringify(['/super']))
    const { container } = renderSidebar()

    // 【契约变更·本次】此前把工具整体收进菜单时漏了「显示在侧边栏」这一半，
    // 用户拖回工具后**哪儿都不出现**（本次修复）。现在它们渲染在底部段、偏好块上方。
    const pinnedSection = container.querySelector('[data-slot="sidebar-pinned-tools"]') as HTMLElement
    expect(pinnedSection).not.toBeNull()
    // 语义仍是真链接（中键 / 新标签页可用）。
    expect(within(pinnedSection).getByRole('link', { name: /超级工作台/ })).toHaveAttribute('href', '/super')

    // 菜单里的「显示在侧边栏」区与它同源，不会出现两套状态。
    const bottom = container.querySelector('[data-slot="side-nav-bottom"]') as HTMLElement
    await user.click(within(bottom).getByTestId('sidebar-tools-more'))
    expect(screen.getByTestId('sidebar-tool-row-shown-/super')).toBeInTheDocument()
  })

  it('「更多」是纯三个点图标，且位于版本号/开源协议一行的上方', () => {
    const { container } = renderSidebar()
    const more = screen.getByTestId('sidebar-tools-more')
    // 可访问名必须落在真 button 上（护栏要求），文字不显示。
    expect(more.tagName).toBe('BUTTON')
    expect(more).toHaveAttribute('aria-label', '更多')
    expect(more.textContent?.trim()).toBe('')

    // 位置：在偏好块内、且在「开源协议」链接之前（DOM 顺序即视觉上方的上下关系）。
    const prefs = container.querySelector('[data-slot="console-sidebar-preferences"]') as HTMLElement
    expect(prefs.contains(more)).toBe(true)
    const licenses = within(prefs).getByRole('link', { name: /开源协议/ })
    expect(more.compareDocumentPosition(licenses) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('折叠开关常驻底部：折叠态仍可展开（顶栏按钮在折叠时会被卸载）', async () => {
    const user = userEvent.setup()
    const onToggleSidebar = vi.fn()
    const { container } = renderSidebar({ collapsed: false, onToggleSidebar })
    const toggle = container.querySelector('[data-testid="sidebar-collapse-toggle"]') as HTMLElement

    // 展开态：在「更多」右侧，语义是「收起侧栏」。
    expect(toggle).not.toBeNull()
    expect(toggle).toHaveAttribute('aria-label', '收起侧栏')
    const more = container.querySelector('[data-testid="sidebar-tools-more"]') as HTMLElement
    if (more) {
      // DOM 顺序即视觉左右：开关必须在「更多」之后（其右侧）。
      expect(more.compareDocumentPosition(toggle) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    }
    await user.click(toggle)
    expect(onToggleSidebar).toHaveBeenCalledTimes(1)

    // 【这是本用例存在的理由】折叠态必须仍然渲染开关，且语义翻转为「展开侧栏」。
    // 顶栏品牌区的折叠按钮写作 `{!collapsed && …}`，折叠时从 DOM 卸载——侧栏这一处
    // 是页面里**唯一**的展开出口，任何条件渲染都会让侧栏再也展不开。
    const { container: collapsedBox } = renderSidebar({ collapsed: true, onToggleSidebar })
    const collapsedToggle = collapsedBox.querySelector('[data-testid="sidebar-collapse-toggle"]') as HTMLElement
    expect(collapsedToggle).not.toBeNull()
    expect(collapsedToggle).toHaveAttribute('aria-label', '展开侧栏')

    // 用户指定：缩放（折叠）后底部**只保留折叠按钮**，主题色 / 明暗 / 语言 / 更多全部撤走。
    // 54px 宽里塞这些只会互相挤，且它们都能在展开后一步到达（明暗/语言在 /settings 也有入口）。
    const collapsedPrefs = collapsedBox.querySelector('[data-slot="console-sidebar-preferences"]') as HTMLElement
    const remaining = [...collapsedPrefs.querySelectorAll('button')].map(
      (b) => b.getAttribute('aria-label') ?? b.getAttribute('data-testid'),
    )
    expect(remaining).toEqual(['展开侧栏'])
    expect(collapsedPrefs.querySelector('[aria-label="主题色"]')).toBeNull()
    expect(collapsedPrefs.querySelector('[data-testid="sidebar-language"]')).toBeNull()
  })

  it('底部两块都不自己画顶部分隔线：只有 SideNavBottom 那一条边界', () => {
    // 【曾经漏掉的位置】上一轮只删了偏好块的 `border-t`，但用户看到的那条线其实在
    // `sidebar-pinned-tools` **内部**——工具段自己画了一条，与外层 SideNavBottom 的边界线
    // 相隔几像素，视觉上就是「按钮栏上方两条线」。两条都要断，否则还会被报一次。
    localStorage.setItem(PINNED_TOOLS_KEY, JSON.stringify(['/super', '/networks']))
    const { container } = renderSidebar()

    const prefs = container.querySelector('[data-slot="console-sidebar-preferences"]') as HTMLElement
    expect(prefs).not.toBeNull()
    expect(prefs.className).not.toContain('border-t')

    const pinnedTools = container.querySelector('[data-slot="sidebar-pinned-tools"]') as HTMLElement
    expect(pinnedTools).not.toBeNull()
    // 工具段自身及其内部任何一层都不该带 `border-t`（分隔线归 SideNavBottom 统管）。
    expect(pinnedTools.className).not.toContain('border-t')
    for (const el of pinnedTools.querySelectorAll('*')) {
      expect(el.className.toString()).not.toContain('border-t')
    }
  })

  it('非运维工作区：扁平列出入口，无分组小标题，也不渲染工具轨。', () => {
    const { container } = renderSidebar({ activeWorkspace: platformWorkspace })
    expect(container.querySelector('[data-slot="console-sidebar-groups"]')).not.toBeNull()
    expect(screen.getByRole('link', { name: /用户管理/ })).toHaveAttribute('href', '/users')
    expect(screen.queryByText('平台设置')).not.toBeInTheDocument()
    expect(container.querySelector('[data-slot="sidebar-tool-rail"]')).toBeNull()
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
    expect(screen.getByRole('link', { name: /开源协议/ })).toHaveAttribute('href', '/licenses')
    expect(screen.getByRole('button', { name: '主题色' })).toBeInTheDocument()
  })
})
