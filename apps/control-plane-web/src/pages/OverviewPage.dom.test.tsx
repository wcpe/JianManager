import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { useAuthStore } from '@/stores/auth'
import OverviewPage from './OverviewPage'

/**
 * 总览页强断言（FR-201）：底部密集实例表渲染种子 + 实例集联动 + 错误注入空态。
 * /nodes 与 /metrics/overview 属它域，本测试文件 server.use 桩占位（隔离运行）。
 */
beforeEach(() => {
  loginMockUser()
  server.use(
    http.get(API('/nodes'), () => HttpResponse.json([{ id: 1, name: 'node-a', status: 1 }])),
    http.get(API('/metrics/overview'), () =>
      HttpResponse.json({
        totals: {
          nodeCount: 1,
          onlineNodeCount: 1,
          runningInstances: 1,
          cpuPct: 12,
          loadAvg: 40,
          memUsedBytes: 1,
          memTotalBytes: 2,
          onlinePlayers: 2,
        },
        resolution: 'raw',
        trends: [],
      }),
    ),
    // FR-461 健康墙属同域，默认空桩（具体用例按需覆盖）。
    http.get(API('/observability/health-wall'), () => HttpResponse.json({ nodes: [] })),
  )
})

function loginPlatformAdmin(): void {
  const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
  loginMockUser(`mock.${payload}.sig`)
  useAuthStore.getState().login(`mock.${payload}.sig`, 'test-refresh-token')
}

