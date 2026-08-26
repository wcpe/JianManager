import { beforeAll, beforeEach, describe, expect, it } from 'vitest'
import { screen, waitFor } from '@testing-library/react'

import { mockInject } from '@jianmanager/devmock/inject'
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

/** MetricsSegment 详情页监控增强：探针版本选择、安装指引与当前指标阈值色条。 */
describe('MetricsSegment（mock 假后端）', () => {
  beforeEach(() => {
    loginMockUser()
  })

  it('展示 ServerProbe 当前版本与实例继承选择', async () => {
    renderWithProviders(<MetricsSegment instanceUuid="inst-1" instanceId={1} />)

    expect(await screen.findByText('ServerProbe 探针更新')).toBeInTheDocument()
    expect(screen.getByText('探针已连接')).toBeInTheDocument()
    expect(screen.getByText(/当前版本: 0\.2\.0 · 全局默认/)).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: '实例版本' })).toHaveValue('0')
  })

  it('探针未连接时显示可操作安装指引', async () => {
    mockInject('get', '/instances/:id/probe/update', {
      kind: 'status',
      status: 200,
      body: {
        instanceId: 1,
        instanceUuid: 'inst-1',
        probeConnected: false,
        versionId: 1,
        version: '0.2.0',
        versionOrigin: 'global',
        lastPushedAt: null,
      },
    })
    // 指标通道也不可用时才标「未连接」（避免 metrics.probeAvailable 默认真把状态盖住）。
    mockInject('get', '/instances/:id/metrics', {
      kind: 'status',
      status: 200,
      body: {
        tps: 20,
        onlinePlayers: 0,
        memoryMb: 512,
        msptMillis: 2,
        threads: 40,
        cpuPercent: 5,
        heapMaxMb: 1024,
        uptimeSeconds: 60,
        worlds: [],
        probeAvailable: false,
      },
    })
    renderWithProviders(<MetricsSegment instanceUuid="inst-1" instanceId={1} />)

    expect(await screen.findByText('探针未连接')).toBeInTheDocument()
    expect(screen.getByText(/可先下发当前版本/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '更新并重启' })).toBeEnabled()
  })

  it('F2：桥未连但 metrics.probeAvailable 时显示运行中，未选版本不掩盖运行态', async () => {
    mockInject('get', '/instances/:id/probe/update', {
      kind: 'status',
      status: 200,
      body: {
        instanceId: 1,
        instanceUuid: 'inst-1',
        probeConnected: false,
        versionId: 0,
        version: '',
        versionError: '制品版本库未配置',
        lastPushedAt: null,
      },
    })
    mockInject('get', '/instances/:id/metrics', {
      kind: 'status',
      status: 200,
      body: {
        tps: 20,
        onlinePlayers: 0,
        memoryMb: 560,
        msptMillis: 1,
        threads: 50,
        cpuPercent: 2,
        heapMaxMb: 2048,
        uptimeSeconds: 86400,
        worlds: [],
        probeAvailable: true,
      },
    })
    renderWithProviders(<MetricsSegment instanceUuid="inst-1" instanceId={1} />)

    expect(await screen.findByText('探针运行中（指标通道）')).toBeInTheDocument()
    expect(screen.getByText(/制品版本库未配置/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '更新探针' })).toBeDisabled()
    expect(screen.queryByText('探针未连接')).not.toBeInTheDocument()
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
