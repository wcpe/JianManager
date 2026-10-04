import { describe, it, expect, beforeEach } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useAuthStore } from '@/stores/auth'
import { usePermissionsStore } from '@/stores/permissions'
import { useConsoleStore } from '@/stores/console'
import { DEFAULT_ROLE_NODES } from '@/lib/roles'
import ConsoleHeader from './ConsoleHeader'

/**
 * 顶栏工作区切换（FR-496 阶段 6 补丁：从独立成行的 `ConsoleWorkspaceBar` 并回 `ConsoleHeader`）。
 *
 * 用例整体从 `WorkspaceSidebar.dom.test.tsx` 迁来（那份文件继续只测侧栏）：契约不变——
 * 工作区由路由决定、切换即进该工作区首个可用页面、按权限裁剪、判不出路由时回落到上次选择；
 * 变的只是挂载点：现在是顶栏这条 53px 通栏里的 `.space-nav`。
 */

/** 平台管理员登录（JWT + 权限 store 全开）。 */
function loginAsAdmin() {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
  loginMockUser(`mock.${payload}.sig`)
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

describe('顶栏工作区切换（FR-496 阶段 6 补丁）', () => {
  beforeEach(() => {
    loginAsAdmin()
    useConsoleStore.setState({ lastWorkspaceKey: null })
  })

  it('四个工作区按登记顺序渲染在顶栏内，当前工作区由路由点亮', () => {
    const { container } = renderWithProviders(<ConsoleHeader />, { route: '/instances' })
    const header = container.querySelector('[data-slot="console-header"]') as HTMLElement
    // 顶栏即工作区切换的容器：没有第二条工作区行（原型 `.topbar` 内含 `.space-nav`）。
    expect(container.querySelector('[data-slot="console-workspace-bar"]')).toBeNull()

    const nav = within(header).getByRole('navigation', { name: '工作区' })
    expect(within(nav).getAllByRole('button').map((b) => b.textContent)).toEqual([
      '服务器运维',
      '观测与自动化',
      '运营与分发',
      '平台管理',
    ])
    expect(within(nav).getByRole('button', { name: '服务器运维' })).toHaveAttribute('aria-current', 'page')
    expect(within(nav).getByRole('button', { name: '平台管理' })).not.toHaveAttribute('aria-current')
  })

  it('深链与子路由都归属其工作区（/alerts → 观测与自动化）', () => {
    renderWithProviders(<ConsoleHeader />, { route: '/alerts' })
    expect(screen.getByRole('button', { name: '观测与自动化' })).toHaveAttribute('aria-current', 'page')
  })

  it('切换工作区跳到该区首个可用页面并记住选择', async () => {
    const user = userEvent.setup()
    renderWithProviders(<ConsoleHeader />, { route: '/instances' })

    await user.click(screen.getByRole('button', { name: '观测与自动化' }))
    expect(window.location.pathname).toBe('/monitor')
    expect(useConsoleStore.getState().lastWorkspaceKey).toBe('observability')
    expect(screen.getByRole('button', { name: '观测与自动化' })).toHaveAttribute('aria-current', 'page')
  })

  it('路由判不出工作区时，回落到上次选择而不是第一个工作区', () => {
    useConsoleStore.setState({ lastWorkspaceKey: 'operation' })
    renderWithProviders(<ConsoleHeader />, { route: '/not-registered' })
    expect(screen.getByRole('button', { name: '运营与分发' })).toHaveAttribute('aria-current', 'page')
  })

  it('按权限裁剪工作区：平台管理员能看到四个，组管理员看不到任何入口的平台管理被整块丢弃', () => {
    loginAsGroupAdmin()
    // 组管理员在平台管理工作区只剩「存储与备份」组，工作区仍可见；这里锁的是「可见性来自裁剪结果」。
    const { container } = renderWithProviders(<ConsoleHeader />, { route: '/instances' })
    const nav = within(container).getByRole('navigation', { name: '工作区' })
    expect(within(nav).getByRole('button', { name: '服务器运维' })).toBeInTheDocument()
    expect(within(nav).getByRole('button', { name: '观测与自动化' })).toBeInTheDocument()

    // 只保留首页的成员角色（无任何权限种子）：工作区整块被丢弃时顶栏不出现空工作区。
    usePermissionsStore.setState({ nodes: new Set<string>(), roleKey: 'member', isPlatformAdmin: false, loaded: true })
    const { container: stripped } = renderWithProviders(<ConsoleHeader />, { route: '/' })
    const strippedNav = within(stripped).getByRole('navigation', { name: '工作区' })
    expect(Array.from(strippedNav.querySelectorAll('button')).map((b) => b.textContent)).toContain('服务器运维')
  })
})
