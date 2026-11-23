import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { loginMockUserAs } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import LogsPage from './LogsPage'
import * as snapshotExport from '@/lib/logs/log-snapshot-export'

const summary = {
  coverage: { complete: true, pending: false, targets: [], partial_reasons: [] },
  quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
}
let nodeReads: URL[] = []
let instanceReads: URL[] = []
let platformReads = 0

beforeEach(() => {
  loginMockUserAs(10)
  nodeReads = []
  instanceReads = []
  platformReads = 0
  HTMLElement.prototype.scrollIntoView ??= () => {}
  server.use(
    http.get(API('/nodes'), () => HttpResponse.json([{ id: 1, name: 'dev-node', status: 1 }])),
    http.get(API('/instances'), () => HttpResponse.json([{ id: 2, name: 'game-server', nodeId: 1 }])),
    http.get(API('/nodes/1/diagnostics/log'), ({ request }) => {
      nodeReads.push(new URL(request.url))
      return HttpResponse.json({ content: 'time=2026-11-12T08:00:00Z level=INFO msg="worker-process-log"', path: '/data/var/log/worker.log', fileBytes: 4096, truncated: true })
    }),
    http.get(API('/diagnostics/log'), () => {
      platformReads++
      return HttpResponse.json({ content: 'time=2026-11-12T08:00:00Z level=INFO msg="cp-process-log"', path: '/data/log/cp.log', fileBytes: 1024, truncated: false })
    }),
    http.get(API('/logs/federation'), ({ request }) => {
      instanceReads.push(new URL(request.url))
      return HttpResponse.json({
        sourceTag: 'federated', view: { view_id: 'instance-view' }, exhausted: true, ...summary,
        items: [{ event_id: 'instance-event', log_source_id: 'inst:2/file', level: 'INFO', event_time_utc: '2026-11-12T08:00:00Z', message: 'instance-history-log' }],
      })
    }),
    http.get(API('/logs/federation/stats'), () => HttpResponse.json({ points: [], ...summary })),
    http.get(API('/logs/federation/facets'), () => HttpResponse.json({ dimensions: [], ...summary })),
    http.get(API('/logs/federation/streams'), () => HttpResponse.json({ streams: [], ...summary })),
    http.get(API('/logs/federation/fields'), () => HttpResponse.json({ fields: [], explore_applied: true, ...summary })),
    http.get(API('/logs/federation/stream-field-names'), () => HttpResponse.json({ fields: [], ...summary })),
    http.get(API('/logs/federation/stream-field-values'), () => HttpResponse.json({ values: [], ...summary })),
  )
})

afterEach(() => {
  vi.restoreAllMocks()
})

async function choose(testId: string, label: string) {
  const user = userEvent.setup()
  screen.getByTestId(testId).focus()
  await user.keyboard('{Enter}')
  await user.click(await screen.findByRole('option', { name: label, exact: true }))
}

