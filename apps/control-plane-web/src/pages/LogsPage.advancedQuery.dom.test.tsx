import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import api from '@/api/client'
import { http, HttpResponse } from 'msw'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { loginMockUser } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import { logScopeKey, normalizeLogScope } from '@/lib/logs/logs-filters'
import LogsPage from './LogsPage'

const summary = {
  coverage: { complete: true, targets: [], partial_reasons: [] },
  quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
}
const event = {
  id: 1, log_source_id: 'inst:2/file', level: 'WARN',
  event_time_utc: '2026-11-11T08:00:00Z', message: 'advanced-query-event',
}
let reads: URLSearchParams[] = []
let exports: URLSearchParams[] = []

afterEach(() => vi.restoreAllMocks())

beforeEach(() => {
  loginMockUser()
  reads = []
  exports = []
  HTMLElement.prototype.scrollIntoView ??= () => {}
  server.use(
    http.get(API('/nodes'), () => HttpResponse.json([])),
    http.get(API('/instances'), () => HttpResponse.json([{ id: 2, name: 'instance-2', nodeId: 1 }])),
    http.get(API('/logs/federation'), ({ request }) => {
      const params = new URL(request.url).searchParams
      reads.push(params)
      if (params.get('logsQL') === 'bad | stats count()') {
        return HttpResponse.json({ error: 'INVALID_REQUEST', message: '不支持管道；请使用分析区' }, { status: 400 })
      }
      return HttpResponse.json({
        sourceTag: 'federated', view: { view_id: 'advanced-view' }, items: [event], ...summary, exhausted: true,
        query_meta: { elapsed_ms: 12.4, returned_rows: 1, limit: 100, max_bytes: 33554432, timeout_ms: 30000, max_fanout: 64 },
      })
    }),
    http.get(API('/logs/federation/stats'), () => HttpResponse.json({ points: [{ agg: { count: 1 } }], ...summary })),
    http.get(API('/logs/federation/facets'), () => HttpResponse.json({ dimensions: [], ...summary })),
    http.get(API('/logs/federation/streams'), () => HttpResponse.json({ streams: [], ...summary })),
    http.get(API('/logs/federation/fields'), () => HttpResponse.json({ fields: [], ...summary })),
    http.get(API('/logs/federation/stream-field-names'), () => HttpResponse.json({ fields: [], ...summary })),
    http.get(API('/logs/federation/stream-field-values'), () => HttpResponse.json({ values: [], ...summary })),
    http.get(API('/diagnostics/log'), () => HttpResponse.json({ content: 'platform text', truncated: true, fileBytes: 100, path: '/logs/cp.log' })),
    http.post(API('/logs/federation/export'), ({ request }) => {
      exports.push(new URL(request.url).searchParams)
      return HttpResponse.json({ artifact: null, export_incomplete: true, incomplete_reasons: ['coverage_incomplete'] })
    }),
  )
})

