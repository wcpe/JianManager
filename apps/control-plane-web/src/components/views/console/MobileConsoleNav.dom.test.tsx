import { describe, it, expect, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { Activity, Boxes, LayoutDashboard, Server, ShieldCheck } from 'lucide-react'
import { MobileConsoleNav } from '@/components/views/console/MobileConsoleNav'
import type { WorkspaceDef } from '@/lib/console/workspace-navigation'
import type { SidebarLinkArgs } from '@/components/views/console/sidebar-link'

/**
 * 手机端底部导航 · 受控视图测（ADR-102）。
 *
 * 补的是**展示契约**（FR-507 阶段 6）：底部格子一格一个**工作区**（≤5 个）、
 * 点开后面板展示该工作区下的分组与子项、点击子项收起面板、工作区激活高亮。
 * 权限裁剪与路由联动由应用侧接线层承担。
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
          cluster: '服务器运维',
          monitoring: '监控',
          system: '系统',
          instances: '全部服务器',
          nodes: '节点',
          auditSettings: '账户与审计',
          settings: '平台设置',
        },
      },
    },
  },
  interpolation: { escapeValue: false },
})

/** 测试自备的链接渲染：朴素 `<a>`（无需路由库），支持函数式 className 并透传 onClick。 */
function renderLink({ to, className, onClick, children }: SidebarLinkArgs) {
  return (
    <a href={to} className={typeof className === 'function' ? className(false) : className} onClick={onClick}>
      {children}
    </a>
  )
}

const workspaces: WorkspaceDef[] = [
  {
    key: 'ops',
    labelKey: 'nav.overview',
    icon: LayoutDashboard,
    groups: [
      {
        key: 'cluster',
        labelKey: 'nav.cluster',
        icon: Boxes,
        children: [
          { to: '/instances', labelKey: 'nav.instances', icon: Server },
          { to: '/nodes', labelKey: 'nav.nodes', icon: Server },
        ],
      },
    ],
  },
  {
    key: 'platform',
    labelKey: 'nav.system',
    icon: ShieldCheck,
    groups: [
      {
        key: 'settings',
        labelKey: 'nav.auditSettings',
        icon: ShieldCheck,
        sections: [
          { labelKey: 'nav.auditSettings', children: [{ to: '/settings', labelKey: 'nav.settings', icon: ShieldCheck }] },
        ],
      },
    ],
  },
  {
    key: 'observability',
    labelKey: 'nav.cluster',
    icon: Activity,
    groups: [
      {
        key: 'monitoring',
        labelKey: 'nav.monitoring',
        icon: Activity,
        // 分组自身即链接（模拟「资源」组下的首页），用于验证叶子分组渲染
        to: '/monitor',
      },
    ],
  },
]

function renderNav(props: Partial<ComponentProps<typeof MobileConsoleNav>> = {}) {
  const merged = { workspaces, pathname: '/', renderLink, ...props }
  const { container } = render(
    <I18nextProvider i18n={testI18n}>
      <MobileConsoleNav {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return { ...merged, container }
}

describe('MobileConsoleNav（移动端底部导航受控视图）', () => {
  it('底部格子一格一个工作区，全部渲染为可展开按钮。', () => {
    renderNav()
    const nav = screen.getByRole('navigation', { name: '移动导航' })
    // 工作区没有 `to`，一律是展开面板的按钮（不再有「直跳链接」那种格子）。
    expect(within(nav).getByRole('button', { name: /总览/ })).toBeInTheDocument()
    expect(within(nav).getByRole('button', { name: /系统/ })).toBeInTheDocument()
    expect(within(nav).queryAllByRole('link')).toHaveLength(0)
  })

  it('点击工作区展开面板，aria-expanded 反映状态。', async () => {
    const user = userEvent.setup()
    renderNav()
    const trigger = screen.getByRole('button', { name: /总览/ })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')

    await user.click(trigger)
    expect(screen.getByRole('button', { name: /总览/ })).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('全部服务器')).toBeInTheDocument()
    expect(screen.getByText('节点')).toBeInTheDocument()
  })

  it('面板内子项点击后收起面板（走注入的 onClick）。', async () => {
    const user = userEvent.setup()
    renderNav()
    await user.click(screen.getByRole('button', { name: /总览/ }))
    await user.click(screen.getByRole('link', { name: /全部服务器/ }))
    expect(screen.getByRole('button', { name: /总览/ })).toHaveAttribute('aria-expanded', 'false')
  })

  it('面板内分节渲染小标题与子链接。', async () => {
    const user = userEvent.setup()
    renderNav()
    await user.click(screen.getByRole('button', { name: /系统/ }))
    expect(screen.getByText('账户与审计')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /平台设置/ })).toHaveAttribute('href', '/settings')
  })

  it('分组自身是链接时（无 children）仍渲染为可点链接。', async () => {
    const user = userEvent.setup()
    renderNav()
    // observability 工作区里那个分组只有 `to`，没有 children
    await user.click(screen.getByRole('button', { name: /服务器运维/ }))
    expect(screen.getByRole('link', { name: /监控/ })).toHaveAttribute('href', '/monitor')
  })

  it('工作区激活高亮按 pathname 命中任一子路由判定。', () => {
    renderNav({ pathname: '/nodes' })
    // 工作区含 /nodes 子路由 → 激活底色；系统工作区只有 /settings，不激活。
    // 用「激活分支专属的 bg-accent text-primary」判定，避开 hover:bg-accent/50 的子串误匹配。
    expect(screen.getByRole('button', { name: /总览/ }).className).toContain('bg-accent text-primary')
    expect(screen.getByRole('button', { name: /系统/ }).className).not.toContain('bg-accent text-primary')
  })

  it('展开中的工作区同时带激活样式。', async () => {
    const user = userEvent.setup()
    renderNav()
    await user.click(screen.getByRole('button', { name: /系统/ }))
    expect(screen.getByRole('button', { name: /系统/ }).className).toContain('bg-accent text-primary')
  })

  it('收起按钮关闭面板。', async () => {
    const user = userEvent.setup()
    renderNav()
    await user.click(screen.getByRole('button', { name: /总览/ }))
    await user.click(screen.getByRole('button', { name: '收起移动导航' }))
    expect(screen.getByRole('button', { name: /总览/ })).toHaveAttribute('aria-expanded', 'false')
  })

  it('工作区超过 5 个时只取前 5 个格子。', () => {
    const many: WorkspaceDef[] = Array.from({ length: 7 }, (_, i) => ({
      key: `w${i}`,
      labelKey: 'nav.overview',
      icon: LayoutDashboard,
      groups: [{ key: `g${i}`, labelKey: 'nav.overview', icon: LayoutDashboard, to: `/p${i}` }],
    }))
    const { container } = renderNav({ workspaces: many })
    expect(container.querySelectorAll('nav > div > button').length).toBe(5)
  })
})

/** 抑制未使用告警：vi 用于潜在扩展。 */
void vi
