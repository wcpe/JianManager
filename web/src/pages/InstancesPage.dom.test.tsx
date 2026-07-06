import { describe, it, expect, beforeEach } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import { useConsoleStore } from '@/stores/console'
import InstancesPage from './InstancesPage'

/**
 * 实例域页面强断言（FR-201）：种子渲染 + 启停状态联动 + 错误注入。
 * 跨域端点（节点/群组）由本测试文件 server.use 桩占位——本域只管实例自身，
 * 桩仅为隔离运行（集成后真实它域 handler 接管）。列表视图免触发卡片实时指标拉取。
 */
beforeEach(() => {
  loginMockUser()
  useConsoleStore.setState({ selectedNodeId: null })
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

function collectInstanceRequests() {
  const paths: string[] = []
  const urls: string[] = []
  const listener = ({ request }: { request: Request }) => {
    const url = new URL(request.url)
    if (url.pathname.startsWith('/api/v1/instances')) {
      paths.push(url.pathname)
      urls.push(url.toString())
    }
  }
  server.events.on('request:start', listener)
  return {
    paths,
    urls,
    stop: () => server.events.removeListener('request:start', listener),
  }
}

describe('InstancesPage（mock 假后端）', () => {
  it('渲染种子实例（名称可见）', async () => {
    const user = userEvent.setup()
    const { container } = renderWithProviders(<InstancesPage />, { route: '/instances' })
    expect(container.firstElementChild).toHaveAttribute('data-page', 'instances')
    expect(container.firstElementChild).toHaveClass('jm-page-stack')
    await switchToListView(user)
    expect(await screen.findByText('survival-1')).toBeInTheDocument()
    expect(screen.getByText('lobby-proxy')).toBeInTheDocument()
    expect(screen.getByText('creative-1')).toBeInTheDocument()
  })

  it('1000+ mock 实例下默认卡片视图只渲染可视窗口', async () => {
    renderWithProviders(<InstancesPage />, { route: '/instances' })

    const surface = await screen.findByTestId('instances-card-virtual')
    expect(Number(surface.dataset.totalCount)).toBeGreaterThanOrEqual(1000)
    expect(await screen.findByText('survival-1')).toBeInTheDocument()
    expect(screen.queryAllByTestId('instances-card-virtual-item').length).toBeLessThan(80)
  })

  it('1000+ 实例页首屏走分页搜索与聚合，不再拉取全集', async () => {
    const requests = collectInstanceRequests()
    try {
      renderWithProviders(<InstancesPage />, { route: '/instances' })
      await screen.findByTestId('instances-card-virtual')

      expect(requests.paths).toContain('/api/v1/instances/search')
      expect(requests.paths).toContain('/api/v1/instances/aggregate')
      expect(requests.paths).not.toContain('/api/v1/instances')
    } finally {
      requests.stop()
    }
  })

  it('修改搜索、状态和视图会写入 URL', async () => {
    renderWithProviders(<InstancesPage />, { route: '/instances' })

    fireEvent.change(await screen.findByRole('searchbox', { name: '搜索实例' }), { target: { value: 'survival' } })
    await waitFor(() => expect(new URLSearchParams(window.location.search).get('q')).toBe('survival'))

    fireEvent.click(screen.getByRole('button', { name: /运行/ }))
    await waitFor(() => expect(new URLSearchParams(window.location.search).get('status')).toBe('RUNNING'))

    fireEvent.click(screen.getByRole('button', { name: '列表视图' }))
    expect(new URLSearchParams(window.location.search).get('view')).toBe('list')

    fireEvent.click(screen.getByRole('button', { name: '组织分组' }))
    expect(new URLSearchParams(window.location.search).get('orgView')).toBe('1')
  })

  it('点击实例名称直接进入实例深链，不再依赖临时工作区状态', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list' })

    await user.click(await screen.findByRole('button', { name: 'survival-1' }))

    expect(window.location.pathname).toBe('/instances/1')
  })

  it('初始 URL 会恢复搜索、状态、列表视图和分组视图', async () => {
    const requests = collectInstanceRequests()
    try {
      renderWithProviders(<InstancesPage />, {
        route: '/instances?q=survival&status=RUNNING&view=list&groupBy=node&sort=name&order=desc&pageSize=50',
      })

      expect(await screen.findByRole('searchbox', { name: '搜索实例' })).toHaveValue('survival')
      expect(screen.getByRole('button', { name: '列表视图' })).toHaveAttribute('aria-pressed', 'true')
      expect(screen.getByRole('combobox', { name: '排序字段' })).toHaveTextContent('名称')
      expect(screen.getByRole('combobox', { name: '排序方向' })).toHaveTextContent('降序')
      expect(screen.getByRole('combobox', { name: '每页数量' })).toHaveTextContent('50 条')
      expect((await screen.findAllByText('node-a')).length).toBeGreaterThan(0)
      await waitFor(() => {
        const search = requests.urls
          .filter((url) => new URL(url).pathname === '/api/v1/instances/search')
          .map((url) => new URL(url).searchParams)
          .find((params) => params.get('q') === 'survival')
        expect(search?.get('status')).toBe('RUNNING')
        expect(search?.get('sort')).toBe('name')
        expect(search?.get('order')).toBe('desc')
        expect(search?.get('pageSize')).toBe('50')
      })
      expect(requests.paths).not.toContain('/api/v1/instances')
    } finally {
      requests.stop()
    }
  })

  it('初始 URL 会把 page 深链下发到分页搜索请求', async () => {
    const requests = collectInstanceRequests()
    try {
      renderWithProviders(<InstancesPage />, { route: '/instances?page=3&pageSize=50&view=list' })

      await screen.findByTestId('instances-table-virtual')
      await waitFor(() => {
        const search = requests.urls
          .filter((url) => new URL(url).pathname === '/api/v1/instances/search')
          .map((url) => new URL(url).searchParams)
          .find((params) => params.get('page') === '3')
        expect(search?.get('pageSize')).toBe('50')
      })
      expect(requests.paths).not.toContain('/api/v1/instances')
    } finally {
      requests.stop()
    }
  })

  it('筛选工具条可折叠，避免小屏长工具条横向翻屏', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances' })

    expect(await screen.findByRole('button', { name: '收起筛选' })).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: '群组' })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '收起筛选' }))

    expect(screen.getByRole('button', { name: '展开筛选' })).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('combobox', { name: '群组' })).not.toBeInTheDocument()
  })

  it('返回列表时恢复虚拟列表滚动位置', async () => {
    const route = '/instances?view=list'
    const first = renderWithProviders(<InstancesPage />, { route })
    const surface = await screen.findByTestId('instances-table-virtual')
    surface.scrollTop = 176
    fireEvent.scroll(surface)
    first.unmount()

    renderWithProviders(<InstancesPage />, { route })
    const restored = await screen.findByTestId('instances-table-virtual')
    await waitFor(() => expect(restored.scrollTop).toBe(176))
  })

  it('分组列表视图使用单个虚拟表，不为每组重复表头', async () => {
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=node' })

    const table = await screen.findByTestId('instances-table-virtual')
    expect(within(table).getAllByRole('table')).toHaveLength(1)
    expect(within(table).getAllByText('节点:端口')).toHaveLength(1)
    expect(within(table).getAllByTestId('instances-group-row').length).toBeGreaterThan(0)
  })

  it('点击列表表头会写入排序 URL 并切换方向', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&page=3' })
    await screen.findByTestId('instances-table-virtual')

    await user.click(screen.getByRole('button', { name: '按名称排序' }))
    await waitFor(() => {
      const params = new URLSearchParams(window.location.search)
      expect(params.get('sort')).toBe('name')
      expect(params.get('order')).toBeNull()
      expect(params.get('page')).toBeNull()
    })

    await user.click(screen.getByRole('button', { name: '按名称排序，当前升序' }))
    await waitFor(() => {
      const params = new URLSearchParams(window.location.search)
      expect(params.get('sort')).toBe('name')
      expect(params.get('order')).toBe('desc')
      expect(params.get('page')).toBeNull()
    })
  })

  it('页眉节点作用域会收敛全部服务器列表', async () => {
    useConsoleStore.setState({ selectedNodeId: 2 })
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances' })
    await switchToListView(user)

    expect(await screen.findByText('creative-1')).toBeInTheDocument()
    expect(screen.queryByText('survival-1')).not.toBeInTheDocument()
    expect(screen.queryByText('lobby-proxy')).not.toBeInTheDocument()
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
    mockInject('get', '/instances/search', { kind: 'status', status: 500 })
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances' })
    await switchToListView(user)

    // 列表查询失败 → 空态文案出现，且不渲染任何种子实例行。
    expect(await screen.findByText('暂无实例')).toBeInTheDocument()
    expect(screen.queryByText('survival-1')).not.toBeInTheDocument()
  })
})
