import { beforeEach, describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'

import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import InstanceConsolePage from './InstanceConsolePage'

/**
 * 无探针可用性语义（FR-343 + FR-446/447）：探针不可用时 TPS/在线数在概览 KPI 卡与顶栏指标条
 * 显式渲染「不可用」，**不再**显示 0 / -1 / 「需探针」伪值；顶栏折叠为一枚 ServerProbe 未连接芯片。
 * CPU/RSS/运行时长与探针无关，仍展示真实值。
 */
function kpiValue(label: string): HTMLElement {
  // KPI 卡：<p>label</p> 与 <p>value</p> 同处一个 .min-w-0 容器；header 指标段的 label 在 <span> 内，排除。
  const labelEl = screen.getAllByText(label).find((el) => el.tagName === 'P')
  expect(labelEl, `未找到 KPI 标签「${label}」`).toBeTruthy()
  const container = labelEl!.parentElement as HTMLElement
  return within(container).getAllByRole('paragraph')[1] as HTMLElement
}

describe('InstanceConsolePage 无探针可用性契约（FR-343 / FR-446/447）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('无探针仍展示 CPU、RSS、运行时长，TPS/在线数与图表按可用性显「不可用」', async () => {
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
        // 三源皆无（探针未装、SLP/Query 不可达）：可用性位全 false。
        playersAvailable: false,
        motdAvailable: false,
        versionAvailable: false,
        maxPlayersAvailable: false,
        playerNamesAvailable: false,
        pluginsAvailable: false,
        mapAvailable: false,
        slpAvailable: false,
        queryAvailable: false,
        sourceMask: 0,
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

    // CPU 现在顶栏指标条与概览 KPI 卡各一份（FR-412）；RSS/运行时长与探针无关，仍显示真实值。
    expect(await screen.findAllByText('37%')).toHaveLength(2)
    expect(screen.getByText('1536 MB')).toBeInTheDocument()
    expect(screen.getByText('RSS')).toBeInTheDocument()
    expect(screen.getByText('1h 1m')).toBeInTheDocument()

    // 顶栏（方案 B）：探针缺失折叠为聚合芯片，悬停可见说明。
    const chip = screen.getByText('ServerProbe 未连接')
    expect(chip.closest('span')).toHaveAttribute('title', '探针未连入，部分在线玩家与世界数据为预览值')

    // 概览 KPI：TPS 与 在线 均显「不可用」——不出现 0 / -1 / 「需探针」。
    expect(kpiValue('TPS')).toHaveTextContent('不可用')
    expect(kpiValue('在线')).toHaveTextContent('不可用')
    expect(screen.queryByText('需探针')).not.toBeInTheDocument()
    expect(screen.queryByText('-1/—')).not.toBeInTheDocument()
    // 顶栏同样显「不可用」（TPS 段 + 在线段）。
    expect(screen.getAllByText('不可用').length).toBeGreaterThanOrEqual(4)

    // MSPT（方案 B）：探针缺失时不渲染任何分段/KPI 单元格。
    expect(screen.queryAllByText('MSPT').filter((el) => el.tagName === 'P' || el.tagName === 'SPAN')).toHaveLength(0)

    const chart = screen.getByRole('heading', { name: 'TPS / MSPT' }).closest('section') as HTMLElement
    expect(within(chart).getByText('探针未连入，部分在线玩家与世界数据为预览值')).toBeInTheDocument()
    expect(chart.querySelector('.grid-cols-24')).not.toBeInTheDocument()
    expect(screen.queryByText('TPS 低于 18，建议检查插件或实体数量')).not.toBeInTheDocument()
    expect(screen.queryByText('mock-api')).not.toBeInTheDocument()
  })
})
