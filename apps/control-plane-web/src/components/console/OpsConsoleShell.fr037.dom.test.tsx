import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { useConsoleStore } from '@/stores/console'
import { useAuthStore } from '@/stores/auth'
import DashboardPage from '@/pages/DashboardPage'

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}

describe('FR-037 运维控制台 Shell（mock 假后端）', () => {
  beforeEach(() => {
    Object.defineProperty(window, 'ResizeObserver', { value: ResizeObserverStub, writable: true })
    loginMockUser()
    localStorage.setItem('refreshToken', 'test-refresh-token')
    localStorage.removeItem('server-selector.favorites')
    localStorage.removeItem('server-selector.recent')
    localStorage.removeItem('sidebar.selectedNodeId')
    useAuthStore.getState().loadFromStorage()
    useConsoleStore.setState({
      selectedNodeId: null,
      openInstanceId: null,
      sidebarCollapsed: false,
      collapsedGroups: {},
      commandPaletteOpen: false,
    })
  })

  it('渲染顶栏品牌区、左侧控制台与右侧工作区', async () => {
    const { container } = renderWithProviders(<DashboardPage />, { route: '/instances' })
    const sidebar = container.querySelector('[data-slot="console-sidebar"]') as HTMLElement
    const header = container.querySelector('[data-slot="console-header"]') as HTMLElement

    expect(container.querySelector('[data-slot="console-shell"]')).toHaveClass('jm-console-shell')
    expect(sidebar).toHaveClass('jm-console-sidebar')
    expect(header).toBeInTheDocument()
    expect(container.querySelector('[data-slot="console-main"]')).toHaveClass('jm-console-main')
    // FR-502 阶段 6：侧栏由六域长列表换成「工作区侧栏」——固定入口 + 资源导航。
    // 【FR-509 更新】侧栏重构删除了 `ServerSelector`（「选择服务器」按钮）与「按节点」分组方式下拉：
    // 资源定位改为资源面板内的「搜索 + 固定三段（置顶 / 后端分组树 / 标签）」。
    // 与 `WorkspaceSidebar.dom.test.tsx` 的「无选择服务器」断言对齐——两处不得再互相矛盾。
    expect(within(sidebar).getByRole('link', { name: '平台首页' })).toBeInTheDocument()
    expect(within(sidebar).queryByRole('button', { name: '选择服务器' })).not.toBeInTheDocument()
    expect(within(sidebar).queryByRole('button', { name: '按节点' })).not.toBeInTheDocument()
    // 资源面板仍在：搜索框是它的一部分（定位能力没有丢，只是换了形状）。
    expect(within(sidebar).getByRole('textbox', { name: '搜索服务器' })).toBeInTheDocument()

    // 方案 C：品牌 Logo 落顶栏品牌区，节点作用域下拉已下线。
    expect(await within(header).findByText('JianManager')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '节点作用域' })).not.toBeInTheDocument()

    // FR-502 阶段 6 补丁：顶栏是唯一一条——工作区切换在顶栏内，其下直接是「侧栏 + 工作区」。
    // 顶栏 44px（原 53px，用户要求整体比例缩小）。
    expect(header).toHaveClass('h-[44px]')
    expect(container.querySelector('[data-slot="console-workspace-bar"]')).toBeNull()
    expect(within(header).getByRole('navigation', { name: '工作区' })).toBeInTheDocument()
    // 顶栏不输出页名/面包屑：页名只由内容页承担。
    expect(container.querySelector('[aria-label="breadcrumb"]')).toBeNull()
  })

  it('服务器深链进入工作区（选择器已随侧栏重构删除，改由深链与资源面板定位）', async () => {
    // 【FR-509】`ServerSelector` 与它的「搜索命中后跳转」路径已删除——侧栏不再有那个弹窗。
    // 该用例原本覆盖的是「从 shell 进入某台服务器的深链」，这里保留同一目标：
    // 直接以 `/instances/3` 渲染控制台，断言 shell 能让该路由落地且不破坏外壳结构。
    renderWithProviders(<DashboardPage />, { route: '/instances/3' })

    await waitFor(() => expect(window.location.pathname).toBe('/instances/3'))
    const sidebar = document.querySelector('[data-slot="console-sidebar"]') as HTMLElement
    expect(sidebar).toBeInTheDocument()
    // 侧栏不再提供选择器弹窗（重构后由资源面板内联搜索承担定位）。
    expect(screen.queryByRole('dialog', { name: '服务器选择器' })).not.toBeInTheDocument()
  })
})
