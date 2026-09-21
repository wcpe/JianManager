import { beforeEach, describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'

import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import InstanceConsolePage from './InstanceConsolePage'

/**
 * 采集优先级链三态渲染（FR-446 / FR-447）：
 * - 探针 + 直探：TPS/在线以探针为准，来源芯片列出全部命中来源。
 * - 仅直探（SLP）：探针缺失 TPS「不可用」，在线数回落 SLP 真实值。
 * - 三源皆无：TPS/在线均「不可用」，无来源芯片（不显示 0）。
 */
function kpiValue(label: string): HTMLElement {
  const labelEl = screen.getAllByText(label).find((el) => el.tagName === 'P')
  expect(labelEl, `未找到 KPI 标签「${label}」`).toBeTruthy()
  return within(labelEl!.parentElement as HTMLElement).getAllByRole('paragraph')[1] as HTMLElement
}

function stubMetrics(body: Record<string, unknown>) {
  server.use(
    http.get(API('/instances/:id/metrics'), () => HttpResponse.json({
      memoryMb: 2048,
      msptMillis: 28,
      threads: 86,
      cpuPercent: 35,
      heapMaxMb: 4096,
      uptimeSeconds: 86400,
      worlds: [],
      motdAvailable: false,
      versionAvailable: false,
      playerNamesAvailable: false,
      pluginsAvailable: false,
      mapAvailable: false,
      ...body,
    })),
    // 探针 server-state 关闭，改由实例实时指标（含直探）提供在线数，隔离被测路径。
    http.get(API('/instances/:id/server-state'), ({ params }) => HttpResponse.json({
      instanceId: Number(params.id),
      connected: false,
      available: false,
      state: null,
      error: '探针未连入',
    })),
  )
}

describe('InstanceConsolePage 采集优先级链三态（FR-446/447）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('探针 + SLP + Query：在线以探针为准显 x/y，TPS 显真实值，来源芯片列出三源', async () => {
    stubMetrics({
      tps: 19.8,
      onlinePlayers: 12,
      probeAvailable: true,
      playersAvailable: true,
      maxPlayers: 20,
      maxPlayersAvailable: true,
      slpAvailable: true,
      queryAvailable: true,
      sourceMask: 7,
    })
    renderWithProviders(<InstanceConsolePage instanceId={1} />, { route: '/instances/1' })

    // 顶栏 + 概览 KPI 各一处「12/20」；await 到数据就绪再断言（instance 未就绪时页面早退）。
    expect(await screen.findAllByText('12/20')).not.toHaveLength(0)
    expect(kpiValue('TPS')).toHaveTextContent('19.8')

    const chip = document.querySelector('[data-metric-sources]') as HTMLElement
    expect(chip).not.toBeNull()
    expect(chip).toHaveAttribute('data-metric-sources', 'probe,slp,query')
    expect(chip).toHaveTextContent('探针')
    expect(chip).toHaveTextContent('SLP 直探')
    expect(chip).toHaveTextContent('Query 直探')
  })

  it('仅直探（SLP）：在线回落到 SLP 真实值，TPS 仍「不可用」，来源芯片仅标 SLP', async () => {
    stubMetrics({
      tps: 0,
      onlinePlayers: 7,
      probeAvailable: false,
      playersAvailable: true,
      maxPlayers: 30,
      maxPlayersAvailable: true,
      slpAvailable: true,
      queryAvailable: false,
      sourceMask: 2,
    })
    renderWithProviders(<InstanceConsolePage instanceId={1} />, { route: '/instances/1' })

    expect(await screen.findAllByText('7/30')).not.toHaveLength(0)
    expect(kpiValue('TPS')).toHaveTextContent('不可用')

    const chip = document.querySelector('[data-metric-sources]') as HTMLElement
    expect(chip).toHaveAttribute('data-metric-sources', 'slp')
    expect(chip).toHaveTextContent('SLP 直探')
    expect(chip).not.toHaveTextContent('Query 直探')
  })

  it('三源皆无：TPS/在线均「不可用」，不渲染来源芯片', async () => {
    stubMetrics({
      tps: 0,
      onlinePlayers: 0,
      probeAvailable: false,
      playersAvailable: false,
      maxPlayers: 0,
      maxPlayersAvailable: false,
      slpAvailable: false,
      queryAvailable: false,
      sourceMask: 0,
    })
    renderWithProviders(<InstanceConsolePage instanceId={1} />, { route: '/instances/1' })

    expect(await screen.findByText('ServerProbe 未连接')).toBeInTheDocument()
    expect(kpiValue('TPS')).toHaveTextContent('不可用')
    expect(kpiValue('在线')).toHaveTextContent('不可用')
    expect(document.querySelector('[data-metric-sources]')).toBeNull()
  })
})
