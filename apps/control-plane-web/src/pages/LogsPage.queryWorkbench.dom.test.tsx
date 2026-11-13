/** VL 全窗口时间桶、字段探索与流值筛选。 */
import { describe, expect, it, beforeEach, afterEach, vi } from 'vitest'
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

describe('LogsPage 查询分析工作台', () => {
  afterEach(() => vi.restoreAllMocks())
  beforeEach(() => {
    vi.spyOn(Date, 'now').mockReturnValue(Date.parse('2026-11-11T08:10:00Z'))
    loginMockUser()
    HTMLElement.prototype.scrollIntoView ??= () => {}
    server.use(
      http.get(API('/nodes'), () => HttpResponse.json([{ id: 1, name: 'node-1', status: 1 }])),
      http.get(API('/instances'), () => HttpResponse.json([{ id: 2, name: 'instance-2', nodeId: 1 }])),
      http.get(API('/logs/federation'), () => HttpResponse.json({
        sourceTag: 'federated', items: [logEvent], view: { view_id: 'structured-view' },
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
      http.get(API('/logs/federation/fields'), ({ request }) => {
        expect(new URL(request.url).searchParams.get('explore')).toBe('true')
        return HttpResponse.json({ fields: ['level', 'parser_version', 'shard'], explore_applied: true, ...summary })
      }),
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

  it.each(['http.status_code', 'trace-id'])('Fields菜单给%s生成安全字段引用', async (field) => {
    const searches: URLSearchParams[] = []
    server.use(
      http.get(API('/logs/federation/fields'), () => HttpResponse.json({ fields: [field], explore_applied: true, ...summary })),
      http.get(API('/logs/federation'), ({ request }) => {
        searches.push(new URL(request.url).searchParams)
        return HttpResponse.json({ sourceTag: 'federated', items: [logEvent], view: { view_id: 'structured-view' }, ...summary, exhausted: true })
      }),
    )
    renderWithProviders(<LogsPage />)
    await screen.findByText('structured-message')
    await waitFor(() => expect(screen.getByTestId('logs-fields-trigger')).toBeEnabled())
    const user = userEvent.setup()
    await user.click(screen.getByTestId('logs-fields-trigger'))
    await user.click(await screen.findByRole('menuitem', { name: field }))
    await waitFor(() => expect(searches.some((params) => params.get('keyword') === `${JSON.stringify(field)}:*`)).toBe(true))
  })

  it('字段部分覆盖展示已确认字段与缺失目标，常见值保持同View且不开放数字统计', async () => {
    let searchParams: URLSearchParams | undefined
    let fieldsParams: URLSearchParams | undefined
    const valueRequests: URLSearchParams[] = []
    let numericRequests = 0
    const partialCoverage = {
      complete: false, partial_reasons: ['ENGINE_NOT_READY', 'RANGE_UNAVAILABLE'],
      targets: [
        { target_id: 'inst:1', state: 'not_ready', reasons: ['RANGE_UNAVAILABLE'] },
        { target_id: 'inst:2', state: 'success' },
      ],
    }
    server.use(
      http.get(API('/instances'), () => HttpResponse.json([{ id: 1, name: 'unavailable-instance', nodeId: 1 }, { id: 2, name: 'instance-2', nodeId: 1 }])),
      http.get(API('/logs/federation'), ({ request }) => {
        searchParams = new URL(request.url).searchParams
        return HttpResponse.json({ sourceTag: 'federated', items: [logEvent], view: { view_id: 'partial-fields-view' }, ...summary, coverage: { ...summary.coverage, pending: true }, exhausted: true })
      }),
      http.get(API('/logs/federation/fields'), ({ request }) => {
        fieldsParams = new URL(request.url).searchParams
        return HttpResponse.json({ fields: ['level', 'stream'], explore_applied: true, coverage: partialCoverage, truncated: false })
      }),
      http.get(API('/logs/federation/facets'), ({ request }) => {
        const params = new URL(request.url).searchParams
        if (params.get('dimensions') === 'level') {
          valueRequests.push(params)
          return HttpResponse.json({ explore_applied: true, dimensions: [{ dimension: 'level', values: [{ value: 'INFO', count: 4 }] }], coverage: partialCoverage, quality: { duplicate_quality: 'exact', stats_quality: 'partial' } })
        }
        return HttpResponse.json({ dimensions: [{ dimension: 'level', values: [{ value: 'INFO', count: 10 }] }], ...summary })
      }),
      http.get(API('/logs/federation/stats'), ({ request }) => {
        if (new URL(request.url).searchParams.has('metricField')) numericRequests++
        return HttpResponse.json({ points: [], ...summary })
      }),
    )
    renderWithProviders(<LogsPage />)
    await screen.findByText('structured-message')
    await userEvent.setup().click(screen.getByTestId('log-field-explorer-trigger'))
    expect(await screen.findByText('字段列表尚不完整，显示已确认的字段。')).toBeInTheDocument()
    const discoveryStatus = screen.getByTestId('field-discovery-status')
    expect(discoveryStatus).toHaveAttribute('data-state', 'partial')
    expect(discoveryStatus).toHaveTextContent('unavailable-instance')
    expect(discoveryStatus).toHaveTextContent('RANGE_UNAVAILABLE')
    expect(screen.queryByText('字段列表查询失败。')).not.toBeInTheDocument()
    await userEvent.setup().selectOptions(screen.getByLabelText('字段', { exact: true }), 'level')
    expect(await screen.findByTestId('field-value-row')).toHaveTextContent('≈4')
    expect(screen.getByRole('tab', { name: '数值统计', exact: true })).toBeDisabled()
    expect(numericRequests).toBe(0)
    expect(valueRequests).toHaveLength(1)
    for (const key of ['view', 'source', 'instanceId', 'nodeId', 'from', 'to', 'keyword']) {
      expect(fieldsParams?.get(key)).toBe(searchParams?.get(key))
      expect(valueRequests[0].get(key)).toBe(searchParams?.get(key))
    }
    expect(fieldsParams?.get('viewId')).toBe('partial-fields-view')
    expect(valueRequests[0].get('viewId')).toBe('partial-fields-view')
    expect(fieldsParams?.get('explore')).toBe('true')
    expect(valueRequests[0].get('explore')).toBe('true')
    expect(valueRequests[0].get('dimensionLimit')).toBe('20')
    expect(screen.getByTestId('field-discovery-status')).toHaveAttribute('data-state', 'partial')
  })

  it.each([
    { name: '旧CP缺少ack', status: 200, body: { fields: ['level'], ...summary }, message: '当前节点不支持字段枚举。' },
    { name: 'HTTP501', status: 501, body: { error: 'LOG_UNSUPPORTED' }, message: '当前节点不支持字段枚举。' },
    { name: 'Worker不支持', status: 200, body: { explore_applied: true, fields: ['level'], coverage: { complete: false, partial_reasons: ['UNSUPPORTED'] } }, message: '当前节点不支持字段枚举。' },
    { name: '权限拒绝', status: 200, body: { explore_applied: true, fields: ['level'], coverage: { complete: false, partial_reasons: ['UNAUTHORIZED_TARGET'] } }, message: '字段列表查询失败。' },
    { name: '错误信封夹带字段', status: 200, body: { explore_applied: true, fields: ['level'], error: 'FORBIDDEN', ...summary }, message: '字段列表查询失败。' },
    { name: '上游失败', status: 200, body: { explore_applied: true, fields: [], coverage: { complete: false, partial_reasons: ['RANGE_UNAVAILABLE'] } }, message: '字段列表查询失败。' },
    { name: '预算截断', status: 200, body: { explore_applied: true, fields: [], coverage: { complete: false }, truncated: true }, message: '字段列表查询失败。' },
  ])('$name不会冒充可用字段列表，允许重试恢复', async ({ status, body, message }) => {
    let recovered = false
    server.use(http.get(API('/logs/federation/fields'), () => recovered
      ? HttpResponse.json({ explore_applied: true, fields: [], ...summary })
      : HttpResponse.json(body, { status })))
    renderWithProviders(<LogsPage />)
    await screen.findByText('structured-message')
    const user = userEvent.setup()
    await user.click(screen.getByTestId('log-field-explorer-trigger'))
    expect(await screen.findByText(message)).toBeInTheDocument()
    expect(screen.queryByText('当前查询尚未提供字段，请先完成实例日志查询。')).not.toBeInTheDocument()
    recovered = true
    await user.click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByText('当前查询尚未提供字段，请先完成实例日志查询。')).toBeInTheDocument()
  })
})
