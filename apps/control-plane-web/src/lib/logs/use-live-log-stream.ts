import { useEffect, useRef, useState } from 'react'

export interface LiveFrame {
  type: 'ready' | 'event' | 'heartbeat' | 'error'
  event?: Record<string, unknown>
  code?: string
  message?: string
  viewId?: string
  partial?: boolean
  replayWindowSeconds?: number
}
export type LiveSubscriber = (params: Record<string, unknown>, options: { signal: AbortSignal; onFrame: (frame: LiveFrame) => void }) => Promise<void>
export interface LiveStreamState {
  identity: string
  items: Record<string, unknown>[]
  status: 'idle' | 'connecting' | 'live' | 'reconnecting' | 'paused' | 'fallback' | 'error'
  code?: string
  message?: string
  partial?: boolean
}

/** Bounded display buffer: batches rendering, deduplicates the short replay window, never stores all history. */
export function appendLiveEvent(items: Record<string, unknown>[], event: Record<string, unknown>, maxRows = 100): Record<string, unknown>[] {
  const keyOf = (row: Record<string, unknown>) => `${String(row.log_source_id ?? row.source ?? '')}\t${String(row.event_id ?? '')}\t${String(row.event_time_utc ?? row._time ?? row.time ?? '')}\t${String(row.message ?? row._msg ?? '')}`
  const key = keyOf(event)
  if (items.some((row) => keyOf(row) === key)) return items
  return [event, ...items].slice(0, maxRows)
}

/** Each effect owns its connection, retry timer and render timer; old query callbacks cannot reach a new scope. */
export function useLiveLogStream(params: Record<string, unknown>, enabled: boolean, paused: boolean, subscribe: LiveSubscriber) {
  const identity = JSON.stringify(params)
  const [state, setState] = useState<LiveStreamState>({ identity: '', items: [], status: 'idle' })
  const latest = useRef(state)
  useEffect(() => { latest.current = state }, [state])
  useEffect(() => {
    if (!enabled || paused) return
    const frozenParams = JSON.parse(identity) as Record<string, unknown>
    const controller = new AbortController()
    let alive = true
    let attempts = 0
    let buffer = latest.current.identity === identity ? latest.current.items : []
    let nextRowId = buffer.reduce((max, row) => Math.max(max, typeof row.id === 'number' ? row.id : 0), 0) + 1
    let renderTimer: ReturnType<typeof setTimeout> | undefined
    let retryTimer: ReturnType<typeof setTimeout> | undefined
    const flush = () => {
      renderTimer = undefined
      if (alive) setState((current) => ({ ...current, identity, items: buffer, status: 'live' }))
    }
    const connect = async () => {
      setState((current) => ({ identity, items: current.identity === identity ? current.items : [], status: attempts ? 'reconnecting' : 'connecting', partial: true }))
      try {
        await subscribe(frozenParams, {
          signal: controller.signal,
          onFrame: (frame) => {
            if (!alive || controller.signal.aborted) return
            if (frame.type === 'ready') {
              setState({ identity, items: buffer, status: 'live', partial: frame.partial ?? true })
            } else if (frame.type === 'event' && frame.event) {
              attempts = 0
              buffer = appendLiveEvent(buffer, { ...frame.event, id: nextRowId++ })
              if (renderTimer === undefined) renderTimer = setTimeout(flush, 100)
            }
          },
        })
        throw Object.assign(new Error('Live stream closed'), { code: 'STREAM_CLOSED', retryable: true })
      } catch (error) {
        if (!alive || controller.signal.aborted) return
        if (renderTimer !== undefined) { clearTimeout(renderTimer); renderTimer = undefined }
        const failure = error as { code?: string; message?: string; retryable?: boolean }
        const code = failure.code || 'STREAM_UNAVAILABLE'
        if (code === 'SESSION_EXPIRED') attempts = 0
        const retryable = failure.retryable !== false && code !== 'FORBIDDEN'
        if (code === 'UNSUPPORTED') {
          setState({ identity, items: buffer, status: 'fallback', code, message: failure.message })
        } else if (retryable && attempts < 6) {
          const delay = Math.min(8000, 500 * 2 ** attempts++)
          setState({ identity, items: buffer, status: 'reconnecting', code, message: failure.message, partial: true })
          retryTimer = setTimeout(() => { void connect() }, delay)
        } else {
          setState({ identity, items: code === 'FORBIDDEN' ? [] : buffer, status: 'error', code, message: failure.message, partial: true })
        }
      }
    }
    void connect()
    return () => {
      alive = false
      controller.abort()
      if (renderTimer !== undefined) clearTimeout(renderTimer)
      if (retryTimer !== undefined) clearTimeout(retryTimer)
    }
  }, [identity, enabled, paused, subscribe])
  const current = state.identity === identity ? state : { identity, items: [], status: 'connecting' as const }
  return { ...current, status: !enabled ? 'idle' as const : paused ? 'paused' as const : current.status }
}
