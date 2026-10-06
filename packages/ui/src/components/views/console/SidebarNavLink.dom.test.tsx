import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import i18n from 'i18next'
import { initReactI18next, I18nextProvider } from 'react-i18next'
import type { ComponentProps, ReactNode } from 'react'
import { LayoutDashboard } from 'lucide-react'
import { SidebarNavLink, type NavLinkRenderArgs } from './SidebarNavLink'

/**
 * 侧栏导航链接 · 受控视图测（ADR-097）。
 *
 * 补的是**展示契约**：图标与文案渲染、`/` 精确匹配、激活态样式、嵌套缩进。
 * 路由渲染由测试自备的 renderLink 提供——这正是包内不依赖 react-router 的验证点：
 * 视图把 to/end/className/children 交出去，接什么路由库由外壳决定。
 */
const testI18n = i18n.createInstance()
void testI18n.use(initReactI18next).init({
  lng: 'zh-CN',
  fallbackLng: 'zh-CN',
  resources: {
    'zh-CN': { translation: { nav: { dashboard: '总览', nodes: '节点' } } },
  },
  interpolation: { escapeValue: false },
})

/** 测试自备的渲染函数：用最朴素的 <a> 承接视图交出的入参（无需任何路由库）。 */
function renderLink({ to, end, className, children }: NavLinkRenderArgs) {
  // 用固定激活态覆盖 className 回调，便于断言样式分支。
  const isActive = to === '/active'
  const cls = typeof className === 'function' ? className(isActive) : className
  return (
    <a href={to} data-end={String(end)} className={cls}>
      {children}
    </a>
  )
}

function renderNavLink(props: Partial<ComponentProps<typeof SidebarNavLink>> = {}) {
  render(
    <I18nextProvider i18n={testI18n}>
      <SidebarNavLink to="/dashboard" labelKey="nav.dashboard" renderLink={renderLink} {...props} />
    </I18nextProvider> as ReactNode,
  )
}

describe('SidebarNavLink（FR-061 侧栏导航链接）', () => {
  it('渲染图标与文案，to 透传为链接地址。', () => {
    renderNavLink({ icon: LayoutDashboard })
    const link = screen.getByRole('link', { name: /总览/ })
    expect(link).toHaveAttribute('href', '/dashboard')
    expect(link.querySelector('svg')).not.toBeNull()
  })

  it('`/` 与 `/networks` 精确匹配，其余为前缀匹配。', () => {
    renderNavLink({ to: '/', labelKey: 'nav.dashboard' })
    expect(screen.getByRole('link')).toHaveAttribute('data-end', 'true')

    renderNavLink({ to: '/networks', labelKey: 'nav.nodes' })
    expect(screen.getAllByRole('link').at(-1)).toHaveAttribute('data-end', 'true')

    renderNavLink({ to: '/instances', labelKey: 'nav.nodes' })
    expect(screen.getAllByRole('link').at(-1)).toHaveAttribute('data-end', 'false')
  })

  it('激活态加粗着色，非激活态用悬停底色。', () => {
    renderNavLink({ to: '/active', labelKey: 'nav.dashboard' })
    expect(screen.getByRole('link').className).toContain('font-semibold')

    renderNavLink({ to: '/other', labelKey: 'nav.dashboard' })
    expect(screen.getAllByRole('link').at(-1)?.className).toContain('hover:bg-accent/55')
  })

  it('嵌套项左缩进并降字号。', () => {
    renderNavLink({ nested: true })
    expect(screen.getByRole('link').className).toContain('pl-8')
  })
})
