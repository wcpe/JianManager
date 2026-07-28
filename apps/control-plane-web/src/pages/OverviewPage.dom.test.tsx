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
    )

    renderWithProviders(<OverviewPage />, { route: '/' })

    expect(await screen.findByText('平台健康')).toBeInTheDocument()
    expect(screen.getByText('Bot Worker（共享）')).toBeInTheDocument()
    expect(await screen.findByText('Bot Worker 资源为共享进程观察值，不代表任一 Bot 或会话的独占资源。')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'node-a 心跳陈旧' })).toHaveAttribute('href', '/monitoring?node=node-a')
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

  it('底部实例表渲染种子实例', async () => {
    const { container } = renderWithProviders(<OverviewPage />, { route: '/' })
    expect(container.firstElementChild).toHaveAttribute('data-page', 'overview')
    expect(container.firstElementChild).toHaveClass('jm-page-stack')
    const instanceTable = within(await screen.findByTestId('overview-instances-virtual'))
    expect(await instanceTable.findByText('survival-1')).toBeInTheDocument()
    expect(instanceTable.getByText('lobby-proxy')).toBeInTheDocument()
    expect(instanceTable.getByText('creative-1')).toBeInTheDocument()
  })

  it('1000+ mock 实例下底部实例表只渲染可视窗口', async () => {
    renderWithProviders(<OverviewPage />, { route: '/' })

    const surface = await screen.findByTestId('overview-instances-virtual')
    await waitFor(() => expect(Number(surface.dataset.totalCount)).toBeGreaterThanOrEqual(1000))
    expect(await screen.findByText('survival-1')).toBeInTheDocument()
    expect(screen.queryAllByTestId('overview-instance-row').length).toBeLessThan(80)
  })

  it('注入实例列表空态 → 表体显示「暂无实例」', async () => {
    mockInject('get', '/instances', { kind: 'empty' })
    renderWithProviders(<OverviewPage />, { route: '/' })
    expect(await screen.findByText('暂无实例')).toBeInTheDocument()
    expect(screen.queryByText('survival-1')).not.toBeInTheDocument()
  })

  it('注入 500 → 实例表降级为空（非崩溃）', async () => {
    mockInject('get', '/instances', { kind: 'status', status: 500 })
    renderWithProviders(<OverviewPage />, { route: '/' })
    await waitFor(() => expect(screen.getByText('暂无实例')).toBeInTheDocument())
    expect(screen.queryByText('survival-1')).not.toBeInTheDocument()
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
      http.get(API('/instances'), () => HttpResponse.json(instances)),
      http.get(API('/tasks'), () => HttpResponse.json({ items: tasks, total: tasks.length, limit: 5, offset: 0 })),
      http.get(API('/alerts/events'), () => HttpResponse.json({ items: alerts, total: alerts.length })),
    )

    renderWithProviders(<OverviewPage />, { route: '/' })

    const grid = await screen.findByTestId('overview-operations-grid')
    expect(grid).toHaveClass('grid-cols-1', 'lg:grid-cols-3')

    const exceptions = within(screen.getByTestId('overview-exceptions'))
    expect(await exceptions.findAllByTestId('overview-exception-item')).toHaveLength(5)
    expect(exceptions.getByText('crashed-1')).toBeInTheDocument()
    expect(exceptions.queryByText('crashed-6')).not.toBeInTheDocument()
    expect(exceptions.getByRole('link', { name: '查看全部' })).toHaveAttribute('href', '/instances?status=CRASHED')

    const recentTasks = within(screen.getByTestId('overview-recent-tasks'))
    expect(await recentTasks.findAllByTestId('overview-task-item')).toHaveLength(5)
    expect(recentTasks.getByText('近期任务 1')).toBeInTheDocument()
    expect(recentTasks.queryByText('近期任务 6')).not.toBeInTheDocument()
    expect(recentTasks.getByRole('link', { name: '查看全部' })).toHaveAttribute('href', '/tasks')

    const activeAlerts = within(screen.getByTestId('overview-active-alerts'))
    expect(await activeAlerts.findAllByTestId('overview-alert-item')).toHaveLength(5)
    expect(activeAlerts.getByText('活跃告警 1')).toBeInTheDocument()
    expect(activeAlerts.queryByText('活跃告警 6')).not.toBeInTheDocument()
    expect(activeAlerts.getByRole('link', { name: '查看全部' })).toHaveAttribute('href', '/alerts')
  })

  it('三个聚合各自显示独立空态', async () => {
    server.use(
      http.get(API('/instances'), () => HttpResponse.json([])),
      http.get(API('/tasks'), () => HttpResponse.json({ items: [], total: 0, limit: 5, offset: 0 })),
      http.get(API('/alerts/events'), () => HttpResponse.json({ items: [], total: 0 })),
    )

    renderWithProviders(<OverviewPage />, { route: '/' })

    expect(await screen.findByText('暂无异常实例')).toBeInTheDocument()
    expect(screen.getByText('暂无近期任务')).toBeInTheDocument()
    expect(screen.getByText('暂无活跃告警')).toBeInTheDocument()
  })

  it.each([
    ['instances', '异常实例加载失败'],
    ['tasks', '近期任务加载失败'],
    ['alerts', '活跃告警加载失败'],
  ])('%s 聚合失败时独立降级且其他聚合仍可用', async (failedDomain, errorText) => {
    server.use(
      http.get(API('/instances'), () =>
        failedDomain === 'instances'
          ? new HttpResponse(null, { status: 500 })
          : HttpResponse.json([{ id: 1, uuid: 'inst-1', nodeId: 1, name: '仍可见异常实例', type: 'paper', role: 'backend', processType: 'direct', status: 'CRASHED', startCommand: '', workDir: '', serverPort: 25565, autoStart: false, autoRestart: false, tags: null, createdAt: '2026-01-01T00:00:00Z' }]),
      ),
      http.get(API('/tasks'), () =>
        failedDomain === 'tasks'
          ? new HttpResponse(null, { status: 500 })
          : HttpResponse.json({ items: [{ id: 1, taskId: 'task-1', nodeId: 1, kind: 'backup_create', state: 'running', progress: 50, title: '仍可见近期任务', detail: '', error: '', result: '', cancelRequested: false, createdBy: 1, createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z' }], total: 1, limit: 5, offset: 0 }),
      ),
      http.get(API('/alerts/events'), () =>
        failedDomain === 'alerts'
          ? new HttpResponse(null, { status: 500 })
          : HttpResponse.json({ items: [{ id: 1, ruleId: 1, targetId: 1, level: 'warn', triggerType: 'metric', value: 90, message: '仍可见活跃告警', count: 1, resolved: false, firedAt: '2026-01-01T00:00:00Z', acknowledged: false, read: false }], total: 1 }),
      ),
    )

    renderWithProviders(<OverviewPage />, { route: '/' })

    expect(await screen.findByText(errorText)).toBeInTheDocument()
    if (failedDomain !== 'instances') {
      expect(within(screen.getByTestId('overview-exceptions')).getByText('仍可见异常实例')).toBeInTheDocument()
    }
    if (failedDomain !== 'tasks') {
      expect(within(screen.getByTestId('overview-recent-tasks')).getByText('仍可见近期任务')).toBeInTheDocument()
    }
    if (failedDomain !== 'alerts') {
      expect(within(screen.getByTestId('overview-active-alerts')).getByText('仍可见活跃告警')).toBeInTheDocument()
    }
  })
})
