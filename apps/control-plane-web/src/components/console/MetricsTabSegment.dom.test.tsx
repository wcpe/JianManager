import { beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'

import { mockInject } from '@jianmanager/devmock/inject'
import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { DirectProbeSummary } from './MetricsFallbackSegment'
import MetricsTabSegment from './MetricsTabSegment'

beforeAll(() => {
  if (!('ResizeObserver' in globalThis)) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver
  }
})

/** 注入实例 metrics：probeAvailable 决定走全量还是轻量（tps 用 -1 模拟探针缺失时的伪值）。 */
function injectMetrics(probeAvailable: boolean) {
  mockInject('get', '/instances/:id/metrics', {
    kind: 'status',
    status: 200,
    body: {
      tps: probeAvailable ? 19.8 : -1,
      onlinePlayers: probeAvailable ? 12 : -1,
      memoryMb: 1536,
      msptMillis: probeAvailable ? 28 : -1,
      threads: 86,
      cpuPercent: 37.4,
      heapMaxMb: probeAvailable ? 4096 : 0,
      uptimeSeconds: 3661,
      worlds: [],
      probeAvailable,
    },
  })
}

/**
 * FR-448 §2.1：`metrics` Tab 同一页签内按探针有无呈现两套样式——
 * 有探针 → ServerProbe 全量视图；无探针 → 轻量进程/直探视图。实例 1 为 backend 画像。
 */
describe('MetricsTabSegment（FR-448 探针有无两套样式）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('探针可用 → 走 ServerProbe 全量视图（探针卡 + 当前健康），不渲染轻量视图', async () => {
    injectMetrics(true)
    renderWithProviders(<MetricsTabSegment instanceId={1} />)

    expect(await screen.findByText('ServerProbe 探针更新')).toBeInTheDocument()
    expect(await screen.findByText('当前健康')).toBeInTheDocument()
    expect(screen.queryByTestId('metrics-fallback')).toBeNull()
    expect(screen.queryByTestId('process-panel')).toBeNull()
    expect(screen.queryByTestId('direct-probe-summary')).toBeNull()
  })

  it('探针不可用 → 切到轻量视图（进程指标 + 直探摘要），不渲染探针全量视图、不落 -1 伪值', async () => {
    injectMetrics(false)
    renderWithProviders(<MetricsTabSegment instanceId={1} />)

    expect(await screen.findByTestId('metrics-fallback')).toBeInTheDocument()
    // 复用 ProcessPanel 承载节点进程指标（探针无关）。
    expect(await screen.findByTestId('process-panel')).toBeInTheDocument()
    // 无直探 → 显式「不可用」，不落 -1/-- 占位。
    expect(screen.getByTestId('direct-probe-summary')).toBeInTheDocument()
    expect(screen.getByText('直探不可用：MC 直探（SLP）尚未接入。')).toBeInTheDocument()
    // 探针全量视图（探针卡 / 当前健康）不得出现。
    expect(screen.queryByText('ServerProbe 探针更新')).toBeNull()
    expect(screen.queryByText('当前健康')).toBeNull()
    // 探针缺失时的伪值 -1 不得透传到轻量视图。
    expect(screen.queryByText('-1')).toBeNull()
  })
})

describe('DirectProbeSummary 直探可用态（FR-446 接入后）', () => {
  it('有直探数据时呈现 MOTD/在线/版本/延迟，不落占位', () => {
    renderWithProviders(
      <DirectProbeSummary
        summary={{ motd: 'A Minecraft Server', onlinePlayers: 5, maxPlayers: 20, version: '1.20.4', latencyMs: 23 }}
      />,
    )
    const card = screen.getByTestId('direct-probe-summary')
    expect(within(card).getByText('A Minecraft Server')).toBeInTheDocument()
    expect(within(card).getByText('5 / 20')).toBeInTheDocument()
    expect(within(card).getByText('1.20.4')).toBeInTheDocument()
    expect(within(card).getByText('23ms')).toBeInTheDocument()
  })

  it('无直探数据时显式「不可用」，不留破折号占位', () => {
    renderWithProviders(<DirectProbeSummary summary={null} />)
    const card = screen.getByTestId('direct-probe-summary')
    expect(within(card).getByText('直探不可用：MC 直探（SLP）尚未接入。')).toBeInTheDocument()
    expect(within(card).queryByText('—')).toBeNull()
  })
})
