import { beforeEach, describe, expect, it } from 'vitest'
import { http, HttpResponse } from 'msw'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { API } from '@jianmanager/devmock/api'
import { server } from '@jianmanager/devmock/server'
import { loginMockUserAs } from '@/test/auth'
import { renderWithProviders } from '@/test/render'
import LogsPage from './LogsPage'

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
    expect(screen.getByRole('tab', { name: '节点/实例日志' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('logs-source-select')).toBeEnabled()
    await choose('logs-source-select', '节点')
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
    expect(screen.getByTestId('logs-export-trigger')).toBeDisabled()
    await choose('logs-source-select', '实例')
    await screen.findByText('instance-history-log')
    expect(screen.queryByTestId('logs-text-footer')).not.toBeInTheDocument()
    expect(screen.getByTestId('logs-node-select')).toHaveTextContent('dev-node')
    expect(instanceReads.at(-1)?.searchParams.get('nodeId')).toBe('1')
    expect(instanceReads.at(-1)?.searchParams.get('source')).toBe('instance')
  })

  it.each(['node_instance', 'all'])('opens a node deep link directly in %s without reading VL', async (view) => {
    renderWithProviders(<LogsPage />, { route: `/logs?view=${view}&source=worker&nodeId=1` })
    await screen.findByText(/worker-process-log/)
    expect(screen.getByTestId('logs-source-select')).toHaveTextContent('节点')
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
    await user.click(screen.getByRole('tab', { name: '节点/实例日志', exact: true }))
    await screen.findByText(/worker-process-log/)
    expect(screen.getByTestId('logs-node-select')).toHaveTextContent('dev-node')
    expect(screen.getByTestId('logs-source-select')).toHaveTextContent('节点')
    expect(instanceReads).toHaveLength(0)
  })

  it('does not let a member enable process logs through URL parameters', async () => {
    loginMockUserAs(0)
    renderWithProviders(<LogsPage />, { route: '/logs?source=worker&nodeId=1' })
    await screen.findByText('instance-history-log')
    expect(screen.getByTestId('logs-source-select')).toBeDisabled()
    expect(screen.getByTestId('logs-source-select')).toHaveTextContent('实例')
    expect(nodeReads).toHaveLength(0)
    expect(platformReads).toBe(0)
    expect(instanceReads[0].searchParams.has('nodeId')).toBe(false)
  })

  it('rejects invalid source and node identifiers instead of dispatching a process-log request', async () => {
    renderWithProviders(<LogsPage />, { route: '/logs?source=unknown&nodeId=1.5' })
    await screen.findByText('instance-history-log')
    expect(screen.getByTestId('logs-source-select')).toHaveTextContent('实例')
    expect(nodeReads).toHaveLength(0)
    expect(instanceReads[0].searchParams.has('nodeId')).toBe(false)
  })

  it('keeps platform logs on the platform tab even when a worker source is in the URL', async () => {
    renderWithProviders(<LogsPage />, { route: '/logs?view=platform&source=worker&nodeId=1' })
    await screen.findByText(/cp-process-log/)
    await waitFor(() => expect(platformReads).toBe(1))
    expect(nodeReads).toHaveLength(0)
    expect(instanceReads).toHaveLength(0)
    expect(screen.getByTestId('logs-source-select')).toHaveTextContent('平台')
    expect(screen.getByTestId('logs-source-select')).toBeDisabled()
  })
})
