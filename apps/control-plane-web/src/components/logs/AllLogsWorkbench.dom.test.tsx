import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { renderWithProviders } from '@/test/render'
import { loginMockUserAs } from '@/test/auth'
import type { LogEntry } from '@/lib/console/console-log-types'
import api from '@/api/client'
import * as snapshotExport from '@/lib/logs/log-snapshot-export'
import { AllLogsWorkbench } from './AllLogsWorkbench'

const nodes = [
  { id: 1, name: 'node-1', status: 'online' },
  { id: 2, name: 'node-2', status: 'online' },
  { id: 3, name: 'node-3', status: 1 },
  { id: 4, name: 'node-4', status: 'online' },
  { id: 5, name: 'node-5', status: 'online' },
  { id: 6, name: 'node-6', status: 'online' },
  { id: 7, name: 'node-offline', status: 'offline' },
]
const instances = [{ id: 3, name: 'instance-3', nodeId: 1 }]
const mapItems = (items: unknown[] | null | undefined): LogEntry[] => (items ?? []).map((raw, index) => {
  const row = raw as Record<string, unknown>
  return {
    id: Number(row.id ?? index), source: 'instance', level: String(row.level ?? 'info'), instanceId: 3, instanceUuid: '', nodeId: 1,
    time: typeof row.time === 'string' ? row.time : '', message: String(row.message ?? row._msg ?? ''), fields: row.fields as Record<string, unknown> | undefined,
  }
})

const tail = (message: string) => ({ content: `time=2026-11-12T10:00:00Z level=INFO ${message}`, truncated: false, fileBytes: 80, path: '/var/log/service.log' })
const envelope = (message = 'instance-snapshot') => ({
  sourceTag: 'federated', view: { view_id: 'snapshot-view' }, exhausted: false,
  items: [{ id: 9, level: 'info', time: '2026-11-12T10:01:00Z', _msg: message }],
  coverage: { complete: true, targets: [], partial_reasons: [] },
  quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
})

