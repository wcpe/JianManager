import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// ---- xterm / fit 侧 mock：只记录关键调用（write/dispose/open），无真实渲染 ----
const xtermHarness = vi.hoisted(() => {
  class MockTerminal {
    options: { fontSize?: number }
    writes: string[] = []
    disposed = false
    openedInto: HTMLElement | null = null

    constructor(options: { fontSize?: number } = {}) {
      this.options = options
      instances.push(this)
    }

    loadAddon() {}
    open(container: HTMLElement) {
      this.openedInto = container
    }
    write(data: string) {
      this.writes.push(data)
    }
    dispose() {
      this.disposed = true
    }
  }
  class MockFitAddon {
    fitCalls = 0
    fit() {
      this.fitCalls++
    }
  }
  const instances: MockTerminal[] = []
  return { instances, MockTerminal, MockFitAddon }
})

// ---- WebSocket 桩：完全手动驱动 open/close/error/message，不依赖计时器 ----
const wsHarness = vi.hoisted(() => {
  class FakeWebSocket {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSED = 3
    readyState = FakeWebSocket.CONNECTING
    sent: string[] = []
    closedByClient = false
    onopen: ((event: Event) => void) | null = null
    onmessage: ((event: MessageEvent) => void) | null = null
    onclose: ((event: CloseEvent) => void) | null = null
    onerror: ((event: Event) => void) | null = null

    constructor(readonly url: string) {
      sockets.push(this)
    }

    send(data: string) {
      this.sent.push(data)
    }

    close() {
      this.closedByClient = true
      this.readyState = FakeWebSocket.CLOSED
      this.onclose?.(new CloseEvent('close'))
    }

    // 测试驱动辅助
    serverOpen() {
      this.readyState = FakeWebSocket.OPEN
      this.onopen?.(new Event('open'))
    }
    serverMessage(data: unknown) {
      this.onmessage?.({ data: JSON.stringify(data) } as MessageEvent)
    }
    serverError() {
      this.onerror?.(new Event('error'))
    }
  }
  const sockets: FakeWebSocket[] = []
  return { sockets, FakeWebSocket }
})

vi.mock('@xterm/xterm', () => ({ Terminal: xtermHarness.MockTerminal }))
vi.mock('@xterm/addon-fit', () => ({ FitAddon: xtermHarness.MockFitAddon }))

import {
  createTerminalSessionManager,
  IDLE_DISCONNECT_MS,
  type TerminalSessionManager,
} from './terminal-session-manager'

/** 递增 token 的凭据桩：可断言「重连必须现取新 token」（FR-140）。 */
function credsStub() {
  let n = 0
  const fn = vi.fn(async () => {
    n++
    return { wsUrl: 'ws://localhost/_test/terminal', token: `tok-${n}` }
  })
  return fn
}

function lastSocket() {
  return wsHarness.sockets.at(-1)!
}

/** 冲刷 fetchCreds 的微任务队列（fake timers 下 Promise 不需要走真实时钟）。 */
async function flush() {
  await vi.advanceTimersByTimeAsync(0)
}

