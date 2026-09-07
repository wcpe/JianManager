import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { http, HttpResponse } from 'msw'
import { toast } from 'sonner'

import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
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
import { HOT_SET_SIZE, IDLE_DISCONNECT_MS, terminalSessionManager } from '@/lib/terminal-session-manager'
import InstanceConsoleCache from './InstanceConsoleCache'

/**
 * 已知 RUNNING 的 mock 实例 id 池。
 *
 * 1/10/11/20 是 seed override（survival-1 / survival-proxy / survival-lobby / creative-proxy）；
 * 其余取自 1200 条生成实例中 `id % 5 === 0` 那一档（STATUS_POOL[0] = RUNNING）。
 * 只有 RUNNING/CRASHED 会连 WS（STOPPED 不拨号，见 TerminalPane 的 canAttach），故只用 RUNNING。
 */
const RUNNING_IDS = [1, 10, 11, 20, 5, 15, 25, 30, 35, 40, 45, 50, 55]

/**
 * 恰好「填满热集 + 1」的实例序列，**由 HOT_SET_SIZE 推导而非写死**（FR-414）：
 * 容量默认值从 3 改到 6 时本测试不需要改任何数字，改到 7、8 也一样。
 */
const IDS = RUNNING_IDS.slice(0, HOT_SET_SIZE + 2)
/** 前 HOT_SET_SIZE 个恰好填满热集；后两个用来触发两轮淘汰。 */
const FILL_IDS = IDS.slice(0, HOT_SET_SIZE)
const OVERFLOW_ID = IDS[HOT_SET_SIZE]
const SECOND_OVERFLOW_ID = IDS[HOT_SET_SIZE + 1]
/** LRU 尾 = 最先打开的那个（其名字被 seed override 为 survival-1，供 toast 断言）。 */
const LRU_TAIL_ID = FILL_IDS[0]

/**
 * 驱动宿主切换实例的最小外壳（等价路由参数变化——路由级 key 归并由
 * Workspace.routekey 测试覆盖，此处专注热集语义）。
 */
function CacheHarness() {
  const [id, setId] = useState(IDS[0])
  return (
    <>
      {IDS.map((n) => (
        <button key={n} type="button" onClick={() => setId(n)}>
          open-{n}
        </button>
      ))}
      <InstanceConsoleCache instanceId={id} />
    </>
  )
}

