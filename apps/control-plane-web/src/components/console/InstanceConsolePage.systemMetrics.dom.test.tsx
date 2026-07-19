import { beforeEach, describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'

import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import InstanceConsolePage from './InstanceConsolePage'

function expectMetricCellsRequireProbe(label: string, count: number) {
  const cells = screen.getAllByText(label).filter((element) => element.tagName === 'P')
  expect(cells).toHaveLength(count)
  for (const labelElement of cells) {
    expect(within(labelElement.parentElement as HTMLElement).getByText('需探针')).toBeInTheDocument()
  }
}

describe('InstanceConsolePage 无探针系统指标契约（FR-343）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('无探针仍展示 CPU、RSS、运行时长，TPS/MSPT/在线数与时序图诚实标记需探针', async () => {
    server.use(
      http.get(API('/instances/:id/metrics'), () => HttpResponse.json({
        tps: -1,
        onlinePlayers: -1,
        memoryMb: 1536,
        msptMillis: 0,
        threads: 0,
        cpuPercent: 37.4,
        heapMaxMb: 0,
        uptimeSeconds: 3661,
        worlds: [],
        probeAvailable: false,
      })),
      http.get(API('/instances/:id/server-state'), ({ params }) => HttpResponse.json({
        instanceId: Number(params.id),
        connected: false,
        available: false,
        state: null,
        error: '探针未连入',
      })),
    )

    renderWithProviders(<InstanceConsolePage instanceId={1} />, { route: '/instances/1' })

    expect(await screen.findByText('37%')).toBeInTheDocument()
    expect(screen.getByText('1536 MB')).toBeInTheDocument()
    expect(screen.getByText('RSS')).toBeInTheDocument()
    expect(screen.getByText('1h 1m')).toBeInTheDocument()

    expectMetricCellsRequireProbe('TPS', 2)
    expectMetricCellsRequireProbe('在线', 2)
    expectMetricCellsRequireProbe('MSPT', 1)

    const chart = screen.getByRole('heading', { name: 'TPS / MSPT' }).closest('section') as HTMLElement
    expect(within(chart).getByText('探针未连入，部分在线玩家与世界数据为预览值')).toBeInTheDocument()
    expect(chart.querySelector('.grid-cols-24')).not.toBeInTheDocument()
    expect(screen.queryByText('TPS 低于 18，建议检查插件或实体数量')).not.toBeInTheDocument()
    expect(screen.queryByText('mock-api')).not.toBeInTheDocument()
  })
})
