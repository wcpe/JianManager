import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { LiveLogError, subscribeLiveLogs, type LiveLogFrame } from '@/api/logLive'
import { loginMockUserAs } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import LogsPage from './LogsPage'

vi.mock('@/api/logLive', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/logLive')>()
  return { ...actual, subscribeLiveLogs: vi.fn() }
})

interface Connection {
  params: Record<string, unknown>
  signal: AbortSignal
  onFrame: (frame: LiveLogFrame) => void
  fail: (error: unknown) => void
}

const summary = {
  coverage: { complete: true, targets: [], partial_reasons: [] },
  quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
}
const event = (message: string, id = message) => ({
  event_id: id,
  log_source_id: 'inst:2/file',
  level: 'INFO',
  event_time_utc: '2026-11-11T08:00:00Z',
  message,
})
const envelope = (message: string) => ({
  sourceTag: 'federated', view: { view_id: 'live-page-view' }, items: [event(message)], ...summary, exhausted: true,
})
let connections: Connection[] = []
let searchReads: URLSearchParams[] = []
let tailReads: URLSearchParams[] = []

beforeEach(() => {
  loginMockUserAs(10)
  connections = []
  searchReads = []
  tailReads = []
  HTMLElement.prototype.scrollIntoView ??= () => {}
  vi.mocked(subscribeLiveLogs).mockReset()
  vi.mocked(subscribeLiveLogs).mockImplementation((params, options) => new Promise<void>((_resolve, reject) => {
    connections.push({ params, ...options, fail: reject })
    options.signal.addEventListener('abort', () => reject(new DOMException('Cancelled', 'AbortError')), { once: true })
  }))
  server.use(
    http.get(API('/nodes'), () => HttpResponse.json([{ id: 1, name: 'node-1', status: 1 }])),
    http.get(API('/instances'), () => HttpResponse.json([{ id: 2, name: 'instance-2', nodeId: 1 }])),
    http.get(API('/logs/federation'), ({ request }) => {
      searchReads.push(new URL(request.url).searchParams)
      return HttpResponse.json(envelope('history-before-follow'))
    }),
    http.get(API('/logs/federation/tail'), ({ request }) => {
      tailReads.push(new URL(request.url).searchParams)
      return HttpResponse.json(envelope('polling-fallback-row'))
    }),
    http.get(API('/logs/federation/stats'), () => HttpResponse.json({ points: [{ agg: { count: 1 } }], ...summary })),
    http.get(API('/logs/federation/facets'), () => HttpResponse.json({ dimensions: [], ...summary })),
    http.get(API('/logs/federation/streams'), () => HttpResponse.json({ streams: [], ...summary })),
    http.get(API('/logs/federation/fields'), () => HttpResponse.json({ fields: [], ...summary })),
    http.get(API('/logs/federation/stream-field-names'), () => HttpResponse.json({ fields: [], ...summary })),
    http.get(API('/logs/federation/stream-field-values'), () => HttpResponse.json({ values: [], ...summary })),
    http.get(API('/diagnostics/log'), () => HttpResponse.json({ content: 'platform-file-row', truncated: false, fileBytes: 17, path: '/logs/cp.log' })),
  )
})

afterEach(() => vi.useRealTimers())

async function startFollow() {
  renderWithProviders(<LogsPage />, { route: '/logs?instanceId=2' })
  await screen.findByText('history-before-follow')
  const user = userEvent.setup()
  await user.click(screen.getByTestId('logs-live-toggle'))
  await waitFor(() => expect(connections).toHaveLength(1))
  await act(async () => connections[0].onFrame({ type: 'ready', partial: true, replayWindowSeconds: 10 }))
  return user
}

async function emit(connection: Connection, message: string) {
  await act(async () => connection.onFrame({ type: 'event', event: event(message) }))
  return screen.findByText(message)
}

