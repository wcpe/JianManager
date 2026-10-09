import { describe, it, expect, beforeEach } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { useConsoleStore } from '@/stores/console'
import {
  INSTANCE_EXPANDED_ROW_HEIGHT,
  INSTANCE_ROW_HEIGHT,
} from '@/components/views/instances/InstanceRowView'
import { INSTANCE_GROUP_ROW_HEIGHT } from '@/components/views/instances/VirtualizedInstanceTables'
import InstancesPage from './InstancesPage'

/**
 * 展开使该实例「项」在虚拟模型里增加的高度 = 追加的后端摘要行高。
 * 主行仍在，故增加量就是追加行的高度本身（不是它的差值）。
 */
const EXPANDED_ROW_EXTRA = INSTANCE_EXPANDED_ROW_HEIGHT

/**
 * 虚拟表「内容总高」（DOM 侧）：已渲染各行（含窗口上/下占位行）声明高度之和。
 *
 * 该值恒等于虚拟模型的总高，且与当前窗口落在哪一段无关——占位行的高度取自 `sizes` 的前缀和，
 * 数据行的高度就是各自的 `sizes`。因此「展开前后的差值」可以直接断言展开行是否被计入模型：
 * 任何一处漏算都会让差值不再是 `EXPANDED_ROW_EXTRA`。
 *
 * 行高统一从行盒读（占位行的高度写在单元格上，故行盒缺省时回落取首格）。
 */
