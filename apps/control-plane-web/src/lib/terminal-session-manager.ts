import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'

import { ConsoleLineBuffer } from './console-line-buffer'
import type { LogLine, LogStream } from './console-log-line'

/**
 * 跨服控制台热集容量**基线**（FR-296 起存在，FR-414 由写死的 3 提到 6 并改为可配）。
 *
 * 3 是「一次只看一个服」年代的数字。分屏（FR-421）与多 window（FR-420）会让多个终端
 * 同时可见，3 个坑位下打开第 4 个可见终端就会把另一个**正在被用户看着的**终端淘汰掉，
 * 眼前的日志凭空消失。故基线提到 6，并由 {@link resolveHotSetCapacity} 按实际可见数动态提升。
 *
 * 名字保留：既有引用点语义不变，只是从「唯一容量」降级为「基线」——
 * 真正生效的容量一律经 {@link resolveHotSetCapacity} 求得，不要直接拿本常量当闸。
 */
export const HOT_SET_SIZE = 6

/**
 * 热集容量硬上限（FR-414）：动态提升不得无限突破。
 *
 * 每个成员是一条常驻 WS + 一份 5000 行缓冲，容量无上限地跟着可见数走等于把
 * 「保活」变成内存泄漏。12 = 基线两倍，够覆盖可视化四 pane 工作台与常规跨服切换，
 * 又把最坏情况的缓冲占用锁在可预期范围内。
 */
export const HOT_SET_SIZE_MAX = 12

/** 容量基线覆盖的持久化键（FR-414「可配」：按机器内存自行调，跨刷新保留）。 */
const HOT_SET_BASELINE_KEY = 'console.hotSetSize'

/** 把任意输入收口到 [1, HOT_SET_SIZE_MAX]：容量为 0 意味着连当前活跃实例都保不住。 */
function clampCapacity(value: number): number {
  if (!Number.isFinite(value)) return HOT_SET_SIZE
  return Math.min(HOT_SET_SIZE_MAX, Math.max(1, Math.floor(value)))
}

function readHotSetBaselineOverride(): number | null {
  try {
    const raw = localStorage.getItem(HOT_SET_BASELINE_KEY)
    if (!raw) return null
    const parsed = Number(raw)
    return Number.isFinite(parsed) ? clampCapacity(parsed) : null
  } catch {
    // 隐私模式 / 无 localStorage 环境：回落默认基线，不影响功能。
    return null
  }
}

let hotSetBaselineOverride: number | null = readHotSetBaselineOverride()

/** 当前生效的容量基线（覆盖优先，否则 {@link HOT_SET_SIZE}）。 */
export function getHotSetBaseline(): number {
  return hotSetBaselineOverride ?? HOT_SET_SIZE
}

/** 设置容量基线覆盖（FR-414）；传 null 复位到默认。越界值按 [1, {@link HOT_SET_SIZE_MAX}] 收口。 */
export function setHotSetBaseline(value: number | null): void {
  hotSetBaselineOverride = value == null ? null : clampCapacity(value)
  try {
    if (hotSetBaselineOverride == null) localStorage.removeItem(HOT_SET_BASELINE_KEY)
    else localStorage.setItem(HOT_SET_BASELINE_KEY, String(hotSetBaselineOverride))
  } catch {
    // 隐私模式忽略：本次会话内仍生效，只是不跨刷新。
  }
}

/**
 * 生效容量 = max(基线, 实际可见终端数)，再按 {@link HOT_SET_SIZE_MAX} 收口（FR-414）。
 *
 * 「取大」而非「照用基线」是因为**淘汰一个正在被看着的终端是纯粹的 bug**：分屏下 8 个
 * pane 同时可见时容量必须至少 8，否则每挂载一个就杀掉另一个。但取大也不能一路顶穿——
 * 上限即闸，可见数再多也不会突破。
 */
export function resolveHotSetCapacity(visibleCount: number, baseline = getHotSetBaseline()): number {
  return clampCapacity(Math.max(clampCapacity(baseline), visibleCount))
}

/**
 * 后台闲置断连阈值（FR-296；FR-414 由 10 分钟收到 5 分钟）。
 *
 * 收紧是容量翻倍的对价：基线 3 时最多 2 个成员在后台闲置，基线 6 时是 5 个；阈值不动
 * 则「后台闲置连接分钟数」随容量线性膨胀（2×10 → 5×10）。折半后 5×5=25 与原先
 * 2×10=20 同量级，而 5 分钟仍远长于「切去别的服看一眼再切回来」的实际间隔，
 * 秒续体验不受影响。
 *
 * 实例控制台不可见持续超过该时长即断 WS 降级为界面状态缓存（行缓冲保留），
 * 再次可见自动重连。
 */
export const IDLE_DISCONNECT_MS = 5 * 60_000

