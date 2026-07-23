/**
 * 会话级 SSE 客户端（FR-372）。
 * - fetch ReadableStream 解析 event/id/data
 * - Last-Event-ID 重连、退避 1→2→5→10s
 * - 按 runId 模块级单例 + 引用计数
 * - complete / 404 永久关闭；401 可刷新 token 后重试
 */

export type SseFrame = {
  event: string
  id?: string
  data: string
  retry?: number
}

export type BotLoadEventClientStatus =
  | 'idle'
  | 'connecting'
  | 'open'
  | 'reconnecting'
  | 'closed'
  | 'error'
  | 'unavailable'

export type BotLoadEventHandler = (frame: SseFrame) => void
export type BotLoadStatusHandler = (status: BotLoadEventClientStatus, detail?: string) => void

export interface BotLoadEventClientOptions {
  runId: number | string
  url: string
  getToken: () => Promise<string | null>
  onEvent: BotLoadEventHandler
  onStatus?: BotLoadStatusHandler
  /** 自定义 fetch，便于测试注入。 */
  fetchImpl?: typeof fetch
  /** 页面是否可见；hidden 超 60s 可断开。 */
  isDocumentHidden?: () => boolean
  /** 退避序列，默认 [1000,2000,5000,10000]。 */
  backoffMs?: number[]
  /** 隐藏断开阈值（ms），默认 60000。 */
  hiddenDisconnectMs?: number
  /** 测试可关闭自动重连循环。 */
  autoReconnect?: boolean
}

/** 解析 SSE 文本缓冲，返回已完整帧与剩余缓冲。 */
export function parseSseBuffer(buffer: string): { frames: SseFrame[]; rest: string } {
  const frames: SseFrame[] = []
  // SSE 事件以空行分隔；兼容 \r\n 与 \n。
  const parts = buffer.split(/\r?\n\r?\n/)
  const rest = parts.pop() ?? ''
  for (const block of parts) {
    if (!block.trim()) continue
    let event = 'message'
    let id: string | undefined
    let retry: number | undefined
    const dataLines: string[] = []
    let hasField = false
    for (const rawLine of block.split(/\r?\n/)) {
      const line = rawLine.endsWith('\r') ? rawLine.slice(0, -1) : rawLine
      if (!line || line.startsWith(':')) continue
      if (line.startsWith('event:')) {
        event = line.slice(6).trim()
        hasField = true
        continue
      }
      if (line.startsWith('id:')) {
        id = line.slice(3).trim()
        hasField = true
        continue
      }
      if (line.startsWith('retry:')) {
        const n = Number(line.slice(6).trim())
        if (Number.isFinite(n)) retry = n
        hasField = true
        continue
      }
      if (line.startsWith('data:')) {
        // 规范允许 "data:" 后可选单空格。
        dataLines.push(line.slice(5).startsWith(' ') ? line.slice(6) : line.slice(5))
        hasField = true
      }
    }
    // 纯注释/空块不产出帧（: keep-alive）。
    if (!hasField) continue
    frames.push({ event, id, data: dataLines.join('\n'), retry })
  }
  return { frames, rest }
}

const DEFAULT_BACKOFF = [1000, 2000, 5000, 10000]

export class BotLoadEventClient {
  private readonly opts: Required<
    Pick<BotLoadEventClientOptions, 'runId' | 'url' | 'getToken' | 'onEvent'>
  > &
    BotLoadEventClientOptions
  private abort: AbortController | null = null
  private lastEventId: string | null = null
  private attempt = 0
  private status: BotLoadEventClientStatus = 'idle'
  private closedPermanently = false
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null
  private hiddenTimer: ReturnType<typeof setTimeout> | null = null
  private visibilityHandler: (() => void) | null = null

  constructor(opts: BotLoadEventClientOptions) {
    this.opts = {
      autoReconnect: true,
      backoffMs: DEFAULT_BACKOFF,
      hiddenDisconnectMs: 60_000,
      ...opts,
    }
  }

  getStatus(): BotLoadEventClientStatus {
    return this.status
  }

  getLastEventId(): string | null {
    return this.lastEventId
  }

  start(): void {
    if (this.closedPermanently) return
    this.clearReconnect()
    void this.connect()
    this.bindVisibility()
  }

  stop(): void {
    this.closedPermanently = true
    this.clearReconnect()
    this.unbindVisibility()
    this.abort?.abort()
    this.abort = null
    this.setStatus('closed')
  }

  /** 收到 complete 后调用：永久关闭。 */
  complete(): void {
    this.closedPermanently = true
    this.clearReconnect()
    this.abort?.abort()
    this.abort = null
    this.setStatus('closed')
  }

  private setStatus(status: BotLoadEventClientStatus, detail?: string) {
    this.status = status
    this.opts.onStatus?.(status, detail)
  }

  private clearReconnect() {
    if (this.reconnectTimer != null) {
      clearTimeout(this.reconnectTimer)
      this.reconnectTimer = null
    }
  }

