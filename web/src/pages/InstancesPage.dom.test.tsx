import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import InstancesPage from './InstancesPage'

/**
 * 实例域页面强断言（FR-201）：种子渲染 + 启停状态联动 + 错误注入。
 * 跨域端点（节点/群组）由本测试文件 server.use 桩占位——本域只管实例自身，
 * 桩仅为隔离运行（集成后真实它域 handler 接管）。列表视图免触发卡片实时指标拉取。
 */
beforeEach(() => {
  loginMockUser()
  // InstancesPage 顶层拉 /nodes 与 /networks（它域）；隔离测试用空集桩占位。
  server.use(
    http.get(API('/nodes'), () => HttpResponse.json([{ id: 1, name: 'node-a' }, { id: 2, name: 'node-b' }])),
    http.get(API('/networks'), () => HttpResponse.json([])),
  )
})

/** 切到列表视图（默认卡片视图会按 running 拉 /instances/:id/metrics——它域，避免触发）。 */
async function switchToListView(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: '列表视图' }))
}

describe('InstancesPage（mock 假后端）', () => {
  it('渲染种子实例（名称可见）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances' })
    await switchToListView(user)
    expect(await screen.findByText('survival-1')).toBeInTheDocument()
    expect(screen.getByText('lobby-proxy')).toBeInTheDocument()
    expect(screen.getByText('creative-1')).toBeInTheDocument()
  })

  it('启动已停止实例 → 行内出现「停止」操作（状态联动 RUNNING）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances' })
    await switchToListView(user)

    // lobby-proxy 种子为 STOPPED：其行内应有「启动」按钮、无「停止」。
    const row = (await screen.findByText('lobby-proxy')).closest('tr') as HTMLElement
    expect(within(row).getByRole('button', { name: '启动' })).toBeInTheDocument()

    await user.click(within(row).getByRole('button', { name: '启动' }))

    // 启动后假后端置 RUNNING，列表失效重拉 → 该行主操作切换为「停止」。
    await waitFor(() => {
      const updated = screen.getByText('lobby-proxy').closest('tr') as HTMLElement
      expect(within(updated).getByRole('button', { name: '停止' })).toBeInTheDocument()
    })
  })

  it('停止运行中实例 → 行内出现「启动」操作（状态联动 STOPPED）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances' })
    await switchToListView(user)

    // survival-1 种子为 RUNNING：行内应有「停止」按钮。
    const row = (await screen.findByText('survival-1')).closest('tr') as HTMLElement
    await user.click(within(row).getByRole('button', { name: '停止' }))

    await waitFor(() => {
      const updated = screen.getByText('survival-1').closest('tr') as HTMLElement
      expect(within(updated).getByRole('button', { name: '启动' })).toBeInTheDocument()
    })
  })

  it('注入 500 → 列表不渲染任何种子实例（错误态非崩溃）', async () => {
    mockInject('get', '/instances', { kind: 'status', status: 500 })
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances' })
    await switchToListView(user)

    // 列表查询失败 → 空态文案出现，且不渲染任何种子实例行。
    expect(await screen.findByText('暂无实例')).toBeInTheDocument()
    expect(screen.queryByText('survival-1')).not.toBeInTheDocument()
  })
})
