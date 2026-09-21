import { describe, it, expect, beforeEach } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { HealthWall } from './HealthWall'

/**
 * 集群健康墙（FR-461）：分级矩阵渲染 + 服务端排序切换 + 一键下钻单台。
 * /observability/health-wall 属观测域，本用例 server.use 桩占位（隔离运行）。
 */
let lastSort: string | null = null

beforeEach(() => {
  loginMockUser()
  lastSort = null
  server.use(
    http.get(API('/observability/health-wall'), ({ request }) => {
      lastSort = new URL(request.url).searchParams.get('sort')
      return HttpResponse.json({
        nodes: [
          { nodeId: 1, nodeUuid: 'n-offline', name: 'node-offline', freshness: 'offline', cpuPct: null, memPct: null, diskPct: null, running: 0, crashed: 0, stopped: 0, activeAlerts: 0, botActive: null, botConnecting: null, level: 'offline', href: '/monitoring?node=n-offline' },
          { nodeId: 2, nodeUuid: 'n-good', name: 'node-good', freshness: 'fresh', cpuPct: 20, memPct: 40, diskPct: 60, running: 3, crashed: 0, stopped: 1, activeAlerts: 0, botActive: 5, botConnecting: 0, level: 'healthy', href: '/monitoring?node=n-good' },
          { nodeId: 3, nodeUuid: 'n-deg', name: 'node-degraded', freshness: 'fresh', cpuPct: 95, memPct: 50, diskPct: 50, running: 2, crashed: 1, stopped: 0, activeAlerts: 1, botActive: 0, botConnecting: 0, level: 'degraded', href: '/monitoring?node=n-deg' },
        ],
      })
    }),
  )
})

describe('HealthWall（mock 假后端）', () => {
  it('渲染逐台分级矩阵 + 图例计数，点击单元格一键下钻', async () => {
    renderWithProviders(<HealthWall enabled />, { route: '/' })

    const cells = await screen.findAllByTestId('health-wall-cell')
    expect(cells).toHaveLength(3)
    // 每格按分级着色并携带下钻地址。
    const offline = cells.find((cell) => cell.dataset.level === 'offline')
    expect(offline).toHaveAttribute('data-level', 'offline')
    expect(screen.getByRole('link', { name: 'node-good 健康' })).toHaveAttribute('href', '/monitoring?node=n-good')
    expect(screen.getByRole('link', { name: 'node-degraded 降级' })).toHaveAttribute('href', '/monitoring?node=n-deg')

    const legend = screen.getByTestId('health-wall-legend')
    expect(within(legend).getByText('离线 1')).toBeInTheDocument()
    expect(within(legend).getByText('降级 1')).toBeInTheDocument()
    expect(within(legend).getByText('健康 1')).toBeInTheDocument()
  })

  it('切换排序键触发服务端 ?sort 重新查询', async () => {
    const user = userEvent.setup()
    renderWithProviders(<HealthWall enabled />, { route: '/' })

    await screen.findAllByTestId('health-wall-cell')
    expect(lastSort).toBe('level')

    await user.selectOptions(screen.getByTestId('health-wall-sort'), 'cpu')
    await screen.findAllByTestId('health-wall-cell')
    expect(lastSort).toBe('cpu')
  })

  it('enabled=false 时不发起请求', async () => {
    renderWithProviders(<HealthWall enabled={false} />, { route: '/' })
    // 未启用时无单元格、无请求。
    expect(screen.queryAllByTestId('health-wall-cell')).toHaveLength(0)
    expect(lastSort).toBeNull()
  })

  it('超限时展示截断提示，并分批「显示更多」展开', async () => {
    const many = Array.from({ length: 205 }, (_, i) => ({
      nodeId: i + 1,
      nodeUuid: `n-${i}`,
      name: `node-${i}`,
      freshness: 'fresh',
      cpuPct: 10,
      memPct: 20,
      diskPct: 30,
      running: 0,
      crashed: 0,
      stopped: 0,
      activeAlerts: 0,
      botActive: null,
      botConnecting: null,
      level: 'healthy',
      href: `/monitoring?node=n-${i}`,
    }))
    server.use(
      http.get(API('/observability/health-wall'), () => HttpResponse.json({ nodes: many, truncated: true })),
    )
    renderWithProviders(<HealthWall enabled />, { route: '/' })

    // 截断提示如实呈现（大集群仅展示最严重的一批）。
    await screen.findByTestId('health-wall-truncated')
    expect(screen.getByText(/仅展示最严重的 205 台/)).toBeInTheDocument()
    // 默认只渲染一页（200 格），其余经「显示更多」逐批展开，避免一次挂载数百 DOM。
    expect(screen.getAllByTestId('health-wall-cell')).toHaveLength(200)
    await userEvent.setup().click(screen.getByTestId('health-wall-more'))
    expect(screen.getAllByTestId('health-wall-cell')).toHaveLength(205)
  })
})
