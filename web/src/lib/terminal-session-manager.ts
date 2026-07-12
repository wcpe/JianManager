import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'

/**
 * 跨服控制台热集容量（FR-296）：最近打开的 ≤N 个服控制台整体保活（LRU 淘汰）。
 * 集中可调：内存/WS 压力超预期时只改这里（见 ADR-067 / spec §6）。
 */
export const HOT_SET_SIZE = 3

/**
 * 后台闲置断连阈值（FR-296）：实例控制台不可见持续超过该时长即断 WS
 * 降级为界面状态缓存（xterm 缓冲保留），再次可见自动重连。
 */
export const IDLE_DISCONNECT_MS = 10 * 60_000

// 重连退避参数：语义与原 Terminal.tsx 完全一致（FIX-B 防 open→立即 close 死循环）。
const MAX_RETRIES = 3
const BASE_RETRY_DELAY = 1000
// 连接存活超过此时长才视为「真正连上」并清零重试计数。
const STABLE_AFTER_MS = 2000
// 暂停渲染时的输出累积上限（约对应 xterm scrollback），超限丢最旧段（ADR-035 导播台节流）。
const PENDING_MAX = 4000

/**
 * 会话状态机（ADR-067）：
 * connecting →（open）connected →（闲置超时）idle-disconnected →（回切）connecting；
 * closed = 断开且未在闲置降级（服务端关闭 / 重试耗尽），acquire/reconnect 可再拉起。
 */
export type TerminalSessionState = 'connecting' | 'connected' | 'idle-disconnected' | 'closed'

/** 一次性终端连接凭据（FR-140：每次连接前现取，token 首连即被 CP 消费失效）。 */
export interface TerminalCreds {
  wsUrl: string
  token: string
}

export type FetchTerminalCreds = () => Promise<TerminalCreds>

interface TerminalSession {
  instanceId: number
  term: Terminal
  fitAddon: FitAddon
  /** term.open 只能调用一次；之后 re-attach 走移动 DOM 节点。 */
  opened: boolean
  ws: WebSocket | null
  state: TerminalSessionState
  fetchCreds: FetchTerminalCreds
  retryCount: number
  retryTimer: ReturnType<typeof setTimeout> | null
  stableTimer: ReturnType<typeof setTimeout> | null
  idleTimer: ReturnType<typeof setTimeout> | null
  /** 实例控制台整体是否不可见（FR-296 热集 hidden 成员）。 */
  hidden: boolean
  lastVisibleAt: number
  /** 暂停 xterm 重绘（导播台节流，ADR-035）：输出进 pending，恢复时 flush。 */
  paused: boolean
  pending: string[]
  outputListeners: Set<(text: string) => void>
  stateListeners: Set<(state: TerminalSessionState) => void>
  disposed: boolean
  /** 连接代际：reconnect/dispose 后使在途 fetchCreds 结果作废。 */
  generation: number
}

/**
 * 终端连接管理器（FR-295，ADR-067）：模块级单例按 instanceId 持有终端会话
 * （xterm 实例 + WS + 状态机 + 闲置计时）。组件订阅制——`Terminal.tsx` 等渲染壳
 * mount 时 acquire+attach、unmount（含 `<Activity>` 隐藏）只 detach，不断 WS、
 * 不 dispose xterm；xterm 内部 buffer 即滚动缓冲，天然跨挂载保留。
 * dispose 仅在 LRU 淘汰（FR-296）/ 独立表面释放 / 登出时调用。
 */
export class TerminalSessionManager {
  private sessions = new Map<number, TerminalSession>()
  /**
   * 被控制台热集 pin 住的实例（FR-296）：独立表面 release 时不释放。
   * 管理器级集合而非会话字段——宿主可在会话尚未创建（终端页签未打开）时先 pin。
   */
  private pinnedIds = new Set<number>()