/** 依次打开一串实例（每步等到其 WS 建立），返回最终热集顺序（新→旧）。 */
async function openAll(user: ReturnType<typeof userEvent.setup>, ids: readonly number[]) {
  for (const [index, id] of ids.entries()) {
    if (index === 0) continue // 首个已由初值渲染
    await user.click(screen.getByRole('button', { name: `open-${id}` }))
    await waitSockets(index + 1)
  }
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
    for (const id of IDS) clearInstanceDrafts(id)
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
    act(() => wsSockets[1].emitMessage({ type: 'stdout', data: 'buffer-of-ten\n' }))
    await user.click(screen.getByRole('button', { name: 'open-11' }))
    await waitSockets(3)
    act(() => wsSockets[2].emitMessage({ type: 'stdout', data: 'buffer-of-eleven\n' }))

    // 三份缓冲的快照引用：切换后引用不变即证明缓冲对象未被重建（比「xterm 对象还在」更直接）。
    const buffersBefore = [10, 11].map((id) => terminalSessionManager.getLines(id))

    // 切走期间实例 1 继续收流（WS 在管理器手里）。
    act(() => wsSockets[0].emitMessage({ type: 'stdout', data: 'pushed-while-away\n' }))

    // 回到实例 1：不新建连接、不重建缓冲，三个控制台都仍在 DOM（隐藏保活）。
    await user.click(screen.getByRole('button', { name: 'open-1' }))
    expect(wsSockets).toHaveLength(3)
    expect(wsSockets.every((s) => !s.closedByClient)).toBe(true)
    // FR-415 起实例控制台走 DOM 输出区（ADR-086），常驻缓冲是行缓冲而非 xterm buffer。
    // 原「3 个 xterm 未被销毁」的断言在此拆成两条更强的：①三个会话与其缓冲逐一存活、
    // ②这条路径根本不创建 xterm（防日后误把 xterm 渲染器接回实例控制台）。
    expect(xtermInstances).toHaveLength(0)
    expect([1, 10, 11].map((id) => terminalSessionManager.hasSession(id))).toEqual([true, true, true])
    expect(terminalSessionManager.getLines(10)).toBe(buffersBefore[0])
    expect(terminalSessionManager.getLines(11)).toBe(buffersBefore[1])
    expect(terminalSessionManager.getLines(1).map((line) => line.raw)).toEqual([
      'buffer-of-one',
      'pushed-while-away',
    ])
    expect(screen.getByText('survival-1')).toBeInTheDocument()
    expect(screen.getByText('survival-proxy')).toBeInTheDocument()
    expect(screen.getByText('survival-lobby')).toBeInTheDocument()
    expect(screen.getByText('buffer-of-one')).toBeInTheDocument()
    expect(screen.getByText('pushed-while-away')).toBeInTheDocument()
    expect(screen.getByText('buffer-of-ten')).toBeInTheDocument()
    expect(screen.getByText('buffer-of-eleven')).toBeInTheDocument()

    // 再循环一轮 A→B→C→A，连接数与缓冲内容依旧稳定。
    for (const name of ['open-10', 'open-11', 'open-1']) {
      await user.click(screen.getByRole('button', { name }))
    }
    expect(wsSockets).toHaveLength(3)
    expect(xtermInstances).toHaveLength(0)
    expect(terminalSessionManager.getLines(10)).toBe(buffersBefore[0])
    expect(terminalSessionManager.getLines(11)).toBe(buffersBefore[1])
  })

  it(`填满热集（${HOT_SET_SIZE} 个）后再开一个：LRU 尾被整体淘汰（WS close + 缓冲丢弃 + 组件卸载）`, async () => {
    const user = userEvent.setup()
    renderWithProviders(<CacheHarness />, { route: `/instances/${LRU_TAIL_ID}?tab=terminal` })

    await waitSockets(1)
    act(() => wsSockets[0].emitMessage({ type: 'stdout', data: 'doomed-buffer\n' }))
    await openAll(user, FILL_IDS)

    // 恰好填满即不淘汰——容量边界的上沿。原用例只测了「超一个」，此处补上「刚满」。
    expect(FILL_IDS.every((id) => terminalSessionManager.hasSession(id))).toBe(true)
    expect(wsSockets).toHaveLength(HOT_SET_SIZE)

    // 再开一个 → 超容 → 淘汰 LRU 尾（最先打开的那个，socket[0]）。
    await user.click(screen.getByRole('button', { name: `open-${OVERFLOW_ID}` }))
    await waitSockets(HOT_SET_SIZE + 1)

    await waitFor(() => {
      expect(wsSockets[0].closedByClient).toBe(true)
      // 原 xterm dispose 断言改为「权威缓冲整体丢弃」——ADR-086 后行缓冲才是被淘汰的那份状态。
      expect(terminalSessionManager.getLines(LRU_TAIL_ID)).toEqual([])
      expect(screen.queryByText('doomed-buffer')).not.toBeInTheDocument()
      expect(screen.queryByText('survival-1')).not.toBeInTheDocument()
    })
    expect(terminalSessionManager.hasSession(LRU_TAIL_ID)).toBe(false)
    // 存活成员一个不受影响。原用例只查了两个 socket，此处查全部（断言更强）。
    expect(wsSockets.slice(1).every((socket) => !socket.closedByClient)).toBe(true)
    expect(FILL_IDS.slice(1).every((id) => terminalSessionManager.hasSession(id))).toBe(true)
    expect(terminalSessionManager.hasSession(OVERFLOW_ID)).toBe(true)
  })

  it('淘汰偏好：LRU 尾带草稿则跳过淘汰更早的无草稿成员；被迫淘汰带草稿者 toast 警示', async () => {
    const warnSpy = vi.spyOn(toast, 'warning').mockReturnValue('t' as unknown as ReturnType<typeof toast.warning>)
    try {
      const user = userEvent.setup()
      renderWithProviders(<CacheHarness />, { route: `/instances/${LRU_TAIL_ID}?tab=terminal` })

      await waitSockets(1)
      await openAll(user, FILL_IDS)

      // LRU 尾带草稿 → 淘汰目标跳过它，改淘汰下一个最久未用的无草稿成员。
      reportInstanceDraft(LRU_TAIL_ID, 'resource-file', true)
      await user.click(screen.getByRole('button', { name: `open-${OVERFLOW_ID}` }))
      await waitFor(() => expect(terminalSessionManager.hasSession(FILL_IDS[1])).toBe(false))
      expect(terminalSessionManager.hasSession(LRU_TAIL_ID)).toBe(true)
      expect(warnSpy).not.toHaveBeenCalled()

      // 候选全带草稿：被迫淘汰 LRU 尾并 toast 警示。
      for (const id of IDS) reportInstanceDraft(id, 'resource-file', true)
      await user.click(screen.getByRole('button', { name: `open-${SECOND_OVERFLOW_ID}` }))
      await waitFor(() => expect(terminalSessionManager.hasSession(LRU_TAIL_ID)).toBe(false))
      expect(warnSpy).toHaveBeenCalledTimes(1)
      expect(String(warnSpy.mock.calls[0][0])).toContain('survival-1')
    } finally {
      warnSpy.mockRestore()
    }
  })

  it('后台闲置超时断连降级，回切自动重连并现取新 token', async () => {
    renderWithProviders(<CacheHarness />, { route: `/instances/${LRU_TAIL_ID}?tab=terminal` })
    await waitSockets(1)
    const firstToken = new URL(wsSockets[0].url).searchParams.get('token')

    // 只伪造 setTimeout/clearTimeout：闲置计时可推进，MSW/微任务不受影响。
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    fireEvent.click(screen.getByRole('button', { name: `open-${IDS[1]}` }))
    await vi.advanceTimersByTimeAsync(IDLE_DISCONNECT_MS + 1000)

    expect(terminalSessionManager.getState(LRU_TAIL_ID)).toBe('idle-disconnected')
    expect(wsSockets[0].closedByClient).toBe(true)
    vi.useRealTimers()

    // 回切实例 1：自动重连，新连接携带新签发 token（不复用已消费旧 token）。
    const socketsBefore = wsSockets.length
    fireEvent.click(screen.getByRole('button', { name: `open-${LRU_TAIL_ID}` }))
    await waitFor(() => expect(terminalSessionManager.getState(LRU_TAIL_ID)).toBe('connected'))
    expect(wsSockets.length).toBeGreaterThan(socketsBefore)
    const lastToken = new URL(wsSockets.at(-1)!.url).searchParams.get('token')
    expect(lastToken).not.toBe(firstToken)
  })
})