describe('node process log entry', () => {
  it('lets admins read node logs from the node/instance tab and requires an explicit node', async () => {
    renderWithProviders(<LogsPage />, { route: '/logs' })
    await screen.findByText('instance-history-log')
    expect(screen.getByRole('tab', { name: '实例日志' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getAllByRole('tab').map((tab) => tab.textContent)).toEqual(['平台日志', '节点日志', '实例日志', '全部日志'])
    await userEvent.setup().click(screen.getByRole('tab', { name: '节点日志', exact: true }))
    expect(screen.getByTestId('logs-text-status')).toHaveTextContent('选择节点')
    expect(screen.getByTestId('logs-empty-state')).toHaveAttribute('data-empty-reason', 'choose_node')
    expect(nodeReads).toHaveLength(0)
    expect(screen.queryByTestId('logs-advanced-toggle')).not.toBeInTheDocument()
    expect(screen.queryByTestId('logs-fields-trigger')).not.toBeInTheDocument()
    await choose('logs-node-select', 'dev-node')
    await screen.findByText(/worker-process-log/)
    expect(nodeReads).toHaveLength(1)
    expect(nodeReads[0].searchParams.get('lines')).toBe('200')
    expect(screen.getByTestId('logs-text-footer')).toHaveTextContent('已加载 1 行')
    expect(screen.getByTestId('logs-text-export')).toBeEnabled()
    expect(screen.queryByTestId('logs-export-trigger')).not.toBeInTheDocument()
    await userEvent.setup().click(screen.getByRole('tab', { name: '实例日志', exact: true }))
    await screen.findByText('instance-history-log')
    expect(screen.queryByTestId('logs-text-footer')).not.toBeInTheDocument()
    expect(screen.getByTestId('logs-node-select')).toHaveTextContent('dev-node')
    expect(instanceReads.at(-1)?.searchParams.get('nodeId')).toBe('1')
    expect(instanceReads.at(-1)?.searchParams.get('source')).toBe('instance')
  })

  it.each(['node_instance', 'node'])('opens a node deep link directly in %s without reading VL', async (view) => {
    renderWithProviders(<LogsPage />, { route: `/logs?view=${view}&source=worker&nodeId=1` })
    await screen.findByText(/worker-process-log/)
    expect(screen.getByRole('tab', { name: '节点日志', exact: true })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('logs-node-select')).toHaveTextContent('dev-node')
    expect(nodeReads).toHaveLength(1)
    expect(instanceReads).toHaveLength(0)
    expect(platformReads).toBe(0)
  })

  it('keeps the chosen node and source when visiting platform logs and returning', async () => {
    renderWithProviders(<LogsPage />, { route: '/logs?source=worker&nodeId=1' })
    await screen.findByText(/worker-process-log/)
    const user = userEvent.setup()
    await user.click(screen.getByRole('tab', { name: '平台日志', exact: true }))
    await screen.findByText(/cp-process-log/)
    await user.click(screen.getByRole('tab', { name: '节点日志', exact: true }))
    await screen.findByText(/worker-process-log/)
    expect(screen.getByTestId('logs-node-select')).toHaveTextContent('dev-node')
    expect(screen.getByRole('tab', { name: '节点日志', exact: true })).toHaveAttribute('aria-selected', 'true')
    expect(instanceReads).toHaveLength(0)
  })

  it('does not let a member enable process logs through URL parameters', async () => {
    loginMockUserAs(0)
    renderWithProviders(<LogsPage />, { route: '/logs?source=worker&nodeId=1' })
    await screen.findByText('instance-history-log')
    expect(screen.getAllByRole('tab')).toHaveLength(1)
    expect(screen.getByRole('tab', { name: '实例日志', exact: true })).toHaveAttribute('aria-selected', 'true')
    expect(nodeReads).toHaveLength(0)
    expect(platformReads).toBe(0)
    expect(instanceReads[0].searchParams.has('nodeId')).toBe(false)
  })

  it('rejects invalid source and node identifiers instead of dispatching a process-log request', async () => {
    renderWithProviders(<LogsPage />, { route: '/logs?source=unknown&nodeId=1.5' })
    await screen.findByText('instance-history-log')
    expect(screen.getByRole('tab', { name: '实例日志', exact: true })).toHaveAttribute('aria-selected', 'true')
    expect(nodeReads).toHaveLength(0)
    expect(instanceReads[0].searchParams.has('nodeId')).toBe(false)
  })

  it('keeps platform logs on the platform tab even when a worker source is in the URL', async () => {
    renderWithProviders(<LogsPage />, { route: '/logs?view=platform&source=worker&nodeId=1' })
    await screen.findByText(/cp-process-log/)
    await waitFor(() => expect(platformReads).toBe(1))
    expect(nodeReads).toHaveLength(0)
    expect(instanceReads).toHaveLength(0)
    expect(screen.getByRole('tab', { name: '平台日志', exact: true })).toHaveAttribute('aria-selected', 'true')
    expect(screen.queryByTestId('logs-node-select')).not.toBeInTheDocument()
    expect(screen.getByTestId('logs-text-export')).toBeEnabled()
  })
})

it('keeps one consistently named header export across tabs and exports the current text or combined window', async () => {
  const download = vi.spyOn(snapshotExport, 'downloadLogSnapshot').mockImplementation(() => {})
  const user = userEvent.setup()
  renderWithProviders(<LogsPage />, { route: '/logs' })
  await screen.findByText('instance-history-log')
  const header = screen.getByTestId('logs-header-actions')
  expect(within(header).getByTestId('logs-export-trigger')).toHaveAccessibleName('导出')
  expect(within(header).getAllByRole('button', { name: '导出', exact: true })).toHaveLength(1)

  await user.click(screen.getByRole('tab', { name: '平台日志', exact: true }))
  await screen.findByText(/cp-process-log/)
  const platformExport = within(header).getByTestId('logs-text-export')
  await waitFor(() => expect(platformExport).toBeEnabled())
  expect(platformExport).toHaveAccessibleName('导出')
  expect(within(screen.getByTestId('logs-toolbar')).queryByRole('button', { name: '导出', exact: true })).not.toBeInTheDocument()
  await user.click(platformExport)
  expect(download.mock.calls[0][0]).toHaveLength(1)
  expect(download.mock.calls[0][0][0]).toMatchObject({ source: 'control_plane' })
  expect(download.mock.calls[0][1][0]).toMatchObject({ source: 'control_plane', limit: 200, truncated: false, capturedAt: expect.any(String) })
  expect(screen.getByTestId('logs-text-status')).toBeEmptyDOMElement()

  await user.click(screen.getByRole('tab', { name: '节点日志', exact: true }))
  expect(within(header).getByTestId('logs-text-export')).toBeDisabled()
  await choose('logs-node-select', 'dev-node')
  await screen.findByText(/worker-process-log/)
  const nodeExport = within(header).getByTestId('logs-text-export')
  await waitFor(() => expect(nodeExport).toBeEnabled())
  expect(nodeExport).toHaveAccessibleName('导出')
  await user.click(nodeExport)
  expect(download.mock.calls[1][0][0]).toMatchObject({ source: 'worker', nodeId: 1 })
  expect(download.mock.calls[1][1][0]).toMatchObject({ source: 'worker', limit: 200, truncated: true })

  await user.click(screen.getByRole('tab', { name: '全部日志', exact: true }))
  const combinedExport = within(header).getByTestId('all-logs-export')
  await waitFor(() => expect(combinedExport).toBeEnabled())
  expect(combinedExport).toHaveAccessibleName('导出')
  expect(within(screen.getByTestId('all-logs-workbench')).queryByRole('button', { name: '导出', exact: true })).not.toBeInTheDocument()
  await user.click(combinedExport)
  expect(download.mock.calls[2][1].map((window) => window.source).sort()).toEqual(['control_plane', 'instance', 'worker'])
  expect(download.mock.calls[2][0]).toHaveLength(3)
  expect(screen.queryByTestId('all-logs-scope')).not.toBeInTheDocument()

  await user.click(screen.getByRole('tab', { name: '实例日志', exact: true }))
  await screen.findByText('instance-history-log')
  expect(within(header).getAllByRole('button', { name: '导出', exact: true })).toHaveLength(1)
  expect(screen.queryByTestId('logs-text-export')).not.toBeInTheDocument()
  expect(screen.queryByTestId('all-logs-export')).not.toBeInTheDocument()
})
