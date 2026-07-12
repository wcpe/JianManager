import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { http, HttpResponse } from 'msw'
import { toast } from 'sonner'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { server } from '@/mocks/server'
import { API } from '@/mocks/api'
import { clearInstanceDrafts, reportInstanceDraft } from '@/lib/console-draft-registry'

vi.mock('@xterm/xterm', async () => {
  const harness = await import('@/test/xterm-ws-harness')
  return { Terminal: harness.MockTerminal }
})
vi.mock('@xterm/addon-fit', async () => {
  const harness = await import('@/test/xterm-ws-harness')
  return { FitAddon: harness.MockFitAddon }
})

import { MockWebSocket, resetTerminalHarness, wsSockets, xtermInstances } from '@/test/xterm-ws-harness'
import { IDLE_DISCONNECT_MS, terminalSessionManager } from '@/lib/terminal-session-manager'
import InstanceConsoleCache from './InstanceConsoleCache'

/**
 * 驱动宿主切换实例的最小外壳（等价路由参数变化——路由级 key 归并由
 * Workspace.routekey 测试覆盖，此处专注热集语义）。
 * seed：1=survival-1、10=survival-proxy、11=survival-lobby、20=creative-proxy，全 RUNNING。
 */
function CacheHarness() {
  const [id, setId] = useState(1)
  return (
    <>
      {[1, 10, 11, 20].map((n) => (
        <button key={n} type="button" onClick={() => setId(n)}>
          open-{n}
        </button>
      ))}
      <InstanceConsoleCache instanceId={id} />
    </>
  )
}

async function waitSockets(count: number) {
  await waitFor(() => expect(wsSockets.length).toBe(count))
}

