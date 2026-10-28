import { describe, it, expect, beforeEach, vi } from 'vitest'
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
import InstancesPage from './InstancesPage'

/** 最小 DataTransfer 桩：jsdom 不实现拖拽数据的读写，这里只满足 setData/getData 调用。 */
function makeDataTransfer() {
  const store = new Map<string, string>()
  return {
    setData: (k: string, v: string) => void store.set(k, v),
    getData: (k: string) => store.get(k) ?? '',
    effectAllowed: '',
    dropEffect: '',
  }
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

/**
 * 切到列表视图。
 *
 * 列表自 FR-452 起已是默认视图，故这里通常只是「确认已在列表」的一次点击（幂等）；
 * 保留它是因为正文用例仍是从默认入口进入，写法不必随默认值变化。
 */
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

/** 已下发的 `/instances/search` 请求的 `q` 参数序列（无 q 的请求记为空串）。 */
function searchQueries(urls: string[]): string[] {
  return urls
    .filter((url) => new URL(url).pathname === '/api/v1/instances/search')
    .map((url) => new URL(url).searchParams.get('q') ?? '')
}

/*
  【文件级超时】本文件每个用例都渲染整页 + 1000+ 条种子实例（jsdom 下单次渲染 1.5~8s，
  见各用例实测耗时），默认 10s 在并行跑多文件或机器有负载时会随机超时——超时来自渲染量，
  不是断言问题。故整文件放宽到 30s；断言本身不变，单个用例要更严格时再就地覆写。
*/
vi.setConfig({ testTimeout: 30_000 })

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

  /*
    【实现变更·本轮】列表改服务端分页后，卡片视图的行模型只剩**当前页**（每页 50 条），
    不再按 1000+ 全集建模。原断言「容器 totalCount ≥ 1000」表达的是「千条也只渲染可视窗口」，
    现改为「容器只承载当前分页、挂载卡片数不超过一页」——守的仍是「不一次铺满千条」这件事。
  */
  it('卡片视图只承载当前分页，不一次铺满 1000+ 实例', async () => {
    renderWithProviders(<InstancesPage />, { route: '/instances?view=card&groupBy=none' })

    const surface = await screen.findByTestId('instances-card-virtual')
    const rows = Number(surface.dataset.totalCount)
    expect(rows).toBeGreaterThan(0)
    expect(rows).toBeLessThanOrEqual(50)
    expect(await screen.findByText('survival-1')).toBeInTheDocument()
    expect(screen.queryAllByTestId('instances-card-virtual-item').length).toBeLessThanOrEqual(50)
  })

  /**
   * 分组卡片视图同样是**一个**容器 + **一个**虚拟窗口：组头行与卡片行同属一个行流。
   *
   * 守的是「挂载量不随分组数放大」这条性质——旧实现每组一段各挂一个 `VirtualizedCardGrid`
   * （各带一个滚动容器与一个虚拟窗口），段数 = 顶层分组数，每段都各自铺满视口窗口；
   * 真机 Chromium（1440×900）实测 `?view=card&groupBy=region` 4 个容器共挂 79 张卡
   * （21+21+21+16）。这里以 region（两级）与 tag（段数更多）两个维度断言：容器恒为 1 个、
   * 组头行确实渲染、挂载卡数仍在单一视口窗口量级。
   */
  it('分组卡片视图使用单个虚拟容器，不为每组重复网格', async () => {
    // 起点用 groupTree（region 已不做分组层级，不会产出组头行）。
    const { unmount } = renderWithProviders(<InstancesPage />, { route: '/instances?view=card&groupBy=groupTree' })

    const surface = await screen.findByTestId('instances-card-virtual')
    expect(screen.queryAllByTestId('instances-card-virtual')).toHaveLength(1)
    await waitFor(() => expect(within(surface).queryAllByTestId('instances-card-group-row').length).toBeGreaterThan(0))
    expect(screen.queryAllByTestId('instances-card-virtual-item').length).toBeLessThan(40)
    unmount()

    /*
      第二个维度改用 env（分组数多于 groupTree，能放大「分段实现」的挂载量差异）。
      原先写的 `groupBy=tag` 不在 GROUP_DIMENSIONS 里、会被解析回退成不分组，
      那样「挂载量仍与分组数无关」这条断言其实没验到分组场景——顺手修正。
    */
    renderWithProviders(<InstancesPage />, { route: '/instances?view=card&groupBy=env' })
    const tagged = await screen.findByTestId('instances-card-virtual')
    expect(screen.queryAllByTestId('instances-card-virtual')).toHaveLength(1)
    await waitFor(() => expect(within(tagged).queryAllByTestId('instances-card-group-row').length).toBeGreaterThan(0))
    expect(screen.queryAllByTestId('instances-card-virtual-item').length).toBeLessThan(40)
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
    const user = userEvent.setup()
    try {
      renderWithProviders(<InstancesPage />, {
        route: '/instances?q=survival&status=RUNNING&view=list&groupBy=node&sort=name&order=desc&pageSize=50',
      })

      expect(await screen.findByRole('searchbox', { name: '搜索实例' })).toHaveValue('survival')
      expect(screen.getByRole('button', { name: '列表视图' })).toHaveAttribute('aria-pressed', 'true')
      /*
        【交互变更·本轮】排序字段 / 排序方向 / 每页数量 / 分组维度从工具栏的独立下拉
        收进了「显示」菜单。URL → 状态 → 请求参数这条链路没变，但界面上不再有
        combobox，故把「界面回显」的断言改为「打开菜单能看到对应项被选中」。
      */
      await user.click(screen.getByRole('button', { name: '显示' }))
      expect(screen.getByRole('menuitemradio', { name: '名称' })).toBeChecked()
      expect(screen.getByRole('menuitemradio', { name: '降序' })).toBeChecked()
      expect(screen.getByRole('menuitemradio', { name: '50 条' })).toBeChecked()
      await user.keyboard('{Escape}')
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

  it('筛选面板可折叠，避免小屏长工具条横向翻屏', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances' })

    // 【交互变更·本轮】「展开筛选 / 收起筛选」两个文案统一为一个「筛选」按钮
    // （开合由 aria-expanded 表达，文案不再随状态变化）。
    expect(await screen.findByRole('button', { name: '筛选' })).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: '群组' })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '筛选' }))

    expect(screen.getByRole('button', { name: '筛选' })).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('combobox', { name: '群组' })).not.toBeInTheDocument()
  })

  /**
   * 搜索输入必须防抖（项目硬规则：服务端搜索不得逐键下发）。
   *
   * 搜索词直接进 queryKey，不防抖就是「每敲一个字符一次 /instances/search」：1000+ 实例下
   * 既是请求风暴，也让列表在输入过程中反复闪回加载态。本用例连打 3 个字符，断言
   * ① 中间态（'s' / 'su'）从未下发；② 落定值 'sur' 只下发一次。
   */
  it('搜索输入防抖：连续键入只下发一次落定后的服务端搜索', async () => {
    const user = userEvent.setup()
    // 把搜索缩到 5 条：本用例断言的是「键入与下发之间的时间关系」，而种子 1000+ 实例下
    // 每次键入的整页重渲染在 jsdom 里可能就要几百毫秒——那会把 300ms 防抖窗口吃掉，
    // 让用例变成对机器快慢的赌注，而不是对防抖行为的断言。
    server.use(
      http.get(API('/instances/search'), () =>
        HttpResponse.json({
          items: Array.from({ length: 5 }, (_, i) => ({
            id: i + 1,
            uuid: `inst-${i + 1}`,
            nodeId: 1,
            name: `srv-${i + 1}`,
            type: 'minecraft_java',
            role: 'backend',
            processType: 'daemon',
            status: 'STOPPED',
            startCommand: '',
            workDir: '/srv',
            serverPort: 0,
            autoStart: false,
            autoRestart: false,
            tags: '',
            createdAt: new Date().toISOString(),
          })),
          total: 5,
          page: 1,
          pageSize: 200,
        }),
      ),
    )
    const requests = collectInstanceRequests()
    try {
      renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none' })
      await screen.findByTestId('instances-table-virtual')
      await screen.findByText('srv-1')

      const box = screen.getByRole('searchbox', { name: '搜索实例' })
      // 逐字键入（每字之间只让 1ms，远小于 300ms 防抖窗口）：真实用户按键本就是独立任务，
      // 若把三次 change 塞进同一批，只有最后一次进查询 key，用例就测不出「逐键下发」了。
      await user.type(box, 'sur', { delay: 1 })

      // 落定前不得有任何带 q 的请求（中间态一个都不许漏出去）。
      expect(searchQueries(requests.urls).filter((q) => q !== '')).toEqual([])

      await waitFor(() => expect(searchQueries(requests.urls)).toContain('sur'))
      // 只有落定值那一次：'s' / 'su' 永不出现，且 'sur' 不重复下发。
      expect(searchQueries(requests.urls).filter((q) => q !== '')).toEqual(['sur'])
    } finally {
      requests.stop()
    }
  })

  /**
   * 搜索键入写 URL 必须用 replace（不得 push）。
   *
   * 逐键 push 会把历史栈塞满：输 10 个字符后要按 11 次「返回」才离开本页。
   */
  it('搜索键入写 URL 走 replace，不往历史栈追加记录', async () => {
    const pushSpy = vi.spyOn(window.history, 'pushState')
    const replaceSpy = vi.spyOn(window.history, 'replaceState')
    try {
      renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none' })
      await screen.findByTestId('instances-table-virtual')
      pushSpy.mockClear()
      replaceSpy.mockClear()

      const box = screen.getByRole('searchbox', { name: '搜索实例' })
      fireEvent.change(box, { target: { value: 'a' } })
      fireEvent.change(box, { target: { value: 'ab' } })

      expect(new URLSearchParams(window.location.search).get('q')).toBe('ab')
      expect(replaceSpy).toHaveBeenCalled()
      expect(pushSpy).not.toHaveBeenCalled()
    } finally {
      pushSpy.mockRestore()
      replaceSpy.mockRestore()
    }
  })

  /**
   * 表头「全选」的作用域必须写进可访问名。
   *
   * 列表分页加载，`allIds` 只覆盖**已加载页**，而页面上同时显示「已加载 N / 共 M」；
   * 只叫「全选」会让用户（尤其读屏用户）理解成全库全选，批量删除的作用域与理解不符。
   */
  it('表头全选框的可访问名写明作用域（只覆盖已加载页）', async () => {
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none' })
    await screen.findByTestId('instances-table-virtual')

    const checkbox = screen.getByRole('checkbox', { name: /全选/ })
    expect(checkbox).toHaveAccessibleName(/已加载 \d+ \/ 共 \d+/)
  })

  /**
   * 返回列表时停在上次翻到的那一页。
   *
   * 【实现变更·本轮】列表改服务端分页、位置由 URL 的 `page` 承载，不再有滚动位置恢复
   * （原实现用 `useStoredVirtualScroll` 把 scrollTop 存进 sessionStorage）。
   * 原两个用例（「恢复滚动位置」「滚动容器归零不清掉待恢复位置」）合并为本用例：
   * 语义从「恢复到某个像素」变成「恢复到某一页」，且「浏览器离开路由时把滚动容器归零」
   * 这一事件不得把页码打回首页——这条守的仍是同一个回归：非用户意图的滚动不该改变位置。
   */
  it('返回列表时停在上次翻到的分页（位置由 URL 的 page 承载）', async () => {
    const user = userEvent.setup()
    const first = renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none' })
    const surface = await screen.findByTestId('instances-table-virtual')

    // 翻到第 2 页：位置写进 URL。
    await user.click(screen.getByRole('button', { name: '下一页' }))
    await waitFor(() => expect(new URLSearchParams(window.location.search).get('page')).toBe('2'))

    // 真实浏览器在离开列表路由时可能把内层滚动容器先归零并触发 scroll；
    // 该归零不是用户主动翻页，不能把页码重置回首页。
    surface.scrollTop = 0
    fireEvent.scroll(surface)
    expect(new URLSearchParams(window.location.search).get('page')).toBe('2')
    first.unmount()

    // 重新进入列表（深链同一 page）→ 分页条仍停在第 2 页。
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none&page=2' })
    await screen.findByTestId('instances-table-virtual')
    await waitFor(() => expect(screen.getByRole('button', { name: '2' })).toHaveAttribute('aria-current', 'page'))
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

    /*
      启动现在要二次确认（本轮新增）：点击行内按钮只弹出确认框，不发请求。
      断言确认框出现、并在确认后才联动——这也顺带把「误点不会直接启服」钉成回归测试。
    */
    const startDialog = await screen.findByRole('dialog')
    expect(within(startDialog).getByText(/启动实例「lobby-proxy」/)).toBeInTheDocument()
    await user.click(within(startDialog).getByRole('button', { name: '启动' }))

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

    // 停止同样要二次确认：确认后才真正下发停止请求。
    const stopDialog = await screen.findByRole('dialog')
    expect(within(stopDialog).getByText(/停止实例「survival-1」/)).toBeInTheDocument()
    await user.click(within(stopDialog).getByRole('button', { name: '停止' }))

    await waitFor(() => {
      const updated = screen.getByText('survival-1').closest('tr') as HTMLElement
      expect(within(updated).getByRole('button', { name: '启动' })).toBeInTheDocument()
    })
  })

  /**
   * 分页条给出「已加载 / 共」与页码可供性（FR-235）。
   *
   * 【实现变更·本轮】列表改服务端分页后不再有「加载更多」按钮：翻页由分页条的
   * 上一页 / 页码 / 下一页承担，计数文案也并入同一条分页条（原来它单独占一行）。
   * 断言因此改为「计数含页码上下文 + 首页上一页禁用 + 下一页可用且能翻页」。
   */
  it('分页条给出「已加载/共」计数与上一页/下一页（FR-235 可供性）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?groupBy=none' })

    // 计数文本：首屏只加载首页，故已加载 < 总数（1000+ seed）。
    const count = await screen.findByTestId('instances-loaded-count')
    expect(count.textContent).toMatch(/已加载 \d+ \/ 共 \d+ 条，第 1 \/ \d+ 页/)

    // 首页：上一页禁用（无处可退），下一页可用（还有后续页）。
    const prev = screen.getByRole('button', { name: '上一页' })
    const next = screen.getByRole('button', { name: '下一页' })
    expect(prev).toBeDisabled()
    expect(next).toBeEnabled()

    // 点击下一页 → 当前页前进，计数与页码按钮同步。
    await user.click(next)
    await waitFor(() => expect(new URLSearchParams(window.location.search).get('page')).toBe('2'))
    await waitFor(() =>
      expect(screen.getByTestId('instances-loaded-count').textContent).toContain('第 2 / '),
    )
    expect(screen.getByRole('button', { name: '2' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('button', { name: '上一页' })).toBeEnabled()
  })

  /**
   * 分组视图的作用域声明（FR-235 可供性）。
   *
   * 【实现变更·本轮】原计数行会追加「（分组仅含当前已加载页）」后缀；分页条合并后
   * 该后缀被去掉，作用域改由「已加载 N / 共 M 条，第 x / y 页」直接表达——
   * N < M 即说明分组头的成员计数只覆盖当前页，无需再加一句提示。
   */
  it('分组视图的作用域由分页计数直接表达（未加载全量）', async () => {
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=node' })
    const table = await screen.findByTestId('instances-table-virtual')

    const count = await screen.findByTestId('instances-loaded-count')
    expect(count.textContent).toMatch(/已加载 \d+ \/ 共 \d+ 条，第 \d+ \/ \d+ 页/)
    const loaded = Number(/已加载 (\d+)/.exec(count.textContent ?? '')?.[1])
    const total = Number(/共 (\d+)/.exec(count.textContent ?? '')?.[1])
    // 已加载只是全量的一个分页：用户不会把分组头的计数误读成「全库统计」。
    expect(loaded).toBeLessThan(total)

    // 分组头照常渲染（node 是真实层级，不随分页改动）。
    expect(within(table).getAllByTestId('instances-group-row').length).toBeGreaterThan(0)
  })

  it('注入 500 → 列表不渲染任何种子实例（错误态非崩溃）', async () => {
    mockInject('get', '/instances/search', { kind: 'status', status: 500 })
    // 显式用平铺视图：分组视图（groupBy 非 none）下表格拿到的是 treeRows，空表时不渲染
    // 空态文案（见交付说明中记录的产品疑点），而本用例要断言的正是「空态文案 + 无种子行」。
    renderWithProviders(<InstancesPage />, { route: '/instances?groupBy=none' })

    // 列表查询失败 → 空态文案出现，且不渲染任何种子实例行。
    expect(await screen.findByText('暂无实例')).toBeInTheDocument()
    expect(screen.queryByText('survival-1')).not.toBeInTheDocument()
  })

  /**
   * proxy 行的内联后端摘要（展开时追加的那一个 `<tr>`）。
   *
   * 【实现变更·本轮】列表改服务端分页后不再有虚拟高度模型，故原用例的
   * 「展开行须计入模型总高」判据已无对象。保留的是它真正要守的产品行为：
   * ① 点「管理后端」在代理行**下方**追加一行摘要；② 该行高度声明为导出常量
   * （行盒与 `INSTANCE_EXPANDED_ROW_HEIGHT` 同源，不随后端条数变形）；
   * ③ 摘要行的 colSpan 跟随当前列数（列设置改列数时仍不错位）；④ 再次点击可收起。
   */
  it('展开 proxy 后端摘要：行下追加一行摘要，再次点击收起', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none' })
    const surface = await screen.findByTestId('instances-table-virtual')

    // 「管理后端」切换按钮只出现在 proxy 行上；点第一个即可（其它 proxy 行的展开态与本断言无关）。
    const toggle = (await screen.findAllByRole('button', { name: '管理后端' }))[0]
    await user.click(toggle)

    const expandedCell = await screen.findByText(/已注册后端|暂无已注册后端/)
    const expandedRow = expandedCell.closest('tr') as HTMLElement
    expect(expandedRow.style.height).toBe(`${INSTANCE_EXPANDED_ROW_HEIGHT}px`)
    // 摘要行紧跟在代理主行之后（DOM 里确实是两个 <tr>，不是替换）。
    expect(expandedRow.previousElementSibling?.querySelector('[aria-label="管理后端"]')).not.toBeNull()
    // 列对齐：摘要格横跨整行（不少于表头实际列数，右侧不留空列）。
    // 不写死具体数字：列数与列设置同源，未来调整列模型时这条断言不该跟着红。
    const headCells = surface.querySelectorAll('thead th').length
    const colSpan = Number(expandedCell.closest('td')?.getAttribute('colspan') ?? '0')
    expect(colSpan).toBeGreaterThanOrEqual(headCells)

    // 再次点击 → 收起，摘要行从 DOM 消失。
    await user.click(toggle)
    await waitFor(() =>
      expect(surface.querySelectorAll(`tbody tr[style*="height: ${INSTANCE_EXPANDED_ROW_HEIGHT}px"]`)).toHaveLength(0),
    )
  })

  /**
   * 分组树表下同一交互不得扰动行序：摘要行只插在成员行之后，组头与成员计数都不变。
   */
  it('展开 proxy 后端摘要：分组树表的组头与行序不受影响', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=node&nodeId=1' })
    const surface = await screen.findByTestId('instances-table-virtual')
    expect(within(surface).getAllByTestId('instances-group-row').length).toBe(1)
    const rowsBefore = surface.querySelectorAll('tbody tr').length

    await user.click((await screen.findAllByRole('button', { name: '管理后端' }))[0])
    await screen.findByText(/已注册后端|暂无已注册后端/)

    // 只多出一行（摘要行），组头仍在原位、仍只有 1 个。
    expect(surface.querySelectorAll('tbody tr').length).toBe(rowsBefore + 1)
    expect(within(surface).getAllByTestId('instances-group-row').length).toBe(1)
    expect(surface.querySelectorAll('tbody tr')[0]).toHaveAttribute('data-testid', 'instances-group-row')
  })

  /**
   * 列设置（页面重构件）：取消勾选「类型 / 角色 / 标签」→ 表头「身份」列与行内身份格同步消失；
   * 选择持久化到 jm.instances.columns。菜单为 modal（打开期间外部 aria-hidden），操作后先关闭再断言页面主体。
   *
   * 【实现变更·本轮】metrics / active / heartbeat 三列已合并为单一的「健康度」列——表头里
   * 不再有独立的「心跳」「实时指标」，三项开关合并控制同一个健康度列（故本用例顺带断言
   * 「健康度」只出现一次、且不存在「心跳」「实时指标」表头）。原用例（勾选心跳 → 出现心跳格
   * → 取消 → 心跳格消失）已无对象，改为断言「身份列（类型/角色/标签）随开关同步消失」——
   * 守的仍是同一份能力：列设置驱动表头与行内同步变化，并写入 localStorage。
   */
  it('列设置：隐藏身份列后表头与行内同步消失，并持久化', async () => {
    // 只清本特性的键：`localStorage.clear()` 会连登录 token 一起清掉（请求全 401、数据不落）。
    localStorage.removeItem('jm.instances.columns')
    localStorage.removeItem('jm.instances.views')
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none' })
    await screen.findByTestId('instances-table-virtual')

    // 缺省可见类型/角色/标签（`DEFAULT_COLUMNS`）→ 表头有「身份」列；
    // 健康度列因缺省开启实时指标 / 活跃度而存在，且**只有一列**（三项合并）。
    const thead = document.querySelector('thead') as HTMLElement
    expect(within(thead).getByText('身份')).toBeInTheDocument()
    expect(within(thead).getAllByText('健康度')).toHaveLength(1)
    expect(within(thead).queryByText('心跳')).toBeNull()
    expect(within(thead).queryByText('实时指标')).toBeNull()

    const cellsBefore = (screen.getByText('survival-1').closest('tr') as HTMLElement).querySelectorAll('td').length

    // 逐项取消勾选类型 / 角色 / 标签（三项合并成一个「身份」列，全关才会整列消失）。
    for (const label of ['类型', '角色', '标签']) {
      await user.click(screen.getByRole('button', { name: '显示' }))
      await user.click(screen.getByRole('menuitemcheckbox', { name: label }))
      await user.keyboard('{Escape}')
    }

    await waitFor(() => expect(within(document.querySelector('thead') as HTMLElement).queryByText('身份')).toBeNull())
    // 行内同步少一格（身份格），健康度格不受影响。
    const cellsAfter = (screen.getByText('survival-1').closest('tr') as HTMLElement).querySelectorAll('td').length
    expect(cellsAfter).toBe(cellsBefore - 1)
    expect(within(document.querySelector('thead') as HTMLElement).getAllByText('健康度')).toHaveLength(1)

    // 持久化：只剩缺省的 metrics / active（稳定序比较）。
    const stored = JSON.parse(localStorage.getItem('jm.instances.columns') ?? '[]') as string[]
    expect([...stored].sort()).toEqual(['active', 'metrics'])
  })

  /**
   * 保存视图（应用侧）：菜单列出已存视图；应用后 URL 恢复快照（分组/视图模式随 URL 同步 effect 落位），
   * 列集合同步恢复（保存时未含的列不得被勾上）。
   *
   * 【实现变更·本轮】metrics / active / heartbeat 已合并为单一「健康度」列，表头里没有
   * 「心跳」「实时指标」可断言了。列集合的恢复改用**列设置菜单的勾选态**核验——
   * 那才是列集合的真身（表头只是它的投影），且不受列合并影响。
   */
  it('保存视图：应用已存视图恢复 URL 与列集合', async () => {
    localStorage.removeItem('jm.instances.columns')
    localStorage.setItem(
      'jm.instances.views',
      JSON.stringify([
        { name: '卡片·按节点', search: '?view=card&groupBy=node', columns: ['type', 'role', 'tags', 'heartbeat'] },
      ]),
    )
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list' })

    await user.click(screen.getByRole('button', { name: '视图' }))
    await user.click(screen.getByRole('menuitem', { name: '卡片·按节点' }))
    await user.keyboard('{Escape}')

    await waitFor(() => expect(window.location.search).toContain('groupBy=node'))
    expect(window.location.search).toContain('view=card')

    // 切回列表视图核对列集合：快照里的心跳被勾上，快照外的实时指标 / 活跃度不得被勾上。
    await user.click(await screen.findByRole('button', { name: '列表视图' }))
    await screen.findByTestId('instances-table-virtual')
    await user.click(screen.getByRole('button', { name: '显示' }))
    expect(screen.getByRole('menuitemcheckbox', { name: '心跳' })).toBeChecked()
    expect(screen.getByRole('menuitemcheckbox', { name: '实时指标' })).not.toBeChecked()
    expect(screen.getByRole('menuitemcheckbox', { name: '活跃度 (TPS / 在线)' })).not.toBeChecked()
    await user.keyboard('{Escape}')

    // 表头投影：心跳在集合里 → 合并后的健康度列可见；身份列同样可见。
    const thead = document.querySelector('thead') as HTMLElement
    expect(within(thead).getByText('健康度')).toBeInTheDocument()
    expect(within(thead).getByText('身份')).toBeInTheDocument()
  })

  /** 保存视图（保存侧）：命名后落 jm.instances.views，快照含当前 search。 */
  it('保存视图：命名后落 jm.instances.views（快照含当前 search）', async () => {
    localStorage.removeItem('jm.instances.views')
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=node' })

    await user.click(screen.getByRole('button', { name: '视图' }))
    await user.click(screen.getByRole('menuitem', { name: /保存当前视图/ }))
    await user.type(await screen.findByLabelText('视图名称'), '运维晨检')
    await user.click(screen.getByRole('button', { name: '保存' }))

    await waitFor(() => {
      const views = JSON.parse(localStorage.getItem('jm.instances.views') ?? '[]') as {
        name: string
        search: string
      }[]
      expect(views).toHaveLength(1)
      expect(views[0].name).toBe('运维晨检')
      expect(views[0].search).toContain('groupBy=node')
    })
  })

  /** 异常强化（页面重构件）：CRASHED 行带 data-attention 与左侧警示条（不改变行高）。 */
  it('异常强化：CRASHED 行带 data-attention 与警示样式', async () => {
    // 本用例只断言渲染结果，不需要模拟用户交互——故不建 userEvent 实例。
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&status=CRASHED' })
    const surface = await screen.findByTestId('instances-table-virtual')

    await waitFor(() => {
      expect(surface.querySelector('tbody tr[data-attention="true"]')).not.toBeNull()
    })
    const row = surface.querySelector('tbody tr[data-attention="true"]') as HTMLElement
    expect(row.className).toContain('bg-status-danger/5')
    expect(row.style.height).toBe(`${INSTANCE_ROW_HEIGHT}px`)
  })

  /**
   * 表头拖拽换位（用户提出的需求）。
   *
   * 断言走「表头真实顺序 + localStorage 落盘」两条：
   * 只看顺序会漏掉「刷新后失效」，只看存储会漏掉「顺序没真的应用到 DOM」。
   */
  it('拖拽表头换位：列顺序变化并持久化', async () => {
    localStorage.removeItem('jm.instances.columnOrder')
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none' })
    const surface = await screen.findByTestId('instances-table-virtual')

    const headers = () =>
      [...surface.querySelectorAll('thead th')].map((th) => th.textContent?.trim() ?? '')

    // 初始顺序：区域在名称左边。
    const before = headers()
    expect(before.indexOf('区域')).toBeLessThan(before.indexOf('名称'))

    const regionTh = surface.querySelector('thead th[data-column-key="region"]') as HTMLElement
    const nameTh = surface.querySelector('thead th[data-column-key="name"]') as HTMLElement
    expect(regionTh).not.toBeNull()
    expect(nameTh).not.toBeNull()

    // 把「区域」拖到「名称」上 → 区域插到名称之后（moveColumn 的语义）。
    fireEvent.dragStart(regionTh, { dataTransfer: makeDataTransfer() })
    fireEvent.dragOver(nameTh, { dataTransfer: makeDataTransfer() })
    fireEvent.drop(nameTh, { dataTransfer: makeDataTransfer() })

    await waitFor(() => {
      const after = headers()
      expect(after.indexOf('名称')).toBeLessThan(after.indexOf('区域'))
    })

    /*
      【必须同时断言行内顺序】只断表头会漏掉最严重的那种错位：表头按新顺序、行仍按旧顺序。
      真实踩过——行组件的 memo 比较器漏了 columnOrder，表头变了行没变，整表列错位，
      而当时「表头 + localStorage」两条断言全绿。
      这里用「名称列显示实例名、区域列显示 r1/z1」把顺序钉死。
    */
    const firstRowCells = [...(surface.querySelector('tbody tr') as HTMLElement).querySelectorAll('td')]
      .map((td) => td.textContent?.replace(/\s+/g, ' ').trim() ?? '')
    const nameIdx = headers().findIndex((h) => h === '名称')
    const regionIdx = headers().findIndex((h) => h === '区域')
    // 表头顺序与行内顺序必须一致：名称表头下是实例名，区域表头下是 r1/z1。
    expect(firstRowCells[nameIdx]).toMatch(/survival-1|lobby-proxy/)
    expect(firstRowCells[regionIdx]).toMatch(/r\d+\/z\d+/)

    // 落盘：刷新后仍应保持用户拖过的顺序。
    const stored = JSON.parse(localStorage.getItem('jm.instances.columnOrder') ?? '[]')
    expect(stored.indexOf('name')).toBeLessThan(stored.indexOf('region'))
  })

  it('钉住的列不可拖拽：全选框列与操作列没有 draggable', async () => {
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none' })
    const surface = await screen.findByTestId('instances-table-virtual')
    const selectTh = surface.querySelector('thead th[data-column-key]')
    // 首列是复选框列（钉住，不可拖）；可拖的列才带 draggable 属性。
    expect(selectTh?.getAttribute('data-column-key')).toBe('select')
    expect(selectTh?.getAttribute('draggable')).toBeNull()
  })

  /**
   * 行右键菜单（用户提出的需求）。
   *
   * 守三件事：右键弹出、菜单里给出与「⋯」一致的动作、Esc 关闭后可再次打开。
   */
  it('右键实例行弹出操作菜单，Esc 可关闭', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none' })
    await screen.findByTestId('instances-table-virtual')

    const row = (await screen.findByText('survival-1')).closest('tr') as HTMLElement
    fireEvent.contextMenu(row, { clientX: 120, clientY: 200 })

    const menu = await screen.findByTestId('instance-context-menu')
    expect(menu).toBeInTheDocument()
    // 菜单里出现实例名作为标题，且至少给出编辑配置与删除两个动作。
    expect(within(menu).getByText('survival-1')).toBeInTheDocument()
    expect(within(menu).getByRole('menuitem', { name: /编辑配置/ })).toBeInTheDocument()
    expect(within(menu).getByRole('menuitem', { name: /删除/ })).toBeInTheDocument()

    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByTestId('instance-context-menu')).not.toBeInTheDocument())
  })

  it('右键菜单是全表单例：不同行右键后仍只有一个菜单在文档里', async () => {
    renderWithProviders(<InstancesPage />, { route: '/instances?view=list&groupBy=none' })
    await screen.findByTestId('instances-table-virtual')

    const rows = screen.getAllByText(/survival-1|lobby-proxy/)
    fireEvent.contextMenu(rows[0].closest('tr') as HTMLElement, { clientX: 100, clientY: 100 })
    await screen.findByTestId('instance-context-menu')

    expect(screen.getAllByTestId('instance-context-menu')).toHaveLength(1)
  })

  /**
   * 卡片视图的启停二次确认（用户追问「卡片模式呢」时发现的缺口）。
   *
   * 此前卡片接线层自己 `useStartInstance().mutate()`，与列表视图各写一条路径：
   * 列表有了确认、卡片仍是一点就执行——同一台实例在两种视图下安全语义不一致。
   * 这条用例把「卡片也必须先确认」钉住。
   */
  it('卡片视图：点停止先弹二次确认，取消不发动', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=card&groupBy=none' })

    /*
      必须把查找限定在**卡片内部**：页面工具栏也有「停止」筛选按钮，
      全局 findAllByRole('button', { name: /停止/ }) 会先命中它，点出的是筛选而不是动作。
      （这条用例初版就是这么写错的，表现为「点了没弹窗」。）
    */
    const cards = await screen.findAllByTestId('instance-worktable-card')
    const runningCard = cards.find((card) =>
      within(card).queryByRole('button', { name: '停止' }) !== null,
    )
    expect(runningCard).toBeDefined()
    await user.click(within(runningCard as HTMLElement).getByRole('button', { name: '停止' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/停止实例「.+」/)).toBeInTheDocument()

    // 取消 → 关窗且不发动
    await user.click(within(dialog).getByRole('button', { name: '取消' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  /**
   * 卡片视图的右键菜单：与列表视图共用页面级单例。
   * 此前卡片完全没有右键入口——同一台实例在列表能右键、在卡片不行。
   */
  it('卡片视图：右键弹出操作菜单', async () => {
    renderWithProviders(<InstancesPage />, { route: '/instances?view=card&groupBy=none' })

    const cards = await screen.findAllByTestId('instance-worktable-card')
    fireEvent.contextMenu(cards[0], { clientX: 200, clientY: 200 })

    const menu = await screen.findByTestId('instance-context-menu')
    expect(menu).toBeInTheDocument()
    // 仍然是全表单例
    expect(screen.getAllByTestId('instance-context-menu')).toHaveLength(1)
  })

  /**
   * 卡片视图必须有分页条并能真翻页（用户追问「卡片模式」时发现的缺陷）。
   *
   * 数据早已改成服务端分页（每页 50 条），但卡片视图走的是虚拟分组渲染路径：
   * 既没有分页条、`onNeedMore` 也是空实现，滚到底不加载——第 2 页及之后的实例
   * 在卡片视图里**完全够不着**。这条用例把「卡片视图也能翻页」钉住。
   */
  it('卡片视图：有分页条且能翻到下一页', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=card&groupBy=none' })

    // 分页条存在（此前卡片视图完全没有这个元素）
    const summary = await screen.findByTestId('instances-loaded-count')
    expect(summary).toBeInTheDocument()
    expect(summary.textContent).toMatch(/第\s*1\s*\/\s*\d+\s*页/)

    // 首页「上一页」禁用、「下一页」可用
    expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled()
    const next = screen.getByRole('button', { name: '下一页' })
    expect(next).toBeEnabled()

    // 翻页后 URL 与页码同步（与列表视图同一套语义）
    await user.click(next)
    await waitFor(() => {
      expect(screen.getByTestId('instances-loaded-count').textContent).toMatch(/第\s*2\s*\/\s*\d+\s*页/)
    })
  })

  /**
   * 列设置在卡片视图下不出现：卡片没有「列」这一维度，留着就是无效控件。
   */
  it('卡片视图：不显示列设置项', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstancesPage />, { route: '/instances?view=card&groupBy=none' })
    await screen.findAllByTestId('instance-worktable-card')

    // 打开「显示」菜单
    const displayMenu = screen.getByRole('button', { name: /显示|筛选与分组/ })
    await user.click(displayMenu)

    // 列设置标题不应出现（列表视图下才会有）
    expect(screen.queryByText(/列$/)).not.toBeInTheDocument()
  })
})
