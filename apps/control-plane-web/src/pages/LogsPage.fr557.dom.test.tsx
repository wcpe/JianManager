/** FR-557：VL 全窗口时间桶、字段探索与流值筛选。 */
import { describe, expect, it, beforeEach } from 'vitest'
import { http, HttpResponse } from 'msw'
import userEvent from '@testing-library/user-event'
import { screen, waitFor } from '@testing-library/react'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import LogsPage from './LogsPage'

const summary = {
  coverage: { complete: true, targets: [], partial_reasons: [] },
  quality: { duplicate_quality: 'exact', stats_quality: 'exact' },
  truncated: false,
}

const logEvent = {
  id: 1,
  log_source_id: 'inst:2/file',
  level: 'INFO',
  event_time_utc: '2026-11-11T08:00:00Z',
  message: 'structured-message',
  fields: { parser_version: 'v1', shard: 'blue' },
}

describe('LogsPage × FR-557 查询分析工作台', () => {
  beforeEach(() => {
    loginMockUser()
    HTMLElement.prototype.scrollIntoView ??= () => {}
    server.use(
      http.get(API('/nodes'), () => HttpResponse.json([{ id: 1, name: 'node-1', status: 1 }])),
      http.get(API('/instances'), () => HttpResponse.json([{ id: 2, name: 'instance-2', nodeId: 1 }])),
      http.get(API('/logs/federation'), () => HttpResponse.json({
        sourceTag: 'federated', items: [logEvent], view: { view_id: 'fr557-view' },
        ...summary, exhausted: true,
      })),
      http.get(API('/logs/federation/stats'), () => HttpResponse.json({
        points: [
          { dimensions: { _time: '2026-11-11T08:00:00Z' }, agg: { count: 3 } },
          { dimensions: { _time: '2026-11-11T08:05:00Z' }, agg: { count: 7 } },
        ],
        ...summary,
      })),
      http.get(API('/logs/federation/facets'), () => HttpResponse.json({
        dimensions: [{ dimension: 'level', values: [{ value: 'INFO', count: 10 }] }], ...summary,
      })),
      http.get(API('/logs/federation/streams'), () => HttpResponse.json({
        streams: [{ log_source_id: 'inst:2/file', stream: 'file', hits: 10 }], complete: true, ...summary,
      })),
      http.get(API('/logs/federation/fields'), () => HttpResponse.json({
        fields: ['level', 'parser_version', 'shard'], ...summary,
      })),
      http.get(API('/logs/federation/stream-field-names'), () => HttpResponse.json({
        fields: ['stream', 'log_source_id'], ...summary,
      })),
      http.get(API('/logs/federation/stream-field-values'), ({ request }) => {
        expect(new URL(request.url).searchParams.get('field')).toBe('stream')
        return HttpResponse.json({ field: 'stream', values: [{ value: 'file', count: 10 }], complete: true, ...summary })
      }),
    )
  })

  it('使用全窗口 Stats timeBucket，而不是当前页时间戳估算', async () => {
    renderWithProviders(<LogsPage />)
    await screen.findByText('structured-message')
    const chart = await screen.findByTestId('logs-time-distribution')
    expect(chart).toHaveAttribute('data-time-detail', expect.stringContaining('2026-11-11T08:05:00Z:7'))
    expect(chart).toHaveAttribute('data-time-detail', expect.not.stringContaining('structured-message'))
  })

  it('Fields 能展示并生成受控追加筛选，详情展示结构化字段', async () => {
    renderWithProviders(<LogsPage />)
    await screen.findByText('structured-message')
    await waitFor(() => expect(screen.getByTestId('logs-fields-trigger')).toBeEnabled())

    const user = userEvent.setup()
    await user.click(screen.getByTestId('logs-fields-trigger'))
    const fieldOption = await screen.findByRole('menuitem', { name: 'parser_version' })
    await user.click(fieldOption)

    await user.click(screen.getByTestId('log-row-expand'))
    expect(await screen.findByTestId('log-row-fields')).toHaveTextContent('parser_version=v1')
    expect(screen.getByTestId('log-row-fields')).toHaveTextContent('shard=blue')
  })
})
