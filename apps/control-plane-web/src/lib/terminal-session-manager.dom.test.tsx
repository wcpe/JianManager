import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// xterm 渲染壳已全站下线（无 xterm 依赖、管理器不持有任何渲染器）：
// 本文件的断言一律落在权威行缓冲（getLines）与 WS 状态上。

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
    serverRaw(data: string) {
      this.onmessage?.({ data } as MessageEvent)
    }
    serverError() {
      this.onerror?.(new Event('error'))
    }
  }
  const sockets: FakeWebSocket[] = []
  return { sockets, FakeWebSocket }
})

import { CONSOLE_BUFFER_LIMIT } from '@/lib/console-line-buffer'
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

describe('TerminalSessionManager 状态机（FR-295/296，FR-415 后缓冲为行缓冲）', () => {
  let manager: TerminalSessionManager

  /** 权威缓冲的整段文本（ADR-086：行缓冲取代 xterm buffer 成为唯一权威缓冲）。 */
  const bufferText = (instanceId: number) =>
    manager
      .getLines(instanceId)
      .map((line) => line.raw)
      .join('\n')

  beforeEach(() => {
    vi.useFakeTimers()
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

  it('仅 acquire + 收流即落权威行缓冲，不需要任何渲染器（ADR-086）', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()
    lastSocket().serverMessage({ type: 'stdout', data: 'hello\n' })

    // xterm 渲染壳已全站下线：这条路径的产物只有权威行缓冲，断言落在它上面。
    expect(bufferText(1)).toContain('hello')
  })

  it('重复 acquire 同一实例不重建：仍是同一条 WS，且已有缓冲不被清空', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()
    lastSocket().serverMessage({ type: 'stdout', data: 'kept-line\n' })
    const before = manager.getLines(1)

    manager.acquire(1, fetchCreds)
    await flush()

    expect(wsHarness.sockets).toHaveLength(1)
    // 引用相同 = 缓冲对象未被替换，连一次多余的重渲染都没有。
    expect(manager.getLines(1)).toBe(before)
    expect(bufferText(1)).toContain('kept-line')
  })

  it('detach 只脱钩渲染层：WS 不断、缓冲不丢、状态不变（ADR-067 核心语义）', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()
    lastSocket().serverMessage({ type: 'stdout', data: 'survives-detach\n' })

    manager.detach(1)

    expect(lastSocket().closedByClient).toBe(false)
    expect(manager.getState(1)).toBe('connected')
    expect(bufferText(1)).toContain('survives-detach')
    // detach 后继续收流：连接仍在管理器手里。
    lastSocket().serverMessage({ type: 'stdout', data: 'after-detach\n' })
    expect(bufferText(1)).toContain('after-detach')
  })

  it('stdout 输出落权威行缓冲并解析出结构化字段，输出监听器收到原始文本', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()

    const seen: string[] = []
    const unsubscribe = manager.onOutput(1, (text) => seen.push(text))
    lastSocket().serverMessage({ type: 'stdout', data: '[09:12:13] [Server thread/INFO]: hello\n' })

    const lines = manager.getLines(1)
    expect(lines).toHaveLength(1)
    expect(lines[0]).toMatchObject({ ts: '09:12:13', level: 'INFO', body: 'hello', kind: 'log' })
    expect(seen).toEqual(['[09:12:13] [Server thread/INFO]: hello\n'])

    unsubscribe()
    lastSocket().serverMessage({ type: 'stdout', data: 'again\n' })
    expect(seen).toEqual(['[09:12:13] [Server thread/INFO]: hello\n'])
  })

  it('stderr 输出按 stderr 兜底成 ERROR 级别', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()
    lastSocket().serverMessage({ type: 'stderr', data: 'boom\n' })

    expect(manager.getLines(1)[0]).toMatchObject({ level: 'ERROR', body: 'boom' })
  })

  it('subscribeLines：每次落行都通知；退订后不再通知', async () => {
    const fetchCreds = credsStub()
    const notify = vi.fn()
    // 先订阅、后 acquire：订阅登记挂在管理器上，不依赖会话已存在。
    const unsubscribe = manager.subscribeLines(1, notify)
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()
    lastSocket().serverMessage({ type: 'stdout', data: 'one\n' })
    expect(notify).toHaveBeenCalledTimes(1)

    lastSocket().serverMessage({ type: 'stdout', data: 'two\n' })
    expect(notify).toHaveBeenCalledTimes(2)

    unsubscribe()
    lastSocket().serverMessage({ type: 'stdout', data: 'three\n' })
    expect(notify).toHaveBeenCalledTimes(2)
    expect(bufferText(1)).toContain('three')
  })

  it('sendCommand：本地回显进缓冲 + 整行写 stdin（data 不补行尾，Worker 侧自己补）', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()

    expect(manager.sendCommand(1, 'say hello')).toBe(true)

    expect(manager.getLines(1).at(-1)).toMatchObject({ kind: 'command', body: 'say hello' })
    expect(lastSocket().sent).toHaveLength(1)
    expect(JSON.parse(lastSocket().sent[0])).toEqual({ type: 'stdin', instanceId: '1', data: 'say hello' })
  })

  it('sendCommand 在未连接时仍本地回显，但返回 false（不静默丢命令）', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    // 不 serverOpen：readyState 仍 CONNECTING。
    expect(manager.sendCommand(1, 'stop')).toBe(false)
    expect(manager.getLines(1).at(-1)).toMatchObject({ kind: 'command', body: 'stop' })
  })

  it('clearLines 清空权威行缓冲', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()
    lastSocket().serverMessage({ type: 'stdout', data: 'to-be-cleared\n' })

    manager.clearLines(1)

    expect(manager.getLines(1)).toEqual([])
  })

  it('getDroppedCount 反映环形缓冲溢出（输出区顶部回溯锚点据此显示）', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()
    for (let i = 0; i < CONSOLE_BUFFER_LIMIT + 5; i++) {
      lastSocket().serverMessage({ type: 'stdout', data: `line-${i}\n` })
    }

    expect(manager.getLines(1)).toHaveLength(CONSOLE_BUFFER_LIMIT)
    expect(manager.getDroppedCount(1)).toBe(5)
  })

  it('无会话时 getLines 返回稳定空引用（订阅方不会因新数组死循环重渲染）', () => {
    expect(manager.getLines(999)).toEqual([])
    expect(manager.getLines(999)).toBe(manager.getLines(998))
    expect(manager.getDroppedCount(999)).toBe(0)
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
    // 缓冲未销毁：滚动历史仍在（界面状态缓存），并追加了闲置提示。
    expect(bufferText(1)).toContain('history-line')
    expect(bufferText(1)).toContain('[闲置超时，连接已暂停，回到该服自动重连]')
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

  it('手动 reconnect：断旧连接、现取新 token 建新连接，缓冲不清', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    const first = lastSocket()
    first.serverOpen()
    first.serverMessage({ type: 'stdout', data: 'kept-across-reconnect\n' })

    manager.reconnect(1)
    await flush()

    expect(wsHarness.sockets).toHaveLength(2)
    expect(lastSocket().url).toContain('token=tok-2')
    expect(lastSocket().url).not.toContain('token=tok-1')
    expect(bufferText(1)).toContain('kept-across-reconnect')
  })

  it('dispose 整体释放：断 WS + 丢缓冲 + 会话移除', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()
    lastSocket().serverMessage({ type: 'stdout', data: 'gone-after-dispose\n' })

    manager.dispose(1)

    expect(lastSocket().closedByClient).toBe(true)
    expect(manager.hasSession(1)).toBe(false)
    expect(manager.getLines(1)).toEqual([])
  })

  it('dispose 通知仍挂载的订阅方（快照回落空数组）', async () => {
    const fetchCreds = credsStub()
    const notify = vi.fn()
    manager.acquire(1, fetchCreds)
    manager.subscribeLines(1, notify)
    await flush()
    lastSocket().serverOpen()
    notify.mockClear()

    manager.dispose(1)
    expect(notify).toHaveBeenCalledTimes(1)
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
  })

  it('token 拉取失败按退避重试，超过上限写入 [连接错误] 系统行', async () => {
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
    expect(manager.getLines(1).at(-1)).toMatchObject({ kind: 'system', raw: '[连接错误]' })
  })

  it('服务端关闭连接写入 [连接已断开] 系统行并转 closed', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    const socket = lastSocket()
    socket.serverOpen()
    socket.close()

    expect(manager.getLines(1).at(-1)).toMatchObject({ kind: 'system', raw: '[连接已断开]' })
    expect(manager.getState(1)).toBe('closed')
  })

  it('setPaused 累积输出不落缓冲，恢复后按原序一次性 flush（导播台节流语义保留）', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()

    manager.setPaused(1, true)
    lastSocket().serverMessage({ type: 'stdout', data: 'buffered-1\n' })
    lastSocket().serverMessage({ type: 'stdout', data: 'buffered-2\n' })
    expect(bufferText(1)).not.toContain('buffered-1')
    expect(manager.getLines(1)).toEqual([])

    manager.setPaused(1, false)
    expect(manager.getLines(1).map((line) => line.raw)).toEqual(['buffered-1', 'buffered-2'])
  })

  it('state 消息按诊断语义落系统行（WORKER_TOKEN_REJECTED 定向提示，FR-276 语义保留）', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()

    lastSocket().serverMessage({ type: 'state', state: 'error', code: 'WORKER_TOKEN_REJECTED', data: '密钥不一致' })
    lastSocket().serverMessage({ type: 'state', state: 'error', data: 'dial refused' })
    lastSocket().serverMessage({ type: 'state', state: 'running' })

    const text = bufferText(1)
    expect(text).toContain('[终端令牌被节点拒绝]')
    expect(text).toContain('密钥不一致')
    expect(text).toContain('[状态: error] dial refused')
    expect(text).toContain('[状态: running]')
    expect(manager.getLines(1).every((line) => line.kind === 'system')).toBe(true)
  })

  it('非 JSON 帧原样入缓冲，不丢内容', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    await flush()
    lastSocket().serverOpen()
    lastSocket().serverRaw('not json at all\n')

    expect(bufferText(1)).toContain('not json at all')
  })

  it('disposeAll 清空全部会话（登出/测试隔离）', async () => {
    const fetchCreds = credsStub()
    manager.acquire(1, fetchCreds)
    manager.acquire(2, fetchCreds)
    await flush()

    manager.disposeAll()

    expect(manager.hasSession(1)).toBe(false)
    expect(manager.hasSession(2)).toBe(false)
    expect(manager.getLines(1)).toEqual([])
    expect(manager.getLines(2)).toEqual([])
  })
})