describe('OverviewPage（mock 假后端）', () => {
  it('平台管理员展示有界健康、异常与共享 Bot 观测并可下钻', async () => {
    loginPlatformAdmin()
    server.use(
      http.get(API('/observability/overview'), () =>
        HttpResponse.json({
          sampledAt: '2026-07-28T00:00:00Z',
          health: { nodeCount: 2, onlineNodeCount: 1, staleNodeCount: 1, offlineNodeCount: 0, runningInstanceCount: 3, crashedInstanceCount: 1, stoppedInstanceCount: 2 },
          resources: { cpuPct: 42.5, loadPct: 35, memoryUsedBytes: 1024, memoryTotalBytes: 2048, freshness: 'fresh' },
          bots: { sharedRuntime: true, notice: 'Bot Worker 资源为共享进程观察值，不代表任一 Bot 或会话的独占资源。', nodeCount: 1, botWorkerRssBytes: 120, botWorkerCpuPct: 3.5, workerProcessRssBytes: 240, workerProcessCpuPct: 1.5, activeCount: 20, connectingCount: 1, eventLoopP95Ms: 4.2, unavailable: [{ nodeId: 2, reason: '节点心跳已过期' }] },
          alerts: [], tasks: [],
          exceptions: [{ kind: 'node_stale', nodeId: 2, title: 'node-a 心跳陈旧', href: '/monitoring?node=node-a' }],
        }),
      ),
      http.get(API('/observability/health-wall'), () =>
        HttpResponse.json({
          nodes: [
            { nodeId: 2, nodeUuid: 'node-a', name: 'node-a', freshness: 'stale', cpuPct: null, memPct: null, diskPct: null, running: 0, crashed: 0, stopped: 0, activeAlerts: 0, botActive: null, botConnecting: null, level: 'stale', href: '/monitoring?node=node-a' },
          ],
        }),
      ),
    )

    renderWithProviders(<OverviewPage />, { route: '/' })

    // FR-461：平台总览页内嵌集群健康墙，单元格可一键下钻。
    const healthWall = within(await screen.findByTestId('health-wall'))
    // 健康墙自持取数，单元格要等 /observability/health-wall 回来才渲染——
    // 原用同步 getByRole，实际是靠「异常项」面板里同名的 `node-a 心跳陈旧` 侥幸命中；
    // 该面板本轮已移除，故改回 findBy 等健康墙自己的一行。
    expect(await healthWall.findByRole('link', { name: 'node-a 陈旧' })).toHaveAttribute('href', '/monitoring?node=node-a')
    expect(screen.getByText('Bot Worker（共享）')).toBeInTheDocument()
    expect(await screen.findByText('Bot Worker 资源为共享进程观察值，不代表任一 Bot 或会话的独占资源。')).toBeInTheDocument()
    /*
      【结构变更·本轮】平台健康 / 异常项 / 首页实例表已移除（与页顶指标卡、「异常实例」、
      「服务器」页分别重复，详见 OverviewPageView 的说明），故原先对
      `platform-observability-grid` / `-health` 的栅格类断言、以及「node-a 心跳陈旧」
      （来自已删除的「异常项」面板）都不再成立。
      Bot Worker 改为独占一行（不再与其他两张卡并排三栏），这里只断言它仍在且可下钻。
    */
    expect(screen.getByText('Bot Worker（共享）')).toBeInTheDocument()
    expect(within(screen.getByTestId('platform-observability-bots')).getByText(/Bot Worker RSS/)).toBeInTheDocument()
  })

  it('资源仪表 Tooltip 只在打开后查询受管归因，并可下钻监控页', async () => {
    server.use(
      http.get(API('/metrics/resource-attribution'), () => HttpResponse.json({
        sampledAt: '2026-07-28T00:00:00Z', freshness: 'fresh',
        nodes: [{ nodeId: 1, nodeUuid: 'node-a', name: '节点 A', status: 'fresh', observedAt: '2026-07-28T00:00:00Z', cpuPct: 10, loadPct: 20, memoryUsedBytes: 1024, memoryTotalBytes: 2048, workerProcessRssBytes: null, workerProcessCpuPct: null, botWorker: { rssBytes: null, cpuPct: null, activeCount: null, connectingCount: null, eventLoopP95Ms: null, available: false, reason: '未启动' } }],
        topInstances: [{ instanceId: 2, instanceUuid: 'inst-a', instanceName: '大厅', nodeId: 1, cpuPct: 10, rssBytes: 512, sampledAt: '2026-07-28T00:00:00Z' }],
        topProcesses: [],
      })),
    )
    const user = userEvent.setup()
    renderWithProviders(<OverviewPage />, { route: '/' })
    await user.click(await screen.findByRole('button', { name: '总内存 资源归因' }))
    const tooltip = await screen.findByTestId('overview-resource-attribution')
    expect(within(tooltip).getByRole('link', { name: /节点 A/ })).toHaveAttribute('href', '/monitoring?node=node-a')
    expect(within(tooltip).getByRole('link', { name: /大厅/ })).toHaveAttribute('href', '/monitoring?instance=inst-a')
    expect(within(tooltip).getAllByText('1K')).toHaveLength(2)
  })

  it('总 CPU 资源归因 Tooltip 显示 CPU 百分比而非 RSS', async () => {
    server.use(
      http.get(API('/metrics/resource-attribution'), () => HttpResponse.json({
        sampledAt: '2026-07-28T00:00:00Z', freshness: 'fresh',
        nodes: [{ nodeId: 1, nodeUuid: 'node-a', name: '节点 A', status: 'fresh', observedAt: '2026-07-28T00:00:00Z', cpuPct: 12.5, loadPct: 20, memoryUsedBytes: 1024, memoryTotalBytes: 2048, workerProcessRssBytes: null, workerProcessCpuPct: null, botWorker: { rssBytes: null, cpuPct: null, activeCount: null, connectingCount: null, eventLoopP95Ms: null, available: false, reason: '未启动' } }],
        topInstances: [{ instanceId: 2, instanceUuid: 'inst-a', instanceName: '大厅', nodeId: 1, cpuPct: 25, rssBytes: 2 * 1024 * 1024 * 1024, sampledAt: '2026-07-28T00:00:00Z' }],
        topProcesses: [],
      })),
    )
    const user = userEvent.setup()
    renderWithProviders(<OverviewPage />, { route: '/' })

    await user.click(await screen.findByRole('button', { name: '总 CPU 资源归因' }))

    const tooltip = await screen.findByTestId('overview-resource-attribution')
    expect(within(tooltip).getByText('12.5%')).toBeInTheDocument()
    expect(within(tooltip).getByText('25.0%')).toBeInTheDocument()
    expect(within(tooltip).queryByText('2.0G')).not.toBeInTheDocument()
  })

  /*
    【结构变更·本轮】首页底部那张「不限量无限滚动」的实例表已移除。
    它与「服务器」页的完整版本重复（那边有搜索/筛选/分组/批量操作），
    同时是本页渲染量的大头（约 783 个元素、单次主线程阻塞约 360ms）。

    原先这里有三条用例覆盖它的渲染 / 按需分页 / 空态降级；表既然不存在，
    这三条一并撤掉，改为守护「它确实不再出现」——否则将来有人顺手加回来，
    页面的性能与信息重复问题会无声回归。
  */
  it('首页不再渲染实例列表（完整列表在「服务器」页）', async () => {
    renderWithProviders(<OverviewPage />, { route: '/' })
    // 等页面主体就绪，避免「还没渲染」被误判成「已移除」。
    expect(await screen.findByText('需要处理')).toBeInTheDocument()
    expect(screen.queryByTestId('overview-instances-virtual')).toBeNull()
    expect(screen.queryByTestId('overview-instance-row')).toBeNull()
  })
  it('同屏三栏聚合异常实例、近期任务与活跃告警，每类最多显示 5 条并复用现有路由', async () => {
    const instances = Array.from({ length: 6 }, (_, index) => ({
      id: index + 1,
      uuid: `inst-${index + 1}`,
      nodeId: 1,
      name: `crashed-${index + 1}`,
      type: 'paper',
      role: 'backend',
      processType: 'direct',
      status: 'CRASHED',
      startCommand: '',
      workDir: '',
      serverPort: 25565 + index,
      autoStart: false,
      autoRestart: false,
      tags: null,
      createdAt: '2026-01-01T00:00:00Z',
    }))
    const tasks = Array.from({ length: 6 }, (_, index) => ({
      id: index + 1,
      taskId: `task-${index + 1}`,
      nodeId: 1,
      kind: 'backup_create',
      state: index === 0 ? 'running' : 'succeeded',
      progress: index === 0 ? 50 : 100,
      title: `近期任务 ${index + 1}`,
      detail: '',
      error: '',
      result: '',
      cancelRequested: false,
      createdBy: 1,
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-01T00:00:00Z',
    }))
    const alerts = Array.from({ length: 6 }, (_, index) => ({
      id: index + 1,
      ruleId: 1,
      targetId: 1,
      level: index === 0 ? 'critical' : 'warn',
      triggerType: 'metric',
      value: 90,
      message: `活跃告警 ${index + 1}`,
      count: 1,
      resolved: false,
      firedAt: '2026-01-01T00:00:00Z',
      acknowledged: false,
      read: false,
      rule: { name: '资源告警' },
    }))
    server.use(
      // 实例改走 /instances/search：异常卡片带 status=CRASHED 过滤，密集表是分页信封。
      // mock 需尊重 pageSize——服务端会截断，异常卡片正是靠 pageSize=5 只取前 5 条。
      http.get(API('/instances/search'), ({ request }) => {
        const url = new URL(request.url)
        const status = url.searchParams.get('status')
        const pageSize = Number(url.searchParams.get('pageSize') ?? 50)
        const list = status ? instances.filter((i) => i.status === status) : instances
        return HttpResponse.json({ items: list.slice(0, pageSize), total: list.length, page: 1, pageSize })
      }),
      http.get(API('/tasks'), () => HttpResponse.json({ items: tasks, total: tasks.length, limit: 5, offset: 0 })),
      http.get(API('/alerts/events'), () => HttpResponse.json({ items: alerts, total: alerts.length })),
    )

    renderWithProviders(<OverviewPage />, { route: '/' })

    /*
      【结构变更·本轮】三栏（异常实例 / 近期任务 / 活跃告警）合并为一张「需要处理」。
      合并理由：三者地位不对等——近期任务多数是**已完成**的（回答「最近发生了什么」），
      另两者才回答「现在要我做什么」，同权重并排时待办项被已完成项淹没。
      合并后按紧急度排序（崩溃实例 > 失败任务 > 严重告警 > 警告 > 其余任务）。

      本用例随之从「三张卡各自截断 5 条」改为：
      ① 三类都在同一张卡里出现；② 排序符合紧急度；③ 条数受 ATTENTION_LIMIT(8) 约束。
    */
    const attention = await screen.findByTestId('overview-attention')

    // ① 三类行都出现在同一张卡内（原先分属三个 testid）。
    await waitFor(() => expect(within(attention).getAllByTestId('overview-exception-item').length).toBeGreaterThan(0))
    expect(within(attention).getAllByTestId('overview-task-item').length).toBeGreaterThan(0)
    expect(within(attention).getAllByTestId('overview-alert-item').length).toBeGreaterThan(0)

    // ② 三类都可见——这是「各类保底席位」要守的东西：
    //    若只做全局按紧急度截断，崩溃实例一多就会把任务与告警整体挤掉。
    const kinds = within(attention)
      .getAllByTestId(/^overview-(exception|task|alert)-item$/)
      .map((el) => el.getAttribute('data-testid') ?? '')
    expect(kinds.some((k) => k.includes('exception'))).toBe(true)
    expect(kinds.some((k) => k.includes('task'))).toBe(true)
    expect(kinds.some((k) => k.includes('alert'))).toBe(true)

    // ③ 合并后条数上限 8（原三栏最多 15 行，合并会更高，故统一收到 8）。
    expect(kinds.length).toBeLessThanOrEqual(8)

    // ④ 首行仍是紧急度最高的一类（崩溃实例），保证「最先看到最该处理的」。
    expect(kinds[0]).toContain('exception')

    // 「查看全部」指向崩溃实例筛选（合并卡的主出口）。
    expect(within(attention).getByRole('link', { name: '查看全部' })).toHaveAttribute(
      'href',
      '/instances?status=CRASHED',
    )
  })

  it('三类都为空时显示「需要处理」的统一空态', async () => {
    server.use(
      http.get(API('/instances/search'), () =>
        HttpResponse.json({ items: [], total: 0, page: 1, pageSize: 50 }),
      ),
      http.get(API('/tasks'), () => HttpResponse.json({ items: [], total: 0, limit: 5, offset: 0 })),
      http.get(API('/alerts/events'), () => HttpResponse.json({ items: [], total: 0 })),
    )

    renderWithProviders(<OverviewPage />, { route: '/' })

    // 【结构变更·本轮】原先是三张卡各显各的空态文案；合并后只有一处空态。
    expect(await screen.findByText('没有需要处理的项')).toBeInTheDocument()
  })

  /*
    【结构变更·本轮】三栏合并成一张「需要处理」后，「某一路取数失败只影响自己那张卡」
    不再成立——三类行同属一张卡，面板级的 isLoading/isError 是三者合并后的状态。
    故原 it.each 三条用例改为：只有三类**全部**失败时卡片才报错，
    任一路有数据时其余行照常显示（信息不被一起吞掉）。
  */
  it('某一路取数失败不影响其余行：全部失败才报错', async () => {
    server.use(
      // 实例与告警都失败，只有任务成功 → 卡片仍应显示任务行，而不是整卡报错。
      http.get(API('/instances/search'), () => new HttpResponse(null, { status: 500 })),
      http.get(API('/alerts/events'), () => new HttpResponse(null, { status: 500 })),
      http.get(API('/tasks'), () =>
        HttpResponse.json({
          items: [{ id: 1, taskId: 'task-1', nodeId: 1, kind: 'backup_create', state: 'failed', progress: 100, title: '仍可见任务', detail: '', error: '', result: '', cancelRequested: false, createdBy: 1, createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z' }],
          total: 1,
          limit: 5,
          offset: 0,
        }),
      ),
    )

    renderWithProviders(<OverviewPage />, { route: '/' })

    const attention = await screen.findByTestId('overview-attention')
    await waitFor(() => expect(within(attention).getAllByTestId('overview-task-item').length).toBeGreaterThan(0))
    expect(within(attention).getByText('仍可见任务')).toBeInTheDocument()
  })
})
