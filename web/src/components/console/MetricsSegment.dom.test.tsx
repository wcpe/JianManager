import { beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { screen, waitFor } from '@testing-library/react'

import { mockInject } from '@/mocks/inject'
import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import MetricsSegment from './MetricsSegment'

beforeAll(() => {
  if (!('ResizeObserver' in globalThis)) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver
  }
})

/** MetricsSegment 详情页监控增强：探针安装指引、离线缓存状态与当前指标阈值色条。 */
describe('MetricsSegment（mock 假后端）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('展示 ServerProbe 离线依赖缓存大小与短指纹', async () => {
    renderWithProviders(<MetricsSegment instanceUuid="inst-1" instanceId={1} />)

    expect(await screen.findByText('ServerProbe 探针更新')).toBeInTheDocument()
    expect(screen.getByText('探针已连接')).toBeInTheDocument()
    expect(screen.getByText(/内嵌版本: 0\.1\.0/)).toBeInTheDocument()
    expect(screen.getByText(/离线依赖缓存: 已内嵌 6\.3 MiB · 9ca6579c/)).toBeInTheDocument()
  })

  it('探针未连接时显示可操作安装指引', async () => {
    mockInject('get', '/instances/:id/probe/update', {
      kind: 'status',
      status: 200,
      body: {
        instanceId: 1,
        instanceUuid: 'inst-1',
        probeConnected: false,
        embeddedVersion: '0.1.0',
        embeddedFingerprint: 'abc123',
        embeddedAvailable: true,
        lastPushedAt: null,
      },
    })
    renderWithProviders(<MetricsSegment instanceUuid="inst-1" instanceId={1} />)

    expect(await screen.findByText('探针未连接')).toBeInTheDocument()
    expect(screen.getByText(/可先推送内嵌探针/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '更新并重启' })).toBeEnabled()
  })

  it('当前 TPS/MSPT/CPU 按阈值标记危险态', async () => {
    mockInject('get', '/instances/:id/metrics', {
      kind: 'status',
      status: 200,
      body: {
        tps: 15,
        onlinePlayers: 12,
        memoryMb: 2048,
        msptMillis: 80,
        threads: 86,
        cpuPercent: 95,
        heapMaxMb: 4096,
        uptimeSeconds: 86400,
        worlds: [],
        probeAvailable: true,
      },
    })
    const { container } = renderWithProviders(<MetricsSegment instanceUuid="inst-1" instanceId={1} />)

    expect(await screen.findByText('当前健康')).toBeInTheDocument()
    await waitFor(() => expect(container.querySelectorAll('[data-health-level="danger"]')).toHaveLength(3))
  })

  it('实例停机时折叠当前健康指标，避免误展示实时数值', async () => {
    renderWithProviders(<MetricsSegment instanceUuid="inst-2" instanceId={2} />)

    expect(await screen.findByText('当前健康')).toBeInTheDocument()
    expect(screen.getByText('实例未运行，当前指标已折叠；可继续查看下方历史曲线。')).toBeInTheDocument()
  })

  it('历史 TPS/MSPT/CPU 曲线显示阈值标记', async () => {
    renderWithProviders(<MetricsSegment instanceUuid="inst-1" instanceId={1} />)

    expect(await screen.findByText('TPS 警戒 18')).toBeInTheDocument()
    expect(screen.getByText('TPS 危险 16')).toBeInTheDocument()
    expect(screen.getByText('MSPT 警戒 50ms')).toBeInTheDocument()
    expect(screen.getByText('MSPT 危险 75ms')).toBeInTheDocument()
    expect(screen.getByText('CPU 警戒 75%')).toBeInTheDocument()
    expect(screen.getByText('CPU 危险 90%')).toBeInTheDocument()
  })
})
