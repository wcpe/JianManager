import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@jianmanager/devmock/inject'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
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

describe('OverviewPage（mock 假后端）', () => {
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
