import { describe, expect, it, vi } from 'vitest'
import { LiveLogError, parseLiveSSE, subscribeLiveLogs } from './logLive'

vi.mock('./client', () => ({ ensureFreshToken: vi.fn(async () => 'bearer-secret'), refreshAccessToken: vi.fn(async () => 'refreshed-secret') }))

function bytes(text: string, stride = 1) {
  const raw = new TextEncoder().encode(text)
  return new ReadableStream<Uint8Array>({ start(controller) {
    for (let i = 0; i < raw.length; i += stride) controller.enqueue(raw.slice(i, i + stride))
    controller.close()
  } })
}

describe('native log SSE decoder', () => {
  it('handles UTF-8 split bytes, CRLF split chunks, comments and multiline data', async () => {
    const frames: unknown[] = []
    const body = bytes(': connected\r\n\r\nevent: ready\r\ndata: {"type":"ready",\r\ndata: "partial":true}\r\n\r\nevent: event\r\ndata: {"type":"event","event":{"_msg":"中文日志"}}\r\n\r\n')
    await expect(parseLiveSSE(body, (frame) => frames.push(frame))).rejects.toMatchObject({ code: 'STREAM_CLOSED' })
    expect(frames).toEqual([{ type: 'ready', partial: true }, { type: 'event', event: { _msg: '中文日志' } }])
  })
  it('delivers a terminal error then stops with the same code', async () => {
    const seen = vi.fn()
    await expect(parseLiveSSE(bytes('event: error\ndata: {"type":"error","code":"FORBIDDEN"}\n\n'), seen)).rejects.toMatchObject({ code: 'FORBIDDEN', retryable: false })
    expect(seen).toHaveBeenCalledOnce()
  })
  it('refuses malformed JSON and an unbounded unterminated frame', async () => {
    await expect(parseLiveSSE(bytes('data: {x}\n\n'), () => {})).rejects.toMatchObject({ code: 'INVALID_STREAM' })
    await expect(parseLiveSSE(bytes('data: ' + 'x'.repeat(100 * 1024), 1024), () => {})).rejects.toMatchObject({ code: 'BUDGET_EXCEEDED' })
  })
  it('cancel releases a pending read instead of waiting for the next log', async () => {
    const closed = vi.fn()
    const body = new ReadableStream<Uint8Array>({ cancel: closed })
    const controller = new AbortController()
    const pending = parseLiveSSE(body, () => {}, controller.signal)
    controller.abort()
    await expect(pending).rejects.toMatchObject({ name: 'AbortError' })
    expect(closed).toHaveBeenCalledOnce()
  })
  it('keeps a callback failure, does not silently swallow consumer errors', async () => {
    await expect(parseLiveSSE(bytes('event: heartbeat\ndata: {"type":"heartbeat"}\n\n'), () => { throw new Error('slow UI') })).rejects.toThrow('slow UI')
  })
})

describe('native log fetch contract', () => {
  it('sends bearer header, removes historical identity and refreshes once on 401', async () => {
    const fetcher = vi.fn()
      .mockResolvedValueOnce(new Response('', { status: 401 }))
      .mockResolvedValueOnce(new Response(bytes('event: ready\ndata: {"type":"ready"}\n\n'), { headers: { 'Content-Type': 'text/event-stream' } }))
    vi.stubGlobal('fetch', fetcher)
    try {
      await expect(subscribeLiveLogs({ from: 'now', to: 'old', cursor: 'old', viewId: 'old', token: 'do-not-leak', source: 'worker', logsQL: 'error', advanced: true }, { signal: new AbortController().signal, onFrame: () => {} })).rejects.toMatchObject({ code: 'STREAM_CLOSED' })
      expect(fetcher).toHaveBeenCalledTimes(2)
      expect(fetcher.mock.calls[0][0]).not.toMatch(/old|do-not-leak/)
      expect(fetcher.mock.calls[0][0]).toContain('source=instance')
      expect(fetcher.mock.calls[1][1].headers.Authorization).toBe('Bearer refreshed-secret')
    } finally { vi.unstubAllGlobals() }
  })
  it.each([404, 405, 501])('reports an older CP without a stream route (%i) as unsupported', async (status) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: 'NOT_IMPLEMENTED' }), { status, headers: { 'Content-Type': 'application/json' } })))
    try {
      await expect(subscribeLiveLogs({}, { signal: new AbortController().signal, onFrame: () => {} })).rejects.toMatchObject({ code: 'UNSUPPORTED', retryable: false, status })
    } finally { vi.unstubAllGlobals() }
  })
  it('marks resource limits permanent and transient disconnect retryable', () => {
    expect(new LiveLogError('BUDGET_EXCEEDED', 'stop').retryable).toBe(false)
    expect(new LiveLogError('SESSION_EXPIRED', 'reconnect').retryable).toBe(true)
  })
})
