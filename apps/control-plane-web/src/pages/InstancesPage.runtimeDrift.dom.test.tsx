import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'

import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { useAuthStore } from '@/stores/auth'
import { useConsoleStore } from '@/stores/console'
import InstancesPage from './InstancesPage'

/** 收集发往 /instances/:id/adopt-runtime 的请求。 */
function collectAdoptRequests() {
  const paths: string[] = []
  const listener = ({ request }: { request: Request }) => {
    const url = new URL(request.url)
    if (url.pathname.endsWith('/adopt-runtime')) paths.push(url.pathname)
  }
  server.events.on('request:start', listener)
  return { paths, stop: () => server.events.removeListener('request:start', listener) }
}

/**
 * FR-471：实例列表的运行态漂移标记与接管入口。
 * 种子 creative-plot（id=21）STOPPED + runtimeDriftPid>0；用 `q=creative-plot`
 * 收敛到单行，避免 1000+ 虚拟列表的其他行干扰断言。
 */
describe('InstancesPage 运行态漂移（FR-471）', () => {
  beforeEach(() => {
    loginMockUser()
    useConsoleStore.setState({ selectedNodeId: null })
    // 它域端点桩：本用例只关心实例域。
    server.use(
      http.get(API('/nodes'), () => HttpResponse.json([{ id: 1, name: 'node-a' }, { id: 2, name: 'node-b' }])),
      http.get(API('/networks'), () => HttpResponse.json([])),
    )
  })

  function renderDriftRow() {
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none&q=creative-plot' })
  }

  it('漂移行显示「运行态漂移」标记', async () => {
    renderDriftRow()

    const row = (await screen.findByText('creative-plot')).closest('tr') as HTMLElement
    const badge = within(row).getByTestId('runtime-drift-badge')
    expect(badge).toHaveTextContent('运行态漂移')
    // tooltip 里带上未纳管 PID，方便鼠标悬停即得证据。
    expect(badge.getAttribute('title')).toContain('41237')
  })

  it('「⋯」菜单里的接管入口经二次确认后才发 adopt-runtime 请求', async () => {
    // DangerConfirm scope=group 要求 role>=1。
    useAuthStore.setState({ role: 1, isAuthenticated: true })
    const user = userEvent.setup()
    const spy = collectAdoptRequests()
    try {
      renderDriftRow()

      const row = (await screen.findByText('creative-plot')).closest('tr') as HTMLElement
      await user.click(within(row).getByRole('button', { name: '更多操作' }))
      await user.click(await screen.findByTestId('instance-menu-adopt-runtime'))

      const dialog = await screen.findByRole('dialog')
      expect(within(dialog).getByText('接管实例「creative-plot」的运行态？')).toBeInTheDocument()
      // 打开菜单/弹窗本身不得发写请求。
      expect(spy.paths).toHaveLength(0)

      await user.click(within(dialog).getByRole('button', { name: '接管' }))
      await waitFor(() => expect(spy.paths).toContain('/api/v1/instances/21/adopt-runtime'))
    } finally {
      spy.stop()
    }
  })

  it('无漂移实例不显示标记与接管菜单项', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none&q=lobby-proxy' })

    const row = (await screen.findByText('lobby-proxy')).closest('tr') as HTMLElement
    expect(within(row).queryByTestId('runtime-drift-badge')).not.toBeInTheDocument()

    await user.click(within(row).getByRole('button', { name: '更多操作' }))
    await screen.findByText('编辑配置')
    expect(screen.queryByTestId('instance-menu-adopt-runtime')).not.toBeInTheDocument()
  })
})
