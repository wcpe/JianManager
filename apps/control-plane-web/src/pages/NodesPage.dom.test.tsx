import { describe, it, expect, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { clearInjections, mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { useAuthStore } from '@/stores/auth'
import NodesPage from './NodesPage'

/* 
  放宽单例超时：本文件的用例渲染整页节点列表，而 devmock 现在有 36 台种子节点
  （为验证 30+ 规模下的分页/排序而注入）。默认 10s 在机器负载高时会随机超时，
  实测「下线节点」这类要等多次轮询的用例会到 10.2s。
*/
vi.setConfig({ testTimeout: 30_000 })

/** 最小 DataTransfer 桩：jsdom 不实现拖拽数据的读写。 */
function makeDataTransfer() {
  const store = new Map<string, string>()
  return {
    setData: (k: string, v: string) => void store.set(k, v),
    getData: (k: string) => store.get(k) ?? '',
    effectAllowed: '',
    dropEffect: '',
  }
}

// 监控分段的 TimeSeriesChart 依赖 ResizeObserver 实测宽度，jsdom 无之 → 补桩使图表不崩（项目既有约定）。
if (!('ResizeObserver' in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
}

/** 
 * NodesPage 强断言：①渲染 seed 节点 ②进入维护写操作→列表联动出维护标记
 * ③详情激活 Tab 可寻址（?tab= 读写 + ?node= 深链）④注入 500→错误态。
 * NodesPage 跨域调 GET /instances（域），本域 worktree 未注册——用 server.use 本地桩返回空，
 * 满足 onUnhandledRequest:'error' 覆盖闸，且不污染他域 handler 文件。
 */
function stubInstances() {
  server.use(http.get(API('/instances'), () => HttpResponse.json([])))
}

/** 监控分段跨域调 GET /metrics/series（观测域），本域未注册——本地桩返回空序列即可（图表走空态提示）。 */
function stubMetricSeries() {
  server.use(
    http.get(API('/metrics/series'), () =>
      HttpResponse.json({ resolution: 'raw', from: '', to: '', series: [] }),
    ),
  )
}

/** 平台管理员 JWT：危险操作门禁（DangerConfirm scope=platform）按 role=10 放行（同 GroupsPage 惯例）。 */
function adminJwt(role = 10): string {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role, exp: Math.floor(Date.now() / 1000) + 900 }))
  return `mock.${payload}.sig`
}

/** 登录为平台管理员：写 sessions（过 requireAuth）+ 同步 auth store role（过危险操作门禁）。 */
function loginPlatformAdmin(): void {
  const token = adminJwt(10)
  loginMockUser(token)
  useAuthStore.getState().login(token, 'test-refresh-token')
}

