import { describe, it, expect, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { Boxes, HardDrive, LayoutDashboard, Server, ShieldCheck, Wrench } from 'lucide-react'
import { ConsoleSidebar } from '@/components/views/console/ConsoleSidebar'
import type { NavGroup } from '@/lib/nav-config'
import type { SidebarLinkArgs } from '@/components/views/console/sidebar-link'

/**
 * 控制台侧栏 · 受控视图测（ADR-097）。
 *
 * 补的是**展示契约**：展开/折叠两态、可展开域的开合与激活态、底部偏好块与语言切换。
 * 权限裁剪（按权限/角色种子）与路由联动由应用侧 `ConsoleSidebar.dom.test.tsx` 覆盖。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': {
      translation: {
        nav: {
          overview: '总览',
          cluster: '集群',
          instances: '全部服务器',
          nodes: '节点',
          system: '系统',
          auditSettings: '账户与审计',
          settings: '平台设置',
          expandSidebar: '展开侧栏',
        },
        licenses: { entry: '开源许可' },
        language: { zh: '简体中文', en: 'English' },
        theme: { toggle: '明暗', light: '浅色', dark: '深色', system: '跟随系统' },
        colorTheme: { label: '主题色', indigo: '墨绿' },
      },
    },
  },
  interpolation: { escapeValue: false },
})

/** 测试自备的链接渲染：朴素 `<a>`（无需任何路由库），透传 data-active。 */
function renderLink({ to, className, ariaCurrent, ariaLabel, title, dataActive, children }: SidebarLinkArgs) {
  return (
    <a
      href={to}
      className={typeof className === 'function' ? className(false) : className}
      aria-current={ariaCurrent}
      aria-label={ariaLabel}
      title={title}
      data-active={dataActive}
    >
      {children}
    </a>
  )
}

const groups: NavGroup[] = [
  { key: 'overview', labelKey: 'nav.overview', icon: LayoutDashboard, to: '/' },
  {
    key: 'cluster',
    labelKey: 'nav.cluster',
    icon: Boxes,
    children: [
      { to: '/instances', labelKey: 'nav.instances', icon: Server },
      { to: '/nodes', labelKey: 'nav.nodes', icon: HardDrive },
    ],
  },
  {
    key: 'system',
    labelKey: 'nav.system',
    icon: ShieldCheck,
    sections: [{ labelKey: 'nav.auditSettings', children: [{ to: '/settings', labelKey: 'nav.settings', icon: Wrench }] }],
  },
]

function renderSidebar(props: Partial<ComponentProps<typeof ConsoleSidebar>> = {}) {
  const onToggleSidebar = vi.fn()
  const onToggleGroup = vi.fn()
  const onLanguageChange = vi.fn()
  const renderServerSelector = vi.fn(() => <div data-testid="selector" />)
  const renderSidebarServerList = vi.fn(() => <div data-testid="server-list" />)
  const merged = {
    groups,
    collapsed: false,
    onToggleSidebar,
    collapsedGroups: {} as Record<string, boolean | undefined>,
    onToggleGroup,
    pathname: '/instances',
    renderLink,
    renderServerSelector,
    renderSidebarServerList,
    appVersion: '1.2.3',
    themeSwitcher: {
      colorTheme: 'indigo' as const,
      theme: 'system' as const,
      onColorThemeChange: vi.fn(),
      onThemeChange: vi.fn(),
    },
    language: { current: 'zh', onChange: onLanguageChange },
    ...props,
  }
  const { container } = render(
    <I18nextProvider i18n={testI18n}>
      <ConsoleSidebar {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return { container, merged, onToggleSidebar, onToggleGroup, onLanguageChange }
}

describe('ConsoleSidebar（FR-268 控制台侧栏受控视图）', () => {
  it('展开态渲染注入的选择器与常驻服务器列，并列出各分组。', () => {
    const { container, merged } = renderSidebar()
    expect(merged.renderServerSelector).toHaveBeenCalled()
    expect(screen.getByTestId('selector')).toBeInTheDocument()
    expect(merged.renderSidebarServerList).toHaveBeenCalled()

    // 展开态在 DOM 里同时存在折叠副本，故按 data-mode 取展开那份。
    const expanded = container.querySelector('[data-mode="expanded"]') as HTMLElement
    expect(within(expanded).getByRole('button', { name: /集群/ })).toBeInTheDocument()
    expect(within(expanded).getByRole('link', { name: /总览/ })).toHaveAttribute('href', '/')
  })

  it('折叠态不渲染选择器与服务器列，但给出展开按钮。', async () => {
    const user = userEvent.setup()
    const { container, onToggleSidebar } = renderSidebar({ collapsed: true })
    const collapsed = container.querySelector('[data-mode="collapsed"]') as HTMLElement

    expect(within(collapsed).queryByTestId('selector')).toBeNull()
    await user.click(within(collapsed).getByRole('button', { name: '展开侧栏' }))
    expect(onToggleSidebar).toHaveBeenCalled()
  })

  it('折叠态分类图标指向第一个可见子页，并按 pathname 标记激活态。', () => {
    const { container } = renderSidebar({ collapsed: true, pathname: '/nodes' })
    const collapsed = container.querySelector('[data-mode="collapsed"]') as HTMLElement
    // 集群域未设 to，折叠态指向其第一个子页 /instances。
    expect(within(collapsed).getByRole('link', { name: '集群' })).toHaveAttribute('href', '/instances')
    // 子路由命中时该分类图标标记激活。
    expect(within(collapsed).getByRole('link', { name: '集群' })).toHaveAttribute('data-active', 'true')
    expect(within(collapsed).getByRole('link', { name: '系统' })).toHaveAttribute('data-active', 'false')
  })

  it('可展开域的开合走注入的 onToggleGroup，aria-expanded 反映状态。', async () => {
    const user = userEvent.setup()
    const { container, onToggleGroup } = renderSidebar()
    const expanded = container.querySelector('[data-mode="expanded"]') as HTMLElement

    await user.click(within(expanded).getByRole('button', { name: /集群/ }))
    expect(onToggleGroup).toHaveBeenCalledWith('cluster')
    expect(within(expanded).getByRole('button', { name: /集群/ })).toHaveAttribute('aria-expanded', 'true')
  })

  it('已折叠的分组：内容标记为 closed 且 aria-expanded=false。', () => {
    const { container } = renderSidebar({ collapsedGroups: { cluster: true } })
    const expanded = container.querySelector('[data-mode="expanded"]') as HTMLElement
    expect(within(expanded).getByRole('button', { name: /集群/ })).toHaveAttribute('aria-expanded', 'false')
    expect(expanded.querySelector('[data-slot="sidebar-nav-group-content"]')).toHaveAttribute('data-state', 'closed')
  })

  it('带标题的二级分节渲染小标题与子链接。', () => {
    const { container } = renderSidebar()
    const expanded = container.querySelector('[data-mode="expanded"]') as HTMLElement
    expect(within(expanded).getByText('账户与审计')).toBeInTheDocument()
    expect(within(expanded).getByRole('link', { name: /平台设置/ })).toHaveAttribute('href', '/settings')
  })

  it('底部偏好块：版本号、许可入口、主题切换与语言切换。', async () => {
    const user = userEvent.setup()
    const { container, onLanguageChange } = renderSidebar()
    const expanded = container.querySelector('[data-mode="expanded"]') as HTMLElement

    expect(within(expanded).getByText('v1.2.3')).toBeInTheDocument()
    expect(within(expanded).getByRole('link', { name: /开源许可/ })).toHaveAttribute('href', '/licenses')
    expect(within(expanded).getByRole('button', { name: '主题色' })).toBeInTheDocument()

    await user.click(within(expanded).getByRole('button', { name: '简体中文' }))
    await user.click(await screen.findByText('English'))
    expect(onLanguageChange).toHaveBeenCalledWith('en')
  })

  it('折叠态底部只留图标（隐藏版本号行）。', () => {
    const { container } = renderSidebar({ collapsed: true })
    const collapsed = container.querySelector('[data-mode="collapsed"]') as HTMLElement
    expect(within(collapsed).queryByText('v1.2.3')).toBeNull()
    expect(within(collapsed).getByRole('button', { name: '主题色' })).toBeInTheDocument()
  })
})