// 重连退避参数：语义与原 Terminal.tsx 完全一致（FIX-B 防 open→立即 close 死循环）。
const MAX_RETRIES = 3
const BASE_RETRY_DELAY = 1000
// 连接存活超过此时长才视为「真正连上」并清零重试计数。
const STABLE_AFTER_MS = 2000
// 暂停渲染时的输出累积上限，超限丢最旧段（ADR-035 导播台节流）。
const PENDING_MAX = 4000

/** 无会话时的稳定空快照：getSnapshot 必须返回同一引用，否则订阅方重渲染死循环。 */
const EMPTY_LINES: readonly LogLine[] = Object.freeze([])

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

/** 暂停期累积的输出：流文本或系统提示行，恢复时按原序 flush。 */
type PendingOutput = { kind: 'stream'; text: string; stream: LogStream } | { kind: 'system'; text: string }

interface TerminalSession {
  instanceId: number
  /** 常驻行缓冲（ADR-086：取代 xterm 内部 buffer 成为保活缓冲的载体，是**唯一权威缓冲**）。 */
  buffer: ConsoleLineBuffer
  /**
   * 遗留 xterm 渲染器（`components/Terminal.tsx`），**惰性创建**：只有 {@link
   * TerminalSessionManager.attach} 被调用才 new。实例控制台已改走 DOM 输出区
   * （FR-415），永不触发这条路径，故新路径零 xterm 开销；`@xterm/*` 依赖与该渲染器
   * 一并保留，全站下线 xterm 属后续 refactor（ADR-086 代价 4）。
   */
  term: Terminal | null
  fitAddon: FitAddon | null
  /** 遗留渲染器创建时用的字号（attach 前 setFontSize 也要能记住）。 */
  legacyFontSize: number
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
  /** 暂停向缓冲落行（导播台节流，ADR-035）：输出进 pending，恢复时一次性 flush。 */
  paused: boolean
  pending: PendingOutput[]
  outputListeners: Set<(text: string) => void>
  stateListeners: Set<(state: TerminalSessionState) => void>
  disposed: boolean
  /** 连接代际：reconnect/dispose 后使在途 fetchCreds 结果作废。 */
  generation: number
}

/**
 * 终端连接管理器（FR-295，ADR-067；FR-415 起缓冲由 xterm 换成行缓冲，ADR-086）：
 * 模块级单例按 instanceId 持有终端会话（行缓冲 + WS + 状态机 + 闲置计时）。
 *
 * 组件订阅制——渲染壳 mount 时 acquire 并 subscribeLines，unmount（含 `<Activity>` 隐藏）
 * 只退订，不断 WS、不丢缓冲；行缓冲天然跨挂载保留。dispose 仅在 LRU 淘汰（FR-296）/
 * 独立表面释放 / 登出时调用。
 */
export class TerminalSessionManager {
  private sessions = new Map<number, TerminalSession>()
  /**
   * 被控制台热集 pin 住的实例（FR-296）：独立表面 release 时不释放。
   * 管理器级集合而非会话字段——宿主可在会话尚未创建（终端页签未打开）时先 pin。
   */
  private pinnedIds = new Set<number>()
  /**
   * 行变更订阅登记，**按 instanceId 挂在管理器上而非会话上**：
   * 渲染壳可能先订阅、后 acquire（`useSyncExternalStore` 的 subscribe 早于 effect），
   * 会话也可能中途被 LRU 淘汰而组件仍挂载。挂管理器才不会漏通知。
   */
  private lineListeners = new Map<number, Set<() => void>>()

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