  /**
   * 获取（或创建）实例终端会话。首次创建即现取 token 连 WS；
   * 已有会话仅更新 fetchCreds（token 拉取闭包可能随组件重建而更新），
   * 若会话处于 idle-disconnected/closed 则自动拉起重连。
   */
  acquire(instanceId: number, fetchCreds: FetchTerminalCreds, options?: { fontSize?: number }): void {
    const existing = this.sessions.get(instanceId)
    if (existing) {
      existing.fetchCreds = fetchCreds
      if ((existing.state === 'idle-disconnected' || existing.state === 'closed') && !existing.retryTimer) {
        existing.retryCount = 0
        void this.connect(existing)
      }
      return
    }

    const term = new Terminal({
      cursorBlink: true,
      disableStdin: false,
      fontSize: options?.fontSize ?? 14,
      fontFamily: 'Consolas, Monaco, monospace',
      theme: { background: '#1a1b26', foreground: '#a9b1d6', cursor: '#c0caf5' },
    })
    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)

    const session: TerminalSession = {
      instanceId,
      term,
      fitAddon,
      opened: false,
      ws: null,
      state: 'connecting',
      fetchCreds,
      retryCount: 0,
      retryTimer: null,
      stableTimer: null,
      idleTimer: null,
      hidden: false,
      lastVisibleAt: Date.now(),
      paused: false,
      pending: [],
      outputListeners: new Set(),
      stateListeners: new Set(),
      disposed: false,
      generation: 0,
    }
    this.sessions.set(instanceId, session)
    void this.connect(session)
  }

  /** 把常驻 xterm 附着到组件容器（首挂 open、再挂移动 DOM 节点）并 fit。 */
  attach(instanceId: number, container: HTMLElement): void {
    const session = this.sessions.get(instanceId)
    if (!session) return
    if (!session.opened) {
      session.term.open(container)
      session.opened = true
    } else {
      const el = session.term.element
      if (el && el.parentElement !== container) container.appendChild(el)
    }
    this.fit(instanceId)
  }

  /**
   * 组件卸载（含 Activity 隐藏）时的解绑：只做渲染层脱钩，不断 WS、不销毁 xterm。
   * DOM 节点由 Activity 保留（隐藏）或随组件树移除（真卸载），xterm buffer 均不受影响。
   */
  detach(instanceId: number): void {
    // 连接与缓冲全部常驻管理器，渲染壳脱钩无需额外动作；保留方法作为语义锚点。
    void instanceId
  }

  /** 独立表面（画布卡片等非 keep-alive 宿主）卸载语义：未被热集 pin 时立即整体释放。 */
  release(instanceId: number): void {
    if (this.pinnedIds.has(instanceId)) return
    this.dispose(instanceId)
  }

  /** 热集成员标记（FR-296 缓存宿主调用）：pin 期间独立表面的 release 不释放会话；可先于会话创建。 */
  pin(instanceId: number): void {
    this.pinnedIds.add(instanceId)
  }

  unpin(instanceId: number): void {
    this.pinnedIds.delete(instanceId)
  }

  /** 实例控制台转入后台（热集 hidden 成员）：起闲置断连计时。 */
  markHidden(instanceId: number): void {
    const session = this.sessions.get(instanceId)
    if (!session || session.disposed) return
    session.hidden = true
    if (session.idleTimer) clearTimeout(session.idleTimer)
    if (session.state === 'idle-disconnected' || session.state === 'closed') return
    session.idleTimer = setTimeout(() => {
      session.idleTimer = null
      if (session.disposed || !session.hidden) return
      // 主动断连：先摘 ws 引用再 close，使 onclose 识别为「被管理器取代」不写断连提示。
      const ws = session.ws
      session.ws = null
      if (session.retryTimer) {
        clearTimeout(session.retryTimer)
        session.retryTimer = null
      }
      this.setState(session, 'idle-disconnected')
      ws?.close()
      this.writeOutput(session, '\r\n[闲置超时，连接已暂停，回到该服自动重连]\r\n')
    }, IDLE_DISCONNECT_MS)
  }

  /** 实例控制台回到前台：取消闲置计时；若已闲置断连则自动重连（现取新 token）。 */
  markVisible(instanceId: number): void {
    const session = this.sessions.get(instanceId)
    if (!session || session.disposed) return
    session.hidden = false
    session.lastVisibleAt = Date.now()
    if (session.idleTimer) {
      clearTimeout(session.idleTimer)
      session.idleTimer = null
    }
    if (session.state === 'idle-disconnected') {
      session.retryCount = 0
      void this.connect(session)
    }
  }

  /** 手动重连（终端工具栏「重新连接」）：断旧连接、清零退避、现取新 token 重建。 */
  reconnect(instanceId: number): void {
    const session = this.sessions.get(instanceId)
    if (!session || session.disposed) return
    session.retryCount = 0
    this.clearTimers(session)
    const ws = session.ws
    session.ws = null
    ws?.close()
    void this.connect(session)
  }

  /** 向该实例终端 WS 发送 JSON 消息；未连接时返回 false。 */
  send(instanceId: number, payload: Record<string, unknown>): boolean {
    const session = this.sessions.get(instanceId)
    const ws = session?.ws
    if (!ws || ws.readyState !== WebSocket.OPEN) return false
    ws.send(JSON.stringify(payload))
    return true
  }

  /** 暂停/恢复 xterm 重绘（导播台节流，ADR-035）：暂停期输出累积，恢复时一次性 flush。 */
  setPaused(instanceId: number, paused: boolean): void {
    const session = this.sessions.get(instanceId)
    if (!session) return
    session.paused = paused
    if (!paused && session.pending.length > 0) {
      for (const chunk of session.pending) session.term.write(chunk)
      session.pending = []
    }
  }

  /** 运行时调整字号（不再重建终端/重连）。 */
  setFontSize(instanceId: number, fontSize: number): void {
    const session = this.sessions.get(instanceId)
    if (!session) return
    session.term.options.fontSize = fontSize
    this.fit(instanceId)
  }

  /** 重排终端尺寸（重新可见 / 容器 resize 后必须补一次，离屏期间容器尺寸为 0）。 */
  fit(instanceId: number): void {
    const session = this.sessions.get(instanceId)
    if (!session || !session.opened) return
    try {
      session.fitAddon.fit()
    } catch {
      // jsdom / 离屏容器无有效尺寸时 fit 可能抛错，忽略（下次可见再 fit）。
    }
  }

  getTerm(instanceId: number): Terminal | undefined {
    return this.sessions.get(instanceId)?.term
  }

  getState(instanceId: number): TerminalSessionState | undefined {
    return this.sessions.get(instanceId)?.state
  }

  hasSession(instanceId: number): boolean {
    return this.sessions.has(instanceId)
  }

  /** 订阅 stdout/stderr 原始文本（玩家名解析、搜索联动等组件侧消费）。 */
  onOutput(instanceId: number, listener: (text: string) => void): () => void {
    const session = this.sessions.get(instanceId)
    if (!session) return () => {}
    session.outputListeners.add(listener)
    return () => session.outputListeners.delete(listener)
  }

  /** 订阅会话状态变化（渲染壳按需展示连接态）。 */
  onStateChange(instanceId: number, listener: (state: TerminalSessionState) => void): () => void {
    const session = this.sessions.get(instanceId)
    if (!session) return () => {}
    session.stateListeners.add(listener)
    return () => session.stateListeners.delete(listener)
  }

  /** 整体释放该实例会话：断 WS + dispose xterm（LRU 淘汰 / 独立表面释放 / 登出）。 */
  dispose(instanceId: number): void {
    this.pinnedIds.delete(instanceId)
    const session = this.sessions.get(instanceId)
    if (!session) return
    session.disposed = true
    this.clearTimers(session)
    const ws = session.ws
    session.ws = null
    ws?.close()
    session.term.dispose()
    session.outputListeners.clear()
    session.stateListeners.clear()
    this.sessions.delete(instanceId)
  }

  /** 释放全部会话（登出 / 测试隔离），并清空热集 pin 登记。 */
  disposeAll(): void {
    for (const instanceId of [...this.sessions.keys()]) this.dispose(instanceId)
    this.pinnedIds.clear()
  }

  // ---- 内部 ----

  private setState(session: TerminalSession, state: TerminalSessionState): void {
    if (session.state === state) return
    session.state = state
    for (const listener of session.stateListeners) listener(state)
  }

  /** 输出写入（暂停时进 pending）：连接提示 / state 行 / stdout 均经此路径。 */
  private writeOutput(session: TerminalSession, out: string): void {
    if (session.paused) {
      session.pending.push(out)
      if (session.pending.length > PENDING_MAX) session.pending.shift()
    } else {
      session.term.write(out)
    }
  }

  private clearTimers(session: TerminalSession): void {
    if (session.retryTimer) {
      clearTimeout(session.retryTimer)
      session.retryTimer = null
    }
    if (session.stableTimer) {
      clearTimeout(session.stableTimer)
      session.stableTimer = null
    }
    if (session.idleTimer) {
      clearTimeout(session.idleTimer)
      session.idleTimer = null
    }
  }

  /**
   * 建立连接：每次都现取一次性凭据（FR-140——token 首连即被消费，复用必 401），
   * 失败按 1s/2s/3s 退避重试至 MAX_RETRIES（连接存活满 STABLE_AFTER_MS 才清零计数，
   * 防 open→立即 close 反复清零绕过上限的死循环刷断连，FIX-B）。
   */
  private async connect(session: TerminalSession): Promise<void> {
    if (session.disposed) return
    const generation = ++session.generation
    this.setState(session, 'connecting')

    let creds: TerminalCreds
    try {
      creds = await session.fetchCreds()
    } catch {
      if (session.disposed || generation !== session.generation) return
      this.scheduleRetry(session)
      return
    }
    if (session.disposed || generation !== session.generation) return

    const ws = new WebSocket(`${creds.wsUrl}?token=${creds.token}`)
    session.ws = ws

    ws.onopen = () => {
      if (session.ws !== ws) return
      if (session.stableTimer) clearTimeout(session.stableTimer)
      session.stableTimer = setTimeout(() => {
        session.retryCount = 0
      }, STABLE_AFTER_MS)
      this.setState(session, 'connected')
    }

    ws.onmessage = (event) => {
      if (session.ws !== ws) return
      this.handleMessage(session, event)
    }

    ws.onclose = () => {
      if (session.stableTimer) {
        clearTimeout(session.stableTimer)
        session.stableTimer = null
      }
      // 被管理器主动取代（闲置断连 / 手动重连 / dispose）：不写断连提示、不改状态。
      if (session.ws !== ws) return
      session.ws = null
      this.writeOutput(session, '\r\n[连接已断开]\r\n')
      this.setState(session, 'closed')
    }

    ws.onerror = () => {
      if (session.ws !== ws) return
      this.scheduleRetry(session)
    }
  }

  private scheduleRetry(session: TerminalSession): void {
    if (session.stableTimer) {
      clearTimeout(session.stableTimer)
      session.stableTimer = null
    }
    if (session.disposed) return
    if (session.retryCount < MAX_RETRIES) {
      session.retryCount++
      session.retryTimer = setTimeout(() => {
        session.retryTimer = null
        if (!session.disposed) void this.connect(session)
      }, BASE_RETRY_DELAY * session.retryCount)
    } else {
      this.writeOutput(session, '\r\n[连接错误]\r\n')
      this.setState(session, 'closed')
    }
  }

  /** 消息处理：语义从原 Terminal.tsx onmessage 原样迁入（含 FR-276 诊断渲染）。 */
  private handleMessage(session: TerminalSession, event: MessageEvent): void {
    try {
      const msg = JSON.parse(String(event.data)) as {
        type?: string
        data?: unknown
        state?: string
        code?: string
      }
      if (msg.type === 'stdout' || msg.type === 'stderr') {
        const text = String(msg.data ?? '')
        this.writeOutput(session, text.replace(/\r?\n/g, '\r\n'))
        for (const listener of session.outputListeners) listener(text)
      } else if (msg.type === 'state') {
        // 错误态渲染后端诊断（FR-276，见 ADR-061）：WORKER_TOKEN_REJECTED 定向提示，
        // 一般错误带出 data 原因，不丢诊断信息。
        let line: string
        if (msg.state === 'error' && msg.code === 'WORKER_TOKEN_REJECTED') {
          line = `\r\n\x1b[1;31m[终端令牌被节点拒绝]\x1b[0m ${String(msg.data ?? '')}\r\n`
        } else if (msg.state === 'error' && msg.data) {
          line = `\r\n[状态: ${msg.state}] ${String(msg.data)}\r\n`
        } else {
          line = `\r\n[状态: ${msg.state}]\r\n`
        }
        this.writeOutput(session, line)
      }
    } catch {
      session.term.write(String(event.data))
    }
  }
}

/** 全局单例：应用运行期唯一的终端会话持有者（ADR-067）。 */
export const terminalSessionManager = new TerminalSessionManager()

/** 测试用工厂：创建相互隔离的管理器实例。 */
export function createTerminalSessionManager(): TerminalSessionManager {
  return new TerminalSessionManager()
}