describe('LogsPage live subscription integration', () => {
  it('starts only a native subscription, renders frames and keeps the authorized filter scope', async () => {
    await startFollow()
    await emit(connections[0], 'native-stream-row')
    expect(connections[0].params).toMatchObject({ source: 'instance', instanceId: 2, view: 'node_instance' })
    expect(connections[0].params.to).toBeUndefined()
    expect(connections[0].params.cursor).toBeUndefined()
    expect(connections[0].params.viewId).toBeUndefined()
    expect(connections[0].signal.aborted).toBe(false)
    expect(screen.getByTestId('logs-live-status')).toHaveAttribute('data-state', 'live')
    expect(screen.getByTestId('logs-live-status')).toHaveTextContent('可能缺行')
    expect(tailReads).toHaveLength(0)
    expect(searchReads).toHaveLength(1)
    expect(screen.queryByText('history-before-follow')).not.toBeInTheDocument()
  })

  it('explicit resume clears only the log selection and returns the viewport to the live edge', async () => {
    const user = await startFollow()
    const retained = await emit(connections[0], 'native-selection-live-row')
    const range = document.createRange()
    range.selectNodeContents(retained)
    document.getSelection()?.removeAllRanges()
    document.getSelection()?.addRange(range)
    await act(async () => document.dispatchEvent(new Event('selectionchange')))
    expect(connections[0].signal.aborted).toBe(true)
    expect(screen.getByTestId('logs-live-pause')).toHaveTextContent('恢复')
    const viewport = screen.getByTestId('logs-virtual')
    viewport.scrollTop = 20
    await user.click(screen.getByTestId('logs-live-pause'))
    expect(document.getSelection()?.isCollapsed).toBe(true)
    expect(viewport.scrollTop).toBe(0)
    await waitFor(() => expect(connections).toHaveLength(2))
    expect(screen.getByText('native-selection-live-row')).toBe(retained)
  })

  it('aborts on manual pause, ignores late frames and retains the same row DOM after resuming', async () => {
    const user = await startFollow()
    const first = connections[0]
    const retained = await emit(first, 'retained-live-row')
    await user.click(screen.getByTestId('logs-live-pause'))
    expect(first.signal.aborted).toBe(true)
    expect(screen.getByTestId('logs-live-status')).toHaveAttribute('data-state', 'paused')
    expect(screen.getByText('retained-live-row')).toBe(retained)
    await act(async () => first.onFrame({ type: 'event', event: event('late-after-pause') }))
    expect(screen.queryByText('late-after-pause')).not.toBeInTheDocument()
    await user.click(screen.getByTestId('logs-live-pause'))
    await waitFor(() => expect(connections).toHaveLength(2))
    expect(screen.getByText('retained-live-row')).toBe(retained)
    await act(async () => connections[1].onFrame({ type: 'ready' }))
    await emit(connections[1], 'after-resume-row')
    expect(screen.getByText('retained-live-row')).toBe(retained)
    expect(tailReads).toHaveLength(0)
    await user.click(screen.getByTestId('logs-live-toggle'))
    expect(connections[1].signal.aborted).toBe(true)
  })

  it('shows an explicit unsupported fallback and queries the polling tail endpoint', async () => {
    await startFollow()
    await act(async () => connections[0].fail(new LiveLogError('UNSUPPORTED', 'old worker does not support native tail')))
    expect(await screen.findByText('polling-fallback-row')).toBeInTheDocument()
    expect(screen.getByTestId('logs-live-status')).toHaveAttribute('data-state', 'fallback')
    expect(screen.getByTestId('logs-live-status')).toHaveTextContent('回退为每 3 秒轮询')
    expect(tailReads).toHaveLength(1)
    expect(tailReads[0].get('mode')).toBe('FOLLOW_LIVE')
    expect(tailReads[0].get('instanceId')).toBe('2')
    expect(connections).toHaveLength(1)
  })

  it('clears displayed live rows on revoked access and does not schedule reconnects', async () => {
    await startFollow()
    await emit(connections[0], 'row-before-revocation')
    vi.useFakeTimers()
    await act(async () => connections[0].fail(new LiveLogError('FORBIDDEN', 'access revoked', 403)))
    expect(screen.getByTestId('logs-live-status')).toHaveAttribute('data-state', 'error')
    expect(screen.getByTestId('logs-live-status')).toHaveTextContent('FORBIDDEN')
    expect(screen.queryByText('row-before-revocation')).not.toBeInTheDocument()
    expect(screen.queryByText('history-before-follow')).not.toBeInTheDocument()
    await act(() => vi.advanceTimersByTimeAsync(60_000))
    expect(connections).toHaveLength(1)
    expect(tailReads).toHaveLength(0)
  })

  it('cancels instance streaming when selecting node files on the same tab', async () => {
    server.use(http.get(API('/nodes/1/diagnostics/log'), () => HttpResponse.json({ content: 'worker-file-row', truncated: false, fileBytes: 15, path: '/data/var/log/worker.log' })))
    const user = await startFollow()
    const old = connections[0]
    await emit(old, 'instance-before-node-switch')
    screen.getByTestId('logs-source-select').focus()
    await user.keyboard('{Enter}')
    await user.click(await screen.findByRole('option', { name: '节点', exact: true }))
    expect(old.signal.aborted).toBe(true)
    screen.getByTestId('logs-node-select').focus()
    await user.keyboard('{Enter}')
    await user.click(await screen.findByRole('option', { name: 'node-1', exact: true }))
    await screen.findByText('worker-file-row')
    await act(async () => old.onFrame({ type: 'event', event: event('old-instance-after-node') }))
    expect(screen.queryByText('old-instance-after-node')).not.toBeInTheDocument()
    expect(screen.queryByText('instance-before-node-switch')).not.toBeInTheDocument()
    expect(screen.queryByTestId('logs-live-status')).not.toBeInTheDocument()
    expect(connections).toHaveLength(1)
    expect(tailReads).toHaveLength(0)
  })

  it('cancels the subscription when switching to a text source and rejects old-scope callbacks', async () => {
    const user = await startFollow()
    const old = connections[0]
    await emit(old, 'instance-before-source-switch')
    await user.click(screen.getByRole('tab', { name: '平台日志' }))
    expect(await screen.findByText('platform-file-row')).toBeInTheDocument()
    expect(old.signal.aborted).toBe(true)
    await act(async () => old.onFrame({ type: 'event', event: event('old-scope-late-row') }))
    expect(screen.queryByText('old-scope-late-row')).not.toBeInTheDocument()
    expect(screen.queryByText('instance-before-source-switch')).not.toBeInTheDocument()
    expect(screen.queryByTestId('logs-live-status')).not.toBeInTheDocument()
    expect(connections).toHaveLength(1)
  })
})