describe('TerminalSessionManager 状态机（FR-295/296）', () => {
  let manager: TerminalSessionManager

  beforeEach(() => {
    vi.useFakeTimers()
    xtermHarness.instances.length = 0
    wsHarness.sockets.length = 0
    vi.stubGlobal('WebSocket', wsHarness.FakeWebSocket)
    manager = createTerminalSessionManager()
  })

  afterEach(() => {
    manager.disposeAll()
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('acquire 首次创建会话：现取 token 连 WS，open 后转 connected', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    expect(manager.hasSession(1)).toBe(true)
    expect(manager.getState(1)).toBe('connecting')

    await flush()
    expect(fetchCreds).toHaveBeenCalledTimes(1)
    expect(wsHarness.sockets).toHaveLength(1)
    expect(lastSocket().url).toContain('token=tok-1')

    lastSocket().serverOpen()
    expect(manager.getState(1)).toBe('connected')
  })

  it('重复 acquire 同一实例不重建：仍是同一条 WS 与同一 xterm', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()

    manager.acquire(1, fetchCreds)
    await flush()

    expect(wsHarness.sockets).toHaveLength(1)
    expect(xtermHarness.instances).toHaveLength(1)
  })

  it('attach/detach 只挂渲染不动连接：detach 后 WS 不断、term 不 dispose', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()

    const container = document.createElement('div')
    manager.attach(1, container)
    expect(xtermHarness.instances[0].openedInto).toBe(container)

    manager.detach(1)
    expect(lastSocket().closedByClient).toBe(false)
    expect(xtermHarness.instances[0].disposed).toBe(false)
    expect(manager.getState(1)).toBe('connected')
  })

  it('stdout 输出写入常驻 xterm 缓冲，输出监听器收到原始文本', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()

    const seen: string[] = []
    const unsubscribe = manager.onOutput(1, (text) => seen.push(text))
    lastSocket().serverMessage({ type: 'stdout', data: 'hello\n' })

    expect(xtermHarness.instances[0].writes.join('')).toContain('hello')
    expect(seen).toEqual(['hello\n'])

    unsubscribe()
    lastSocket().serverMessage({ type: 'stdout', data: 'again\n' })
    expect(seen).toEqual(['hello\n'])
  })

  it('markHidden 起 10 分钟闲置计时：超时断 WS 转 idle-disconnected，缓冲保留', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()
    lastSocket().serverMessage({ type: 'stdout', data: 'history-line\n' })

    manager.markHidden(1)
    await vi.advanceTimersByTimeAsync(IDLE_DISCONNECT_MS + 1)

    expect(lastSocket().closedByClient).toBe(true)
    expect(manager.getState(1)).toBe('idle-disconnected')
    // xterm 未销毁：滚动缓冲仍在（界面状态缓存）。
    expect(xtermHarness.instances[0].disposed).toBe(false)
    expect(xtermHarness.instances[0].writes.join('')).toContain('history-line')
  })

  it('闲置超时前 markVisible 取消计时：连接保持', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()

    manager.markHidden(1)
    await vi.advanceTimersByTimeAsync(IDLE_DISCONNECT_MS - 1000)
    manager.markVisible(1)
    await vi.advanceTimersByTimeAsync(IDLE_DISCONNECT_MS * 2)

    expect(lastSocket().closedByClient).toBe(false)
    expect(manager.getState(1)).toBe('connected')
  })

  it('idle-disconnected 后 markVisible 自动重连且现取新 token（FR-140）', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()

    manager.markHidden(1)
    await vi.advanceTimersByTimeAsync(IDLE_DISCONNECT_MS + 1)
    expect(manager.getState(1)).toBe('idle-disconnected')

    manager.markVisible(1)
    await flush()

    expect(wsHarness.sockets).toHaveLength(2)
    expect(lastSocket().url).toContain('token=tok-2')
    lastSocket().serverOpen()
    expect(manager.getState(1)).toBe('connected')
  })

  it('手动 reconnect：断旧连接、现取新 token 建新连接', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    const first = lastSocket()
    first.serverOpen()

    manager.reconnect(1)
    await flush()

    expect(wsHarness.sockets).toHaveLength(2)
    expect(lastSocket().url).toContain('token=tok-2')
    expect(lastSocket().url).not.toContain('token=tok-1')
  })

  it('dispose 整体释放：断 WS + dispose xterm + 会话移除', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()

    manager.dispose(1)

    expect(lastSocket().closedByClient).toBe(true)
    expect(xtermHarness.instances[0].disposed).toBe(true)
    expect(manager.hasSession(1)).toBe(false)
  })

  it('release：未 pin 即释放（独立表面卸载语义）；pin 后 release 保活', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()

    manager.pin(1)
    manager.release(1)
    expect(manager.hasSession(1)).toBe(true)

    manager.unpin(1)
    manager.release(1)
    expect(manager.hasSession(1)).toBe(false)
    expect(xtermHarness.instances[0].disposed).toBe(true)
  })

  it('token 拉取失败按退避重试，超过上限写入 [连接错误]', async () => {
    const fetchCreds = vi.fn(async () => {
      throw new Error('boom')
    })
    manager.acquire(1, fetchCreds)
    await flush()
    expect(fetchCreds).toHaveBeenCalledTimes(1)

    // 退避 1s/2s/3s 三次重试后放弃。
    await vi.advanceTimersByTimeAsync(1000)
    expect(fetchCreds).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(2000)
    expect(fetchCreds).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(3000)
    expect(fetchCreds).toHaveBeenCalledTimes(4)

    await vi.advanceTimersByTimeAsync(60_000)
    expect(fetchCreds).toHaveBeenCalledTimes(4)
    expect(xtermHarness.instances[0].writes.join('')).toContain('[连接错误]')
  })

  it('setPaused 累积输出不写 xterm，恢复后一次性 flush（导播台节流语义保留）', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()

    manager.setPaused(1, true)
    lastSocket().serverMessage({ type: 'stdout', data: 'buffered-1\n' })
    lastSocket().serverMessage({ type: 'stdout', data: 'buffered-2\n' })
    const writesWhilePaused = xtermHarness.instances[0].writes.join('')
    expect(writesWhilePaused).not.toContain('buffered-1')

    manager.setPaused(1, false)
    const flushed = xtermHarness.instances[0].writes.join('')
    expect(flushed).toContain('buffered-1')
    expect(flushed).toContain('buffered-2')
  })

  it('state 消息按诊断语义写入（WORKER_TOKEN_REJECTED 定向提示，FR-276 语义保留）', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()

    lastSocket().serverMessage({ type: 'state', state: 'error', code: 'WORKER_TOKEN_REJECTED', data: '密钥不一致' })
    lastSocket().serverMessage({ type: 'state', state: 'error', data: 'dial refused' })
    lastSocket().serverMessage({ type: 'state', state: 'running' })

    const text = xtermHarness.instances[0].writes.join('')
    expect(text).toContain('[终端令牌被节点拒绝]')
    expect(text).toContain('[状态: error] dial refused')
    expect(text).toContain('[状态: running]')
  })

  it('disposeAll 清空全部会话（登出/测试隔离）', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    manager.acquire(2, fetchCreds)
    await flush()

    manager.disposeAll()

    expect(manager.hasSession(1)).toBe(false)
    expect(manager.hasSession(2)).toBe(false)
    expect(xtermHarness.instances.every((t) => t.disposed)).toBe(true)
  })
})
