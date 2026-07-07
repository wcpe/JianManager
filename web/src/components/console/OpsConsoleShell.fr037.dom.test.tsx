import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
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

  it('渲染左侧控制台、节点作用域与右侧工作区', async () => {
    const { container } = renderWithProviders(<DashboardPage />, { route: '/instances' })
    const sidebar = container.querySelector('[data-slot="console-sidebar"]') as HTMLElement

    expect(container.querySelector('[data-slot="console-shell"]')).toHaveClass('jm-console-shell')
    expect(sidebar).toHaveClass('jm-console-sidebar')
    expect(container.querySelector('[data-slot="console-header"]')).toBeInTheDocument()
    expect(container.querySelector('[data-slot="console-main"]')).toHaveClass('jm-console-main')
    expect(within(sidebar).getByRole('link', { name: '平台首页' })).toHaveAttribute('href', '/')
    expect(within(sidebar).getByRole('button', { name: '服务器', exact: true })).toBeInTheDocument()
    expect(within(sidebar).getByRole('button', { name: '平台管理', exact: true })).toBeInTheDocument()

    const nodeScope = await screen.findByRole('button', { name: '节点作用域' })
    expect(nodeScope).toHaveTextContent('全部节点')
  })

  it('节点作用域联动服务器选择器，选择实例后进入工作区深链', async () => {
    const user = userEvent.setup()
    renderWithProviders(<DashboardPage />, { route: '/instances' })

    await user.click(await screen.findByRole('button', { name: '节点作用域' }))
    await user.click(await screen.findByText('beta'))
    await waitFor(() => expect(useConsoleStore.getState().selectedNodeId).toBe(2))

    await user.click(screen.getByRole('button', { name: '选择服务器' }))
    const dialog = await screen.findByRole('dialog', { name: '服务器选择器' })
    expect(dialog).toBeInTheDocument()
    await user.type(screen.getByRole('searchbox', { name: '搜索服务器' }), 'creative-1')

    const virtual = await screen.findByTestId('server-selector-virtual')
    await waitFor(() => expect(Number(virtual.dataset.totalCount)).toBeGreaterThanOrEqual(1))
    await user.click(await screen.findByRole('button', { name: /creative-1.*CRASHED/ }))

    await waitFor(() => expect(window.location.pathname).toBe('/instances/3'))
  })
})
