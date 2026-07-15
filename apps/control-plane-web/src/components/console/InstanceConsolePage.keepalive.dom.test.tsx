import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect } from 'react'
import { useQueryClient, type QueryClient } from '@tanstack/react-query'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'

vi.mock('@xterm/xterm', async () => {
  const harness = await import('@/test/xterm-ws-harness')
  return { Terminal: harness.MockTerminal }
})
vi.mock('@xterm/addon-fit', async () => {
  const harness = await import('@/test/xterm-ws-harness')
  return { FitAddon: harness.MockFitAddon }
})

import { MockWebSocket, resetTerminalHarness, wsSockets, xtermInstances } from '@/test/xterm-ws-harness'
import InstanceConsolePage from './InstanceConsolePage'

// MetricsSegment 图表（recharts）依赖 ResizeObserver 实测宽度，jsdom 无之 → 补桩。
beforeAll(() => {
  if (!('ResizeObserver' in globalThis)) {
    globalThis.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver
  }
})

let capturedQueryClient: QueryClient | null = null
function CaptureQueryClient({ onClient }: { onClient: (qc: QueryClient) => void }) {
  const qc = useQueryClient()
  useEffect(() => onClient(qc), [qc, onClient])
  return null
}

async function findSocket() {
  await waitFor(() => expect(wsSockets.length).toBeGreaterThan(0))
  return wsSockets.at(-1)!
}

/**
 * FR-295 页签 keep-alive：访问过的页签切走不卸载（Activity 隐藏），
 * 终端 WS 不断、缓冲与本地状态保留，隐藏页签轮询暂停。
 * seed：实例 1 = survival-1 RUNNING（mocks/handlers/domains/instance.ts）。
 */
describe('InstanceConsolePage 页签 keep-alive（FR-295）', () => {
  beforeEach(() => {
    loginMockUser()
    resetTerminalHarness()
    capturedQueryClient = null
    vi.stubGlobal('WebSocket', MockWebSocket)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('终端页签往返 5 次：WS 连接数保持 1、xterm 不重建、滚动缓冲保留', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstanceConsolePage instanceId={1} />, { route: '/instances/1?tab=terminal' })

    // 终端首连：一条 WS + 一个 xterm。
    const socket = await findSocket()
    await waitFor(() => expect(xtermInstances.length).toBe(1))
    act(() => socket.emitMessage({ type: 'stdout', data: 'keepalive-buffer-line\n' }))
    expect(screen.getByText('keepalive-buffer-line')).toBeInTheDocument()

    // 终端 ↔ 玩家页签往返 5 次。
    for (let i = 0; i < 5; i++) {
      await user.click(screen.getByRole('button', { name: '玩家' }))
      await user.click(screen.getByRole('button', { name: '控制台' }))
    }

    // WS 未断未重连、xterm 未重建，缓冲仍在 DOM（Activity 隐藏保留）。
    expect(wsSockets).toHaveLength(1)
    expect(socket.closedByClient).toBe(false)
    expect(xtermInstances).toHaveLength(1)
    expect(xtermInstances[0].disposed).toBe(false)
    expect(screen.getByText('keepalive-buffer-line')).toBeInTheDocument()

    // 切走期间服务端继续推送，缓冲连续（WS 在管理器手里持续收数据）。
    await user.click(screen.getByRole('button', { name: '玩家' }))
    act(() => socket.emitMessage({ type: 'stdout', data: 'pushed-while-hidden\n' }))
    await user.click(screen.getByRole('button', { name: '控制台' }))
    expect(screen.getByText('pushed-while-hidden')).toBeInTheDocument()
  })

  it('页签本地状态保留：终端搜索词切走切回不丢', async () => {
    const user = userEvent.setup()
    renderWithProviders(<InstanceConsolePage instanceId={1} />, { route: '/instances/1?tab=terminal' })

    await findSocket()
    await user.click(await screen.findByRole('button', { name: '搜索终端' }))
    await user.type(screen.getByRole('searchbox', { name: '搜索终端输入' }), 'stop')

    await user.click(screen.getByRole('button', { name: '玩家' }))
    await user.click(screen.getByRole('button', { name: '控制台' }))

    expect(screen.getByRole('searchbox', { name: '搜索终端输入' })).toHaveValue('stop')
  })

  it('隐藏页签停轮询：监控页签切走后 metricSeries 查询观察者归零（数据保留缓存）', async () => {
    const user = userEvent.setup()
    renderWithProviders(
      <>
        <CaptureQueryClient onClient={(qc) => { capturedQueryClient = qc }} />
        <InstanceConsolePage instanceId={1} />
      </>,
      { route: '/instances/1?tab=metrics' },
    )

    // 监控页签激活：metricSeries 查询有活跃观察者（30s 轮询由观察者驱动）。
    await waitFor(() => {
      const queries = capturedQueryClient!.getQueryCache().findAll({ queryKey: ['metricSeries'] })
      expect(queries.length).toBeGreaterThan(0)
      expect(queries.some((q) => q.getObserversCount() > 0)).toBe(true)
    })

    // 切到概览：监控页签 Activity 隐藏 → effects 卸载 → 其查询订阅暂停（轮询停止）。
    // 概览自身也用 metricSeries 画真实 TPS 火花线（FR-343 去 mock-api），概览可见时其查询活跃；
    // 它与监控页签的宽查询 queryKey 不同（概览带 metrics=['inst_tps']），故此处只校验监控页签查询归零。
    await user.click(screen.getByRole('button', { name: '概览' }))
    await waitFor(() => {
      const queries = capturedQueryClient!.getQueryCache().findAll({ queryKey: ['metricSeries'] })
        .filter((q) => !q.queryKey.includes('inst_tps'))
      expect(queries.length).toBeGreaterThan(0)
      expect(queries.every((q) => q.getObserversCount() === 0)).toBe(true)
    })
    // 缓存数据未被清除：切回可瞬时呈现。
    const cached = capturedQueryClient!.getQueryCache().findAll({ queryKey: ['metricSeries'] })
    expect(cached.length).toBeGreaterThan(0)

    // 切回监控：订阅恢复。
    await user.click(screen.getByRole('button', { name: '监控' }))
    await waitFor(() => {
      const queries = capturedQueryClient!.getQueryCache().findAll({ queryKey: ['metricSeries'] })
      expect(queries.some((q) => q.getObserversCount() > 0)).toBe(true)
    })
  })
})