    const session: TerminalSession = {
      instanceId,
      buffer: new ConsoleLineBuffer(),
      term: null,
      fitAddon: null,
      legacyFontSize: options?.fontSize ?? 14,
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

  /**
   * 把**遗留 xterm 渲染器**附着到容器（首挂惰性创建并回放已有缓冲，再挂只移动 DOM 节点）。
   *
   * 新的 DOM 输出区（FR-415）不走这里——它订阅行缓冲，不需要管理器持有任何 DOM。
   */
  attach(instanceId: number, container: HTMLElement): void {
    const session = this.sessions.get(instanceId)
    if (!session) return
    if (!session.term) {
      const term = new Terminal({
        cursorBlink: true,
        disableStdin: false,
        fontSize: session.legacyFontSize,
        fontFamily: 'Consolas, Monaco, monospace',
        theme: { background: '#1a1b26', foreground: '#a9b1d6', cursor: '#c0caf5' },
      })
      const fitAddon = new FitAddon()
      term.loadAddon(fitAddon)
      session.term = term
      session.fitAddon = fitAddon
      term.open(container)
      // 回放已积攒的缓冲：会话可能早于遗留渲染器存在（新控制台先连上、再被 attach）。
      const history = session.buffer.snapshot()
      if (history.length > 0) term.write(`${history.map((line) => line.raw).join('\r\n')}\r\n`)
    } else {
      const el = session.term.element
      if (el && el.parentElement !== container) container.appendChild(el)
    }
    this.fit(instanceId)
  }

  /**
   * 组件卸载（含 Activity 隐藏）时的解绑：只做渲染层脱钩，不断 WS、不丢缓冲。
   * 保留为语义锚点——ADR-067 的「卸载只 detach 渲染层」在新架构下即「退订 + 不动会话」。
   */
  detach(instanceId: number): void {
    void instanceId
  }

  /** 重排遗留 xterm 尺寸（无遗留渲染器时空转）。 */
  fit(instanceId: number): void {
    const session = this.sessions.get(instanceId)
    if (!session?.fitAddon) return
    try {
      session.fitAddon.fit()
    } catch {
      // jsdom / 离屏容器无有效尺寸时 fit 可能抛错，忽略（下次可见再 fit）。
    }
  }

  /** 运行时调整遗留 xterm 字号。新输出区的字号是纯 CSS，由组件自己拿 prop。 */
  setFontSize(instanceId: number, fontSize: number): void {
    const session = this.sessions.get(instanceId)
    if (!session) return
    session.legacyFontSize = fontSize
    if (!session.term) return
    session.term.options.fontSize = fontSize
    this.fit(instanceId)
  }

  /** 遗留 xterm 实例（仅 `components/Terminal.tsx` 使用）。 */
  getTerm(instanceId: number): Terminal | undefined {
    return this.sessions.get(instanceId)?.term ?? undefined
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
      this.writeSystem(session, '[闲置超时，连接已暂停，回到该服自动重连]')
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

  /**
   * 提交一行命令（FR-415，spec §2.2）：本地回显进缓冲 + **整行**写 stdin。
   *
   * `data` 不补行尾：Worker 侧三种启动策略写 stdin 时各自补 `\n`
   * （`direct` 用 `Fprintln`、`daemon`/`docker` 拼 `command + "\n"`），
   * 前端再补一个会给游戏服多送一条空命令。WS 消息契约（`stdin`）本身不变。
   */
  sendCommand(instanceId: number, line: string): boolean {
    const session = this.sessions.get(instanceId)
    if (!session || session.disposed) return false
    // 本地回显不等服务端：命令是否被接受由后续输出说明，回显只保证「我按下的东西看得见」。
    session.buffer.appendCommand(line)
    this.notifyLines(instanceId)
    return this.send(instanceId, { type: 'stdin', instanceId: String(instanceId), data: line })
  }

  /** 暂停/恢复向缓冲落行（导播台节流，ADR-035）：暂停期输出累积，恢复时一次性 flush。 */
  setPaused(instanceId: number, paused: boolean): void {
    const session = this.sessions.get(instanceId)
    if (!session) return
    session.paused = paused
    if (paused || session.pending.length === 0) return
    const pending = session.pending
    session.pending = []
    for (const item of pending) {
      if (item.kind === 'stream') {
        session.buffer.appendChunk(item.text, item.stream)
        session.term?.write(item.text.replace(/\r?\n/g, '\r\n'))
      } else {
        session.buffer.appendSystem(item.text)
        session.term?.write(`\r\n${item.text}\r\n`)
      }
    }
    this.notifyLines(instanceId)
  }

  /** 当前行快照（引用稳定，仅在内容变更后换新引用）。 */
  getLines(instanceId: number): readonly LogLine[] {
    return this.sessions.get(instanceId)?.buffer.snapshot() ?? EMPTY_LINES
  }

  /** 因超出缓冲上限被丢弃的行数（>0 即输出区顶部挂回溯锚点，spec §2.1）。 */
  getDroppedCount(instanceId: number): number {
    return this.sessions.get(instanceId)?.buffer.droppedCount ?? 0
  }

  /**
   * 当前历史回溯接缝（FR-419）。
   *
   * 环形缓冲未溢出时等同会话起点；溢出后改为最早保留行的接收时刻，令数据库回溯补上
   * 会话内被挤掉的日志，不在 `startedAt` 处留下空洞。
   */
  getHistoryAnchorAt(instanceId: number): string | undefined {
    return this.sessions.get(instanceId)?.buffer.historyAnchorAt.toISOString()
  }

  /** 订阅行变更（渲染壳经 `useSyncExternalStore` 消费）。 */
  subscribeLines(instanceId: number, listener: () => void): () => void {
    let listeners = this.lineListeners.get(instanceId)
    if (!listeners) {
      listeners = new Set()
      this.lineListeners.set(instanceId, listeners)
    }
    listeners.add(listener)
    return () => {
      listeners.delete(listener)
      if (listeners.size === 0) this.lineListeners.delete(instanceId)
    }
  }

  /** 清空该实例行缓冲（「清屏」）。 */
  clearLines(instanceId: number): void {
    const session = this.sessions.get(instanceId)
    if (!session) return
    session.buffer.clear()
    session.term?.clear()
    this.notifyLines(instanceId)
  }

  getState(instanceId: number): TerminalSessionState | undefined {
    return this.sessions.get(instanceId)?.state
  }

  /**
   * 当前标记为可见的会话数（FR-414）：热集容量动态提升的输入。
   *
   * 取自既有的 {@link markVisible} / {@link markHidden} 信号而非新造一套登记——
   * 那两个方法本就是「这个终端此刻在不在用户眼前」的权威来源，分屏（FR-421）只需
   * 对多个实例调 markVisible，容量便自动跟上。
   */
  getVisibleCount(): number {
    let count = 0
    for (const session of this.sessions.values()) {
      if (!session.hidden && !session.disposed) count++
    }
    return count
  }

  hasSession(instanceId: number): boolean {
    return this.sessions.has(instanceId)
  }

  /** 订阅 stdout/stderr 原始文本（组件侧的额外解析消费）。 */
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

  /** 整体释放该实例会话：断 WS + 丢缓冲（LRU 淘汰 / 独立表面释放 / 登出）。 */
  dispose(instanceId: number): void {
    this.pinnedIds.delete(instanceId)
    const session = this.sessions.get(instanceId)
    if (!session) return
    session.disposed = true
    this.clearTimers(session)
    const ws = session.ws
    session.ws = null
    ws?.close()
    session.term?.dispose()
    session.term = null
    session.fitAddon = null
    session.outputListeners.clear()
    session.stateListeners.clear()
    this.sessions.delete(instanceId)
    // 通知仍挂载的渲染壳：会话已消失，快照回落空数组。
    this.notifyLines(instanceId)
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

  private notifyLines(instanceId: number): void {
    const listeners = this.lineListeners.get(instanceId)
    if (!listeners) return
    for (const listener of listeners) listener()
  }

  /** 流输出落缓冲（暂停时进 pending）。 */
  private writeStream(session: TerminalSession, text: string, stream: LogStream): void {
    if (session.paused) {
      session.pending.push({ kind: 'stream', text, stream })
      if (session.pending.length > PENDING_MAX) session.pending.shift()
      return
    }
    session.buffer.appendChunk(text, stream)
    // xterm 只认 CRLF 换行，LF 单独出现不会回到行首。
    session.term?.write(text.replace(/\r?\n/g, '\r\n'))
    this.notifyLines(session.instanceId)
  }

  /** 系统提示行落缓冲（连接提示 / state 诊断）。 */
  private writeSystem(session: TerminalSession, text: string): void {
    if (session.paused) {
      session.pending.push({ kind: 'system', text })
      if (session.pending.length > PENDING_MAX) session.pending.shift()
      return
    }
    session.buffer.appendSystem(text)
    session.term?.write(`\r\n${text}\r\n`)
    this.notifyLines(session.instanceId)
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
      this.writeSystem(session, '[连接已断开]')
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
      this.writeSystem(session, '[连接错误]')
      this.setState(session, 'closed')
    }
  }

  /** 消息处理：语义从原实现原样迁入（含 FR-276 诊断渲染），只把落点从 xterm 换成行缓冲。 */
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
        this.writeStream(session, text, msg.type === 'stderr' ? 'stderr' : 'stdout')
        for (const listener of session.outputListeners) listener(text)
      } else if (msg.type === 'state') {
        // 错误态渲染后端诊断（FR-276，见 ADR-061）：WORKER_TOKEN_REJECTED 定向提示，
        // 一般错误带出 data 原因，不丢诊断信息。
        let line: string
        if (msg.state === 'error' && msg.code === 'WORKER_TOKEN_REJECTED') {
          line = `\u001b[1;31m[终端令牌被节点拒绝]\u001b[0m ${String(msg.data ?? '')}`
        } else if (msg.state === 'error' && msg.data) {
          line = `[状态: ${msg.state}] ${String(msg.data)}`
        } else {
          line = `[状态: ${msg.state}]`
        }
        this.writeSystem(session, line)
      }
    } catch {
      // 非 JSON 帧：原样入缓冲，不丢内容（诊断价值大于格式洁癖）。
      session.buffer.appendChunk(String(event.data), 'stdout')
      session.term?.write(String(event.data))
      this.notifyLines(session.instanceId)
    }
  }
}

/** 全局单例：应用运行期唯一的终端会话持有者（ADR-067）。 */
export const terminalSessionManager = new TerminalSessionManager()

/** 测试用工厂：创建相互隔离的管理器实例。 */
export function createTerminalSessionManager(): TerminalSessionManager {
  return new TerminalSessionManager()
}
