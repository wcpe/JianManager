import { beforeEach, describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'

import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import InstanceConsolePage from './InstanceConsolePage'

/**
 * 无探针诚实标记：概览 KPI 卡（P 单元格）逐项标出「需探针」，不得显示 0 或空；
 * 顶栏指标条（方案 B 分段状态条）不再逐项写「需探针」，而是聚合为一枚「ServerProbe 未连接」芯片。
 */
function expectMetricCellsRequireProbe(label: string, count: number) {
  const cells = screen.getAllByText(label).filter((element) => element.tagName === 'P' || element.tagName === 'SPAN')
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

    // CPU 现在顶栏指标条与概览 KPI 卡各一份（FR-412）。
    expect(await screen.findAllByText('37%')).toHaveLength(2)
    expect(screen.getByText('1536 MB')).toBeInTheDocument()
    expect(screen.getByText('RSS')).toBeInTheDocument()
    expect(screen.getByText('1h 1m')).toBeInTheDocument()

    // 顶栏（方案 B）：探针缺失的 TPS/MSPT/在线 折叠为聚合芯片，不再逐项「需探针」。
    const chip = screen.getByText('ServerProbe 未连接')
    expect(chip.closest('span')).toHaveAttribute('title', '探针未连入，部分在线玩家与世界数据为预览值')

    // 概览 KPI 卡逐项诚实标记保留（TPS/在线）。
    expectMetricCellsRequireProbe('TPS', 1)
    expectMetricCellsRequireProbe('在线', 1)
    // MSPT（方案 B）：探针缺失时不渲染任何分段/KPI 单元格——诚实标记收敛到芯片与图表空态，
    // 不再出现孤立的「MSPT 需探针」单元格。
    expect(screen.queryAllByText('MSPT').filter((el) => el.tagName === 'P' || el.tagName === 'SPAN')).toHaveLength(0)

    const chart = screen.getByRole('heading', { name: 'TPS / MSPT' }).closest('section') as HTMLElement
    expect(within(chart).getByText('探针未连入，部分在线玩家与世界数据为预览值')).toBeInTheDocument()
    expect(chart.querySelector('.grid-cols-24')).not.toBeInTheDocument()
    expect(screen.queryByText('TPS 低于 18，建议检查插件或实体数量')).not.toBeInTheDocument()
    expect(screen.queryByText('mock-api')).not.toBeInTheDocument()
  })
})