function virtualContentHeight(surface: HTMLElement): number {
  const px = (el: Element | null | undefined) =>
    Number.parseFloat((el as HTMLElement | null)?.style.height ?? '') || 0
  return [...surface.querySelectorAll('tbody tr')].reduce(
    (sum, row) => sum + (px(row) || px(row.querySelector('td'))),
    0,
  )
}

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
    // FR-452：默认视图改为分组树表 + region 维度，故显式请求平铺以断言「实例名可见」。
    const { container } = renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none' })
    expect(container.firstElementChild).toHaveAttribute('data-page', 'instances')
    // 阶段 6 页面迁移起，外壳改用布局层 PageShell（原为手写的 jm-page-stack 骨架类）。
    // data-page 由 PageShell spread 透传，故上一行断言不变；这里改认它的标识。
    expect(container.firstElementChild).toHaveAttribute('data-slot', 'page-shell')
    expect(await screen.findByText('survival-1')).toBeInTheDocument()
    expect(screen.getByText('lobby-proxy')).toBeInTheDocument()
    expect(screen.getByText('creative-1')).toBeInTheDocument()
  })

  it('1000+ mock 实例下卡片视图只渲染可视窗口', async () => {
    renderWithProviders(<InstancesPage />, { route: '/instances?view=card&groupBy=none' })

    const surface = await screen.findByTestId('instances-card-virtual')
    expect(Number(surface.dataset.totalCount)).toBeGreaterThanOrEqual(1000)
    expect(await screen.findByText('survival-1')).toBeInTheDocument()
    expect(screen.queryAllByTestId('instances-card-virtual-item').length).toBeLessThan(80)
  })

  it('1000+ 实例页首屏走分页搜索与聚合，不再拉取全集', async () => {
    const requests = collectInstanceRequests()
    try {
      renderWithProviders(<InstancesPage />, { route: '/instances?view=card&groupBy=none' })
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

    // FR-452：list 已成为默认视图 → 选中它时不再写 view 参数（缺省即 list）。
    fireEvent.click(screen.getByRole('button', { name: '卡片视图' }))
    expect(new URLSearchParams(window.location.search).get('view')).toBe('card')
    fireEvent.click(screen.getByRole('button', { name: '列表视图' }))
    expect(new URLSearchParams(window.location.search).get('view')).toBeNull()
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

  it('路由切换时浏览器把滚动容器归零也不会清掉待恢复位置', async () => {
    const route = '/instances?view=list'
    const first = renderWithProviders(<InstancesPage />, { route })
    const surface = await screen.findByTestId('instances-table-virtual')
    surface.scrollTop = 176
    fireEvent.scroll(surface)

    // 真实浏览器在离开列表路由时可能把内层滚动容器先归零并触发 scroll；
    // 该归零不是用户主动滚回顶部，不能删除已经保存的返回位置。
    surface.scrollTop = 0
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
    // 平铺视图避免分组头把种子实例挤出虚拟窗口。
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none' })

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

  it('展示「已加载/共」计数与显式「加载更多」按钮（FR-235 可供性）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances' })
    await switchToListView(user)

    // 计数文本：已加载首页 < 总数（1000+ seed），故计数与加载更多按钮均出现。
    const count = await screen.findByTestId('instances-loaded-count')
    // 计数文案「已加载 N / 共 M」（FR-235 可供性）。
    expect(count.textContent).toMatch(/已加载 \d+ \/ 共 \d+/)

    const loadMore = screen.getByTestId('instances-load-more')
    expect(loadMore).toBeInTheDocument()

    // 点击加载更多 → 追加下一页 → 已加载数增加。
    const loadedBefore = Number(count.textContent?.match(/已加载 (\d+)/)?.[1] ?? '0')
    await user.click(loadMore)
    await waitFor(() => {
      const after = Number(
        screen.getByTestId('instances-loaded-count').textContent?.match(/已加载 (\d+)/)?.[1] ?? '0',
      )
      expect(after).toBeGreaterThan(loadedBefore)
    })
  })

  it('分组视图标注「仅当前已加载页」（FR-235 可供性）', async () => {
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=node' })
    await screen.findByTestId('instances-table-virtual')

    const count = await screen.findByTestId('instances-loaded-count')
    // 分组下追加「（分组仅含当前已加载页）」标注（FR-235 可供性）。
    expect(count.textContent).toContain('（分组仅含当前已加载页）')
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

  /**
   * proxy 行的内联后端摘要（展开时追加的那一个 `<tr>`）必须计入虚拟高度模型（平铺表）。
   *
   * 展开是在同一个虚拟「项」下**追加一个 `<tr>`**：虚拟窗口若只按「1 项 = 实例行高」计算，
   * 这段高度完全不计入占位，其后所有行的位置与滚动条长度都会漂移。
   * 断言用「内容总高 = 模型总高」这一与窗口无关的量：模型总高必须恰好多出一个追加行，
   * 且追加行盒自己也声明为该高度（声明值与模型同源）。
   */
  it('展开 proxy 后端摘要：展开行计入虚拟高度模型（平铺表）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none' })
    const surface = await screen.findByTestId('instances-table-virtual')

    // 「管理后端」切换按钮只出现在 proxy 行上；点第一个即可（其它 proxy 行的展开态与本断言无关）。
    await user.click((await screen.findAllByRole('button', { name: '管理后端' }))[0])
    const expandedCell = await screen.findByText(/已注册后端|暂无已注册后端/)
    const expandedRow = expandedCell.closest('tr') as HTMLElement
    expect(expandedRow.style.height).toBe(`${INSTANCE_EXPANDED_ROW_HEIGHT}px`)

    // 【关键】把窗口滚到展开行之后：此后展开行与追加行都不在 DOM 里（虚拟窗口已卸载它们），
    // 「内容总高」若仍然涨出追加行的高度，就只可能来自模型的逐行高度——展开行真的被计入了。
    // （只断言「展开后总高变大」是不够的：追加行本身就在窗口里时，它无论如何都会被算进 DOM。）
    surface.scrollTop = 6000
    fireEvent.scroll(surface)
    expect(surface.querySelectorAll(`tbody tr[style*="height: ${INSTANCE_EXPANDED_ROW_HEIGHT}px"]`)).toHaveLength(0)

    // 平铺表每一项都是实例行，故模型总高 = N × 实例行高 + 追加行高。
    const total = Number(surface.dataset.totalCount)
    expect(virtualContentHeight(surface)).toBe(total * INSTANCE_ROW_HEIGHT + EXPANDED_ROW_EXTRA)
  })

  /**
   * 同一判据在分组树表（组头 / 实例行 / 展开追加行三种高度共存）同样成立。
   *
   * 收敛到单个节点：树表的行模型 = 1 个组头行 + 该节点的全部实例行（全部已加载时才可精确推算），
   * 于是模型总高可写成 `组头 + N × 实例行 + 追加行` 这一确定式。
   */
  it('展开 proxy 后端摘要：展开行计入虚拟高度模型（分组树表）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=node&nodeId=1' })
    const surface = await screen.findByTestId('instances-table-virtual')
    expect(within(surface).getAllByTestId('instances-group-row').length).toBe(1)

    await user.click((await screen.findAllByRole('button', { name: '管理后端' }))[0])
    await screen.findByText(/已注册后端|暂无已注册后端/)

    surface.scrollTop = 3000
    fireEvent.scroll(surface)
    expect(surface.querySelectorAll(`tbody tr[style*="height: ${INSTANCE_EXPANDED_ROW_HEIGHT}px"]`)).toHaveLength(0)

    const loaded = Number(/已加载 (\d+)/.exec(screen.getByTestId('instances-loaded-count').textContent ?? '')?.[1])
    expect(loaded).toBeGreaterThan(0)
    expect(virtualContentHeight(surface)).toBe(
      INSTANCE_GROUP_ROW_HEIGHT + loaded * INSTANCE_ROW_HEIGHT + EXPANDED_ROW_EXTRA,
    )
  })
})
