import { describe, it, expect, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { Boxes, LayoutDashboard, Server, ShieldCheck } from 'lucide-react'
import { MobileConsoleNav } from '@/components/views/console/MobileConsoleNav'
import type { NavGroup } from '@/lib/shared/nav-config'
import type { SidebarLinkArgs } from '@/components/views/console/sidebar-link'

/**
 * 手机端底部导航 · 受控视图测（ADR-097）。
 *
 * 补的是**展示契约**：主域条（≤5 个）、有链接直接跳 / 无链接展开面板、面板内子项与分节、
 * 点击后收起面板、主域激活高亮。权限裁剪与路由联动由应用侧接线层承担。
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

const groups: NavGroup[] = [
  { key: 'overview', labelKey: 'nav.overview', icon: LayoutDashboard, to: '/' },
  {
    key: 'cluster',
    labelKey: 'nav.cluster',
    icon: Boxes,
    children: [
      { to: '/instances', labelKey: 'nav.instances', icon: Server },
      { to: '/nodes', labelKey: 'nav.nodes', icon: Server },
    ],
  },
  {
    key: 'system',
    labelKey: 'nav.system',
    icon: ShieldCheck,
    sections: [{ labelKey: 'nav.auditSettings', children: [{ to: '/settings', labelKey: 'nav.settings', icon: ShieldCheck }] }],
  },
]

function renderNav(props: Partial<ComponentProps<typeof MobileConsoleNav>> = {}) {
  const merged = { groups, pathname: '/', renderLink, ...props }
  const { container } = render(
    <I18nextProvider i18n={testI18n}>
      <MobileConsoleNav {...merged} />
    </I18nextProvider> as ReactNode,
  )
  return { ...merged, container }
}

describe('MobileConsoleNav（移动端底部导航受控视图）', () => {
  it('主域条最多渲染 5 个入口；有链接的主域渲染为链接。', () => {
    renderNav()
    const nav = screen.getByRole('navigation', { name: '移动导航' })
    expect(within(nav).getByRole('link', { name: /总览/ })).toHaveAttribute('href', '/')
    // 无 to 的主域是按钮（展开面板）。
    expect(within(nav).getByRole('button', { name: /集群/ })).toBeInTheDocument()
    expect(within(nav).getByRole('button', { name: /系统/ })).toBeInTheDocument()
  })

  it('点击无链接的主域展开面板，aria-expanded 反映状态。', async () => {
    const user = userEvent.setup()
    renderNav()
    const trigger = screen.getByRole('button', { name: /集群/ })
    expect(trigger).toHaveAttribute('aria-expanded', 'false')

    await user.click(trigger)
    expect(screen.getByRole('button', { name: /集群/ })).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByText('全部服务器')).toBeInTheDocument()
    expect(screen.getByText('节点')).toBeInTheDocument()
  })

  it('面板内子项点击后收起面板（走注入的 onClick）。', async () => {
    const user = userEvent.setup()
    renderNav()
    await user.click(screen.getByRole('button', { name: /集群/ }))
    await user.click(screen.getByRole('link', { name: /全部服务器/ }))
    expect(screen.getByRole('button', { name: /集群/ })).toHaveAttribute('aria-expanded', 'false')
  })

  it('面板内分节渲染小标题与子链接。', async () => {
    const user = userEvent.setup()
    renderNav()
    await user.click(screen.getByRole('button', { name: /系统/ }))
    expect(screen.getByText('账户与审计')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /平台设置/ })).toHaveAttribute('href', '/settings')
  })

  it('主域激活高亮按 pathname 命中子路由判定。', () => {
    renderNav({ pathname: '/nodes' })
    // 集群域含 /nodes 子路由 → 激活底色；系统域只有 /settings，不激活。
    // 用「激活分支专属的 bg-accent text-primary」判定，避开 hover:bg-accent/50 的子串误匹配。
    expect(screen.getByRole('button', { name: /集群/ }).className).toContain('bg-accent text-primary')
    expect(screen.getByRole('button', { name: /系统/ }).className).not.toContain('bg-accent text-primary')
  })

  it('展开中的主域同时带激活样式。', async () => {
    const user = userEvent.setup()
    renderNav()
    await user.click(screen.getByRole('button', { name: /系统/ }))
    expect(screen.getByRole('button', { name: /系统/ }).className).toContain('bg-accent text-primary')
  })

  it('收起按钮关闭面板。', async () => {
    const user = userEvent.setup()
    renderNav()
    await user.click(screen.getByRole('button', { name: /集群/ }))
    await user.click(screen.getByRole('button', { name: '收起移动导航' }))
    expect(screen.getByRole('button', { name: /集群/ })).toHaveAttribute('aria-expanded', 'false')
  })

  it('组数超过 5 时只取前 5 个主域。', () => {
    const many: NavGroup[] = Array.from({ length: 7 }, (_, i) => ({
      key: `g${i}`,
      labelKey: 'nav.overview',
      icon: LayoutDashboard,
      to: `/p${i}`,
    }))
    const { container } = renderNav({ groups: many })
    expect(container.querySelectorAll('a[href^="/p"]').length).toBe(5)
  })
})

/** 抑制未使用告警：vi 用于潜在扩展。 */
void vi