describe('NodesPage（mock 假后端）', () => {
  it('渲染 seed 节点列表（alpha / beta）', async () => {
    loginMockUser()
    stubInstances()
    const { container } = renderWithProviders(<NodesPage />)
    expect(container.firstElementChild).toHaveAttribute('data-page', 'nodes')
    // 阶段 6 重做：外壳改用布局层 PageShell（原为手写的 jm-page-stack + 双栏 flex 类）。
    expect(container.firstElementChild).toHaveAttribute('data-slot', 'page-shell')

    // 列表态**不**自动选中节点（与旧双栏的「进页默认选中首个」不同），故 alpha 与其 host
    // 各只出现一次；下面用 getAllByText(...).length > 0，两种情形都成立，不必跟着改。
    expect(await screen.findByText('beta')).toBeInTheDocument()
    expect(screen.getAllByText('alpha').length).toBeGreaterThan(0)
    expect(screen.getAllByText('10.0.0.11').length).toBeGreaterThan(0) // alpha host：列表 + 详情
  })

  it('顶部 KPI：状态卡带图标，资源占用卡用环形仪表', async () => {
    loginMockUser()
    stubInstances()
    renderWithProviders(<NodesPage />)

    // 状态卡沿用 StatCard：图标块 + 大数字 + 标签。
    const total = await screen.findByTestId('nodes-kpi-total')
    expect(total.querySelector('[data-slot="stat-card"] svg')).not.toBeNull()

    // 资源卡不再是一排裸百分比：环由两条 circle（底轨 + 进度）组成，中心仍给出百分比。
    const cpu = await screen.findByTestId('nodes-kpi-cpu')
    expect(cpu.querySelectorAll('svg circle')).toHaveLength(2)
    expect(cpu.querySelector('[data-slot="stat-card"]')).toBeNull()
    // 圆球不套卡片，且中心只留数字（用户要求去掉百分号）。
    expect(cpu.firstElementChild?.className).not.toMatch(/\bborder\b|\bbg-card\b|\bshadow-/)
    expect(cpu.textContent).toMatch(/\d+/)
    expect(cpu.textContent).not.toContain('%')
  })

  it('切换归档视图可见 gamma-archived，清理确认文案明示不清理远端文件（394）', async () => {
    loginPlatformAdmin()
    stubInstances()
    const user = userEvent.setup()
    // 阶段 6 重做后 /nodes 是列表态、/nodes?node=x 才是详情态；「清理」按钮在详情态，
    // 故深链要同时带 view 与 node（gamma-archived 的 id 见 devmock node.ts = 3）。
    renderWithProviders(<NodesPage />, { route: '/nodes?view=archive&node=3' })

    // 列表 + 详情都会渲染名称，用 getAllByText。
    expect((await screen.findAllByText('gamma-archived')).length).toBeGreaterThan(0)
    // 默认选中归档首项 → 详情出清理按钮。
    const purgeBtn = await screen.findByRole('button', { name: '清理' })
    await user.click(purgeBtn)
    // DangerConfirm 描述须明示不清理远端/机器上文件。
    await waitFor(() => {
      const text = document.body.textContent ?? ''
      expect(text).toMatch(/不会清理节点机器上的任何文件|不会清理/)
    })
  })

  it('对节点进入维护后，列表联动出现「维护」标记', async () => {
    loginMockUser()
    stubInstances()
    const user = userEvent.setup()
    // 阶段 6 重做后列表态**不**自动选中（旧双栏会默认选中首个），操作菜单只在详情态出现，
    // 故显式深链到 alpha（id=1）。
    renderWithProviders(<NodesPage />, { route: '/nodes?node=1' })

    /* 
      左栏节点列表已移除（改为顶部下拉选择器），不能再靠等左栏里的 'beta' 判断就绪。
      改为等详情页自身的指标条——它是详情态必然渲染的元素。
    */
    // 指标条已换成球，改用状态球作为详情态就绪信号。
    await screen.findByTestId('node-detail-status-orb')
    const actionsBtn = await screen.findByRole('button', { name: '操作' })
    await user.click(actionsBtn)
    await user.click(await screen.findByRole('menuitem', { name: '进入维护' }))
    /* 
      进入维护有二次确认（会中断新实例调度）。菜单项与确认按钮同名「进入维护」，
      所以必须在**弹窗内**点，否则会再次命中菜单项（菜单此时已关闭，会超时）。
    */
    const confirmDialog = await screen.findByRole('dialog')
    await user.click(within(confirmDialog).getByRole('button', { name: '进入维护' }))

    /* 
      setNodeMaintenance 成功后失效 ['nodes'] → 重新拉取，handler 现回 maintenance:true。
      断言目标从「左栏行徽标」改为详情页顶部的维护徽章——左栏已移除，
      而顶部徽章是维护态在详情页的常显呈现（选择器里的分组要展开才看得到，不适合当就绪判据）。
    */
    /* 
      维护徽章已并入头部状态球（球的 label 直接显示「维护中」），
      不再有单独的 badge 元素，故断言球内文字。
    */
    await waitFor(() =>
      expect(screen.getByTestId('node-detail-status-orb').textContent).toMatch(/维护中/),
    )
  })

  it('添加节点向导生成 CP 托管脚本命令，且手动 token 不落配置', async () => {
    loginMockUser()
    stubInstances()
    const user = userEvent.setup()
    renderWithProviders(<NodesPage />)

    await user.click(await screen.findByRole('button', { name: '添加节点' }))
    await user.click(await screen.findByRole('button', { name: '生成一键命令' }))

    await waitFor(() => {
      const text = document.body.textContent ?? ''
      expect(text).toContain('/install-worker.sh')
      expect(text).toContain('--control-plane')
      expect(text).toContain('--token')
    })

    await user.click(await screen.findByRole('tab', { name: '手动连接' }))
    expect(await screen.findByText(/不写入 worker\.yml/)).toBeInTheDocument()
  })

  it('URL 带 ?node=2&tab=monitor 时深链选中 beta 并激活监控分段', async () => {
    loginMockUser()
    stubInstances()
    stubMetricSeries()
    renderWithProviders(<NodesPage />, { route: '/nodes?node=2&tab=monitor' })

    // 详情身份块出 beta（深链选中 id=2，非默认首个 alpha）。
    const heading = await screen.findByRole('heading', { name: 'beta' })
    expect(heading).toBeInTheDocument()
    // 监控分段渲染：CPU 趋势面板 + 时间范围控件可见（概览分段两者都没有）。
    await waitFor(() => expect(new URLSearchParams(window.location.search).get('tab')).toBe('monitor'))
    expect(await screen.findByText('CPU 趋势')).toBeInTheDocument()
    expect(screen.getByTestId('node-metric-range-bar')).toBeInTheDocument()
  })

  it('切换详情分段时 URL 同步写入 ?tab=，切回概览时省略参数', async () => {
    loginMockUser()
    stubInstances()
    stubMetricSeries()
    const user = userEvent.setup()
    // 阶段 6 重做后详情分段只在 /nodes?node=x 下出现，故深链到 alpha（id=1）。
    renderWithProviders(<NodesPage />, { route: '/nodes?node=1' })

    // 左栏已移除、指标条改球 → 用状态球作为详情态就绪信号。
    await screen.findByTestId('node-detail-status-orb')
    // 默认 overview 不写 tab 参数。
    expect(new URLSearchParams(window.location.search).get('tab')).toBeNull()

    // 点「监控」分段 → URL 写入 tab=monitor。
    await user.click(await screen.findByRole('button', { name: '监控' }))
    await waitFor(() => expect(new URLSearchParams(window.location.search).get('tab')).toBe('monitor'))
    // 曲线只在监控这一个出口：恰好一张「CPU 趋势」，不存在概览那条常显趋势带。
    expect(await screen.findAllByText('CPU 趋势')).toHaveLength(1)

    // 切回「概览」→ tab 参数被省略（保持链接简洁），且概览不再画任何曲线。
    await user.click(await screen.findByRole('button', { name: '概览' }))
    await waitFor(() => expect(new URLSearchParams(window.location.search).get('tab')).toBeNull())
    expect(screen.queryByText('CPU 趋势')).toBeNull()
    expect(screen.queryByTestId('node-metric-range-bar')).toBeNull()
  })

  it('离线节点名下有实例：下线被 409 拒并列实例清单，强制下线级联删除', async () => {
    loginPlatformAdmin()
    stubInstances()
    const user = userEvent.setup()
    renderWithProviders(<NodesPage />)

    // 选中离线节点 beta（seed id=2，其名下有 seed 实例，如 creative-1）。
    await user.click(await screen.findByText('beta'))
    await screen.findByRole('heading', { name: 'beta' })
    await user.click(screen.getByRole('button', { name: '操作' }))
    await user.click(await screen.findByRole('menuitem', { name: '下线' }))

    // 第一道确认（既有 DangerConfirm）：输入节点名后确认。
    await user.type(await screen.findByPlaceholderText('beta'), 'beta')
    await user.click(screen.getByRole('button', { name: '下线' }))

    // 守卫 409 → 实例清单模态：列出名下实例 + 离线节点的强制下线入口。
    //
    // 断言必须限定在模态内：选中 beta 之后，右侧详情面板**也会**列出该节点的实例，
    // 于是 `creative-1` 在页面上出现两处，全局 getByText 会因「命中多个」抛错。
    // 这里要验的是「模态里列了哪些实例」，用 role=dialog 收敛作用域才是准确的口径。
    expect(await screen.findByText('无法下线：节点名下仍有实例')).toBeInTheDocument()
    const guardDialog = await screen.findByRole('dialog')
    expect(within(guardDialog).getByText('creative-1')).toBeInTheDocument()
    await user.click(within(guardDialog).getByRole('button', { name: '强制下线' }))

    // 第二道确认（强制级联，文案明示不清理远端文件）：再次输入节点名。
    expect(await screen.findByText('强制下线节点')).toBeInTheDocument()
    expect(screen.getByText(/不会被清理/)).toBeInTheDocument()
    await user.type(await screen.findByPlaceholderText('beta'), 'beta')
    await user.click(screen.getByRole('button', { name: '强制下线' }))

    // 级联删除成功：节点从列表消失。
    await waitFor(() => expect(screen.queryByText('beta')).not.toBeInTheDocument())
  })

  it('监控分段：实例对比只发 1 条批量指标请求（消 N+1），批量目标 ≤12', async () => {
    loginMockUser()
    /* 
      本用例守的是「对比图不为 N 个实例各发一条」这条不变量。
      页签现在有两类批量请求：对比（所选指标）与清单用量（cpu/堆），故按 metrics 分类计数，
      不能再用「总请求数 == 1」——那会把合法的用量查询误判成回归。
    */
    const batchCalls: { metrics: string[]; targets: number }[] = []
    const searchPageSizes: string[] = []
    server.use(
      http.post(API('/metrics/series/batch'), async ({ request }) => {
        const body = (await request.json()) as { targetIds?: string[]; metrics?: string[] }
        batchCalls.push({ metrics: body.metrics ?? [], targets: body.targetIds?.length ?? 0 })
        return HttpResponse.json({ resolution: 'raw', from: '', to: '', series: {}, skipped: [] })
      }),
      http.get(API('/instances/search'), ({ request }) => {
        const url = new URL(request.url)
        // 只在按节点分页时记录；其它 search 调用不干扰。
        if (url.searchParams.get('nodeId')) searchPageSizes.push(url.searchParams.get('pageSize') ?? '')
        const items = Array.from({ length: 12 }, (_, i) => ({
          id: 100 + i,
          uuid: `cmp-${i}`,
          nodeId: 2,
          name: `srv-${String(i).padStart(2, '0')}`,
          type: 'minecraft_java',
          role: 'backend',
          processType: 'daemon',
          status: 'RUNNING',
          startCommand: 'java -jar s.jar',
          workDir: '/x',
          image: '',
          cpuLimit: 0,
          memLimitMb: 0,
          diskLimitMb: 0,
          serverPort: 25600 + i,
          autoStart: false,
          autoRestart: true,
          tags: '',
          createdAt: '2026-01-01T00:00:00Z',
        }))
        return HttpResponse.json({ items, total: 600, page: 1, pageSize: 12 })
      }),
    )
    const user = userEvent.setup()
    renderWithProviders(<NodesPage />, { route: '/nodes?node=2&tab=monitor' })

    // 对比图已并入监控分段：它与本页的五张节点曲线同屏。
    expect(await screen.findByText('实例指标对比')).toBeInTheDocument()

    // 对比图：恰好一条批量请求，目标数 ≤ 12。
    await waitFor(() => expect(batchCalls.filter((c) => c.metrics.includes('inst_tps'))).toHaveLength(1))
    expect(batchCalls.find((c) => c.metrics.includes('inst_tps'))!.targets).toBeLessThanOrEqual(12)
    // 清单用量：也只是一条批量请求（不是每台一条），且覆盖清单里那 12 台。
    const usageCalls = batchCalls.filter((c) => c.metrics.includes('inst_cpu_pct'))
    expect(usageCalls).toHaveLength(1)
    expect(usageCalls[0]!.targets).toBeLessThanOrEqual(12)
    // 对比清单按 12 台上限取。
    expect(searchPageSizes).toContain('12')

    // 切换对比指标（TPS→MSPT）仍是单条批量查询，不回退 N 条逐实例请求。
    // 指标档位已收敛为分段控件（Segments/Segment）：是 role=tab，不再是 button。
    await user.click(await screen.findByRole('tab', { name: 'MSPT' }))
    await waitFor(() => expect(batchCalls.filter((c) => c.metrics.includes('inst_mspt'))).toHaveLength(1))
  })

  it('注入 500：节点列表请求失败，列表区不崩溃（无节点行）', async () => {
    loginMockUser()
    stubInstances()
    mockInject('get', '/nodes', { kind: 'status', status: 500 })
    renderWithProviders(<NodesPage />)

    // 错误态：列表空（不出现 seed 节点名），页面整体仍渲染（搜索框可见）。
    await screen.findByRole('textbox')
    await waitFor(() => {
      expect(screen.queryByText('alpha')).not.toBeInTheDocument()
    })
    expect(within(document.body).queryByText('beta')).not.toBeInTheDocument()
  })

  /** 
   * `/nodes` 取数失败必须与「暂无节点」空态分开。
   *
   * 只判 `isLoading` 时失败会渲染成「暂无节点」，运营看到的是「节点都没了」——
   * 这是会直接导致误判集群规模的假信号。错误态还须给出重试入口。
   */
  it('注入 500：列表显示错误态而非「暂无节点」，重试可恢复', async () => {
    loginMockUser()
    stubInstances()
    mockInject('get', '/nodes', { kind: 'status', status: 500 })
    const user = userEvent.setup()
    renderWithProviders(<NodesPage />)

    expect(await screen.findByTestId('nodes-list-error')).toBeInTheDocument()
    expect(screen.queryByText('暂无节点')).not.toBeInTheDocument()

    // 重试：撤销注入后点「刷新」，列表恢复出 seed 节点。
    clearInjections()
    await user.click(screen.getByRole('button', { name: '刷新' }))
    expect(await screen.findByText('beta')).toBeInTheDocument()
    expect(screen.queryByTestId('nodes-list-error')).not.toBeInTheDocument()
  })

  /** 
   * 切到「归档」视图不应再请求实例聚合。
   *
   * 归档列表只显示名称/主机/下线时间，用不到各节点实例数；原先无条件取数，
   * 切到归档仍照发一次 `/instances/aggregate`。
   */
  it('归档视图不请求 /instances/aggregate（按视图门控取数）', async () => {
    loginPlatformAdmin()
    stubInstances()
    const aggregateCalls: string[] = []
    const listener = ({ request }: { request: Request }) => {
      const url = new URL(request.url)
      if (url.pathname === '/api/v1/instances/aggregate') aggregateCalls.push(url.toString())
    }
    server.events.on('request:start', listener)
    try {
      // 走详情态（?node=）才会渲染左栏归档列表（列表态卡片墙用的是 active 列表）。
      renderWithProviders(<NodesPage />, { route: '/nodes?view=archive&node=3' })
      expect((await screen.findAllByText('gamma-archived')).length).toBeGreaterThan(0)
      // 归档视图已就绪（列表渲染出来）后，聚合请求数必须仍为 0。
      expect(aggregateCalls).toEqual([])
    } finally {
      server.events.removeListener('request:start', listener)
    }
  })

  /** 
   * 列表视图（本轮新增，节点页优化 A1）。
   *
   * 原来只有卡片网格：2 个节点时页面约 62% 是空白，且卡片只用到了 NodeInfo 二十多个
   * 字段里的 4 个。列表视图把密度提上来，并把负载/系统/隧道/心跳这些已有信号摆出来。
   */
  it('默认列表视图：表头齐全且展示此前卡片未用的字段', async () => {
    loginMockUser()
    renderWithProviders(<NodesPage />, { route: '/nodes' })

    const table = await screen.findByTestId('nodes-table')
    expect(table).toBeInTheDocument()

    // 表头：状态/名称/IP/系统/负载/实例数/隧道/心跳
    for (const label of ['状态', '名称', 'IP', '系统', '负载', '隧道', '心跳']) {
      expect(within(table).getByText(label)).toBeInTheDocument()
    }

    // 至少渲染出一行节点
    await waitFor(() => expect(screen.getAllByTestId('nodes-table-row').length).toBeGreaterThan(0))
  })

  it('点表头排序：写入 URL 并切换方向', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<NodesPage />, { route: '/nodes' })

    const table = await screen.findByTestId('nodes-table')
    const nameHeader = within(table).getByRole('button', { name: /名称/ })

    await user.click(nameHeader)
    await waitFor(() => expect(window.location.search).toContain('sort=name'))

    // 再点同一列 → 方向翻转
    await user.click(within(table).getByRole('button', { name: /名称/ }))
    await waitFor(() => expect(window.location.search).toContain('order=desc'))
  })

  it('离线节点不画空资源条（改为明确的无数据占位）', async () => {
    loginMockUser()
    renderWithProviders(<NodesPage />, { route: '/nodes' })

    const table = await screen.findByTestId('nodes-table')
    // 离线节点（beta）那一行的负载列应是 `--`，而不是三条 0% 空轨
    await waitFor(() => {
      const rows = screen.getAllByTestId('nodes-table-row')
      const offlineRow = rows.find((r) => /beta/.test(r.textContent ?? '') && /离线/.test(r.textContent ?? ''))
      expect(offlineRow).toBeDefined()
      expect(offlineRow!.textContent).toContain('--')
    })
    expect(table).toBeInTheDocument()
  })

  it('视图切换：卡片 / 列表可切换，且选择持久化', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<NodesPage />, { route: '/nodes' })

    await screen.findByTestId('nodes-table')

    // 切到卡片
    await user.click(screen.getByRole('tab', { name: '卡片' }))
    await waitFor(() => expect(screen.queryByTestId('nodes-table')).not.toBeInTheDocument())
    expect(localStorage.getItem('jm.nodes.layout')).toBe('card')

    // 切回列表
    await user.click(screen.getByRole('tab', { name: '列表' }))
    await waitFor(() => expect(screen.getByTestId('nodes-table')).toBeInTheDocument())
    expect(localStorage.getItem('jm.nodes.layout')).toBe('list')
  })

  /** 
   * 表头拖拽换位（用户要求「表头参考服务器列表可以拖拽」）。
   *
   * 断言走「表头真实顺序 + localStorage 落盘 + 行内顺序」三条：
   * 只断表头会漏掉最严重的那种错位——表头按新序、行仍按旧序（实例页真实踩过）。
   */
  it('拖拽表头换位：列顺序变化、行内同步、且持久化', async () => {
    loginMockUser()
    localStorage.removeItem('jm.nodes.columnOrder')
    renderWithProviders(<NodesPage />, { route: '/nodes' })

    const table = await screen.findByTestId('nodes-table')
    const headers = () => [...table.querySelectorAll('thead th')].map((th) => th.getAttribute('data-column-key'))

    const before = headers()
    expect(before.indexOf('name')).toBeLessThan(before.indexOf('system'))

    const nameTh = table.querySelector('thead th[data-column-key="name"]') as HTMLElement
    const systemTh = table.querySelector('thead th[data-column-key="system"]') as HTMLElement
    expect(nameTh).not.toBeNull()
    expect(systemTh).not.toBeNull()

    const dt = makeDataTransfer()
    fireEvent.dragStart(nameTh, { dataTransfer: dt })
    fireEvent.dragOver(systemTh, { dataTransfer: dt })
    fireEvent.drop(systemTh, { dataTransfer: dt })

    await waitFor(() => {
      const after = headers()
      expect(after.indexOf('system')).toBeLessThan(after.indexOf('name'))
    })

    // 落盘
    const stored = JSON.parse(localStorage.getItem('jm.nodes.columnOrder') ?? '[]')
    expect(stored.indexOf('system')).toBeLessThan(stored.indexOf('name'))

    // 行内顺序必须跟随表头（否则整表错列）
    const firstRow = screen.getAllByTestId('nodes-table-row')[0]
    const cells = [...firstRow.querySelectorAll('td')].map((td) => td.textContent?.replace(/\s+/g, ' ').trim() ?? '')
    const systemIdx = headers().indexOf('system')
    expect(cells[systemIdx]).toMatch(/linux|windows|darwin/)
  })

  it('钉住的列不可拖拽：状态列没有 draggable', async () => {
    loginMockUser()
    renderWithProviders(<NodesPage />, { route: '/nodes' })
    const table = await screen.findByTestId('nodes-table')
    const statusTh = table.querySelector('thead th[data-column-key="status"]')
    expect(statusTh?.getAttribute('draggable')).toBeNull()
  })

  /** 节点表与服务器表同款列宽拖拽：钉住只限制「换位」，不限制「调宽」。 */
  it('节点表每列都有列宽手柄，排序表头也不例外', async () => {
    loginMockUser()
    renderWithProviders(<NodesPage />, { route: '/nodes' })
    const table = await screen.findByTestId('nodes-table')
    for (const key of ['status', 'name', 'address', 'load', 'heartbeat']) {
      expect(table.querySelector(`[data-testid="node-col-resize-${key}"]`)).not.toBeNull()
    }
  })

  /**
   * 批量操作：勾选后出现批量条，取消选择后消失。
   *
   * 这条守的是「勾选框确实接在选择状态上」——只渲染复选框、不接状态的话，
   * 批量条永远不会出现，属于看着有、其实没接线的那类假功能。
   */
  it('勾选节点后出现批量操作条，取消后消失', async () => {
    loginMockUser()
    stubInstances()
    const user = userEvent.setup()
    renderWithProviders(<NodesPage />, { route: '/nodes' })

    const box = await screen.findByTestId('node-select-1')
    await user.click(box)
    expect(await screen.findByTestId('nodes-batch-bar')).toBeInTheDocument()
    expect(screen.getByText(/已选\s*1\s*台节点/)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '取消选择' }))
    await waitFor(() => expect(screen.queryByTestId('nodes-batch-bar')).not.toBeInTheDocument())
  })

  /** 底部分页（用户要求「底部的节点…改成分页」）。 */
  it('分页条存在且能翻页', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<NodesPage />, { route: '/nodes' })

    const summary = await screen.findByTestId('nodes-loaded-count')
    expect(summary.textContent).toMatch(/第\s*1\s*\//)
    expect(screen.getByRole('button', { name: '上一页' })).toBeDisabled()

    // 真的翻一页：只断言「第 1 页」的话，翻页按钮接错了也测不出来。
    const next = screen.getByRole('button', { name: '下一页' })
    expect(next).toBeEnabled()
    await user.click(next)
    await waitFor(() => expect(screen.getByTestId('nodes-loaded-count').textContent).toMatch(/第\s*2\s*\//))
    expect(await screen.findByRole('button', { name: '上一页' })).toBeEnabled()
  })

  /** 各节点负载改成折线图（用户要求「统计表格那种折线图」）。 */
  it('节点运行概览渲染 CPU 折线图容器', async () => {
    loginMockUser()
    renderWithProviders(<NodesPage />, { route: '/nodes' })

    // 集群平均水位仍在
    expect(await screen.findByTestId('nodes-avg-cpu')).toBeInTheDocument()
    // 折线图容器存在（高度已撑起，避免图表塌陷导致页面跳动）
    expect(await screen.findByTestId('nodes-cpu-chart')).toBeInTheDocument()
  })

  /** 
   * KPI 卡片可点即筛（用户提出「节点多了要能快速定位问题」）。
   *
   * 此前「离线 1」只是个数字，点不动；而找离线节点是运维最高频的动作。
   */
  it('点 KPI「离线」卡即按状态筛选，再点取消', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<NodesPage />, { route: '/nodes' })

    await screen.findByTestId('nodes-table')

    // 用 testid 精确点 KPI 卡：「离线」这个词在 KPI、状态徽章、chip 里都会出现，按文本查会撞车。
    const offlineCard = (await screen.findByTestId('nodes-kpi-offline')).querySelector('button, [role="button"]') as HTMLElement
    await user.click(offlineCard)

    await waitFor(() => expect(window.location.search).toContain('status=offline'))
    // 只剩离线节点（beta），在线的 alpha 不该出现
    await waitFor(() => {
      const rows = screen.getAllByTestId('nodes-table-row')
      expect(rows.every((r) => /离线/.test(r.textContent ?? ''))).toBe(true)
    })
  })

  /** 「异常优先」排序：把离线/维护/高负载排到最前。 */
  it('异常优先：写 URL 且离线节点排在最前', async () => {
    const user = userEvent.setup()
    loginMockUser()
    renderWithProviders(<NodesPage />, { route: '/nodes' })
    await screen.findByTestId('nodes-table')

    await user.click(screen.getByRole('button', { name: '异常优先' }))
    await waitFor(() => expect(window.location.search).toContain('sort=attention'))

    await waitFor(() => {
      const first = screen.getAllByTestId('nodes-table-row')[0]
      // 离线节点（beta）应排第一
      expect(first.textContent).toMatch(/beta/)
      expect(first.textContent).toMatch(/离线/)
    })
  })

  /** 
   * 「配置」分区（用户要求把配置设置安排好）。
   *
   * 断言重点是「汇总 + 直达」这个设计意图：
   * 可写项给出「去配置」跳转（点它切到对应分区），只读项明确标「只读」，
   * 避免用户在两处都找不到编辑入口的常见困惑。
   */
  it('配置分区：汇总可写项并支持跳转，只读项有标记', async () => {
    const user = userEvent.setup()
    loginMockUser()
    stubInstances()
    stubMetricSeries()
    renderWithProviders(<NodesPage />, { route: '/nodes?node=1&tab=config' })

    // 配置项齐备（8 项）
    await screen.findByTestId('node-config-proxy')
    for (const key of ['proxy', 'cacheCap', 'pm', 'probe', 'grpcPort', 'wsPort', 'autoStart', 'health']) {
      expect(screen.getByTestId(`node-config-${key}`)).toBeInTheDocument()
    }

    // 只读项带「只读」标记（grpcPort 无在线编辑接口）
    expect(within(screen.getByTestId('node-config-grpcPort')).getByText('只读')).toBeInTheDocument()

    // 点「去配置」→ 切到 proxy 分区，URL 同步
    const proxyRow = screen.getByTestId('node-config-proxy')
    await user.click(within(proxyRow).getByRole('button', { name: /去配置/ }))
    await waitFor(() => expect(new URLSearchParams(window.location.search).get('tab')).toBe('proxy'))
  })
})