  private scheduleReconnect(reason?: string) {
    if (this.closedPermanently || this.opts.autoReconnect === false) {
      this.setStatus('error', reason)
      return
    }
    const delays = this.opts.backoffMs ?? DEFAULT_BACKOFF
    const delay = delays[Math.min(this.attempt, delays.length - 1)] ?? 10_000
    this.attempt += 1
    this.setStatus('reconnecting', reason)
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null
      void this.connect()
    }, delay)
  }

  private async connect() {
    if (this.closedPermanently) return
    this.abort?.abort()
    this.abort = new AbortController()
    this.setStatus(this.attempt > 0 ? 'reconnecting' : 'connecting')

    try {
      const token = await this.opts.getToken()
      if (this.abort.signal.aborted || this.closedPermanently) return
      if (!token) {
        this.scheduleReconnect('no_token')
        return
      }

      const headers: Record<string, string> = {
        Authorization: `Bearer ${token}`,
        Accept: 'text/event-stream',
      }
      if (this.lastEventId) headers['Last-Event-ID'] = this.lastEventId

      const fetchImpl = this.opts.fetchImpl ?? fetch
      const resp = await fetchImpl(this.opts.url, {
        headers,
        signal: this.abort.signal,
      })

      if (resp.status === 404) {
        this.closedPermanently = true
        this.setStatus('error', 'not_found')
        return
      }
      if (resp.status === 401) {
        // 上层 getToken 应已刷新；仍 401 则退避重试。
        this.scheduleReconnect('unauthorized')
        return
      }
      if (resp.status === 503) {
        this.setStatus('unavailable', 'stream_unavailable')
        this.scheduleReconnect('unavailable')
        return
      }
      if (!resp.ok || !resp.body) {
        this.scheduleReconnect(`http_${resp.status}`)
        return
      }

      this.attempt = 0
      this.setStatus('open')
      const reader = resp.body.getReader()
      const decoder = new TextDecoder()
      let buffer = ''

      while (true) {
        const { done, value } = await reader.read()
        if (done) {
          this.scheduleReconnect('stream_end')
          break
        }
        buffer += decoder.decode(value, { stream: true })
        const { frames, rest } = parseSseBuffer(buffer)
        buffer = rest
        for (const frame of frames) {
          if (frame.id) this.lastEventId = frame.id
          if (frame.event === 'complete') {
            this.opts.onEvent(frame)
            this.complete()
            return
          }
          this.opts.onEvent(frame)
        }
      }
    } catch (err) {
      if (this.abort?.signal.aborted || this.closedPermanently) return
      this.scheduleReconnect(err instanceof Error ? err.message : 'network_error')
    }
  }

  private bindVisibility() {
    if (typeof document === 'undefined') return
    this.unbindVisibility()
    this.visibilityHandler = () => {
      const hidden = this.opts.isDocumentHidden
        ? this.opts.isDocumentHidden()
        : document.visibilityState === 'hidden'
      if (hidden) {
        if (this.hiddenTimer != null) return
        this.hiddenTimer = setTimeout(() => {
          this.hiddenTimer = null
          // 长时间隐藏：断开以省资源，可见时重连。
          this.abort?.abort()
          this.setStatus('idle', 'hidden')
        }, this.opts.hiddenDisconnectMs ?? 60_000)
      } else {
        if (this.hiddenTimer != null) {
          clearTimeout(this.hiddenTimer)
          this.hiddenTimer = null
        }
        if (!this.closedPermanently && this.status !== 'open' && this.status !== 'connecting') {
          void this.connect()
        }
      }
    }
    document.addEventListener('visibilitychange', this.visibilityHandler)
  }

  private unbindVisibility() {
    if (this.hiddenTimer != null) {
      clearTimeout(this.hiddenTimer)
      this.hiddenTimer = null
    }
    if (this.visibilityHandler && typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', this.visibilityHandler)
      this.visibilityHandler = null
    }
  }
}

type SharedEntry = {
  client: BotLoadEventClient
  refCount: number
  listeners: Set<BotLoadEventHandler>
  statusListeners: Set<BotLoadStatusHandler>
}

const shared = new Map<string, SharedEntry>()

/**
 * 按 runId 获取共享 SSE 订阅。引用计数归零时关闭连接。
 * 返回 unsubscribe。
 */
export function subscribeBotLoadRunStream(opts: {
  runId: number | string
  url: string
  getToken: () => Promise<string | null>
  onEvent: BotLoadEventHandler
  onStatus?: BotLoadStatusHandler
  fetchImpl?: typeof fetch
}): () => void {
  const key = String(opts.runId)
  let entry = shared.get(key)
  if (!entry) {
    const listeners = new Set<BotLoadEventHandler>()
    const statusListeners = new Set<BotLoadStatusHandler>()
    const client = new BotLoadEventClient({
      runId: opts.runId,
      url: opts.url,
      getToken: opts.getToken,
      fetchImpl: opts.fetchImpl,
      onEvent: (frame) => {
        for (const l of listeners) l(frame)
      },
      onStatus: (status, detail) => {
        for (const l of statusListeners) l(status, detail)
      },
    })
    entry = { client, refCount: 0, listeners, statusListeners }
    shared.set(key, entry)
    client.start()
  }
  entry.refCount += 1
  entry.listeners.add(opts.onEvent)
  if (opts.onStatus) entry.statusListeners.add(opts.onStatus)

  return () => {
    const cur = shared.get(key)
    if (!cur) return
    cur.listeners.delete(opts.onEvent)
    if (opts.onStatus) cur.statusListeners.delete(opts.onStatus)
    cur.refCount -= 1
    if (cur.refCount <= 0) {
      cur.client.stop()
      shared.delete(key)
    }
  }
}

/** 测试辅助：清空共享表。 */
export function __resetBotLoadStreamSingletonsForTest() {
  for (const e of shared.values()) e.client.stop()
  shared.clear()
}

/** 测试辅助：当前共享连接数。 */
export function __botLoadStreamSharedCountForTest(): number {
  return shared.size
}
