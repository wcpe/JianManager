import { describe, it, expect, beforeEach } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import { mockInject } from '@/mocks/inject'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
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
    renderWithProviders(<OverviewPage />, { route: '/' })
    expect(await screen.findByText('survival-1')).toBeInTheDocument()
    expect(screen.getByText('lobby-proxy')).toBeInTheDocument()
    expect(screen.getByText('creative-1')).toBeInTheDocument()
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
})
