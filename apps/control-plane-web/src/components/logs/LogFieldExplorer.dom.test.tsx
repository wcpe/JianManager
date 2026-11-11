import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import { useState } from 'react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { server } from '@jianmanager/devmock/server'
import { API } from '@jianmanager/devmock/api'
import { renderWithProviders } from '@/test/render'
import { loginMockUser } from '@/test/auth'
import api from '@/api/client'
import { LogFieldExplorer } from './LogFieldExplorer'

const params = { viewId: 'fixed-view', instanceId: 2, from: '2026-11-10T00:00:00Z', to: '2026-11-11T00:00:00Z', keyword: '(error OR warn)' }
const exact = { coverage: { complete: true }, quality: { duplicate_quality: 'exact', stats_quality: 'exact' } }
const fields = ['shard', 'duration_ms']

beforeEach(() => loginMockUser())
afterEach(() => vi.restoreAllMocks())

describe('LogFieldExplorer', () => {
  it('关闭或未选字段时不查询；选择后只请求该字段的20项分布并保留query view/scope', async () => {
    const seen: URLSearchParams[] = []
    server.use(http.get(API('/logs/federation/facets'), ({ request }) => {
      seen.push(new URL(request.url).searchParams)
      return HttpResponse.json({ ...exact, explore_applied: true, dimensions: [{ dimension: 'shard', values: [{ value: 'blue', count: 4 }] }] })
    }))
    const user = userEvent.setup()
    renderWithProviders(<LogFieldExplorer params={params} enabled fields={fields} onFilter={() => {}} />)
    expect(seen).toHaveLength(0)
    await user.click(screen.getByTestId('log-field-explorer-trigger'))
    expect(screen.getByText('选择字段后按需查询，未选字段不扫描数据。')).toBeInTheDocument()
    expect(seen).toHaveLength(0)
    await user.selectOptions(screen.getByLabelText('字段'), 'shard')
    expect(await screen.findByTestId('field-value-row')).toHaveTextContent('blue')
    expect(seen).toHaveLength(1)
    expect(Object.fromEntries(seen[0])).toMatchObject({ ...params, instanceId: '2', dimensions: 'shard', dimensionLimit: '20', explore: 'true' })
  })

  it('常见值支持正向/排除精确筛选，恶意值与OR仍在字符串中', async () => {
    const value = 'blue" OR * \\ tail'
    const onFilter = vi.fn()
    server.use(http.get(API('/logs/federation/facets'), () => HttpResponse.json({ ...exact, explore_applied: true, dimensions: [{ dimension: 'shard', values: [{ value, count: 2 }] }] })))
    const user = userEvent.setup()
    renderWithProviders(<LogFieldExplorer params={params} enabled fields={fields} onFilter={onFilter} />)
    await user.click(screen.getByTestId('log-field-explorer-trigger'))
    await user.selectOptions(screen.getByLabelText('字段'), 'shard')
    const row = await screen.findByTestId('field-value-row')
    await user.click(within(row).getByRole('button', { name: /仅包含/ }))
    await user.click(within(row).getByRole('button', { name: /排除/ }))
    expect(onFilter.mock.calls).toEqual([[`(shard:=${JSON.stringify(value)})`], [`NOT (shard:=${JSON.stringify(value)})`]])
  })

  it('数值分析显式执行并传metricField/groupBy/timeBucket；展示count/sum/min/max与加权avg', async () => {
    const seen: URLSearchParams[] = []
    server.use(
      http.get(API('/logs/federation/facets'), () => HttpResponse.json({ ...exact, explore_applied: true, dimensions: [{ dimension: 'duration_ms', values: [] }] })),
      http.get(API('/logs/federation/stats'), ({ request }) => {
        seen.push(new URL(request.url).searchParams)
        return HttpResponse.json({ ...exact, points: [{ time_bucket_utc: '2026-11-10T00:05:00Z', dimensions: { shard: 'blue' }, agg: { count: 2, sum: 10, min: 3, max: 7, has_min_max: true } }] })
      }),
    )
    const user = userEvent.setup()
    renderWithProviders(<LogFieldExplorer params={params} enabled fields={fields} onFilter={() => {}} />)
    await user.click(screen.getByTestId('log-field-explorer-trigger'))
    await user.selectOptions(screen.getByLabelText('字段'), 'duration_ms')
    await user.click(screen.getByRole('tab', { name: '数值统计' }))
    await user.selectOptions(screen.getByLabelText('分组字段'), 'shard')
    await user.selectOptions(screen.getByLabelText('时间桶'), '5m')
    expect(seen).toHaveLength(0)
    await user.click(screen.getByRole('button', { name: '运行统计' }))
    const row = await screen.findByTestId('field-stats-row')
    expect(seen).toHaveLength(1)
    expect(Object.fromEntries(seen[0])).toMatchObject({ ...params, instanceId: '2', metricField: 'duration_ms', groupBy: 'shard', timeBucket: '5m' })
    expect(within(row).getByTestId('stats-count')).toHaveTextContent('2')
    expect(within(row).getByTestId('stats-sum')).toHaveTextContent('10')
    expect(within(row).getByTestId('stats-min')).toHaveTextContent('3')
    expect(within(row).getByTestId('stats-max')).toHaveTextContent('7')
    expect(within(row).getByTestId('stats-avg')).toHaveTextContent('5')
    expect(row).toHaveTextContent('2026-11-10T00:05:00Z')
  })

  it('unsupported查询失败可见且允许重试，不能以disabled按钮代替错误', async () => {
    let calls = 0
    server.use(http.get(API('/logs/federation/facets'), () => {
      calls++
      return calls === 1
        ? HttpResponse.json({ error: 'LOG_UNSUPPORTED', message: '旧节点不支持字段查询' }, { status: 501 })
        : HttpResponse.json({ ...exact, explore_applied: true, dimensions: [{ dimension: 'shard', values: [{ value: 'recovered', count: 1 }] }] })
    }))
    const user = userEvent.setup()
    renderWithProviders(<LogFieldExplorer params={params} enabled fields={fields} onFilter={() => {}} />)
    await user.click(screen.getByTestId('log-field-explorer-trigger'))
    await user.selectOptions(screen.getByLabelText('字段'), 'shard')
    expect(await screen.findByText('当前节点不支持该查询。')).toBeInTheDocument()
    expect(screen.queryByText('当前窗口没有该字段值。')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByText('recovered')).toBeInTheDocument()
    expect(calls).toBe(2)
  })

  it('部分覆盖和截断同时可见，空结果不声称成功且提供重试', async () => {
    server.use(http.get(API('/logs/federation/facets'), () => HttpResponse.json({ explore_applied: true, coverage: { complete: false }, truncated: true, dimensions: [] })))
    const user = userEvent.setup()
    renderWithProviders(<LogFieldExplorer params={params} enabled fields={fields} onFilter={() => {}} />)
    await user.click(screen.getByTestId('log-field-explorer-trigger'))
    await user.selectOptions(screen.getByLabelText('字段'), 'shard')
    expect(await screen.findByText('仅返回部分结果，覆盖不完整。')).toBeInTheDocument()
    expect(screen.getByText('结果已截断，请缩小范围或减少分组。')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重试' })).toBeEnabled()
    expect(screen.queryByText('当前窗口没有该字段值。')).not.toBeInTheDocument()
  })

  it('点与连字符字段可分面、生成安全包含排除筛选并进行数值分组', async () => {
    const onFilter = vi.fn()
    const numericRequests: URLSearchParams[] = []
    server.use(
      http.get(API('/logs/federation/facets'), ({ request }) => {
        const dimension = new URL(request.url).searchParams.get('dimensions')
        const value = dimension === 'http.status_code' ? '200' : 'edge-a'
        return HttpResponse.json({ ...exact, explore_applied: true, dimensions: [{ dimension, values: [{ value, count: 10 }] }] })
      }),
      http.get(API('/logs/federation/stats'), ({ request }) => {
        numericRequests.push(new URL(request.url).searchParams)
        return HttpResponse.json({ ...exact, points: [{ dimensions: { 'trace-id': 'edge-a' }, agg: { count: 10, sum: 2000, min: 200, max: 200, avg: 200, has_min_max: true } }] })
      }),
    )
    const user = userEvent.setup()
    renderWithProviders(<LogFieldExplorer params={params} enabled fields={['http.status_code', 'trace-id']} onFilter={onFilter} />)
    await user.click(screen.getByTestId('log-field-explorer-trigger'))
    for (const [field, value] of [['http.status_code', '200'], ['trace-id', 'edge-a']]) {
      await user.selectOptions(screen.getByLabelText('字段'), field)
      await waitFor(() => expect(screen.getByTestId('field-value-row')).toHaveTextContent(value))
      const row = screen.getByTestId('field-value-row')
      await user.click(within(row).getByRole('button', { name: /仅包含/ }))
      await user.click(within(row).getByRole('button', { name: /排除/ }))
    }
    expect(onFilter.mock.calls).toEqual([
      ['("http.status_code":="200")'], ['NOT ("http.status_code":="200")'],
      ['("trace-id":="edge-a")'], ['NOT ("trace-id":="edge-a")'],
    ])
    await user.selectOptions(screen.getByLabelText('字段'), 'http.status_code')
    await user.click(screen.getByRole('tab', { name: '数值统计' }))
    await user.selectOptions(screen.getByLabelText('分组字段'), 'trace-id')
    await user.click(screen.getByRole('button', { name: '运行统计' }))
    expect(await screen.findByTestId('field-stats-row')).toHaveTextContent('edge-a')
    expect(Object.fromEntries(numericRequests[0])).toMatchObject({ metricField: 'http.status_code', groupBy: 'trace-id' })
    expect(numericRequests[0]?.get('explore')).toBeNull()
  })

  it('非法字段不请求；数值字段返回不可用指标时不冒充零统计', async () => {
    let facetsCalls = 0
    server.use(
      http.get(API('/logs/federation/facets'), () => { facetsCalls++; return HttpResponse.json({ ...exact, explore_applied: true, dimensions: [{ dimension: 'shard', values: [] }] }) }),
      http.get(API('/logs/federation/stats'), () => HttpResponse.json({ ...exact, points: [{ agg: { count: 2, has_min_max: false } }] })),
    )
    const user = userEvent.setup()
    renderWithProviders(<LogFieldExplorer params={params} enabled fields={['bad OR *', 'shard']} onFilter={() => {}} />)
    await user.click(screen.getByTestId('log-field-explorer-trigger'))
    await user.selectOptions(screen.getByLabelText('字段'), 'bad OR *')
    expect(screen.getByText('该字段名不符合当前节点查询契约，无法执行查询。')).toBeInTheDocument()
    expect(facetsCalls).toBe(0)
    await user.selectOptions(screen.getByLabelText('字段'), 'shard')
    await user.click(screen.getByRole('tab', { name: '数值统计' }))
    await user.click(screen.getByRole('button', { name: '运行统计' }))
    expect(await screen.findByText('该字段未返回有效数值指标，请选择数值字段或检查节点响应。')).toBeInTheDocument()
    expect((await screen.findByTestId('field-stats-row')).querySelector('[data-testid="stats-sum"]')).toHaveTextContent('—')
  })

  it('关闭浮层会取消正在执行的字段查询，Escape不抢占原生日志选择', async () => {
    const get = vi.spyOn(api, 'get')
    const abort = vi.spyOn(XMLHttpRequest.prototype, 'abort')
    let requested = false
    let release = () => {}
    server.use(http.get(API('/logs/federation/facets'), async () => {
      requested = true
      await new Promise<void>((resolve) => { release = resolve })
      return HttpResponse.json({ ...exact, explore_applied: true, dimensions: [] })
    }))
    const user = userEvent.setup()
    renderWithProviders(<LogFieldExplorer params={params} enabled fields={fields} onFilter={() => {}} />)
    await user.click(screen.getByTestId('log-field-explorer-trigger'))
    await user.selectOptions(screen.getByLabelText('字段'), 'shard')
    try {
      await waitFor(() => expect(requested).toBe(true))
      // MSW's XHR projection does not link Request.signal; inspect axios's actual signal and native abort.
      const calls = get.mock.calls.filter(([path]) => path === '/logs/federation/facets')
      expect(calls).toHaveLength(1)
      const signal = calls[0]?.[1]?.signal
      expect(signal?.aborted).toBe(false)
      await user.keyboard('{Escape}')
      await waitFor(() => expect(signal?.aborted).toBe(true))
      expect(abort).toHaveBeenCalledTimes(1)
      expect(screen.queryByTestId('field-explorer-result')).not.toBeInTheDocument()
    } finally {
      release()
    }
  })

  it('缺失统计数组不能显示为成功空样本；统计错误允许重试', async () => {
    server.use(
      http.get(API('/logs/federation/facets'), () => HttpResponse.json({ ...exact, explore_applied: true, dimensions: [{ dimension: 'duration_ms', values: [] }] })),
      http.get(API('/logs/federation/stats'), () => HttpResponse.json({ ...exact })),
    )
    const user = userEvent.setup()
    renderWithProviders(<LogFieldExplorer params={params} enabled fields={fields} onFilter={() => {}} />)
    await user.click(screen.getByTestId('log-field-explorer-trigger'))
    await user.selectOptions(screen.getByLabelText('字段'), 'duration_ms')
    await user.click(screen.getByRole('tab', { name: '数值统计' }))
    await user.click(screen.getByRole('button', { name: '运行统计' }))
    expect(await screen.findByText('节点响应缺少所选字段数据，未将其当作成功空结果。')).toBeInTheDocument()
    expect(screen.queryByText('当前窗口没有统计样本。')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重试' })).toBeEnabled()
  })

  it.each(['worker', 'control_plane'])('已知%s文本来源即使传enabled也不发VL请求', async (source) => {
    const user = userEvent.setup()
    renderWithProviders(<LogFieldExplorer params={{ ...params, source }} enabled fields={fields} onFilter={() => {}} />)
    await user.click(screen.getByTestId('log-field-explorer-trigger'))
    expect(screen.getByText('仅实例 VL 日志的固定查询视图支持字段分析。')).toBeInTheDocument()
    expect(screen.queryByLabelText('字段')).not.toBeInTheDocument()
  })

  it('仅实例VL固定view启用；数据上下文切换会清空字段选择并停止旧查询', async () => {
    let calls = 0
    server.use(http.get(API('/logs/federation/facets'), () => {
      calls++
      return HttpResponse.json({ ...exact, explore_applied: true, dimensions: [{ dimension: 'shard', values: [] }] })
    }))
    const user = userEvent.setup()
    let update = (value: { params: typeof params; enabled: boolean }) => { void value }
    function Harness() {
      const [context, setContext] = useState({ params, enabled: false })
      update = setContext
      return <LogFieldExplorer {...context} fields={fields} onFilter={() => {}} />
    }
    renderWithProviders(<Harness />)
    await user.click(screen.getByTestId('log-field-explorer-trigger'))
    expect(screen.getByText('仅实例 VL 日志的固定查询视图支持字段分析。')).toBeInTheDocument()
    expect(calls).toBe(0)
    act(() => update({ params, enabled: true }))
    await user.selectOptions(screen.getByLabelText('字段'), 'shard')
    await screen.findByText('当前窗口没有该字段值。')
    expect(calls).toBe(1)
    act(() => update({ params: { ...params, viewId: 'next-view' }, enabled: true }))
    expect(screen.getByLabelText('字段')).toHaveValue('')
    expect(calls).toBe(1)
  })
})