describe('InstanceConsoleCache 跨服热缓存（FR-296）', () => {
  beforeEach(() => {
    loginMockUser()
    resetTerminalHarness()
    vi.stubGlobal('WebSocket', MockWebSocket)
    // 递增 token：断言重连「现取新 token」（FR-140）。
    let tokenSeq = 0
    server.use(
      http.get(API('/instances/:id/terminal-token'), () => {
        tokenSeq += 1
        return HttpResponse.json({
          token: `hot-token-${tokenSeq}`,
          wsUrl: 'ws://localhost/_mock/terminal',
          expiresIn: 30,
        })
      }),
    )
  })

  afterEach(() => {
    for (const id of [1, 10, 11, 20]) clearInstanceDrafts(id)
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('3 服循环切换：组件不重建、各自 WS 保持、缓冲连续', async () => {
    const user = userEvent.setup()
    renderWithProviders(<CacheHarness />, { route: '/instances/1?tab=terminal' })

    await waitSockets(1)
    act(() => wsSockets[0].emitMessage({ type: 'stdout', data: 'buffer-of-one\n' }))

    await user.click(screen.getByRole('button', { name: 'open-10' }))
    await waitSockets(2)
    await user.click(screen.getByRole('button', { name: 'open-11' }))
    await waitSockets(3)

    // 切走期间实例 1 继续收流（WS 在管理器手里）。
    act(() => wsSockets[0].emitMessage({ type: 'stdout', data: 'pushed-while-away\n' }))

    // 回到实例 1：不新建连接、不重建 xterm，三个控制台都仍在 DOM（隐藏保活）。
    await user.click(screen.getByRole('button', { name: 'open-1' }))
    expect(wsSockets).toHaveLength(3)
    expect(xtermInstances).toHaveLength(3)
    expect(wsSockets.every((s) => !s.closedByClient)).toBe(true)
    expect(xtermInstances.every((t) => !t.disposed)).toBe(true)
    expect(screen.getByText(/服务器控制台 \/ survival-1/)).toBeInTheDocument()
    expect(screen.getByText(/服务器控制台 \/ survival-proxy/)).toBeInTheDocument()
    expect(screen.getByText(/服务器控制台 \/ survival-lobby/)).toBeInTheDocument()
    expect(screen.getByText('buffer-of-one')).toBeInTheDocument()
    expect(screen.getByText('pushed-while-away')).toBeInTheDocument()

    // 再循环一轮 A→B→C→A，计数依旧稳定。
    for (const name of ['open-10', 'open-11', 'open-1']) {
      await user.click(screen.getByRole('button', { name }))
    }
    expect(wsSockets).toHaveLength(3)
    expect(xtermInstances).toHaveLength(3)
  })

  it('打开第 4 服：LRU 尾被整体淘汰（WS close + xterm dispose + 组件卸载）', async () => {
    const user = userEvent.setup()
    renderWithProviders(<CacheHarness />, { route: '/instances/1?tab=terminal' })

    await waitSockets(1)
    await user.click(screen.getByRole('button', { name: 'open-10' }))
    await waitSockets(2)
    await user.click(screen.getByRole('button', { name: 'open-11' }))
    await waitSockets(3)

    // 热集 [11,10,1]，打开 20 → 淘汰 LRU 尾 = 实例 1（socket[0]/xterm[0]）。
    await user.click(screen.getByRole('button', { name: 'open-20' }))
    await waitSockets(4)

    await waitFor(() => {
      expect(wsSockets[0].closedByClient).toBe(true)
      expect(xtermInstances[0].disposed).toBe(true)
      expect(screen.queryByText(/服务器控制台 \/ survival-1/)).not.toBeInTheDocument()
    })
    expect(terminalSessionManager.hasSession(1)).toBe(false)
    // 存活成员不受影响。
    expect(wsSockets[1].closedByClient).toBe(false)
    expect(wsSockets[2].closedByClient).toBe(false)
    expect(screen.getByText(/服务器控制台 \/ creative-proxy/)).toBeInTheDocument()
  })

  it('淘汰偏好：LRU 尾带草稿则跳过淘汰更早的无草稿成员；被迫淘汰带草稿者 toast 警示', async () => {
    const warnSpy = vi.spyOn(toast, 'warning').mockReturnValue('t' as unknown as ReturnType<typeof toast.warning>)
    try {
      const user = userEvent.setup()
      renderWithProviders(<CacheHarness />, { route: '/instances/1?tab=terminal' })

      await waitSockets(1)
      await user.click(screen.getByRole('button', { name: 'open-10' }))
      await waitSockets(2)
      await user.click(screen.getByRole('button', { name: 'open-11' }))
      await waitSockets(3)

      // 实例 1（LRU 尾）带草稿 → 淘汰目标改为无草稿的实例 10。
      reportInstanceDraft(1, 'resource-file', true)
      await user.click(screen.getByRole('button', { name: 'open-20' }))
      await waitFor(() => expect(terminalSessionManager.hasSession(10)).toBe(false))
      expect(terminalSessionManager.hasSession(1)).toBe(true)
      expect(warnSpy).not.toHaveBeenCalled()

      // 候选全带草稿：被迫淘汰 LRU 尾（实例 1）并 toast 警示。
      reportInstanceDraft(11, 'resource-file', true)
      reportInstanceDraft(20, 'resource-file', true)
      await user.click(screen.getByRole('button', { name: 'open-10' }))
      await waitFor(() => expect(terminalSessionManager.hasSession(1)).toBe(false))
      expect(warnSpy).toHaveBeenCalledTimes(1)
      expect(String(warnSpy.mock.calls[0][0])).toContain('survival-1')
    } finally {
      warnSpy.mockRestore()
    }
  })

  it('后台闲置超时断连降级，回切自动重连并现取新 token', async () => {
    renderWithProviders(<CacheHarness />, { route: '/instances/1?tab=terminal' })
    await waitSockets(1)
    const firstToken = new URL(wsSockets[0].url).searchParams.get('token')

    // 只伪造 setTimeout/clearTimeout：闲置计时可推进，MSW/微任务不受影响。
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    fireEvent.click(screen.getByRole('button', { name: 'open-10' }))
    await vi.advanceTimersByTimeAsync(IDLE_DISCONNECT_MS + 1000)

    expect(terminalSessionManager.getState(1)).toBe('idle-disconnected')
    expect(wsSockets[0].closedByClient).toBe(true)
    vi.useRealTimers()

    // 回切实例 1：自动重连，新连接携带新签发 token（不复用已消费旧 token）。
    const socketsBefore = wsSockets.length
    fireEvent.click(screen.getByRole('button', { name: 'open-1' }))
    await waitFor(() => expect(terminalSessionManager.getState(1)).toBe('connected'))
    expect(wsSockets.length).toBeGreaterThan(socketsBefore)
    const lastToken = new URL(wsSockets.at(-1)!.url).searchParams.get('token')
    expect(lastToken).not.toBe(firstToken)
  })
})