describe('advanced log query mode', () => {
  it('is available for the default authorized-instance view, preserves scope and uses explicit apply', async () => {
    renderWithProviders(<LogsPage />, { route: '/logs?instanceId=2' })
    await screen.findByText('advanced-query-event')
    const user = userEvent.setup()
    await user.click(screen.getByTestId('logs-advanced-toggle'))
    const input = screen.getByTestId('logs-query-input')
    fireEvent.change(input, { target: { value: 'error OR level:WARN' } })
    expect(screen.getByTestId('logs-export-trigger')).toBeDisabled()
    await user.click(screen.getByTestId('logs-query-apply'))
    await waitFor(() => expect(reads.at(-1)?.get('logsQL')).toBe('error OR level:WARN'))
    expect(reads.at(-1)?.get('advanced')).toBe('true')
    expect(reads.at(-1)?.get('instanceId')).toBe('2')
    expect(reads.at(-1)?.has('keyword')).toBe(false)
    const meta = await screen.findByTestId('logs-query-meta')
    expect(meta).toHaveTextContent('12')
    expect(screen.getByTestId('logs-toolbar')).toContainElement(meta)
    expect(screen.getByTestId('logs-coverage-slot')).not.toContainElement(meta)
    expect(screen.queryByTestId('logs-query-budget-summary')).not.toBeInTheDocument()
  })

  it('export shares the applied advanced filter and remains disabled for an unapplied draft', async () => {
    renderWithProviders(<LogsPage />, { route: '/logs?instanceId=2' })
    await screen.findByText('advanced-query-event')
    const user = userEvent.setup()
    await user.click(screen.getByTestId('logs-advanced-toggle'))
    fireEvent.change(screen.getByTestId('logs-query-input'), { target: { value: 'stream:="stderr"' } })
    await user.click(screen.getByTestId('logs-query-apply'))
    await waitFor(() => expect(reads.at(-1)?.get('logsQL')).toBe('stream:="stderr"'))
    await waitFor(() => expect(screen.getByTestId('logs-export-trigger')).toBeEnabled())
    await user.click(screen.getByTestId('logs-export-trigger'))
    await user.click(await screen.findByRole('menuitem', { name: '导出当前页' }))
    await waitFor(() => expect(exports).toHaveLength(1))
    expect(exports[0]?.get('advanced')).toBe('true')
    expect(exports[0]?.get('logsQL')).toBe('stream:="stderr"')
    expect(exports[0]?.get('viewId')).toBe('advanced-view')
    fireEvent.change(screen.getByTestId('logs-query-input'), { target: { value: 'stream:="stdout"' } })
    expect(screen.getByTestId('logs-export-trigger')).toBeDisabled()
    expect(screen.queryByTestId('logs-query-meta')).not.toBeInTheDocument()
  })

  it('shows the actual query rejection, never an empty success', async () => {
    renderWithProviders(<LogsPage />)
    await screen.findByText('advanced-query-event')
    const user = userEvent.setup()
    await user.click(screen.getByTestId('logs-advanced-toggle'))
    fireEvent.change(screen.getByTestId('logs-query-input'), { target: { value: 'bad | stats count()' } })
    await user.click(screen.getByTestId('logs-query-apply'))
    expect(await screen.findByTestId('logs-query-error')).toHaveTextContent('不支持管道；请使用分析区')
    expect(screen.getByTestId('logs-export-trigger')).toBeDisabled()
    expect(screen.getByTestId('logs-empty-state')).toHaveAttribute('data-empty-success', 'false')
  })

  it('preserves the visual keyword draft and clears advanced parameters when switching back', async () => {
    renderWithProviders(<LogsPage />)
    await screen.findByText('advanced-query-event')
    const user = userEvent.setup()
    fireEvent.change(screen.getByTestId('logs-query-input'), { target: { value: 'visual-word' } })
    await waitFor(() => expect(reads.at(-1)?.get('keyword')).toBe('visual-word'))
    await user.click(screen.getByTestId('logs-advanced-toggle'))
    fireEvent.change(screen.getByTestId('logs-query-input'), { target: { value: 'level:ERROR' } })
    await user.click(screen.getByTestId('logs-query-apply'))
    await waitFor(() => expect(reads.at(-1)?.get('logsQL')).toBe('level:ERROR'))
    await user.click(screen.getByTestId('logs-advanced-toggle'))
    expect(screen.getByTestId('logs-query-input')).toHaveValue('visual-word')
    expect(screen.getByTestId('logs-advanced-toggle')).toHaveAttribute('aria-pressed', 'false')
    // The visual query may be served from fresh React Query cache. Verify the
    // active export scope, not a redundant network request that needn't occur.
    await waitFor(() => expect(screen.getByTestId('logs-export-trigger')).toBeEnabled())
    await user.click(screen.getByTestId('logs-export-trigger'))
    await user.click(await screen.findByRole('menuitem', { name: '导出当前页' }))
    await waitFor(() => expect(exports).toHaveLength(1))
    expect(exports[0]?.get('keyword')).toBe('visual-word')
    expect(exports[0]?.has('logsQL')).toBe(false)
    expect(exports[0]?.has('advanced')).toBe(false)
  })

  it('cancels the actual in-flight request and leaves controls available', async () => {
    let started = false
    let release: (() => void) | undefined
    const get = vi.spyOn(api, 'get')
    server.use(http.get(API('/logs/federation'), async ({ request }) => {
      const params = new URL(request.url).searchParams
      if (params.get('logsQL') === 'slow') {
        started = true
        await new Promise<void>((resolve) => { release = resolve })
      }
      return HttpResponse.json({ sourceTag: 'federated', view: { view_id: 'cancel-view' }, items: [event], ...summary, exhausted: true,
        query_meta: { elapsed_ms: 1, returned_rows: 1, limit: 100, max_bytes: 33554432, timeout_ms: 30000, max_fanout: 64 } })
    }))
    renderWithProviders(<LogsPage />)
    await screen.findByText('advanced-query-event')
    const user = userEvent.setup()
    await user.click(screen.getByTestId('logs-advanced-toggle'))
    fireEvent.change(screen.getByTestId('logs-query-input'), { target: { value: 'slow' } })
    await user.click(screen.getByTestId('logs-query-apply'))
    await waitFor(() => expect(started).toBe(true))
    const signal = get.mock.calls.find(([path, config]) => path === '/logs/federation' && config?.params?.logsQL === 'slow')?.[1]?.signal
    expect(signal?.aborted).toBe(false)
    await user.click(screen.getByTestId('logs-query-cancel'))
    await waitFor(() => expect(signal?.aborted).toBe(true))
    release?.()
    expect(await screen.findByTestId('logs-query-cancelled')).toBeInTheDocument()
    expect(screen.getByTestId('logs-query-apply')).toBeEnabled()
  })

  it('rejects an empty or over-byte-limit draft without dispatching it', async () => {
    renderWithProviders(<LogsPage />)
    await screen.findByText('advanced-query-event')
    const user = userEvent.setup()
    await user.click(screen.getByTestId('logs-advanced-toggle'))
    fireEvent.change(screen.getByTestId('logs-query-input'), { target: { value: '' } })
    expect(screen.getByTestId('logs-query-apply')).toBeDisabled()
    expect(screen.getByTestId('logs-query-error')).toHaveTextContent('LogsQL')
    fireEvent.change(screen.getByTestId('logs-query-input'), { target: { value: '错'.repeat(3000) } })
    expect(screen.getByTestId('logs-query-apply')).toBeDisabled()
    expect(screen.getByTestId('logs-query-error')).toHaveTextContent('8192')
    expect(reads.every((params) => (params.get('logsQL')?.length ?? 0) < 3000)).toBe(true)
  })

  it('does not pass off unfiltered old-CP rows as advanced results', async () => {
    server.use(http.get(API('/logs/federation'), () => HttpResponse.json({
      sourceTag: 'federated', view: { view_id: 'old-cp-view' }, items: [event], ...summary, exhausted: true,
    })))
    renderWithProviders(<LogsPage />)
    await screen.findByText('advanced-query-event')
    await userEvent.setup().click(screen.getByTestId('logs-advanced-toggle'))
    expect(await screen.findByTestId('logs-query-error')).toHaveTextContent('不支持安全高级查询')
    expect(screen.queryByText('advanced-query-event')).not.toBeInTheDocument()
    expect(screen.getByTestId('logs-export-trigger')).toBeDisabled()
  })

  it('does not offer advanced query for platform text logs', async () => {
    // A platform-admin token is necessary for the platform route.
    const payload = btoa(JSON.stringify({ userId: 1, username: 'admin', role: 10, exp: Math.floor(Date.now() / 1000) + 900 }))
    loginMockUser(`mock.${payload}.sig`)
    renderWithProviders(<LogsPage />, { route: '/logs?view=platform' })
    await screen.findByText('platform text')
    expect(screen.queryByTestId('logs-advanced-toggle')).not.toBeInTheDocument()
    expect(reads).toHaveLength(0)
  })
})

describe('advanced scope identity', () => {
  it('keeps the active expression only and differentiates filters for cache/export/view identities', () => {
    const scope = normalizeLogScope({ view: 'node_instance', advanced: true, logsQL: '  error OR warn ', keyword: 'inactive' })
    expect(scope).toMatchObject({ advanced: true, logsQL: 'error OR warn' })
    expect(scope).not.toHaveProperty('keyword')
    expect(logScopeKey(scope)).not.toBe(logScopeKey({ ...scope, logsQL: 'error' }))
    expect(normalizeLogScope({ ...scope, advanced: false, keyword: 'visual' })).not.toHaveProperty('logsQL')
  })
})