beforeEach(() => {
  loginMockUserAs(10)
  document.getSelection()?.removeAllRanges()
  HTMLElement.prototype.scrollIntoView ??= () => {}
  server.use(
    http.get(API('/diagnostics/log'), () => HttpResponse.json(tail('platform-snapshot'))),
    http.get(API('/nodes/:id/diagnostics/log'), ({ params }) => HttpResponse.json(tail(`node-${params.id}-snapshot`))),
    http.get(API('/logs/federation'), () => HttpResponse.json(envelope())),
  )
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('AllLogsWorkbench', () => {
  it('proves the all tab combines bounded text windows and the VL current page without deduping same text', async () => {
    server.use(
      http.get(API('/diagnostics/log'), () => HttpResponse.json(tail('same text from platform'))),
      ...[1, 2, 3, 4].map((id) => http.get(API(`/nodes/${id}/diagnostics/log`), () => HttpResponse.json(tail(`same text from node ${id}`)))),
      http.get(API('/logs/federation'), () => HttpResponse.json({ items: [{ id: 9, level: 'warn', event_time_utc: '2026-11-12T10:01:00Z', _msg: 'same text from instance' }], notes: [], coverage: { complete: true } })),
    )
    renderWithProviders(<AllLogsWorkbench nodes={nodes} instances={instances} initialNodeId={null} initialInstanceId={null} mapInstanceItems={mapItems} />)

    expect(await screen.findByText(/same text from instance/)).toBeInTheDocument()
    expect(screen.getAllByTestId('log-message').length).toBeGreaterThanOrEqual(5)
    expect(screen.getByTestId('all-logs-source-status')).toHaveTextContent('node-5')
    expect(screen.getByTestId('all-logs-source-status')).toHaveTextContent(/scope limited/i)
    expect(screen.getByTestId('all-logs-footer')).toHaveTextContent(/\d+ 行/)
    expect(screen.getByTestId('all-logs-source-status')).not.toHaveAttribute('open')
    expect(screen.queryByTestId('all-logs-scope')).not.toBeInTheDocument()
    expect(screen.getByTestId('all-logs-export')).toHaveAccessibleName('导出')
  })

  it('shows failures and partial coverage as source status, including a retryable 429', async () => {
    server.use(
      http.get(API('/diagnostics/log'), () => HttpResponse.json(tail('platform'))),
      http.get(API('/nodes/1/diagnostics/log'), () => HttpResponse.json({ message: 'unavailable' }, { status: 503 })),
      http.get(API('/nodes/2/diagnostics/log'), () => HttpResponse.json(tail('node 2'))),
      http.get(API('/nodes/3/diagnostics/log'), () => HttpResponse.json(tail('node 3'))),
      http.get(API('/nodes/4/diagnostics/log'), () => HttpResponse.json(tail('node 4'))),
      http.get(API('/logs/federation'), () => HttpResponse.json({ items: [], notes: ['partial coverage'], coverage: { complete: false } }, { status: 429 })),
    )
    renderWithProviders(<AllLogsWorkbench nodes={nodes} instances={instances} initialNodeId={null} initialInstanceId={null} mapInstanceItems={mapItems} />)
    await waitFor(() => expect(screen.getByTestId('all-logs-source-status')).toHaveTextContent(/failed|429|partial|读取失败/i), { timeout: 4_000 })
    expect(screen.getByTestId('all-logs-source-status')).toHaveTextContent(/retry|failed|读取失败/i)
  })

  it('reads at most four online nodes, reports others and never refreshes skipped or offline sources', async () => {
    const requests: string[] = []
    server.use(http.get(API('/nodes/:id/diagnostics/log'), ({ params }) => {
      requests.push(String(params.id))
      return HttpResponse.json(tail(`node-${params.id}-snapshot`))
    }))
    renderWithProviders(<AllLogsWorkbench nodes={nodes} instances={instances} initialNodeId={null} initialInstanceId={null} mapInstanceItems={mapItems} />)
    await screen.findByText('instance-snapshot')
    await waitFor(() => expect(requests).toHaveLength(4))
    expect(requests.sort()).toEqual(['1', '2', '3', '4'])
    expect(screen.getByTestId('all-logs-source-status')).toHaveTextContent('node-offline')
    await waitFor(() => expect(screen.getByTestId('all-logs-refresh')).toBeEnabled())
    await userEvent.setup().click(screen.getByTestId('all-logs-refresh'))
    await waitFor(() => expect(requests).toHaveLength(8))
    expect(requests.every((id) => ['1', '2', '3', '4'].includes(id))).toBe(true)
  })

  it('freezes a selected native row and prevents refresh until selection is cleared', async () => {
    renderWithProviders(<AllLogsWorkbench nodes={nodes.slice(0, 1)} instances={instances} initialNodeId={null} initialInstanceId={null} mapInstanceItems={mapItems} />)
    const row = await screen.findByText('instance-snapshot')
    await waitFor(() => expect(screen.getByTestId('all-logs-refresh')).toBeEnabled())
    const range = document.createRange()
    range.selectNodeContents(row)
    document.getSelection()?.addRange(range)
    fireEvent(document, new Event('selectionchange'))
    await waitFor(() => expect(screen.getByTestId('all-logs-refresh')).toBeDisabled())
    expect(screen.getByText('instance-snapshot')).toBe(row)
    document.getSelection()?.removeAllRanges()
    fireEvent(document, new Event('selectionchange'))
    await waitFor(() => expect(screen.getByTestId('all-logs-refresh')).toBeEnabled())
  })

  it('exports only the filtered current rows with separate text and VL boundaries', async () => {
    const download = vi.spyOn(snapshotExport, 'downloadLogSnapshot').mockImplementation(() => {})
    renderWithProviders(<AllLogsWorkbench nodes={nodes.slice(0, 1)} instances={instances} initialNodeId={null} initialInstanceId={null} mapInstanceItems={mapItems} />)
    await screen.findByText('instance-snapshot')
    await waitFor(() => expect(screen.getByTestId('all-logs-export')).toBeEnabled())
    const user = userEvent.setup()
    await user.type(screen.getByPlaceholderText('搜索已加载文本'), 'instance-snapshot')
    await user.click(screen.getByTestId('all-logs-export'))
    const [rows, windows] = download.mock.calls[0]
    expect(rows).toHaveLength(1)
    expect(rows[0].source).toBe('instance')
    expect(windows.map((window) => window.source).sort()).toEqual(['control_plane', 'instance', 'worker'])
    expect(windows.find((window) => window.source === 'instance')).toMatchObject({ from: expect.any(String), to: expect.any(String), limit: 100 })
    expect(windows.find((window) => window.source === 'worker')?.from).toBeUndefined()
  })

  it('cancels old node scope and never mixes its late response into the new node window', async () => {
    let started = false
    const transport = vi.spyOn(api, 'get')
    let release: () => void = () => {}
    server.use(http.get(API('/nodes/1/diagnostics/log'), async () => {
      started = true
      await new Promise<void>((resolve) => { release = resolve })
      return HttpResponse.json(tail('old-node-late'))
    }))
    renderWithProviders(<AllLogsWorkbench nodes={nodes.slice(0, 2)} instances={instances} initialNodeId={1} initialInstanceId={null} mapInstanceItems={mapItems} />)
    try {
      await waitFor(() => expect(started).toBe(true))
      const user = userEvent.setup()
      screen.getByTestId('all-logs-node-select').focus()
      await user.keyboard('{Enter}')
      await user.click(await screen.findByRole('option', { name: 'node-2', exact: true }))
      await screen.findByText(/node-2-snapshot/)
      const oldSignal = transport.mock.calls.find(([url]) => url === '/nodes/1/diagnostics/log')?.[1]?.signal
      await waitFor(() => expect(oldSignal?.aborted).toBe(true))
      release()
      await waitFor(() => expect(screen.queryByText(/old-node-late/)).not.toBeInTheDocument())
    } finally { release() }
  })

  it('resolves the real instance identity from the VL source reference for labels and downloads', async () => {
    const download = vi.spyOn(snapshotExport, 'downloadLogSnapshot').mockImplementation(() => {})
    const wireMapper = (items: unknown[] | null | undefined) => mapItems(items).map((item) => ({ ...item, instanceId: 0, nodeId: 0, sourceRef: 'inst:3/file' }))
    renderWithProviders(<AllLogsWorkbench nodes={nodes.slice(0, 1)} instances={instances} initialNodeId={null} initialInstanceId={null} mapInstanceItems={wireMapper} />)
    await screen.findByText('instance-snapshot')
    expect(screen.queryByText('Instance 0·VL')).not.toBeInTheDocument()
    expect(screen.getByText('instance-3·VL')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId('all-logs-export')).toBeEnabled())
    await userEvent.setup().click(screen.getByTestId('all-logs-export'))
    expect(download.mock.calls[0][0].find((item) => item.source === 'instance')).toMatchObject({ instanceId: 3, nodeId: 1, sourceRef: 'inst:3/file' })
  })

  it('does not request diagnostics or VL for a non administrator', async () => {
    loginMockUserAs(1)
    let logRequests = 0
    server.use(
      http.get(API('/diagnostics/log'), () => { logRequests++; return HttpResponse.json(tail('private')) }),
      http.get(API('/nodes/:id/diagnostics/log'), () => { logRequests++; return HttpResponse.json(tail('private')) }),
      http.get(API('/logs/federation'), () => { logRequests++; return HttpResponse.json({ items: [] }) }),
    )
    renderWithProviders(<AllLogsWorkbench nodes={nodes} instances={instances} initialNodeId={null} initialInstanceId={null} mapInstanceItems={mapItems} />)
    expect(screen.getByTestId('all-logs-workbench')).toHaveTextContent(/仅平台管理员|platform administrators/i)
    expect(screen.queryByTestId('all-logs-source-status')).not.toBeInTheDocument()
    await waitFor(() => expect(logRequests).toBe(0))
  })
})
