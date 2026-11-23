import { ensureFreshToken, refreshAccessToken } from './client'

export interface LiveLogFrame {
  type: 'ready' | 'event' | 'heartbeat' | 'error'
  event?: Record<string, unknown>
  code?: string
  message?: string
  viewId?: string
  partial?: boolean
  replayWindowSeconds?: number
}

const permanentCodes = new Set(['UNSUPPORTED', 'FORBIDDEN', 'SLOW_CONSUMER', 'BUDGET_EXCEEDED', 'INVALID_REQUEST'])
export class LiveLogError extends Error {
  readonly retryable: boolean
  readonly code: string
  readonly status?: number
  constructor(code: string, message: string, status?: number) {
    super(message)
    this.name = 'LiveLogError'
    this.code = code
    this.status = status
    this.retryable = !permanentCodes.has(code) && status !== 401 && status !== 403
  }
}

const MAX_FRAME_CHARS = 96 * 1024
const frameKinds = new Set(['ready', 'event', 'heartbeat', 'error'])

/** One SSE record, with comments ignored and multi-line data preserved. */
function decodeRecord(record: string): LiveLogFrame | null {
  let event = ''
  const data: string[] = []
  for (const line of record.split('\n')) {
    if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) data.push(line.slice(5).replace(/^ /, ''))
  }
  if (!data.length) return null
  let value: unknown
  try { value = JSON.parse(data.join('\n')) } catch {
    throw new LiveLogError('INVALID_STREAM', '实时日志流包含无效 JSON')
  }
  if (!value || typeof value !== 'object') throw new LiveLogError('INVALID_STREAM', '无效的日志事件')
  const frame = value as LiveLogFrame
  if (!frameKinds.has(frame.type) || (event && frame.type !== event)) {
    throw new LiveLogError('INVALID_STREAM', '未知的日志事件类型')
  }
  if (frame.type === 'event' && (!frame.event || typeof frame.event !== 'object' || Array.isArray(frame.event))) {
    throw new LiveLogError('INVALID_STREAM', '日志事件缺少正文')
  }
  return frame
}

/** Decode UTF-8/chunked SSE without splitting Chinese text or retaining an
 * unbounded partial frame. This is a single-connection primitive, not polling. */
export async function parseLiveSSE(
  body: ReadableStream<Uint8Array>,
  onFrame: (frame: LiveLogFrame) => void,
  signal?: AbortSignal,
): Promise<void> {
  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buffered = ''
  let pendingCR = false
  const aborted = () => { void reader.cancel().catch(() => {}) }
  signal?.addEventListener('abort', aborted, { once: true })
  const consume = (text: string, final: boolean) => {
    if (pendingCR) { text = '\r' + text; pendingCR = false }
    if (!final && text.endsWith('\r')) { pendingCR = true; text = text.slice(0, -1) }
    buffered += text.replace(/\r\n|\r/g, '\n')
    let boundary = buffered.indexOf('\n\n')
    while (boundary !== -1) {
      const record = buffered.slice(0, boundary)
      buffered = buffered.slice(boundary + 2)
      if (record.length > MAX_FRAME_CHARS) throw new LiveLogError('BUDGET_EXCEEDED', '实时日志事件超过大小限制')
      const frame = decodeRecord(record)
      if (frame) {
        onFrame(frame)
        if (frame.type === 'error') throw new LiveLogError(frame.code ?? 'STREAM_UNAVAILABLE', frame.message ?? '实时日志连接失败')
      }
      boundary = buffered.indexOf('\n\n')
    }
    if (buffered.length > MAX_FRAME_CHARS) throw new LiveLogError('BUDGET_EXCEEDED', '实时日志事件超过大小限制')
  }
  try {
    if (signal?.aborted) throw new DOMException('已取消', 'AbortError')
    for (;;) {
      const { value, done } = await reader.read()
      if (signal?.aborted) throw new DOMException('已取消', 'AbortError')
      if (done) { consume(decoder.decode(), true); break }
      consume(decoder.decode(value, { stream: true }), false)
    }
    // An incomplete last record is not delivered as a successful event.
    throw new LiveLogError('STREAM_CLOSED', '实时日志连接已断开；重新连接仅有限回放，可能缺行')
  } finally {
    signal?.removeEventListener('abort', aborted)
    await reader.cancel().catch(() => {})
    reader.releaseLock()
  }
}

export async function subscribeLiveLogs(
  params: Record<string, unknown>,
  options: { signal: AbortSignal; onFrame: (frame: LiveLogFrame) => void },
): Promise<void> {
  const query = new URLSearchParams()
  // A native subscription has an open end and its own view identity. A historical
  // end/cursor/view must never get silently reused as a live query identity.
  const omitted = new Set(['to', 'viewId', 'cursor', 'page', 'pageSize', 'tailMode', 'limit', 'token', 'accessToken'])
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== null && !omitted.has(key)) query.set(key, String(value))
  }
  query.set('source', 'instance')
  let token = await ensureFreshToken()
  const connect = () => fetch(`/api/v1/logs/federation/live?${query}`, {
    method: 'GET', signal: options.signal, credentials: 'same-origin',
    headers: { Accept: 'text/event-stream', ...(token ? { Authorization: `Bearer ${token}` } : {}) },
  })
  let response = await connect()
  if (response.status === 401 && !options.signal.aborted) {
    await response.body?.cancel()
    try { token = await refreshAccessToken() } catch { throw new LiveLogError('FORBIDDEN', '登录已失效，请重新登录', 401) }
    response = await connect()
  }
  if (!response.ok) {
    let code = response.status === 403 || response.status === 401 ? 'FORBIDDEN'
      : [404, 405, 501].includes(response.status) ? 'UNSUPPORTED'
        : response.status === 429 ? 'RATE_LIMITED' : 'STREAM_UNAVAILABLE'
    let message = `日志连接失败 (${response.status})`
    try {
      const value = await response.json() as { error?: string; message?: string }
      if (code !== 'UNSUPPORTED') code = value.error ?? code
      message = value.message ?? message
    } catch { /* preserve HTTP status on non-JSON failures */ }
    throw new LiveLogError(code, message, response.status)
  }
  if (!response.body || !response.headers.get('Content-Type')?.startsWith('text/event-stream')) {
    await response.body?.cancel()
    throw new LiveLogError('UNSUPPORTED', '服务器不支持实时日志流')
  }
  await parseLiveSSE(response.body, options.onFrame, options.signal)
}
